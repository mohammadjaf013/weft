package audioenc

import (
	"context"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	ffexec "github.com/mohammadjaf013/weft/runtime/executor/ffmpeg"
)

func TestProcessEncodesAudio(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/pod.mp3",
		InputURI: "local:work/pod.mp3",
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
	if got := out.Assets[0].Name; got != "pod.m4a" {
		t.Errorf("asset name = %q, want pod.m4a", got)
	}
	args := fake.RecordedArgs()
	if len(args) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(args))
	}
	if !contains(args[0], "-c:a") || !contains(args[0], "aac") {
		t.Errorf("argv missing aac encoder: %v", args[0])
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
