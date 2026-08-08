package subtitle

import (
	"context"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	ffexec "github.com/mohammadjaf013/weft/runtime/executor/ffmpeg"
)

func TestProcessConvertsAndPackages(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{
		ExitCode:  0,
		MediaInfo: core.MediaInfo{HasSubtitles: true},
	}, nil)
	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/movie.srt",
		InputURI: "local:work/movie.srt",
		Params:   map[string]any{},
		Executor: fake,
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(out.Assets))
	}
	if out.Assets[0].Kind != "subtitle" || out.Assets[0].Name != "movie.vtt" {
		t.Errorf("asset[0] = %+v, want subtitle movie.vtt", out.Assets[0])
	}
	// without a lang param the destination falls back to subtitle/und
	if out.Assets[0].Dir != "subtitle/und" {
		t.Errorf("asset[0].Dir = %q, want subtitle/und", out.Assets[0].Dir)
	}
	args := fake.RecordedArgs()
	if len(args) != 1 {
		t.Fatalf("expected 1 exec (vtt conversion), got %d", len(args))
	}
	if !contains(args[0], "webvtt") {
		t.Errorf("exec missing webvtt codec: %v", args[0])
	}
}

func TestProcessLangNamesSubdir(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{
		ExitCode:  0,
		MediaInfo: core.MediaInfo{HasSubtitles: true},
	}, nil)
	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/movie.srt",
		InputURI: "local:work/movie.srt",
		Params:   map[string]any{"lang": "fa"},
		Executor: fake,
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	for _, a := range out.Assets {
		if a.Dir != "subtitle/fa" {
			t.Errorf("asset %s Dir = %q, want subtitle/fa", a.Name, a.Dir)
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestProcessSkipsWhenNoSubtitleTrack(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil) // MediaInfo zero => no subtitles
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
	if len(out.Assets) != 0 {
		t.Fatalf("expected no assets when input lacks subtitles, got %d", len(out.Assets))
	}
	if n := len(fake.RecordedArgs()); n != 0 {
		t.Fatalf("expected zero ffmpeg execs, got %d", n)
	}
}
