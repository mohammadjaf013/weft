// Package chaos holds failure-injection tests that run against the real daemon.
// They prove the system's resilience guarantees: a panicking plugin fails only
// its own job, a dead worker's task is requeued after its lease expires, and a
// flaky webhook endpoint is retried before the event is abandoned.
package chaos

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/daemon"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	"github.com/mohammadjaf013/weft/plugins/upload"
	ffexec "github.com/mohammadjaf013/weft/runtime/executor/ffmpeg"
	"github.com/mohammadjaf013/weft/runtime/registry"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// --- helpers shared across chaos tests ---

type stubMedia struct {
	name  string
	kinds []string
}

func (p *stubMedia) Name() string { return p.name }
func (p *stubMedia) Capabilities() core.Capabilities {
	return core.Capabilities{Name: p.name, SupportedKinds: p.kinds}
}
func (p *stubMedia) Process(_ context.Context, in core.TaskInput) (core.TaskOutput, error) {
	dir := mediautil.WorkDir(in)
	name := mediautil.BaseName(in) + ".dat"
	local := filepath.Join(dir, name)
	if err := mediautil.WriteFile(local, "data"); err != nil {
		return core.TaskOutput{}, err
	}
	return core.TaskOutput{Assets: []core.AssetRef{{Kind: "attachment", Name: name, URI: "local:" + local}}}, nil
}

// panicPlugin panics on Process — the worst a plugin can do.
type panicPlugin struct{}

func (p *panicPlugin) Name() string { return "panic" }
func (p *panicPlugin) Capabilities() core.Capabilities {
	return core.Capabilities{Name: "panic", SupportedKinds: []string{"video_encode"}}
}
func (p *panicPlugin) Process(_ context.Context, _ core.TaskInput) (core.TaskOutput, error) {
	panic("boom")
}

func baseRegister(reg *registry.Registry, c *cfg.Config) error {
	for _, name := range []string{"thumbnail", "subtitle", "audio_encode", "master_playlist"} {
		if err := reg.Register(&stubMedia{name: "stub-" + name, kinds: []string{name}}); err != nil {
			return err
		}
	}
	return reg.Register(&upload.Plugin{})
}

type harness struct {
	d       *daemon.Daemon
	cancel  context.CancelFunc
	doneCh  chan error
	baseURL string
}

func startDaemon(t *testing.T, register func(*registry.Registry, *cfg.Config) error, mutate func(*cfg.Config)) *harness {
	t.Helper()
	mediautil.WorkRoot = t.TempDir()
	c := cfg.Default()
	c.Database.Path = filepath.Join(t.TempDir(), "weft.db")
	c.Network.Listen = "127.0.0.1:0"
	c.Security.APIKeys = false
	c.AI.Provider = ""
	c.AI.AutoGenerate.Enabled = false
	c.Storage.Local.BasePath = t.TempDir()
	c.Workers.Min = 1
	c.Plugins.Enabled = []string{"media"}
	c.Workers.LeaseTTLSeconds = 1
	if mutate != nil {
		mutate(c)
	}

	d, err := daemon.Open(c, nil, daemon.Options{
		Executor:       ffexec.NewFake(core.Result{ExitCode: 0}, nil),
		PluginRegister: register,
		InputResolver: func(job core.Job) (string, error) {
			return filepath.Join(mediautil.WorkRoot, string(job.ID)+".in"), nil
		},
	})
	if err != nil {
		t.Fatalf("open daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()

	h := &harness{d: d, cancel: cancel, doneCh: done}
	h.baseURL = waitAddr(t, d)
	t.Cleanup(func() { cancel(); _ = d.Store().Close() })
	return h
}

func waitAddr(t *testing.T, d *daemon.Daemon) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for d.Addr == "" {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not bind")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "http://" + d.Addr
}

// createJob POSTs a job; returns the job id or fails.
func (h *harness) createJob(t *testing.T, profile string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"input_ref": "s3://b/m.mp4", "profile": profile, "priority": "normal"})
	resp, err := http.Post(h.baseURL+"/jobs", "application/json", bytesReader(body))
	if err != nil {
		t.Fatalf("POST /jobs: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /jobs: %d id=%q", resp.StatusCode, out.ID)
	}
	return out.ID
}

func (h *harness) jobStatus(t *testing.T, id string) core.JobStatus {
	t.Helper()
	resp, err := http.Get(h.baseURL + "/jobs/" + id)
	if err != nil {
		t.Fatalf("GET job: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Status core.JobStatus `json:"status"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Status
}

func (h *harness) waitStatus(t *testing.T, id string, want core.JobStatus, timeout time.Duration) core.JobStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last core.JobStatus
	for time.Now().Before(deadline) {
		last = h.jobStatus(t, id)
		if last == want {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

func post(t *testing.T, baseURL, path string, payload []byte) int {
	t.Helper()
	resp, err := http.Post(baseURL+path, "application/json", bytesReader(payload))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func getJSON(t *testing.T, baseURL, path string) map[string]any {
	t.Helper()
	resp, err := http.Get(baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// --- Test 1: plugin panic fails only its own job ---

func TestPluginPanicFailsOnlyItsJob(t *testing.T) {
	register := func(r *registry.Registry, c *cfg.Config) error {
		// panic plugin claims video_encode; everything else is a stub
		if err := r.Register(&panicPlugin{}); err != nil {
			return err
		}
		return baseRegister(r, c)
	}
	h := startDaemon(t, register, nil)

	// this job must FAIL (its video_encode task panics)
	bad := h.createJob(t, "vod-h264")
	if got := h.waitStatus(t, bad, core.JobFailed, 10*time.Second); got != core.JobFailed {
		t.Fatalf("panicking job ended %q, want failed", got)
	}

	// the daemon must still serve: a healthy job completes afterwards
	good := h.createJob(t, "audio")
	if got := h.waitStatus(t, good, core.JobCompleted, 10*time.Second); got != core.JobCompleted {
		t.Fatalf("healthy job after panic ended %q, want completed", got)
	}
}

// --- Test 2: expired lease requeues a dead worker's task ---

func TestExpiredLeaseRecovers(t *testing.T) {
	h := startDaemon(t, baseRegister, nil)

	// Simulate a crashed worker deterministically: write a job whose single task
	// is still leased with an expired lease and a dead worker id. The scheduler
	// will not hand out leased tasks; only the expirer can requeue it.
	ctx := context.Background()
	jobID := core.JobID("job_crashed")
	now := core.Now()
	if err := h.d.Store().SaveJobEvent(ctx, core.Job{
		ID: jobID, Status: core.JobQueued, Priority: core.PriorityNormal,
		Profile: "audio", InputRef: "s3://b/m.mp4",
		CreatedAt: now, UpdatedAt: now,
	}, core.Event{ID: core.NewID("evt"), JobID: jobID, Kind: core.EvtJobCreated, CreatedAt: now}); err != nil {
		t.Fatalf("SaveJobEvent: %v", err)
	}
	past := now.Add(-time.Hour)
	expired := core.Task{
		ID: "task_crashed", JobID: jobID, Kind: "audio_encode",
		Status: core.TaskLeased, WorkerID: "ghost",
		LeaseExpiresAt: &past, StartedAt: &past,
	}
	if err := h.d.Store().SaveTask(ctx, expired); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}

	// the expirer (2s interval) must requeue it; the worker then completes the job
	if got := h.waitStatus(t, string(jobID), core.JobCompleted, 15*time.Second); got != core.JobCompleted {
		t.Fatalf("job after lease expiry ended %q, want completed", got)
	}
}

// --- Test 3: flaky webhook is retried until delivered ---

func TestFlakyWebhookRetriesToSuccess(t *testing.T) {
	var hits int32
	var mu sync.Mutex
	var body []byte
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError) // first two deliveries fail
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = b
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	h := startDaemon(t, baseRegister, func(c *cfg.Config) {
		c.Webhooks = []cfg.WebhookCfg{{
			ID: "wh1", URL: recv.URL, Secret: "s", Events: []string{"job.completed"}, MaxRetries: 5,
		}}
	})

	id := h.createJob(t, "audio")
	if got := h.waitStatus(t, id, core.JobCompleted, 10*time.Second); got != core.JobCompleted {
		t.Fatalf("job ended %q", got)
	}

	// dispatcher retries; expect delivery within a few backoff ticks
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := len(body) > 0
		mu.Unlock()
		if ok {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if atomic.LoadInt32(&hits) < 3 {
		t.Errorf("expected at least 3 attempts (2 failures + success), got %d", hits)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(body) == 0 {
		t.Fatal("webhook was never delivered successfully")
	}
}
