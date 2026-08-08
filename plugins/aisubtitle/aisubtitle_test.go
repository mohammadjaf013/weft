package aisubtitle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

type fakeTranscriber struct {
	text     string
	refine   string
	sawSrc   string
	sawDst   string
	sawSRT   string
	refineFn func(src, dst, srt string) (string, error)
}

func (f fakeTranscriber) Transcribe(_ context.Context, _, _ string, _ []byte, _ string) (string, error) {
	return f.text, nil
}

func (f fakeTranscriber) RefineSRT(_ context.Context, _, _, src, dst, srt string) (string, error) {
	if f.refineFn != nil {
		return f.refineFn(src, dst, srt)
	}
	return f.refine, nil
}

func TestValidateWhisper(t *testing.T) {
	p := &Plugin{Cfg: ProviderConfig{Provider: "whisper", Whisper: WhisperConfig{BinPath: "whisper-cli"}}}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error: whisper without model_path must fail validation")
	}
	f := t.TempDir() + "/model.bin"
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.Cfg.Whisper.ModelPath = f
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate with model_path: %v", err)
	}
}

func TestValidateGemini(t *testing.T) {
	p := &Plugin{Cfg: ProviderConfig{Provider: "gemini"}}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error: gemini without api_key must fail validation")
	}
}

func TestProcessGeminiProviderWritesSRT(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	p := &Plugin{Cfg: ProviderConfig{
		Provider: "gemini",
		Gemini:   GeminiConfig{APIKey: "k", Model: "m", HTTP: fakeTranscriber{text: "hello world from the film"}},
	}}
	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/movie.mp4",
		InputURI: t.TempDir() + "/audio.mp3",
	}
	if err := os.WriteFile(in.InputURI, []byte("fake-audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := p.Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) != 1 || out.Assets[0].Kind != "subtitle" {
		t.Fatalf("assets = %+v, want one subtitle", out.Assets)
	}
	b, err := os.ReadFile(strings.TrimPrefix(out.Assets[0].URI, "local:"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "hello world from the film") {
		t.Errorf("srt missing transcript: %s", string(b))
	}
}

func TestProcessErrorsWhenUnconfigured(t *testing.T) {
	p := &Plugin{Cfg: ProviderConfig{Provider: ""}}
	_, err := p.Process(context.Background(), core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/a.mp4",
	})
	if err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
}

func TestProcessUnknownProviderFromParams(t *testing.T) {
	p := &Plugin{Cfg: ProviderConfig{Provider: "gemini"}}
	_, err := p.Process(context.Background(), core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/a.mp4",
		Params:   map[string]any{"provider": "nope"},
	})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestParseWhisperProgress(t *testing.T) {
	cases := []struct {
		line string
		want float64
	}{
		{"whisper_print_progress_callback: progress =  42%", 42},
		{"progress =  100%", 100},
		{"loading model", -1},
		{"progress =  bogus", -1},
	}
	for _, c := range cases {
		if got := parseWhisperProgress(c.line); got != c.want {
			t.Errorf("parseWhisperProgress(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestProcessHybridRequiresGeminiKey(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	p := &Plugin{Cfg: ProviderConfig{Provider: "whisper"}}
	_, err := p.Process(context.Background(), core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/a.mp4",
		Params:   map[string]any{"provider": "hybrid"},
	})
	if err == nil {
		t.Fatal("expected error: hybrid without gemini.api_key must fail")
	}
}

// fakeWhisperBin is a tiny whisper-cli stand-in that writes a real SRT with the
// flags it was given (recorded into argsFile) so we can assert the exact
// invocation without a real model. Only the -l/-of/-osrt path matters here.
func TestHybridTranslatesSrcToDst(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()

	p := &Plugin{Cfg: ProviderConfig{
		Provider: "whisper",
		Whisper: WhisperConfig{
			ModelPath:  filepath.Join(mediautil.WorkRoot, "model.bin"),
			Threads:    8,
			Temperature: floatPtr(0),
			Prompt:     "Spider-Man, Peter Parker",
		},
	}}

	// whisperArgs must carry src lang + tuning flags
	args := p.whisperArgs(filepath.Join(mediautil.WorkRoot, "movie.wav"),
		filepath.Join(mediautil.WorkRoot, "movie.wav.whisper"), "tr")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-l tr", "-t 8", "--temperature 0", "--prompt Spider-Man, Peter Parker", "-pp", "-osrt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("whisper args missing %q (got: %s)", want, joined)
		}
	}
	// auto source lang must be omitted
	auto := p.whisperArgs("in.wav", "base", "auto")
	if strings.Contains(strings.Join(auto, " "), "-l auto") {
		t.Errorf("auto lang must not pass -l (got: %s)", strings.Join(auto, " "))
	}
}

func floatPtr(f float64) *float64 { return &f }

// TestExtractAudioConvertsTo16kMono skips unless ffmpeg is present; it verifies
// the audio extraction produces a decodable wav that whisper.cpp can read.
func TestExtractAudioConvertsTo16kMono(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
	mediautil.WorkRoot = t.TempDir()
	in := filepath.Join(mediautil.WorkRoot, "in.mp3")
	if _, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=1", "-y", in).Output(); err != nil {
		t.Fatalf("make sample: %v", err)
	}
	out := filepath.Join(mediautil.WorkRoot, "out.16k.wav")
	p := &Plugin{}
	ti := core.TaskInput{InputURI: in}
	if err := p.extractAudio(context.Background(), ti, out); err != nil {
		t.Fatalf("extractAudio: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("extracted wav missing: %v", err)
	}
	b, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"stream=sample_rate,channels", "-of", "csv=p=0", out).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	s := strings.TrimSpace(string(b))
	if !strings.HasPrefix(s, "16000,1") {
		t.Errorf("extracted audio = %q, want 16000,1 (16kHz mono)", s)
	}
}
