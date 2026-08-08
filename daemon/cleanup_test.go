package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

func TestSourcePath(t *testing.T) {
	cases := map[string]string{
		"/abs/movie.mp4":    filepath.FromSlash("/abs/movie.mp4"),
		"local:/abs/m.mp4":  filepath.FromSlash("/abs/m.mp4"),
		"local:/x/../y.mp4": filepath.FromSlash("/y.mp4"),
		"s3://in/movie.mp4": "",
		"ssh://h/movie.mp4": "",
		"http://h/movie":    "",
		"rel/path.mp4":      "",
		"":                  "",
	}
	for in, want := range cases {
		if got := sourcePath(in); got != want {
			t.Errorf("sourcePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanerDeletesSourceAndWorkDirs(t *testing.T) {
	root := t.TempDir()
	store, err := sqlite.Open(filepath.Join(root, "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	// source file + per-task work dir on disk
	src := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(src, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	workRoot := filepath.Join(root, "workroot")
	workDir := filepath.Join(workRoot, "work", "task_hls1")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "seg.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveTask(ctx, core.Task{ID: "task_hls1", JobID: "j1", Kind: "hls"}); err != nil {
		t.Fatal(err)
	}
	// persist the job so the cleaner can load DeleteSource; use a unix-style
	// local ref so sourcePath behaves the same on every OS
	if err := store.SaveJob(ctx, core.Job{ID: "j1", Status: core.JobCompleted, InputRef: "local:" + filepath.ToSlash(src), DeleteSource: true}); err != nil {
		t.Fatal(err)
	}

	c := &cleaner{store: store, workRoot: workRoot}
	c.cleanupJob(ctx, "j1")

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source not deleted: %v", err)
	}
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Errorf("work dir not deleted: %v", err)
	}
}

func TestCleanerSkipsRemoteSource(t *testing.T) {
	root := t.TempDir()
	store, err := sqlite.Open(filepath.Join(root, "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SaveJob(ctx, core.Job{ID: "j2", Status: core.JobCompleted, InputRef: "s3://in/movie.mp4", DeleteSource: true}); err != nil {
		t.Fatal(err)
	}
	c := &cleaner{store: store, workRoot: filepath.Join(root, "wr")}
	c.cleanupJob(ctx, "j2")
	// no panic; nothing local to delete
}
