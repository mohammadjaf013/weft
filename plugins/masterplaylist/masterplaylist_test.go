package masterplaylist

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

func TestProcessGeneratesMasterPlaylist(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/movie.mp4",
		Params: map[string]any{
			"renditions": []map[string]any{
				{"uri": "movie/720p/movie.m3u8", "bandwidth": "3000000", "resolution": "1280x720"},
				{"uri": "movie/1080p/movie.m3u8", "bandwidth": "6000000", "resolution": "1920x1080"},
			},
		},
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(out.Assets))
	}
	if out.Assets[0].Kind != "playlist" {
		t.Errorf("asset kind = %q, want playlist", out.Assets[0].Kind)
	}
	b, err := os.ReadFile(strings.TrimPrefix(out.Assets[0].URI, "local:"))
	if err != nil {
		t.Fatalf("read playlist: %v", err)
	}
	content := string(b)
	for _, want := range []string{"#EXTM3U", "BANDWIDTH=3000000", "1280x720", "BANDWIDTH=6000000", "1920x1080", "movie/1080p/movie.m3u8"} {
		if !strings.Contains(content, want) {
			t.Errorf("playlist missing %q:\n%s", want, content)
		}
	}
}

func TestProcessDefaultRendition(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	out, err := (&Plugin{}).Process(context.Background(), core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/movie.mp4",
		Params:   map[string]any{},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	b, _ := os.ReadFile(strings.TrimPrefix(out.Assets[0].URI, "local:"))
	if !strings.Contains(string(b), "BANDWIDTH=3000000") {
		t.Errorf("default rendition missing:\n%s", string(b))
	}
}
