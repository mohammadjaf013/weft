package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/storage/local"
	"github.com/mohammadjaf013/weft/runtime/cron"
	"github.com/mohammadjaf013/weft/runtime/metrics"
	"github.com/mohammadjaf013/weft/runtime/registry"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

func newTestServer(t *testing.T) (*Server, *sqlite.Store, *KeyManager) {
	t.Helper()
	store, err := sqlite.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	bus := core.NewEventBus()
	t.Cleanup(bus.Close)
	sm := core.NewStateMachine(store, bus)
	sched := core.NewDAGScheduler(store, bus, sm)

	reg := registry.New()
	reg.Register(&stubPlugin{name: "video", kinds: []string{"video_encode"}})
	reg.Register(&stubPlugin{name: "thumbnail", kinds: []string{"thumbnail"}})
	reg.Register(&stubPlugin{name: "subtitle", kinds: []string{"subtitle"}})
	reg.Register(&stubPlugin{name: "ai_subtitle", kinds: []string{"ai_subtitle"}})
	reg.Register(&stubPlugin{name: "master_playlist", kinds: []string{"master_playlist"}})
	reg.Register(&stubPlugin{name: "upload", kinds: []string{"upload"}})
	reg.Register(&stubPlugin{name: "audio_encode", kinds: []string{"audio_encode"}})

	m := metrics.New(fakeSnap{})
	cfg := cfg.Default()
	cfg.AI.Provider = ""

	km := NewKeyManager(store, "admin-test-key")
	srv := NewServer(Options{
		Store:    store,
		Bus:      bus,
		Sched:    sched,
		SM:       sm,
		Registry: reg,
		Metrics:  m,
		Config:   cfg,
		Keys:     km,
	})
	return srv, store, km
}

type fakeSnap struct{}

func (fakeSnap) Snapshot() (uint64, uint64, uint64, uint64) { return 0, 0, 0, 0 }

type stubPlugin struct {
	name  string
	kinds []string
}

func (p *stubPlugin) Name() string { return p.name }
func (p *stubPlugin) Capabilities() core.Capabilities {
	return core.Capabilities{Name: p.name, SupportedKinds: p.kinds}
}
func (p *stubPlugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	return core.TaskOutput{}, nil
}

func doRequest(t *testing.T, srv *Server, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body == "" {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	return rr
}

func TestHealthNoAuth(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rr := doRequest(t, srv, "GET", "/health", "", "")
	if rr.Code != 200 {
		t.Fatalf("health = %d", rr.Code)
	}
}

func TestRootReportsVersionProfilesPlugins(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rr := doRequest(t, srv, "GET", "/", "", "")
	if rr.Code != 200 {
		t.Fatalf("root = %d", rr.Code)
	}
	var out struct {
		Service  string   `json:"service"`
		Version  string   `json:"version"`
		Status   string   `json:"status"`
		Profiles []string `json:"profiles"`
		Plugins  []struct {
			Name  string   `json:"name"`
			Kinds []string `json:"kinds"`
		} `json:"plugins"`
		Endpoints []string `json:"endpoints"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Service != "weft" || out.Status != "ok" {
		t.Fatalf("service/status = %q/%q", out.Service, out.Status)
	}
	if out.Version != core.Version {
		t.Errorf("version = %q, want %q", out.Version, core.Version)
	}
	if len(out.Profiles) == 0 {
		t.Error("root: no profiles listed")
	}
	if len(out.Plugins) == 0 {
		t.Error("root: no plugins listed")
	}
	if len(out.Endpoints) == 0 {
		t.Error("root: no endpoints listed")
	}
}

func TestUnauthorizedWithoutToken(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rr := doRequest(t, srv, "GET", "/jobs", "", "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestBadScopeForbidden(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, err := km.Create("reader", []string{"jobs:read"})
	if err != nil {
		t.Fatal(err)
	}
	// POST /jobs requires jobs:write
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"s3://x/m.mp4","profile":"vod-h264"}`, raw)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateAndGetJob(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("writer", []string{"jobs:read", "jobs:write", "profiles:read"})
	// unknown profile → 400 unknown_profile
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"s3://x/m.mp4","profile":"nope"}`, raw)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "unknown_profile") {
		t.Fatalf("want unknown_profile 400, got %d %s", rr.Code, rr.Body.String())
	}
	// valid create → 201
	rr = doRequest(t, srv, "POST", "/jobs", `{"input_ref":"s3://x/movie.mp4","profile":"vod-h264","priority":"normal"}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)
	if created["status"] != "queued" {
		t.Fatalf("status = %v", created["status"])
	}
	tasks, _ := created["tasks"].([]any)
	if len(tasks) < 4 {
		t.Fatalf("expected >=4 tasks, got %d", len(tasks))
	}
	// get it back
	rr = doRequest(t, srv, "GET", "/jobs/"+id, "", raw)
	if rr.Code != 200 {
		t.Fatalf("get = %d: %s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got["status"] != "queued" {
		t.Fatalf("got status %v", got["status"])
	}
}

func TestInvalidPriority(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"vod-h264","priority":"urgent"}`, raw)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "invalid_priority") {
		t.Fatalf("want invalid_priority 400, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestJobLangAppliedToSubtitleTask(t *testing.T) {
	srv, store, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"vod-h264","lang":"fa"}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := core.JobID(created["id"].(string))

	tasks, err := store.ListTasks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tk := range tasks {
		if tk.Kind == "subtitle" {
			found = true
			if got, _ := tk.Params["lang"].(string); got != "fa" {
				t.Errorf("subtitle task lang = %q, want fa", got)
			}
		}
	}
	if !found {
		t.Fatal("vod-h264 profile has no subtitle task")
	}
}

// TestAutoAISubtitleDependsOnRealTask guards against the ai_subtitle task
// being wired to a task kind that doesn't actually exist in the profile
// (e.g. "video_encode", which vod-h264/vod-hevc never produce — the
// encode+package step is called "hls"). A dependency on a missing task ID
// can never be satisfied, so the job wedges forever with subtitle/upload
// stuck pending right after hls+thumbnail finish.
func TestAutoAISubtitleDependsOnRealTask(t *testing.T) {
	srv, store, km := newTestServer(t)
	// newTestServer's cfg comes from cfg.Default(), which has
	// AI.AutoGenerate.Enabled = true, so vod-h264 (which has a subtitle
	// task) should auto-insert an ai_subtitle task ahead of it.
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"vod-h264"}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := core.JobID(created["id"].(string))

	tasks, err := store.ListTasks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[core.TaskID]core.Task{}
	for _, tk := range tasks {
		byID[tk.ID] = tk
	}

	var ai, sub *core.Task
	for i := range tasks {
		switch tasks[i].Kind {
		case "ai_subtitle":
			ai = &tasks[i]
		case "subtitle":
			sub = &tasks[i]
		}
	}
	if ai == nil {
		t.Fatal("vod-h264 with auto_generate on should have an ai_subtitle task")
	}
	if len(ai.DependsOn) == 0 {
		t.Fatal("ai_subtitle task has no dependencies; it will never become ready")
	}
	for _, dep := range ai.DependsOn {
		if dep == "" {
			t.Fatal("ai_subtitle depends on an empty task ID; it can never be satisfied")
		}
		if _, ok := byID[dep]; !ok {
			t.Fatalf("ai_subtitle depends on %q, which is not a task in this job", dep)
		}
	}
	if sub == nil {
		t.Fatal("vod-h264 profile has no subtitle task")
	}
	found := false
	for _, dep := range sub.DependsOn {
		if dep == ai.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("subtitle task does not depend on the inserted ai_subtitle task")
	}
}

func TestJobProviderAppliedToAISubtitleTask(t *testing.T) {
	srv, store, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"ai-subtitle","lang":"fa","provider":"hybrid"}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := core.JobID(created["id"].(string))

	tasks, err := store.ListTasks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tk := range tasks {
		if tk.Kind == "ai_subtitle" {
			found = true
			if got, _ := tk.Params["provider"].(string); got != "hybrid" {
				t.Errorf("ai_subtitle provider = %q, want hybrid", got)
			}
			if got, _ := tk.Params["lang"].(string); got != "fa" {
				t.Errorf("ai_subtitle lang = %q, want fa", got)
			}
		}
	}
	if !found {
		t.Fatal("ai-subtitle profile has no ai_subtitle task")
	}
}

func TestJobUnknownProviderRejected(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"ai-subtitle","provider":"nope"}`, raw)
	if rr.Code == 201 {
		t.Fatalf("create with bogus provider should fail, got 201")
	}
}

func TestJobSrcLangAppliedToAISubtitleTask(t *testing.T) {
	srv, store, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"ai-subtitle","lang":"fa","src_lang":"tr"}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := core.JobID(created["id"].(string))

	tasks, err := store.ListTasks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tk := range tasks {
		if tk.Kind == "ai_subtitle" {
			found = true
			if got, _ := tk.Params["src_lang"].(string); got != "tr" {
				t.Errorf("ai_subtitle src_lang = %q, want tr", got)
			}
			if got, _ := tk.Params["lang"].(string); got != "fa" {
				t.Errorf("ai_subtitle lang = %q, want fa", got)
			}
		}
	}
	if !found {
		t.Fatal("ai-subtitle profile has no ai_subtitle task")
	}
}

func TestJobActionCancel(t *testing.T) {	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"vod-h264"}`, raw)
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)

	rr = doRequest(t, srv, "POST", "/jobs/"+id+"/cancel", "", raw)
	if rr.Code != 200 {
		t.Fatalf("cancel = %d: %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, srv, "GET", "/jobs/"+id, "", raw)
	var got map[string]any
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got["status"] != "cancelled" {
		t.Fatalf("status = %v", got["status"])
	}
}

func TestJobPriorityUpdate(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"vod-h264","priority":"low"}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)

	rr = doRequest(t, srv, "PATCH", "/jobs/"+id+"/priority", `{"priority":"emergency"}`, raw)
	if rr.Code != 200 {
		t.Fatalf("priority update = %d: %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, srv, "GET", "/jobs/"+id, "", raw)
	var got map[string]any
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got["priority"] != "emergency" {
		t.Fatalf("priority = %v, want emergency", got["priority"])
	}

	// invalid priority value -> 400
	rr = doRequest(t, srv, "PATCH", "/jobs/"+id+"/priority", `{"priority":"urgent"}`, raw)
	if rr.Code != 400 {
		t.Fatalf("invalid priority = %d, want 400: %s", rr.Code, rr.Body.String())
	}

	// once running, priority can no longer change
	rr = doRequest(t, srv, "POST", "/jobs/"+id+"/cancel", "", raw)
	if rr.Code != 200 {
		t.Fatalf("cancel = %d: %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, srv, "PATCH", "/jobs/"+id+"/priority", `{"priority":"low"}`, raw)
	if rr.Code != http.StatusConflict {
		t.Fatalf("priority update on cancelled job = %d, want 409: %s", rr.Code, rr.Body.String())
	}
}

func TestJobEvents(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"vod-h264"}`, raw)
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)

	rr = doRequest(t, srv, "GET", "/jobs/"+id+"/events", "", raw)
	if rr.Code != 200 {
		t.Fatalf("events = %d", rr.Code)
	}
	var evs map[string]any
	json.Unmarshal(rr.Body.Bytes(), &evs)
	events, _ := evs["events"].([]any)
	if len(events) < 1 {
		t.Fatalf("expected >=1 event, got %d", len(events))
	}
}

func TestConfigExportImport(t *testing.T) {
	srv, _, km := newTestServer(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "weft.yaml")
	srv.cfgPath = cfgPath
	srv.cfg.Security.AdminAPIKey = "super-secret-admin-key"
	raw, _, _ := km.Create("w", []string{"config:manage"})

	rr := doRequest(t, srv, "GET", "/config/export", "", raw)
	if rr.Code != 200 {
		t.Fatalf("export = %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "network:") {
		t.Fatalf("export missing network section: %s", body)
	}
	if strings.Contains(body, "super-secret-admin-key") {
		t.Fatal("default export leaked admin_api_key")
	}
	if !strings.Contains(body, "<redacted>") {
		t.Fatal("default export should show a <redacted> placeholder for admin_api_key")
	}

	// re-importing the redacted export must be rejected, not silently wipe
	// the real secret on next restart.
	rr = doRequest(t, srv, "POST", "/config/import", body, raw)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("import of redacted config = %d, want 400: %s", rr.Code, rr.Body.String())
	}

	// include_secrets=true round-trips the real value.
	rr = doRequest(t, srv, "GET", "/config/export?include_secrets=true", "", raw)
	if rr.Code != 200 {
		t.Fatalf("export with secrets = %d: %s", rr.Code, rr.Body.String())
	}
	fullBody := rr.Body.String()
	if !strings.Contains(fullBody, "super-secret-admin-key") {
		t.Fatal("include_secrets=true export did not include the real admin_api_key")
	}
	rr = doRequest(t, srv, "POST", "/config/import", fullBody, raw)
	if rr.Code != 200 {
		t.Fatalf("import = %d: %s", rr.Code, rr.Body.String())
	}
	written, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "super-secret-admin-key") {
		t.Fatal("imported config was not written to cfgPath")
	}

	// a server with no configured path refuses import (no file to write to).
	srv2, _, km2 := newTestServer(t)
	raw2, _, _ := km2.Create("w", []string{"config:manage"})
	rr = doRequest(t, srv2, "POST", "/config/import", fullBody, raw2)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("import without cfgPath = %d, want 501: %s", rr.Code, rr.Body.String())
	}

	// malformed/invalid config is rejected before ever touching disk.
	rr = doRequest(t, srv, "POST", "/config/import", "network:\n  listen: \"\"\n", raw)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("import of invalid config = %d, want 400: %s", rr.Code, rr.Body.String())
	}
}

func TestCronEndpoints(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"cron:manage"})

	// no scheduler configured: empty list, not an error
	rr := doRequest(t, srv, "GET", "/cron", "", raw)
	if rr.Code != 200 {
		t.Fatalf("cron list (no scheduler) = %d: %s", rr.Code, rr.Body.String())
	}

	ran := 0
	sched := cron.New()
	if err := sched.Register(cron.Job{
		Name: "cleanup", Schedule: "0 3 * * *",
		Run: func(ctx context.Context) error { ran++; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	srv.SetCronScheduler(sched)

	rr = doRequest(t, srv, "GET", "/cron", "", raw)
	if rr.Code != 200 {
		t.Fatalf("cron list = %d: %s", rr.Code, rr.Body.String())
	}
	var listOut struct {
		Jobs []struct {
			Name     string `json:"name"`
			Schedule string `json:"schedule"`
		} `json:"jobs"`
	}
	json.Unmarshal(rr.Body.Bytes(), &listOut)
	if len(listOut.Jobs) != 1 || listOut.Jobs[0].Name != "cleanup" {
		t.Fatalf("cron list = %+v", listOut)
	}

	rr = doRequest(t, srv, "POST", "/cron/cleanup/run", "", raw)
	if rr.Code != 200 {
		t.Fatalf("cron run = %d: %s", rr.Code, rr.Body.String())
	}
	if ran != 1 {
		t.Fatalf("job ran %d times, want 1", ran)
	}

	rr = doRequest(t, srv, "POST", "/cron/nope/run", "", raw)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cron run unknown job = %d, want 404: %s", rr.Code, rr.Body.String())
	}
}

func TestWebhooksCRUD(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("wh", []string{"webhooks:manage"})

	rr := doRequest(t, srv, "POST", "/webhooks", `{"url":"https://a/b","events":["job.completed"]}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create webhook = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)

	rr = doRequest(t, srv, "GET", "/webhooks", "", raw)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), id) {
		t.Fatalf("list = %d %s", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, srv, "DELETE", "/webhooks/"+id, "", raw)
	if rr.Code != 200 {
		t.Fatalf("delete = %d", rr.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("m", []string{"metrics:read"})
	rr := doRequest(t, srv, "GET", "/metrics", "", raw)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "weft_jobs") {
		t.Fatalf("metrics = %d %s", rr.Code, rr.Body.String()[:min(120, rr.Body.Len())])
	}
}

func TestProfilesEndpoint(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("p", []string{"profiles:read"})
	rr := doRequest(t, srv, "GET", "/profiles", "", raw)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "vod-h264") {
		t.Fatalf("profiles = %d %s", rr.Code, rr.Body.String())
	}
}

func TestKeysCRUD(t *testing.T) {
	srv, _, km := newTestServer(t)
	admin, _, _ := km.Create("a", []string{"keys:manage"})
	rr := doRequest(t, srv, "POST", "/keys", `{"name":"x","scopes":["jobs:read"]}`, admin)
	if rr.Code != 201 {
		t.Fatalf("create key = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	raw := created["key"].(string)
	if !strings.HasPrefix(raw, "wft_live_") {
		t.Fatalf("raw key missing prefix: %q", raw)
	}
	// the new key must actually work for its granted scope: GET /jobs (jobs:read)
	rr = doRequest(t, srv, "GET", "/jobs", "", raw)
	if rr.Code != 200 {
		t.Fatalf("new key usage = %d: %s", rr.Code, rr.Body.String())
	}
	// but not for a scope it lacks
	rr = doRequest(t, srv, "GET", "/profiles", "", raw)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for un-scoped profile access, got %d", rr.Code)
	}
}

func TestBenchmarkEndpoint(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("m", []string{"metrics:read"})
	rr := doRequest(t, srv, "POST", "/benchmark", "", raw)
	if rr.Code != 201 {
		t.Fatalf("benchmark = %d: %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, srv, "GET", "/benchmark", "", raw)
	if rr.Code != 200 {
		t.Fatalf("get benchmark = %d", rr.Code)
	}
}

func TestUnknownDestination(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"vod-h264","destination_id":99}`, raw)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "unknown_destination") {
		t.Fatalf("want unknown_destination, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestUnknownSourceServer(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"movies/x.mp4","profile":"vod-h264","source_server_id":99}`, raw)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "unknown_source_server") {
		t.Fatalf("want unknown_source_server, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestJobCreateWithSourceServer(t *testing.T) {
	srv, store, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read", "storage:manage"})

	rr := doRequest(t, srv, "POST", "/storage/servers", `{"id":5,"type":"local","config":{"base_path":"./somewhere"}}`, raw)
	if rr.Code != 201 {
		t.Fatalf("register server = %d: %s", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, srv, "POST", "/jobs", `{"input_ref":"movies/x.mp4","profile":"vod-h264","source_server_id":5}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := core.JobID(created["id"].(string))

	job, err := store.LoadJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if job.SourceServerID != 5 {
		t.Fatalf("SourceServerID = %d, want 5", job.SourceServerID)
	}

	rr = doRequest(t, srv, "GET", "/jobs/"+string(id), "", raw)
	var got map[string]any
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got["source_server_id"] != float64(5) {
		t.Fatalf("GET /jobs/{id} source_server_id = %v, want 5", got["source_server_id"])
	}
}

// TestJobGetExposesDestPath guards against destination_id being visible
// without the subdirectory it was combined with: an operator debugging
// "where did my upload actually go" needs destination_id (which storage
// server) AND dest_path (which subdir under that server's base_path) — the
// API used to return only the former.
func TestJobGetExposesDestPath(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"x","profile":"vod-h264","path":"Movie-Test/17322419-2206-4ec0-8bef-b70cb9a6752d"}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)

	rr = doRequest(t, srv, "GET", "/jobs/"+id, "", raw)
	var got map[string]any
	json.Unmarshal(rr.Body.Bytes(), &got)
	want := "Movie-Test/17322419-2206-4ec0-8bef-b70cb9a6752d"
	if got["dest_path"] != want {
		t.Fatalf("GET /jobs/{id} dest_path = %v, want %q", got["dest_path"], want)
	}
}

// TestStorageListExposesBasePath guards against destination_id being
// meaningless without knowing the disk root it resolves to: base_path isn't
// a credential (unlike key_path/password/access_key/secret_key, which must
// stay hidden), so it should be visible alongside host/type.
func TestStorageListExposesBasePath(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"storage:manage"})
	rr := doRequest(t, srv, "POST", "/storage/servers", `{"id":7,"type":"local","config":{"base_path":"/var/videos"}}`, raw)
	if rr.Code != 201 {
		t.Fatalf("register server = %d: %s", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, srv, "GET", "/storage/servers", "", raw)
	if rr.Code != 200 {
		t.Fatalf("list = %d: %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Servers []map[string]any `json:"servers"`
	}
	json.Unmarshal(rr.Body.Bytes(), &got)
	found := false
	for _, sv := range got.Servers {
		if sv["id"] == float64(7) {
			found = true
			if sv["base_path"] != "/var/videos" {
				t.Errorf("base_path = %v, want /var/videos", sv["base_path"])
			}
		}
	}
	if !found {
		t.Fatal("registered server 7 not in /storage/servers list")
	}
}

func TestSystemEndpoint(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("m", []string{"metrics:read"})
	rr := doRequest(t, srv, "GET", "/system", "", raw)
	if rr.Code != 200 {
		t.Fatalf("system = %d: %s", rr.Code, rr.Body.String())
	}
	var snap map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap["num_cpu"] == nil {
		t.Errorf("system missing num_cpu: %s", rr.Body.String())
	}
	if snap["hostname"] == "" || snap["hostname"] == nil {
		t.Errorf("system missing hostname: %s", rr.Body.String())
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestRebuildMasterEndpoint(t *testing.T) {
	dir := t.TempDir()
	srv, _, km := newTestServer(t)
	srv.stbuild = func(ctx context.Context, destinationID int, destPath string) (core.Storage, error) {
		if destinationID != 0 {
			t.Fatalf("unexpected destination %d", destinationID)
		}
		return local.New(dir)
	}
	seed := []string{"360p.m3u8", "720p.m3u8", "1080p.m3u8", "subtitle/fa/x.vtt", "audio/tr/x.m3u8"}
	for _, f := range seed {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("#EXTM3U\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"destination_id":0,"path":""}`
	admin, _, _ := km.Create("admin", []string{"storage:manage"})
	rr := doRequest(t, srv, "POST", "/storage/rebuild-master", body, admin)
	if rr.Code != 200 {
		t.Fatalf("rebuild-master = %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Renditions []string `json:"renditions"`
		Subtitles  []struct {
			Lang string `json:"lang"`
		} `json:"subtitles"`
		Audios []struct {
			Lang string `json:"lang"`
		} `json:"audios"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Renditions) != 3 {
		t.Errorf("renditions = %v", resp.Renditions)
	}
	if len(resp.Subtitles) != 1 || resp.Subtitles[0].Lang != "fa" {
		t.Errorf("subtitles = %+v", resp.Subtitles)
	}
	if len(resp.Audios) != 1 || resp.Audios[0].Lang != "tr" {
		t.Errorf("audios = %+v", resp.Audios)
	}
	b, err := os.ReadFile(filepath.Join(dir, "playlist.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `SUBTITLES="subs"`) {
		t.Errorf("master missing subtitle group:\n%s", b)
	}
}

func TestDeleteJob(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:read", "jobs:write"})

	// create + cancel so the job is in a deletable (terminal) state
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"s3://x/movie.mp4","profile":"vod-h264"}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)
	rr = doRequest(t, srv, "POST", "/jobs/"+id+"/cancel", `{}`, raw)
	if rr.Code != 200 {
		t.Fatalf("cancel = %d: %s", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, srv, "DELETE", "/jobs/"+id, "", raw)
	if rr.Code != 200 {
		t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, srv, "GET", "/jobs/"+id, "", raw)
	if rr.Code != 404 {
		t.Fatalf("get after delete = %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDeleteJobPurgesStorageFiles is the regression test for DeleteJob only
// clearing DB rows: before the fix, a job's published assets stayed on
// destination storage forever after DELETE /jobs/{id}. Now the handler calls
// Storage.Delete for every recorded output before removing the DB rows.
func TestDeleteJobPurgesStorageFiles(t *testing.T) {
	dir := t.TempDir()
	srv, store, km := newTestServer(t)
	srv.stbuild = func(ctx context.Context, destinationID int, destPath string) (core.Storage, error) {
		return local.New(dir)
	}
	raw, _, _ := km.Create("w", []string{"jobs:read", "jobs:write"})

	j := core.Job{ID: "jpurge", Status: core.JobCancelled, Priority: core.PriorityNormal, Profile: "vod-h264"}
	if err := store.SaveJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	thumbDir := filepath.Join(dir, "thumbnails")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(thumbDir, "movie_thumb_01.jpg")
	if err := os.WriteFile(assetPath, []byte("fake-jpeg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTaskOutputs(context.Background(), "task1", "jpurge", []core.AssetRef{
		{Kind: "thumbnail", Name: "movie_thumb_01.jpg", URI: "local:thumbnails/movie_thumb_01.jpg", Dir: "thumbnails"},
	}); err != nil {
		t.Fatal(err)
	}

	rr := doRequest(t, srv, "DELETE", "/jobs/jpurge", "", raw)
	if rr.Code != 200 {
		t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(assetPath); !os.IsNotExist(err) {
		t.Fatalf("asset file still exists after delete: err=%v", err)
	}
}

// TestDeleteJobPurgeFilesOptOut verifies ?purge_files=false skips storage
// deletion (e.g. when the destination path is shared with something else).
func TestDeleteJobPurgeFilesOptOut(t *testing.T) {
	dir := t.TempDir()
	srv, store, km := newTestServer(t)
	srv.stbuild = func(ctx context.Context, destinationID int, destPath string) (core.Storage, error) {
		return local.New(dir)
	}
	raw, _, _ := km.Create("w", []string{"jobs:read", "jobs:write"})

	j := core.Job{ID: "jkeep", Status: core.JobCancelled, Priority: core.PriorityNormal, Profile: "vod-h264"}
	if err := store.SaveJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(dir, "keep.jpg")
	if err := os.WriteFile(assetPath, []byte("keep-me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTaskOutputs(context.Background(), "task1", "jkeep", []core.AssetRef{
		{Kind: "thumbnail", Name: "keep.jpg", URI: "local:keep.jpg"},
	}); err != nil {
		t.Fatal(err)
	}

	rr := doRequest(t, srv, "DELETE", "/jobs/jkeep?purge_files=false", "", raw)
	if rr.Code != 200 {
		t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(assetPath); err != nil {
		t.Fatalf("asset file should survive purge_files=false, stat err=%v", err)
	}
}

func TestDeleteActiveJobConflict(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:read", "jobs:write"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"s3://x/movie.mp4","profile":"vod-h264"}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)

	// queued job cannot be deleted; cancel first
	rr = doRequest(t, srv, "DELETE", "/jobs/"+id, "", raw)
	if rr.Code != 409 || !strings.Contains(rr.Body.String(), "job_active") {
		t.Fatalf("want job_active 409, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestTaskLogEndpoint(t *testing.T) {
	srv, store, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:read", "jobs:write"})
	rr := doRequest(t, srv, "POST", "/jobs", `{"input_ref":"s3://x/movie.mp4","profile":"vod-h264"}`, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := core.JobID(created["id"].(string))
	tasks, _ := store.ListTasks(context.Background(), id)
	if len(tasks) == 0 {
		t.Fatal("no tasks")
	}
	taskID := tasks[0].ID
	if err := store.SaveTaskLog(context.Background(), taskID, id, "ffmpeg stderr tail"); err != nil {
		t.Fatal(err)
	}

	rr = doRequest(t, srv, "GET", "/jobs/"+string(id)+"/tasks/"+string(taskID)+"/log", "", raw)
	if rr.Code != 200 {
		t.Fatalf("log = %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ffmpeg stderr tail") {
		t.Fatalf("log body = %s", rr.Body.String())
	}
	// missing task → 404
	rr = doRequest(t, srv, "GET", "/jobs/"+string(id)+"/tasks/nope/log", "", raw)
	if rr.Code != 404 {
		t.Fatalf("missing log = %d", rr.Code)
	}
}

func TestScaleWorkersEndpoint(t *testing.T) {
	srv, _, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"workers:write"})
	rr := doRequest(t, srv, "POST", "/workers/scale", `{"count":0}`, raw)
	if rr.Code != 501 {
		t.Fatalf("want 501 when scaler nil, got %d: %s", rr.Code, rr.Body.String())
	}

	// with a scaler wired, count validation kicks in first
	mock := &mockScaler{}
	srv.scaler = mock
	rr = doRequest(t, srv, "POST", "/workers/scale", `{"count":0}`, raw)
	if rr.Code != 400 {
		t.Fatalf("want 400 for count=0, got %d: %s", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, srv, "POST", "/workers/scale", `{"count":4}`, raw)
	if rr.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if mock.count != 4 {
		t.Fatalf("scaler called with %d, want 4", mock.count)
	}
}

type mockScaler struct{ count int }

func (m *mockScaler) Scale(ctx context.Context, count int) error {
	m.count = count
	return nil
}

func TestCreateJobTrimAndThumbParams(t *testing.T) {
	srv, store, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read"})
	body := `{"input_ref":"s3://x/movie.mp4","profile":"vod-h264","trim_start":50,"trim_end":10,"thumb_count":5,"thumb_size":"1080x1080"}`
	rr := doRequest(t, srv, "POST", "/jobs", body, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := core.JobID(created["id"].(string))
	tasks, err := store.ListTasks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		switch tk.Kind {
		case "hls", "thumbnail":
			if tk.Params["trim_start"] != float64(50) {
				t.Errorf("%s task trim_start = %v, want 50", tk.Kind, tk.Params["trim_start"])
			}
			if tk.Params["trim_end"] != float64(10) {
				t.Errorf("%s task trim_end = %v, want 10", tk.Kind, tk.Params["trim_end"])
			}
		}
		if tk.Kind == "thumbnail" {
			if tk.Params["thumb_count"] != float64(5) || tk.Params["thumb_size"] != "1080x1080" {
				t.Errorf("thumbnail params = %v", tk.Params)
			}
		}
	}
}

// TestTrimUpdateProfile verifies the post-hoc trim-update profile builds a
// hls->upload graph and picks up trim_start/trim_end exactly like any other
// profile's hls task — it reuses buildTasks' existing trim wiring, no
// separate trim engine for "already published" vs "at creation time".
func TestTrimUpdateProfile(t *testing.T) {
	srv, store, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:write", "jobs:read"})
	body := `{"input_ref":"movie.mp4","profile":"trim-update","name":"movie","trim_start":30,"trim_end":5}`
	rr := doRequest(t, srv, "POST", "/jobs", body, raw)
	if rr.Code != 201 {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := core.JobID(created["id"].(string))
	tasks, err := store.ListTasks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]core.Task{}
	for _, tk := range tasks {
		kinds[tk.Kind] = tk
	}
	if _, ok := kinds["thumbnail"]; ok {
		t.Fatal("trim-update must not include a thumbnail task")
	}
	if _, ok := kinds["subtitle"]; ok {
		t.Fatal("trim-update must not include a subtitle task")
	}
	hlsTask, ok := kinds["hls"]
	if !ok {
		t.Fatal("trim-update must include an hls task")
	}
	if hlsTask.Params["trim_start"] != float64(30) || hlsTask.Params["trim_end"] != float64(5) {
		t.Fatalf("hls trim params = %v", hlsTask.Params)
	}
	if hlsTask.Params["name"] != "movie" {
		t.Fatalf("hls name param = %v, want movie", hlsTask.Params["name"])
	}
	uploadTask, ok := kinds["upload"]
	if !ok {
		t.Fatal("trim-update must include an upload task")
	}
	found := false
	for _, dep := range uploadTask.DependsOn {
		if dep == hlsTask.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("upload task must depend on the hls task")
	}
}

func TestGetAssetBase64(t *testing.T) {
	dir := t.TempDir()
	srv, store, km := newTestServer(t)
	srv.stbuild = func(ctx context.Context, destinationID int, destPath string) (core.Storage, error) {
		return local.New(dir)
	}
	raw, _, _ := km.Create("w", []string{"jobs:read"})
	j := core.Job{ID: "jasset", Status: core.JobCompleted, Priority: core.PriorityNormal, Profile: "vod-h264"}
	if err := store.SaveJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	// asset lives in the storage dir as thumbnails/movie_thumb_01.jpg
	thumbDir := filepath.Join(dir, "thumbnails")
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("fake-jpeg-bytes")
	if err := os.WriteFile(filepath.Join(thumbDir, "movie_thumb_01.jpg"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTaskOutputs(context.Background(), "task1", "jasset", []core.AssetRef{
		{Kind: "thumbnail", Name: "movie_thumb_01.jpg", URI: "local:thumbnails/movie_thumb_01.jpg", Dir: "thumbnails"},
	}); err != nil {
		t.Fatal(err)
	}

	rr := doRequest(t, srv, "GET", "/jobs/jasset/assets/movie_thumb_01.jpg", "", raw)
	if rr.Code != 200 {
		t.Fatalf("asset = %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Name string `json:"name"`
		Mime string `json:"mime"`
		Data string `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &out)
	if out.Name != "movie_thumb_01.jpg" {
		t.Errorf("name = %q", out.Name)
	}
	if out.Mime != "image/jpeg" {
		t.Errorf("mime = %q, want image/jpeg", out.Mime)
	}
	want := base64.StdEncoding.EncodeToString(content)
	if out.Data != want {
		t.Errorf("data mismatch")
	}
	// unknown asset -> 404
	rr = doRequest(t, srv, "GET", "/jobs/jasset/assets/nope.jpg", "", raw)
	if rr.Code != 404 {
		t.Fatalf("missing asset = %d", rr.Code)
	}
}

// TestGetAssetTooLarge is the regression test for the unbounded base64 asset
// response: before the fix, handleGetAsset would read and base64-encode an
// asset of any size into one JSON response. Now an asset whose recorded
// Bytes exceeds maxAssetBytes is rejected with 413 before it's even opened.
func TestGetAssetTooLarge(t *testing.T) {
	dir := t.TempDir()
	srv, store, km := newTestServer(t)
	srv.stbuild = func(ctx context.Context, destinationID int, destPath string) (core.Storage, error) {
		return local.New(dir)
	}
	raw, _, _ := km.Create("w", []string{"jobs:read"})
	j := core.Job{ID: "jbig", Status: core.JobCompleted, Priority: core.PriorityNormal, Profile: "vod-h264"}
	if err := store.SaveJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	// the file itself doesn't need to actually be huge — the recorded Bytes
	// size is what the size check uses to reject before ever opening it.
	if err := os.WriteFile(filepath.Join(dir, "huge.ts"), []byte("small-on-disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTaskOutputs(context.Background(), "task1", "jbig", []core.AssetRef{
		{Kind: "video", Name: "huge.ts", URI: "local:huge.ts", Bytes: maxAssetBytes + 1},
	}); err != nil {
		t.Fatal(err)
	}

	rr := doRequest(t, srv, "GET", "/jobs/jbig/assets/huge.ts", "", raw)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized asset = %d: %s, want 413", rr.Code, rr.Body.String())
	}
}

func TestGetJobIncludesAssets(t *testing.T) {
	srv, store, km := newTestServer(t)
	raw, _, _ := km.Create("w", []string{"jobs:read"})
	j := core.Job{ID: "jassets2", Status: core.JobCompleted, Priority: core.PriorityNormal, Profile: "vod-h264"}
	if err := store.SaveJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTaskOutputs(context.Background(), "task1", "jassets2", []core.AssetRef{
		{Kind: "thumbnail", Name: "movie_poster.jpg", URI: "local:thumbnails/movie_poster.jpg", Dir: "thumbnails", Bytes: 42},
	}); err != nil {
		t.Fatal(err)
	}
	rr := doRequest(t, srv, "GET", "/jobs/jassets2", "", raw)
	if rr.Code != 200 {
		t.Fatalf("get = %d", rr.Code)
	}
	var out struct {
		Assets []map[string]any `json:"assets"`
	}
	json.Unmarshal(rr.Body.Bytes(), &out)
	if len(out.Assets) != 1 || out.Assets[0]["name"] != "movie_poster.jpg" {
		t.Fatalf("assets = %v", out.Assets)
	}
	if out.Assets[0]["bytes"] != float64(42) {
		t.Fatalf("asset bytes = %v", out.Assets[0]["bytes"])
	}
}

var _ = time.Now
