package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	"github.com/mohammadjaf013/weft/plugins/storage/local"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

// TestResolveWorkerCountAuto is the regression test for "workers.max: 0 =
// auto" silently meaning "use workers.min" — before the fix, a bigger host
// got exactly as many workers as a laptop unless max was set explicitly.
func TestResolveWorkerCountAuto(t *testing.T) {
	cases := []struct {
		name           string
		min, max, cpu  int
		want           int
	}{
		{"auto on an 8-core box", 1, 0, 8, 8},
		{"auto respects a higher min", 4, 0, 2, 4},
		{"explicit max wins over auto", 1, 3, 8, 3},
		{"max below min falls back to min", 4, 2, 8, 4},
		{"min defaults to 1 when unset", 0, 0, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveWorkerCount(c.min, c.max, c.cpu)
			if got != c.want {
				t.Errorf("resolveWorkerCount(min=%d, max=%d, cpu=%d) = %d, want %d", c.min, c.max, c.cpu, got, c.want)
			}
		})
	}
}

func TestResolveInputLocal(t *testing.T) {
	d := &Daemon{}
	f := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{f, "local:" + f} {
		got, err := d.resolveInput(context.Background(), core.Job{InputRef: ref})
		if err != nil {
			t.Fatalf("%q: %v", ref, err)
		}
		if got != f {
			t.Fatalf("%q resolved to %q, want %q", ref, got, f)
		}
	}
}

func TestResolveInputErrors(t *testing.T) {
	d := &Daemon{}
	cases := []struct {
		name string
		job  core.Job
	}{
		{"empty", core.Job{InputRef: ""}},
		{"remote", core.Job{InputRef: "s3://bucket/movie.mp4"}},
		{"remote-ssh", core.Job{InputRef: "ssh://host/movie.mp4"}},
		{"missing", core.Job{InputRef: filepath.Join(t.TempDir(), "nope.mp4")}},
	}
	for _, c := range cases {
		if _, err := d.resolveInput(context.Background(), c.job); err == nil {
			t.Errorf("%s: expected error for %q", c.name, c.job.InputRef)
		}
	}
}

// TestResolveInputFromSourceServer is the regression test for D1: a job with
// SourceServerID set fetches InputRef (a relative path) from that REGISTERED
// storage server into a local cache, instead of resolveInput rejecting it
// outright. Uses a "local" type server as a stand-in for a remote one — it's
// backend-agnostic beyond that, ssh/s3 go through the exact same
// core.Storage.Open call.
func TestResolveInputFromSourceServer(t *testing.T) {
	store, err := sqlite.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	remoteDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remoteDir, "movies"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("source-bytes")
	if err := os.WriteFile(filepath.Join(remoteDir, "movies", "foo.mp4"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStorageServer(ctx, sqlite.StorageServer{
		ID: 5, Type: "local",
		Config: map[string]any{"base_path": remoteDir},
	}); err != nil {
		t.Fatal(err)
	}

	oldRoot := mediautil.WorkRoot
	mediautil.WorkRoot = t.TempDir()
	defer func() { mediautil.WorkRoot = oldRoot }()

	d := &Daemon{store: store, cfg: cfg.Default()}
	job := core.Job{ID: "jsrc", SourceServerID: 5, InputRef: "movies/foo.mp4"}
	got, err := d.resolveInput(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Fatalf("cached content = %q, want %q", data, content)
	}
	if !strings.Contains(filepath.ToSlash(got), "cache/jsrc") {
		t.Fatalf("cache path %q doesn't look like a per-job cache dir", got)
	}
}

func TestResolveInputFromSourceServerUnregistered(t *testing.T) {
	store, err := sqlite.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d := &Daemon{store: store, cfg: cfg.Default()}
	job := core.Job{ID: "jbad", SourceServerID: 99, InputRef: "movies/foo.mp4"}
	if _, err := d.resolveInput(context.Background(), job); err == nil {
		t.Fatal("expected error for an unregistered source_server_id")
	}
}

// TestResolveInputHTTP verifies a direct http(s):// InputRef (no
// source_server_id) is fetched with a plain GET into the local cache.
func TestResolveInputHTTP(t *testing.T) {
	content := []byte("http-source-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	oldRoot := mediautil.WorkRoot
	mediautil.WorkRoot = t.TempDir()
	defer func() { mediautil.WorkRoot = oldRoot }()

	d := &Daemon{cfg: cfg.Default()}
	job := core.Job{ID: "jhttp", InputRef: srv.URL + "/movie.mp4"}
	got, err := d.resolveInput(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Fatalf("cached content = %q, want %q", data, content)
	}
}

func TestResolveInputHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	oldRoot := mediautil.WorkRoot
	mediautil.WorkRoot = t.TempDir()
	defer func() { mediautil.WorkRoot = oldRoot }()

	d := &Daemon{cfg: cfg.Default()}
	job := core.Job{ID: "jerr", InputRef: srv.URL + "/nope.mp4"}
	if _, err := d.resolveInput(context.Background(), job); err == nil {
		t.Fatal("expected error for a 404 response")
	}
}

func TestStorageForLocalWithDestPath(t *testing.T) {
	base := filepath.Join(t.TempDir(), "out")
	c := cfg.Default()
	c.Storage.Local.BasePath = base
	d := &Daemon{cfg: c}
	for _, c := range []struct {
		path string
		want string
	}{
		{"", "movie.mp4"},
		{"series", "series/movie.mp4"},
		{"movie/", "movie/movie.mp4"},
	} {
		st, err := d.storageFor(core.Job{DestinationID: 0, DestPath: c.path})
		if err != nil {
			t.Fatalf("path %q: %v", c.path, err)
		}
		lst, ok := st.(*local.Storage)
		if !ok {
			t.Fatalf("path %q: storage type %T, want *local.Storage", c.path, st)
		}
		if got := lst.ResolveName("movie.mp4"); got != filepath.Join(base, c.want) {
			t.Errorf("path %q: resolve = %q, want %q", c.path, got, filepath.Join(base, c.want))
		}
	}
}
