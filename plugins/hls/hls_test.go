package hls

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	ffexec "github.com/mohammadjaf013/weft/runtime/executor/ffmpeg"
)

func TestProcessSegmentsHLS(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0, MediaInfo: core.MediaInfo{HasVideo: true}}, nil)
	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params:   map[string]any{},
		Executor: fake,
	}
	// simulate ffmpeg output: master + variant playlists + segments
	outDir := filepath.Join(mediautil.WorkRoot, "work", string(in.TaskID), "hls")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{
		"playlist.m3u8",
		"360p.m3u8", "360p_000.ts", "360p_001.ts",
		"720p.m3u8", "720p_000.ts",
	} {
		if err := os.WriteFile(filepath.Join(outDir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// master + 2 variants + 3 segments
	if len(out.Assets) != 6 {
		t.Fatalf("expected 6 assets, got %d", len(out.Assets))
	}
	names := map[string]bool{}
	for _, a := range out.Assets {
		names[a.Name] = true
	}
	for _, want := range []string{
		"playlist.m3u8",
		"360p.m3u8", "360p_000.ts", "360p_001.ts",
		"720p.m3u8", "720p_000.ts",
	} {
		if !names[want] {
			t.Errorf("assets missing %s (have %v)", want, names)
		}
	}
	// master playlist is written by the plugin (no ffmpeg)
	if _, err := os.Stat(filepath.Join(outDir, "playlist.m3u8")); err != nil {
		t.Errorf("plugin must write master playlist: %v", err)
	}
	args := fake.RecordedArgs()
	if len(args) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(args))
	}
	if !contains(args[0], "-hls_playlist_type") || !contains(args[0], "vod") {
		t.Errorf("argv missing vod playlist type: %v", args[0])
	}
	// single-pass multi-rendition: must contain multiple .m3u8 outputs
	m3u8s := 0
	for _, a := range args[0] {
		if strings.HasSuffix(a, ".m3u8") {
			m3u8s++
		}
	}
	if m3u8s < 2 {
		t.Errorf("expected multiple .m3u8 outputs (multi-rendition), got %d: %v", m3u8s, args[0])
	}
	// no audio stream in the source → must NOT map or encode audio
	if contains(args[0], "-c:a") {
		t.Errorf("silent input must not encode audio: %v", args[0])
	}
	if contains(args[0], "-map") && containsPair(args[0], "-map", "0:a:0") {
		t.Errorf("silent input must not map audio: %v", args[0])
	}
}

// TestProcessVideoWithAudio verifies a video WITH an audio stream keeps the
// audio map and aac encode (the opposite of the silent-input case).
func TestProcessVideoWithAudio(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0, MediaInfo: core.MediaInfo{HasVideo: true, HasAudio: true}}, nil)
	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params:   map[string]any{},
		Executor: fake,
	}
	outDir := filepath.Join(mediautil.WorkRoot, "work", string(in.TaskID), "hls")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"playlist.m3u8", "360p.m3u8", "360p_000.ts"} {
		if err := os.WriteFile(filepath.Join(outDir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&Plugin{}).Process(context.Background(), in); err != nil {
		t.Fatalf("Process: %v", err)
	}
	args := fake.RecordedArgs()
	if len(args) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(args))
	}
	if !contains(args[0], "-c:a") || !contains(args[0], "aac") {
		t.Errorf("audio-capable input must encode aac: %v", args[0])
	}
	if !containsPair(args[0], "-map", "0:a:0") {
		t.Errorf("audio-capable input must map audio: %v", args[0])
	}
}

// TestProcessAudioHLS verifies an audio-only input (mp3) is packaged as audio
// HLS: -vn, no video scale filter, aac audio, .ts segments + .m3u8. The
// produced .ts segments are returned as assets so upload can move them.
func TestProcessAudioHLS(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0, MediaInfo: core.MediaInfo{HasAudio: true}}, nil)
	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/pod.mp3",
		InputURI: "local:work/pod.mp3",
		Params:   map[string]any{},
		Executor: fake,
	}
	// simulate ffmpeg output: a playlist plus two segments in the hls dir
	outDir := filepath.Join(mediautil.WorkRoot, "work", string(in.TaskID), "hls")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"pod.m3u8", "pod_0.ts", "pod_1.ts"} {
		if err := os.WriteFile(filepath.Join(outDir, n), []byte("seg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// playlist + 2 segments
	if len(out.Assets) != 3 {
		t.Fatalf("assets = %d, want 3 (playlist + 2 segments)", len(out.Assets))
	}
	names := map[string]bool{}
	for _, a := range out.Assets {
		names[a.Name] = true
	}
	if !names["pod.m3u8"] || !names["pod_0.ts"] || !names["pod_1.ts"] {
		t.Errorf("assets = %v, want pod.m3u8 + pod_0.ts + pod_1.ts", names)
	}
	// audio-only outputs land under an audio/ subfolder
	for _, a := range out.Assets {
		if a.Dir != "audio" {
			t.Errorf("asset %s Dir = %q, want audio", a.Name, a.Dir)
		}
	}
	args := fake.RecordedArgs()
	if len(args) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(args))
	}
	if !contains(args[0], "-vn") {
		t.Errorf("audio HLS must disable video: %v", args[0])
	}
	if contains(args[0], "-filter:v") || contains(args[0], "-c:v") {
		t.Errorf("audio HLS must not use video filter/codec: %v", args[0])
	}
	if !contains(args[0], "-c:a") || !contains(args[0], "aac") {
		t.Errorf("audio HLS must encode aac: %v", args[0])
	}
	if !strings.HasSuffix(last(args[0]), ".m3u8") {
		t.Errorf("argv missing .m3u8 output: %v", args[0])
	}}

func last(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[len(xs)-1]
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// containsPair reports whether want is immediately followed by want2 in xs.
func containsPair(xs []string, want, want2 string) bool {
	for i := 0; i+1 < len(xs); i++ {
		if xs[i] == want && xs[i+1] == want2 {
			return true
		}
	}
	return false
}
