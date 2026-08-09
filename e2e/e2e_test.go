// Package e2e exercises the real daemon: config → store → scheduler → worker →
// plugins → storage → webhook, driven over the HTTP API. No real ffmpeg is
// needed; a fake executor plus stub media plugins stand in for external tools.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/daemon"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	"github.com/mohammadjaf013/weft/plugins/upload"
	ffexec "github.com/mohammadjaf013/weft/runtime/executor/ffmpeg"
	"github.com/mohammadjaf013/weft/runtime/registry"
	"github.com/mohammadjaf013/weft/runtime/webhook"
)

// stubPlugin writes a real file and returns it as an asset (like the real
// media plugins would after ffmpeg). The real upload plugin then copies it.
type stubPlugin struct {
	name  string
	kinds []string
}

func (p *stubPlugin) Name() string { return p.name }
func (p *stubPlugin) Capabilities() core.Capabilities {
	return core.Capabilities{Name: p.name, SupportedKinds: p.kinds}
}

func (p *stubPlugin) Process(_ context.Context, in core.TaskInput) (core.TaskOutput, error) {
	dir := mediautil.WorkDir(in)
	name := mediautil.BaseName(in) + "." + p.name + ".dat"
	local := filepath.Join(dir, name)
	if err := mediautil.WriteFile(local, "asset-"+string(in.TaskID)); err != nil {
		return core.TaskOutput{}, err
	}
	return core.TaskOutput{Assets: []core.AssetRef{
		{Kind: "attachment", Name: name, URI: "local:" + local},
	}}, nil
}

// registerStubs populates a registry: media stubs + the real upload plugin.
func registerStubs(reg *registry.Registry, c *cfg.Config) error {
	for _, name := range []string{"video_encode", "thumbnail", "subtitle", "audio_encode", "master_playlist"} {
		if err := reg.Register(&stubPlugin{name: "stub-" + name, kinds: []string{name}}); err != nil {
			return err
		}
	}
	return reg.Register(&upload.Plugin{})
}

type delivered struct {
	wire      string
	payload   []byte
	signature string
}

// TestJobLifecycleToWebhook runs one job end-to-end and asserts the job lands
// in completed and the matching webhook fires with a valid HMAC signature.
func TestJobLifecycleToWebhook(t *testing.T) {
	workRoot := t.TempDir()
	mediautil.WorkRoot = workRoot

	storeDir := t.TempDir()
	uploadBase := t.TempDir()

	// webhook receiver
	var mu sync.Mutex
	var got []delivered
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, delivered{wire: r.Header.Get("X-Weft-Event"), payload: b, signature: r.Header.Get("X-Weft-Signature")})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	secret := "test-secret"

	c := cfg.Default()
	c.Database.Path = filepath.Join(storeDir, "weft.db")
	c.Network.Listen = "127.0.0.1:0"
	c.Security.APIKeys = false
	c.AI.Provider = ""
	c.AI.AutoGenerate.Enabled = false
	c.Storage.Local.BasePath = uploadBase
	c.Workers.Min = 1
	c.Plugins.Enabled = []string{"stub-media"}
	c.Webhooks = []cfg.WebhookCfg{{ID: "wh1", URL: recv.URL, Secret: secret, Events: []string{"*"}, MaxRetries: 3}}

	d, err := daemon.Open(c, nil, daemon.Options{
		Executor:       ffexec.NewFake(core.Result{ExitCode: 0}, nil),
		PluginRegister: registerStubs,
		InputResolver: func(ctx context.Context, job core.Job) (string, error) {
			return filepath.Join(workRoot, string(job.ID)+".in"), nil
		},
	})
	if err != nil {
		t.Fatalf("daemon open: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- d.Serve(ctx) }()

	t.Cleanup(func() {
		cancel()
		_ = d.Store().Close()
	})
	addr := waitAddr(t, d)
	baseURL := "http://" + addr

	// create a job over the API (API keys off in this config)
	job := post(t, baseURL, "/jobs", map[string]any{
		"input_ref": "s3://bucket/movie.mp4",
		"profile":   "audio",
		"priority":  "normal",
	})
	if job["id"] == nil {
		t.Fatalf("create job response: %v", job)
	}
	jobIDstr := job["id"].(string)

	// poll until completed
	id := jobIDstr
	status := waitStatus(t, baseURL, id, string(core.JobCompleted), 15*time.Second)
	if status != string(core.JobCompleted) {
		t.Fatalf("job %s ended in status %q, want %q", id, status, core.JobCompleted)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no webhook delivered in time")
		}
		time.Sleep(200 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, dl := range got {
		if !strings.Contains(string(dl.payload), jobIDstr) {
			continue
		}
		if ok := webhook.Verify(secret, dl.payload, dl.signature); !ok {
			t.Errorf("webhook signature invalid for %s", dl.wire)
		}
		found = true
	}
	if !found {
		t.Fatalf("no webhook referencing job %s; got %d deliveries", jobIDstr, len(got))
	}

	// Confirm the upload actually landed in the local storage base dir.
	entries, _ := os.ReadDir(uploadBase)
	if len(entries) == 0 {
		t.Error("expected uploaded assets in local storage base")
	}

	// shutdown cleanly
	cancel()
	select {
	case err := <-serveDone:
		if err != nil && !strings.Contains(err.Error(), "server closed") {
			t.Errorf("serve exited: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("serve did not shut down")
	}
}

func waitAddr(t *testing.T, d *daemon.Daemon) string {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if d.Addr != "" {
			return d.Addr
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not bind an address")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func doJSON(t *testing.T, baseURL, method, path string, body any) ([]byte, int) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, baseURL+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode
}

// waitStatus polls a job until its status is `want` or the timeout expires.
func waitStatus(t *testing.T, baseURL, id, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		b, code := doJSON(t, baseURL, http.MethodGet, "/jobs/"+id, nil)
		if code != 200 {
			last = fmt.Sprintf("http_%d", code)
		} else {
			var out struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			_ = json.Unmarshal(b, &out)
			last = out.Status
		}
		if last == want {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

// post creates a resource and returns the decoded JSON response.
func post(t *testing.T, baseURL, path string, body map[string]any) map[string]any {
	t.Helper()
	b, code := doJSON(t, baseURL, http.MethodPost, path, body)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("POST %s: %d %s", path, code, string(b))
	}
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}
