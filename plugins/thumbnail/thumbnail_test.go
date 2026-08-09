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

func TestProcessCustomThumbCount(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t3",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params: map[string]any{
			"thumb_count": float64(5),
			"thumb_size":  "1080x1080",
		},
		Executor: fake,
	}
	work := mediautil.WorkDir(in)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s/movie_thumb_%02d.jpg", work, i), []byte("jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) != 5 {
		t.Fatalf("custom mode should produce exactly 5 thumbnails, got %d", len(out.Assets))
	}
	for _, a := range out.Assets {
		if a.Kind != "thumbnail" || a.Dir != "thumbnails" {
			t.Errorf("asset = %+v, want kind=thumbnail dir=thumbnails", a)
		}
	}
	args := fake.RecordedArgs()
	if len(args) != 1 {
		t.Fatalf("custom mode should run a single ffmpeg, got %d", len(args))
	}
	var scale, frames bool
	for _, a := range args[0] {
		if strings.Contains(a, "scale=1080:1080") {
			scale = true
		}
		if a == "-frames:v" {
			frames = true
		}
	}
	if !scale {
		t.Errorf("custom size missing scale=1080:1080: %v", args[0])
	}
	if !frames {
		t.Errorf("custom count missing -frames:v: %v", args[0])
	}
}

func TestProcessCustomThumbOriginalSize(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t4",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params: map[string]any{
			"thumb_count": float64(1),
			"thumb_size":  "original",
		},
		Executor: fake,
	}
	work := mediautil.WorkDir(in)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(work+"/movie_thumb_01.jpg", []byte("jpeg"), 0o644)
	if _, err := (&Plugin{}).Process(context.Background(), in); err != nil {
		t.Fatalf("Process: %v", err)
	}
	args := fake.RecordedArgs()[0]
	for _, a := range args {
		if strings.Contains(a, "scale=") {
			t.Errorf("original size must not scale: %v", args)
		}
	}
}

func TestProcessTrimCoordinated(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	// MediaInfo with a known duration so TrimFromParams can compute the window.
	fake := ffexec.NewFake(core.Result{ExitCode: 0, MediaInfo: core.MediaInfo{HasVideo: true, DurationSec: 100}}, nil)
	in := core.TaskInput{
		TaskID:   "t5",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params: map[string]any{
			"trim_start": float64(50),
			"trim_end":   float64(10),
		},
		Executor: fake,
	}
	work := mediautil.WorkDir(in)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(work+"/movie_poster.jpg", []byte("jpeg"), 0o644)
	if _, err := (&Plugin{}).Process(context.Background(), in); err != nil {
		t.Fatalf("Process: %v", err)
	}
	// every default ffmpeg run must carry the -ss seek and -t window
	args := fake.RecordedArgs()
	if len(args) < 3 {
		t.Fatalf("expected >=3 execs, got %d", len(args))
	}
	for _, a := range args {
		if !containsAny(a, "-ss", "50.000") {
			t.Errorf("trimmed run missing -ss 50.000: %v", a)
		}
		if !containsAny(a, "-t", "40.000") {
			t.Errorf("trimmed run missing -t 40.000: %v", a)
		}
	}
}

func containsAny(xs []string, want, want2 string) bool {
	for _, x := range xs {
		if x == want || x == want2 {
			return true
		}
	}
	return false
}
