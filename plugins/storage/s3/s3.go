// Package s3 implements core.Storage over an S3-compatible object store (AWS
// S3, MinIO, R2) using hand-rolled SigV4 signing — no AWS SDK dependency, so
// the daemon stays a single static binary. Compatible with any endpoint that
// speaks the S3 REST API.
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/mohammadjaf013/weft/core"
)

type Config struct {
	Endpoint  string // https://s3.example.com (no bucket path)
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	// BasePath is an optional key prefix under which all assets are stored
	// (e.g. "movie" or "series"); empty means the bucket root.
	BasePath string
	// Do is the HTTP client's Do. Overridable for tests.
	Do func(req *http.Request) (*http.Response, error)
}

type Storage struct {
	cfg Config
}

var _ core.Storage = (*Storage)(nil)

func New(cfg Config) (*Storage, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("s3 storage: endpoint, bucket, access_key and secret_key required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.Do == nil {
		cfg.Do = (&http.Client{Timeout: 60 * time.Second}).Do
	}
	return &Storage{cfg: cfg}, nil
}

func (s *Storage) Scheme() string { return "s3" }

func (s *Storage) key(ref core.AssetRef) string {
	k := strings.TrimPrefix(ref.URI, "s3://")
	if i := strings.Index(k, "/"); i >= 0 {
		k = k[i+1:]
	}
	if k == "" || k == ref.URI {
		k = ref.Name
	}
	if s.cfg.BasePath != "" {
		k = s.cfg.BasePath + "/" + k
	}
	return path.Clean(k)
}

// signed performs a SigV4-signed HTTP request and returns the response. The
// caller must close the response body. A non-nil query is included both in the
// URL and in the signed canonical query string (required by ListObjectsV2).
func (s *Storage) signed(ctx context.Context, method, key string, body []byte, query url.Values) (*http.Response, error) {
	if key == "" {
		key = "/"
	}
	u := s.cfg.Endpoint + "/" + s.cfg.Bucket + "/" + strings.TrimPrefix(key, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	payloadHash := sha256Hex(body)

	canonicalURI := "/" + strings.TrimPrefix(s.cfg.Bucket, "/") + "/" + strings.TrimPrefix(key, "/")
	// For path-style endpoints the canonical URI must be /bucket/key.
	canonicalQuery := ""
	if len(query) > 0 {
		canonicalQuery = canonicalQueryString(query)
	}
	canonicalHeaders := "host:" + hostOf(u) + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		"\n",
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := dateStamp + "/" + s.cfg.Region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	sig := hmacSHA256Hex(s.cfg.SecretKey, dateStamp, s.cfg.Region, stringToSign)
	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.cfg.AccessKey, scope, signedHeaders, sig)

	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Host", hostOf(u))
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("Authorization", auth)
	if body != nil && len(body) > 0 {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := s.cfg.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("s3 %s %s: %d %s", method, key, resp.StatusCode, truncate(b))
	}
	return resp, nil
}

func (s *Storage) Open(ctx context.Context, ref core.AssetRef) (io.ReadCloser, error) {
	resp, err := s.signed(ctx, http.MethodGet, s.key(ref), nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (s *Storage) Save(ctx context.Context, ref core.AssetRef, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	resp, err := s.signed(ctx, http.MethodPut, s.key(ref), body, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (s *Storage) Delete(ctx context.Context, ref core.AssetRef) error {
	resp, err := s.signed(ctx, http.MethodDelete, s.key(ref), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (s *Storage) Copy(ctx context.Context, src, dst core.AssetRef) error {
	// Pull then push — simple, correct for small-to-medium assets. Production
	// would use x-amz-copy-source, kept out to avoid per-object ACL surprises.
	in, err := s.Open(ctx, src)
	if err != nil {
		return err
	}
	defer in.Close()
	return s.Save(ctx, dst, in)
}

func (s *Storage) Verify(ctx context.Context, ref core.AssetRef) (bool, error) {
	resp, err := s.signed(ctx, http.MethodHead, s.key(ref), nil, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// List lists objects under the bucket's base path (ListObjectsV2), returning
// keys relative to the base path. Only the first page is returned.
func (s *Storage) List(ctx context.Context) ([]string, error) {
	q := url.Values{}
	q.Set("list-type", "2")
	base := strings.TrimPrefix(s.cfg.BasePath, "/")
	base = strings.TrimPrefix(base, "/")
	if base != "" {
		q.Set("prefix", base+"/")
	}
	resp, err := s.signed(ctx, http.MethodGet, "/", nil, q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("s3 list: parse: %w", err)
	}
	prefix := ""
	if base != "" {
		prefix = base + "/"
	}
	var files []string
	for _, c := range result.Contents {
		k := strings.TrimPrefix(c.Key, prefix)
		if k == "" || k == c.Key {
			continue
		}
		files = append(files, k)
	}
	return files, nil
}

// canonicalQueryString renders the SigV4 canonical (URI-encoded, sorted) query
// string from a set of parameters.
func canonicalQueryString(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// helpers for SigV4 (all stdlib)

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Host
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// hmacSHA256Hex derives the chain secret/dates + signing key and returns the
// final signature hex.
func hmacSHA256Hex(secret, dateStamp, region, stringToSign string) string {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	return hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func truncate(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
