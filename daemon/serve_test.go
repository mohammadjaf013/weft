package daemon

import (
	"os"
	"path/filepath"
	"testing"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/storage/local"
)

func TestResolveInputLocal(t *testing.T) {
	d := &Daemon{}
	f := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{f, "local:" + f} {
		got, err := d.resolveInput(core.Job{InputRef: ref})
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
		if _, err := d.resolveInput(c.job); err == nil {
			t.Errorf("%s: expected error for %q", c.name, c.job.InputRef)
		}
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
