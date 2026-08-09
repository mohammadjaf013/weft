package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	cfg "github.com/mohammadjaf013/weft/configs"
)

// client is a thin HTTP client over the Weft REST API. Every CLI command
// (except serve/version/init-config/doctor) is exactly this: build a request,
// call the API, print the result. Nothing CLI-only.
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

// remoteFlags holds the common flags every API-backed command accepts.
type remoteFlags struct {
	api    string
	key    string
	config string
}

// pullFlags reorders args so every flag registered on fs comes before any
// positional arg. Go's flag package stops parsing at the first non-flag arg,
// so `weft jobs create ref --profile audio` would otherwise treat --profile as
// positional. This lets users write flags in any order.
func pullFlags(fs *flag.FlagSet, args []string) []string {
	known := map[string]bool{"h": true, "help": true}
	fs.VisitAll(func(f *flag.Flag) { known[f.Name] = true })
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name := strings.TrimLeft(a, "-")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		f := fs.Lookup(name)
		if f != nil {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && !isBoolFlag(f) && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		rest = append(rest, a)
	}
	return append(flags, rest...)
}

func isBoolFlag(f *flag.Flag) bool {
	type boolFlag interface{ IsBoolFlag() bool }
	if bf, ok := f.Value.(boolFlag); ok {
		return bf.IsBoolFlag()
	}
	return false
}

// remoteFlagSet registers the shared --api/--key/--config flags and parses
// them. Commands that need extra flags (e.g. --profile) register them on the
// returned set BEFORE calling parseRemote.
func remoteFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.String("api", "", "API base URL (default from config network.listen)")
	fs.String("key", "", "bearer token (default from config security.admin_api_key)")
	fs.String("config", "", "path to config file (default: auto-discover weft.yaml in cwd, /opt/weft, /etc/weft, ~/.weft, ~)")
	return fs
}

// parseRemote parses the shared flags and returns the remote config. Defaults
// come from the config file: base URL from network.listen, token from
// security.admin_api_key. Flags may appear anywhere in args. When no config is
// found in the current directory, common well-known locations are tried so the
// CLI works from anywhere on the machine (/opt/weft, /etc/weft, $HOME/.weft).
func parseRemote(fs *flag.FlagSet, args []string) (remoteFlags, error) {
	if err := fs.Parse(pullFlags(fs, args)); err != nil {
		return remoteFlags{}, err
	}
	rf := remoteFlags{
		api:    fs.Lookup("api").Value.String(),
		key:    fs.Lookup("key").Value.String(),
		config: fs.Lookup("config").Value.String(),
	}
	path := findConfig(rf.config)
	if c, err := cfg.Load(path); err == nil {
		if rf.api == "" && c.Network.Listen != "" {
			rf.api = "http://" + c.Network.Listen
		}
		if rf.key == "" {
			rf.key = c.Security.AdminAPIKey
		}
	} else if !os.IsNotExist(err) {
		return remoteFlags{}, err
	}
	if rf.api == "" {
		rf.api = "http://127.0.0.1:8443"
	}
	return rf, nil
}

// findConfig resolves the config path to use: an explicit --config wins, else
// the current directory's weft.yaml, else well-known install locations.
func findConfig(explicit string) string {
	if explicit != "" {
		return explicit
	}
	candidates := []string{
		"weft.yaml",
		"/opt/weft/weft.yaml",
		"/etc/weft/weft.yaml",
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			home+"/.weft/weft.yaml",
			home+"/weft.yaml",
		)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "weft.yaml"
}

// remoteClient parses the shared flags (returning the flagset so callers can
// read positional args via fs.Args()) and builds a ready client.
func remoteClient(args []string) (*client, *flag.FlagSet, error) {
	fs := remoteFlagSet("weft")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return nil, nil, err
	}
	return newClient(rf), fs, nil
}

func newClient(rf remoteFlags) *client {
	return &client{
		baseURL: strings.TrimSuffix(rf.api, "/"),
		token:   rf.key,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// get performs a GET and decodes JSON into out (out may be nil).
func (c *client) get(path string, out any) error {
	return c.do(http.MethodGet, path, nil, out)
}

// getText performs a GET and returns the raw response body.
func (c *client) getText(path string, out *[]byte) error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("api %s %s: %w", http.MethodGet, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("api error %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	*out = raw
	return nil
}

// post performs a POST/PUT with a JSON body and decodes JSON into out.
func (c *client) post(path string, body any, out any) error {
	return c.do(http.MethodPost, path, body, out)
}

// del performs a DELETE.
func (c *client) del(path string, out any) error {
	return c.do(http.MethodDelete, path, nil, out)
}

// patch performs a PATCH with a JSON body and decodes JSON into out.
func (c *client) patch(path string, body any, out any) error {
	return c.do(http.MethodPatch, path, body, out)
}

// postRaw sends a POST with a pre-encoded body (e.g. YAML, not JSON) and the
// given Content-Type, decoding a JSON response into out.
func (c *client) postRaw(path, contentType string, body []byte, out any) error {
	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("api %s %s: %w", http.MethodPost, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("api error %d %s: %s", resp.StatusCode, e.Error.Code, e.Error.Message)
		}
		return fmt.Errorf("api error %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *client) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("api %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 401 && c.token == "" {
		return fmt.Errorf("api requires a bearer token: set security.admin_api_key in weft.yaml, or pass --key <token>")
	}
	if resp.StatusCode >= 400 {
		// surface the API's error shape when present
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("api error %d (%s): %s", resp.StatusCode, e.Error.Code, e.Error.Message)
		}
		return fmt.Errorf("api error %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}
