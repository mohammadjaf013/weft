package s3

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
)

type recordingDo struct {
	reqs []*http.Request
	code int
	body []byte
}

func (r *recordingDo) Do(req *http.Request) (*http.Response, error) {
	r.reqs = append(r.reqs, req)
	body := r.body
	if body == nil {
		body = []byte("ok")
	}
	return &http.Response{
		StatusCode: r.code,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func newTest(t *testing.T) (*Storage, *recordingDo) {
	t.Helper()
	rd := &recordingDo{code: http.StatusOK}
	s, err := New(Config{
		Endpoint:  "https://s3.example.com",
		Region:    "us-east-1",
		Bucket:    "media",
		AccessKey: "AKID",
		SecretKey: "SECRET",
		Do:        rd.Do,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, rd
}

func TestSavePutsSigned(t *testing.T) {
	s, rd := newTest(t)
	err := s.Save(context.Background(), core.AssetRef{Name: "v.mp4", URI: "s3://media/v.mp4"}, strings.NewReader("bytes"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(rd.reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(rd.reqs))
	}
	req := rd.reqs[0]
	if req.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", req.Method)
	}
	if req.Header.Get("x-amz-date") == "" {
		t.Error("missing x-amz-date header")
	}
	if auth := req.Header.Get("Authorization"); !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKID/") || !strings.Contains(auth, "s3/aws4_request") {
		t.Errorf("bad Authorization: %q", auth)
	}
	if !strings.HasSuffix(req.URL.Path, "/media/v.mp4") {
		t.Errorf("url path = %q, want /media/v.mp4", req.URL.Path)
	}
}

func TestOpenGets(t *testing.T) {
	s, rd := newTest(t)
	in, err := s.Open(context.Background(), core.AssetRef{Name: "v.mp4", URI: "s3://media/v.mp4"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	b, _ := io.ReadAll(in)
	in.Close()
	if string(b) != "ok" {
		t.Errorf("body = %q", string(b))
	}
	if rd.reqs[0].Method != http.MethodGet {
		t.Errorf("method = %s, want GET", rd.reqs[0].Method)
	}
}

func TestVerifyHeads(t *testing.T) {
	s, rd := newTest(t)
	ok, err := s.Verify(context.Background(), core.AssetRef{Name: "v.mp4", URI: "s3://media/v.mp4"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("Verify returned false")
	}
	if rd.reqs[0].Method != http.MethodHead {
		t.Errorf("method = %s, want HEAD", rd.reqs[0].Method)
	}
}

func TestErrorOnNon200(t *testing.T) {
	rd := &recordingDo{code: http.StatusNotFound}
	s, _ := New(Config{
		Endpoint: "https://s3.example.com", Bucket: "media",
		AccessKey: "AKID", SecretKey: "SECRET", Do: rd.Do,
	})
	if err := s.Save(context.Background(), core.AssetRef{Name: "v", URI: "s3://media/v"}, strings.NewReader("x")); err == nil {
		t.Fatal("expected error for 404")
	}
}
