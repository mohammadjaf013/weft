package thumbnail

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	ffexec "github.com/mohammadjaf013/weft/runtime/executor/ffmpeg"
)

func TestProcessProducesPosterSpriteVTT(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params:   map[string]any{},
		Executor: fake,
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) < 3 {
		t.Fatalf("expected >=3 assets, got %d", len(out.Assets))
	}
	if out.Assets[0].Kind != "thumbnail" {
		t.Errorf("asset[0].Kind = %q, want thumbnail", out.Assets[0].Kind)
	}
	if out.Assets[1].Kind != "sprite" {
		t.Errorf("asset[1].Kind = %q, want sprite", out.Assets[1].Kind)
	}
	if out.Assets[2].Kind != "vtt" {
		t.Errorf("asset[2].Kind = %q, want vtt", out.Assets[2].Kind)
	}
	args := fake.RecordedArgs()
	if len(args) != 3 {
		t.Fatalf("expected 3 ffmpeg execs, got %d", len(args))
	}
	// The generated VTT must actually exist on disk.
	if _, err := os.Stat(mediautil.WorkDir(in) + "/movie_preview.vtt"); err != nil {
		t.Errorf("generated vtt missing: %v", err)
	}
}

func TestProcessWithRealStills(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t2",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params:   map[string]any{},
		Executor: fake,
	}
	// Simulate ffmpeg having produced numbered stills.
	work := mediautil.WorkDir(in)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s/movie_%03d.jpg", work, i), []byte("jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// Every still jpg must be reported as an asset in thumbnails/.
	var stills []core.AssetRef
	for _, a := range out.Assets {
		if a.Kind == "thumbnail" && a.Dir == "thumbnails" && a.Name != "movie_poster.jpg" {
			stills = append(stills, a)
		}
	}
	if len(stills) == 0 {
		t.Fatalf("expected per-second stills in assets, got none")
	}
	// The stills argv must write numbered jpgs (movie_001.jpg style).
	found := false
	for _, args := range fake.RecordedArgs() {
		for _, a := range args {
			if strings.Contains(a, "movie_%03d.jpg") {
				found = true
			}
		}
	}
	if !found {
		t.Error("stills argv missing numbered jpg pattern")
	}
}
