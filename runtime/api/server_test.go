package api

import (
	"bytes"
	"context"
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

var _ = time.Now
