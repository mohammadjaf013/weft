package videoenc

import (
	"context"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	ffexec "github.com/mohammadjaf013/weft/runtime/executor/ffmpeg"
)

func TestProcessBuildsLadderAndReportsAssets(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()

	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		JobID:    "job-1",
		TaskID:   "task-1",
		Kind:     "video_encode",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params:   map[string]any{},
		Executor: fake,
	}

	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) != len(mediautil.DefaultH264Ladder) {
		t.Fatalf("expected %d assets, got %d", len(mediautil.DefaultH264Ladder), len(out.Assets))
	}
	if got := out.Assets[0].Name; got != "movie_360p.mp4" {
		t.Errorf("first asset name = %q, want movie_360p.mp4", got)
	}
	if got := out.Assets[3].Name; got != "movie_1080p.mp4" {
		t.Errorf("last asset name = %q, want movie_1080p.mp4", got)
	}

	args := fake.RecordedArgs()
	if len(args) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(args))
	}
	found1080 := false
	for _, a := range args[0] {
		if a == "scale=1920:1080" {
			found1080 = true
		}
	}
	if !found1080 {
		t.Errorf("argv missing 1080p scale filter: %v", args[0])
	}
}

func TestProcessHonorsCodecParam(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()

	for _, tc := range []struct {
		codec    string
		wantLibc string
	}{
		{"h264", "libx264"},
		{"hevc", "libx265"},
	} {
		fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
		in := core.TaskInput{
			JobID:    "job-1",
			TaskID:   "task-1",
			Kind:     "video_encode",
			InputRef: "s3://in/movie.mp4",
			InputURI: "local:work/movie.mp4",
			Params:   map[string]any{"codec": tc.codec},
			Executor: fake,
		}
		if _, err := (&Plugin{}).Process(context.Background(), in); err != nil {
			t.Fatalf("codec %s: Process: %v", tc.codec, err)
		}
		args := fake.RecordedArgs()
		if len(args) != 1 {
			t.Fatalf("codec %s: expected 1 exec, got %d", tc.codec, len(args))
		}
		if !containsStr(args[0], "-c:v") || !containsStr(args[0], tc.wantLibc) {
			t.Errorf("codec %s: argv must encode with %s: %v", tc.codec, tc.wantLibc, args[0])
		}
	}
}

func TestProcessRejectsUnknownCodec(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	_, err := (&Plugin{}).Process(context.Background(), core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params:   map[string]any{"codec": "vp9"},
		Executor: ffexec.NewFake(core.Result{}, nil),
	})
	if err == nil {
		t.Fatal("expected error for unsupported codec")
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestProcessErrorsWithoutInput(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	_, err := (&Plugin{}).Process(context.Background(), core.TaskInput{
		TaskID: "t1", Params: map[string]any{},
		Executor: ffexec.NewFake(core.Result{}, nil),
	})
	if err == nil {
		t.Fatal("expected error for empty InputURI")
	}
}
