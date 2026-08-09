package mediautil

import (
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
)

func TestBaseName(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"s3://in/movie.mp4", "movie"},
		{"local:/abs/path/pod.mp3", "pod"},
		{`G:\develop\weft\rtest\sample.mp4`, "sample"}, // Windows path
		{"film.mkv", "film"},
		{"nested/dir/video.mp4", "video"},
		{"", "task_x"}, // no ref -> task id fallback
	}
	for _, c := range cases {
		in := core.TaskInput{InputRef: c.ref, TaskID: "task_x"}
		if got := BaseName(in); got != c.want {
			t.Errorf("BaseName(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

func TestTrimFromParams(t *testing.T) {
	// no trim params -> inactive
	tr, err := TrimFromParams(map[string]any{}, 100)
	if err != nil {
		t.Fatalf("empty params must not error: %v", err)
	}
	if tr.Active() {
		t.Fatalf("empty params must be inactive, got %+v", tr)
	}
	// both sides
	tr, err = TrimFromParams(map[string]any{"trim_start": float64(50), "trim_end": float64(10)}, 100)
	if err != nil {
		t.Fatalf("both-sides trim must not error: %v", err)
	}
	if !tr.Active() || tr.Start != 50 || tr.Keep != 40 {
		t.Fatalf("trim = %+v, want start=50 keep=40", tr)
	}
	// only start
	tr, err = TrimFromParams(map[string]any{"trim_start": float64(20)}, 100)
	if err != nil {
		t.Fatalf("start-only trim must not error: %v", err)
	}
	if tr.Start != 20 || tr.Keep != 80 {
		t.Fatalf("start-only trim = %+v", tr)
	}
	// only end
	tr, err = TrimFromParams(map[string]any{"trim_end": float64(5)}, 100)
	if err != nil {
		t.Fatalf("end-only trim must not error: %v", err)
	}
	if tr.Start != 0 || tr.Keep != 95 {
		t.Fatalf("end-only trim = %+v", tr)
	}
	// trim_start with unknown duration (probe failed) is fine: it doesn't need
	// the duration to skip from the start, so Keep=0 (unlimited) is correct.
	tr, err = TrimFromParams(map[string]any{"trim_start": float64(20)}, 0)
	if err != nil {
		t.Fatalf("start-only trim with unknown duration must not error: %v", err)
	}
	if tr.Start != 20 || tr.Keep != 0 {
		t.Fatalf("start-only trim w/ unknown duration = %+v, want start=20 keep=0", tr)
	}
	// trim_end with unknown duration (probe failed) MUST error instead of
	// silently producing an untrimmed clip.
	if _, err := TrimFromParams(map[string]any{"trim_end": float64(5)}, 0); err == nil {
		t.Fatal("trim_end with unprobeable duration must error, got nil")
	}
	// trim_start + trim_end leaving nothing to encode must error.
	if _, err := TrimFromParams(map[string]any{"trim_start": float64(90), "trim_end": float64(20)}, 100); err == nil {
		t.Fatal("trim window leaving no content must error, got nil")
	}
}

func TestTrimArgs(t *testing.T) {
	tr := Trim{Start: 50, Keep: 40}
	in := tr.InputArgs()
	if len(in) != 2 || in[0] != "-ss" || in[1] != "50.000" {
		t.Fatalf("InputArgs = %v", in)
	}
	out := tr.OutputArgs()
	if len(out) != 2 || out[0] != "-t" || out[1] != "40.000" {
		t.Fatalf("OutputArgs = %v", out)
	}
	// keep <= 0 means no -t (play to the end)
	if a := (Trim{Start: 30}).OutputArgs(); len(a) != 0 {
		t.Fatalf("no-keep OutputArgs = %v", a)
	}
	if a := (Trim{}).InputArgs(); len(a) != 0 {
		t.Fatalf("inactive InputArgs = %v", a)
	}
}

func TestHLSMultiArgsTrim(t *testing.T) {
	args := HLSMultiArgsCodec("in.mp4", DefaultH264Ladder[:2], "out", "m", 6, true, "h264", Trim{Start: 50, Keep: 40})
	// -ss must appear before -i
	ssIdx, iIdx := -1, -1
	for i, a := range args {
		if a == "-ss" {
			ssIdx = i
		}
		if a == "-i" {
			iIdx = i
		}
	}
	if ssIdx == -1 || ssIdx > iIdx {
		t.Fatalf("trim -ss must precede -i: %v", args)
	}
	// -t must appear once per output (2 rungs)
	ts := 0
	for i, a := range args {
		if a == "-t" {
			ts++
			if i+1 >= len(args) || args[i+1] != "40.000" {
				t.Fatalf("bad -t at %d: %v", i, args)
			}
		}
	}
	if ts != 2 {
		t.Fatalf("-t count = %d, want 2 (one per rung): %v", ts, args)
	}
	// no trim -> no -ss/-t
	plain := HLSMultiArgsCodec("in.mp4", DefaultH264Ladder[:1], "out", "m", 6, true, "h264", Trim{})
	if containsStr(plain, "-ss") || containsStr(plain, "-t") {
		t.Fatalf("plain args must not contain trim: %v", plain)
	}
}

func TestAudioHLSArgsTrim(t *testing.T) {
	args := AudioHLSArgs("in.mp3", "out", "pod", 6, "", Trim{Start: 10, Keep: 90})
	if !containsStr(args, "-ss") || !containsStr(args, "10.000") {
		t.Fatalf("audio args missing input seek: %v", args)
	}
	if !containsStr(args, "-t") || !containsStr(args, "90.000") {
		t.Fatalf("audio args missing duration: %v", args)
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
		if strings.Contains(x, want) {
			return true
		}
	}
	return false
}
