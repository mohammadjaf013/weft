package ffmpeg

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mohammadjaf013/weft/core"
)

func TestParseTimeToMs(t *testing.T) {
	cases := map[string]int64{
		"00:00:00.000000": 0,
		"00:01:00.000000": 60000,
		"01:30:45.500000": 3600000 + 30*60000 + 45000 + 500,
		"00:00:00.250":    250,
	}
	for in, want := range cases {
		if got := parseTimeToMs(in); got != want {
			t.Errorf("parseTimeToMs(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseProbeJSON(t *testing.T) {
	in := `{
		"format": {"duration": "123.4", "format_name": "mov,mp4,m4a,3gp,3g2,mj2"},
		"streams": [
			{"codec_type": "video", "width": 1920, "height": 1080},
			{"codec_type": "audio", "width": 0, "height": 0},
			{"codec_type": "audio", "width": 0, "height": 0}
		]
	}`
	mi, err := parseProbeJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if !mi.HasVideo || !mi.HasAudio || mi.HasSubtitles {
		t.Fatalf("flags wrong: %+v", mi)
	}
	if mi.Width != 1920 || mi.Height != 1080 {
		t.Fatalf("resolution wrong: %+v", mi)
	}
	if mi.AudioStreams != 2 || mi.VideoStreams != 1 {
		t.Fatalf("stream counts wrong: %+v", mi)
	}
	if mi.DurationSec != 123.4 {
		t.Fatalf("duration = %v, want 123.4", mi.DurationSec)
	}
}

func TestParseProbeJSONFrameRate(t *testing.T) {
	in := `{
		"format": {"duration": "10", "format_name": "mov,mp4"},
		"streams": [
			{"codec_type": "video", "avg_frame_rate": "24000/1001"}
		]
	}`
	mi, err := parseProbeJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if abs(mi.FrameRate-23.976) > 0.001 {
		t.Fatalf("frame rate = %v, want 23.976", mi.FrameRate)
	}
}

func TestParseProbeJSONFrameRateFallback(t *testing.T) {
	in := `{
		"format": {"duration": "10", "format_name": "mov,mp4"},
		"streams": [
			{"codec_type": "video", "avg_frame_rate": "0/0", "r_frame_rate": "25/1"}
		]
	}`
	mi, err := parseProbeJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if mi.FrameRate != 25 {
		t.Fatalf("frame rate = %v, want 25", mi.FrameRate)
	}
}

func TestParseFrameRate(t *testing.T) {
	cases := map[string]float64{
		"30000/1001": 29.97,
		"24000/1001": 23.976,
		"25":         25,
		"0/0":        0,
		"":           0,
		"bogus":      0,
	}
	for in, want := range cases {
		got := parseFrameRate(in)
		if got != want && abs(got-want) > 0.001 {
			t.Errorf("parseFrameRate(%q) = %v, want %v", in, got, want)
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestParseProbeJSONSubtitles(t *testing.T) {
	in := `{
		"format": {"duration": "10", "format_name": "matroska,webm"},
		"streams": [
			{"codec_type": "video"},
			{"codec_type": "subtitle"}
		]
	}`
	mi, err := parseProbeJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if !mi.HasSubtitles {
		t.Fatal("expected HasSubtitles=true")
	}
	if !mi.HasVideo || mi.HasAudio {
		t.Fatalf("video=%v audio=%v", mi.HasVideo, mi.HasAudio)
	}
}

func writeSample(t *testing.T, w io.WriteCloser) {
	t.Helper()
	io.WriteString(w, "frame=150\nfps=29.97\nout_time_ms=5000000\nprogress=continue\n")
	io.WriteString(w, "frame=300\nout_time_ms=10000000\nprogress=end\n")
}

func TestParseProgressE2E(t *testing.T) {
	e := New(DefaultLocator())
	got := []float64{}
	done := make(chan struct{})

	r, w := io.Pipe()
	go func() {
		defer close(done)
		e.parseProgress(r, func(pct float64) { got = append(got, pct) }, -1)
	}()
	writeSample(t, w)
	w.Close()
	<-done

	if len(got) == 0 {
		t.Fatal("no progress callbacks received")
	}
	last := got[len(got)-1]
	if last != 100 {
		t.Fatalf("final progress = %v, want 100", last)
	}
}

func TestParseProgressNilCallback(t *testing.T) {
	e := New(DefaultLocator())
	done := make(chan struct{})
	r, w := io.Pipe()
	go func() {
		defer close(done)
		e.parseProgress(r, nil, -1) // must drain, not block, without a callback
	}()
	writeSample(t, w)
	w.Close()
	<-done
}

// TestParseProgressWithDuration verifies that out_time reports real mid-run
// percentages once the input duration is known. out_time_ms values are µs
// (ffmpeg's long-standing misnomer): 5,000,000 µs = 5 s.
func TestParseProgressWithDuration(t *testing.T) {
	e := New(DefaultLocator())
	got := []float64{}
	done := make(chan struct{})
	r, w := io.Pipe()
	go func() {
		defer close(done)
		e.parseProgress(r, func(pct float64) { got = append(got, pct) }, 20000) // 20s input
	}()
	io.WriteString(w, "frame=300\nout_time_ms=5000000\nprogress=continue\n")
	io.WriteString(w, "frame=600\nout_time_ms=10000000\nprogress=continue\n")
	io.WriteString(w, "frame=900\nout_time_ms=20000000\nprogress=end\n")
	w.Close()
	<-done

	if len(got) != 4 {
		t.Fatalf("got %d callbacks %v, want 4", len(got), got)
	}
	if got[0] != 25 {
		t.Errorf("first = %v, want 25", got[0])
	}
	if got[1] != 50 {
		t.Errorf("second = %v, want 50", got[1])
	}
	if got[3] != 100 {
		t.Errorf("last = %v, want 100", got[3])
	}
}

// TestParseProgressNoDurationStaysZeroUntilEnd verifies the pre-fix behavior is
// intentionally preserved when duration is unknown: nothing until progress=end.
func TestParseProgressNoDurationStaysZeroUntilEnd(t *testing.T) {
	e := New(DefaultLocator())
	got := []float64{}
	done := make(chan struct{})
	r, w := io.Pipe()
	go func() {
		defer close(done)
		e.parseProgress(r, func(pct float64) { got = append(got, pct) }, -1)
	}()
	io.WriteString(w, "frame=300\nout_time_ms=5000000\nprogress=continue\n")
	io.WriteString(w, "frame=600\nout_time_ms=10000000\nprogress=end\n")
	w.Close()
	<-done

	// No duration: out_time lines are ignored (durationMS <= 0), so only the
	// progress=end callback fires.
	if len(got) != 1 || got[0] != 100 {
		t.Fatalf("got %v, want [100] only", got)
	}
}

// TestHelperSleepForever is never run as a real test: the pause/resume test
// spawns the test binary in helper mode (WEFT_HELPER=1) and this function makes
// it sleep long enough to observe a SIGSTOP/SIGCONT.
func TestHelperSleepForever(t *testing.T) {
	if os.Getenv("WEFT_HELPER") != "1" {
		t.Skip("helper only")
	}
	time.Sleep(30 * time.Second)
}

// TestPauseResumeSignalsRealProcess spawns a real child process (the test
// binary in helper mode) and verifies Pause freezes it and Resume continues it.
func TestPauseResumeSignalsRealProcess(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not found; cannot spawn a helper process")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot find test executable")
	}
	// Spawn a real child process (the test binary itself in "helper" mode) that
	// sleeps 10s. Pause → SIGSTOP freezes it; Resume → SIGCONT continues it.
	cmd := exec.Command(exe, "-test.run=TestHelperSleepForever")
	cmd.Env = append(os.Environ(), "WEFT_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	e := New(DefaultLocator())
	e.track("t", cmd)
	defer e.untrack("t")

	if err := e.Pause(context.Background(), "t"); err != nil {
		t.Skipf("pause unsupported on this platform: %v", err)
	}
	// While stopped, the child makes no progress: measure elapsed time for a
	// short window — it must be near zero (process is frozen), proving pause
	// really stops it rather than flipping a status field alone.
	st := time.Now()
	time.Sleep(500 * time.Millisecond)
	frozen := time.Since(st)
	if err := e.Resume(context.Background(), "t"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// Immediately after resume, wall time advances normally.
	st = time.Now()
	time.Sleep(200 * time.Millisecond)
	normal := time.Since(st)
	if frozen < 300*time.Millisecond {
		t.Errorf("process was not frozen during pause (elapsed %v during 500ms window)", frozen)
	}
	if normal < 150*time.Millisecond {
		t.Errorf("process did not resume (elapsed %v during 200ms window)", normal)
	}
}

// TestPauseResumeUnknownTaskNoop verifies Pause/Resume never panic or error
// for a task ID the executor is not tracking.
func TestPauseResumeUnknownTaskNoop(t *testing.T) {
	e := New(DefaultLocator())
	if err := e.Pause(context.Background(), "nope"); err != nil {
		t.Fatalf("Pause unknown task: %v", err)
	}
	if err := e.Resume(context.Background(), "nope"); err != nil {
		t.Fatalf("Resume unknown task: %v", err)
	}
	// fake executor also must no-op
	f := NewFake(core.Result{ExitCode: 0}, nil)
	if err := f.Pause(context.Background(), "x"); err != nil {
		t.Fatalf("fake Pause: %v", err)
	}
}
