package posterupload

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/storage/local"
)

func TestProcessCopiesImageToPoster(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "cover.jpg")
	content := []byte("fake-jpeg-bytes")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	destDir := t.TempDir()
	st, err := local.New(destDir)
	if err != nil {
		t.Fatal(err)
	}

	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "cover.jpg",
		InputURI: "local:" + src,
		Params:   map[string]any{"name": "movie"},
		Storage:  st,
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(out.Assets))
	}
	a := out.Assets[0]
	if a.Name != "movie_poster.jpg" || a.Dir != "thumbnails" {
		t.Errorf("asset = %+v, want name=movie_poster.jpg dir=thumbnails", a)
	}
	got, err := os.ReadFile(filepath.Join(destDir, "thumbnails", "movie_poster.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("uploaded content = %q, want %q", got, content)
	}
}

func TestProcessRequiresInputAndStorage(t *testing.T) {
	if _, err := (&Plugin{}).Process(context.Background(), core.TaskInput{}); err == nil {
		t.Fatal("expected error for missing InputURI")
	}
	if _, err := (&Plugin{}).Process(context.Background(), core.TaskInput{InputURI: "local:x.jpg"}); err == nil {
		t.Fatal("expected error for missing Storage")
	}
}
