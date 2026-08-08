package ffmpeg

import (
	"io"
	"testing"
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
