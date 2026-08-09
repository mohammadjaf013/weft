// Package aisubtitle implements the ai_subtitle plugin with two providers:
// whisper (local model, offline) and gemini (API). One Plugin, one provider
// active at a time via config — Core/Runtime never know which is active.
package aisubtitle

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

// ProviderConfig selects the active provider and its settings. Validated at
// Agent startup (config.Validate) so a misconfigured provider never reaches a
// running Job.
type ProviderConfig struct {
	Provider      string // whisper | gemini
	Whisper       WhisperConfig
	Gemini        GeminiConfig
	Languages     []string
	OnlyIfMissing bool
}

type WhisperConfig struct {
	ModelPath   string
	BinPath     string // whisper.cpp style binary; defaults to "whisper-cli"
	Language    string // source language of the audio (whisper -l), e.g. en, tr
	Threads     int    // -t CPU threads
	Temperature *float64 // --temperature; 0.0 for deterministic output
	Prompt      string // --prompt initial context: names, setting, vocabulary
}

type GeminiConfig struct {
	APIKey  string
	Model   string
	Timeout time.Duration
	HTTP    interface {
		// overridable in tests
		Transcribe(ctx context.Context, key, model string, audio []byte, lang string) (string, error)
		RefineSRT(ctx context.Context, key, model, srcLang, dstLang, srt string) (string, error)
	}
}

type Plugin struct {
	Cfg ProviderConfig
}

func (p *Plugin) Name() string { return "ai-subtitle" }

func (p *Plugin) Capabilities() core.Capabilities {
	return core.Capabilities{
		Name:            "ai-subtitle",
		SupportedInputs: []string{"mp4", "mkv", "mov", "m4a", "mp3", "wav", "flac"},
		SupportedKinds:  []string{"ai_subtitle"},
		EstimatedCPU:    2.0,
		EstimatedRAMMB:  1024,
	}
}

// Validate returns an error when the configured provider can't possibly work.
func (p *Plugin) Validate() error {
	switch p.Cfg.Provider {
	case "whisper":
		if p.Cfg.Whisper.BinPath == "" {
			p.Cfg.Whisper.BinPath = "whisper-cli"
		}
		if p.Cfg.Whisper.ModelPath == "" {
			return fmt.Errorf("ai_subtitle: whisper provider requires model_path")
		}
		if fi, err := os.Stat(p.Cfg.Whisper.ModelPath); err != nil || fi.IsDir() {
			return fmt.Errorf("ai_subtitle: whisper model_path %q not readable", p.Cfg.Whisper.ModelPath)
		}
	case "gemini":
		if p.Cfg.Gemini.APIKey == "" {
			return fmt.Errorf("ai_subtitle: gemini provider requires api_key")
		}
	case "":
		return nil // plugin not configured; must not be registered as enabled
	default:
		return fmt.Errorf("ai_subtitle: unknown provider %q", p.Cfg.Provider)
	}
	return nil
}

func (p *Plugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if in.InputURI == "" {
		return core.TaskOutput{}, fmt.Errorf("ai_subtitle: no input resolved for %s", in.InputRef)
	}
	// dstLang is the language the subtitle must end up in (also names the
	// output folder). srcLang is the language the audio actually is spoken in
	// (whisper's -l). They differ when we transcribe in en and translate to fa.
	dstLang := paramStr(in.Params, "language")
	if dstLang == "" {
		dstLang = p.Cfg.Whisper.Language
	}
	srcLang := paramStr(in.Params, "src_lang")
	if srcLang == "" {
		srcLang = p.Cfg.Whisper.Language
	}
	provider := p.Cfg.Provider
	if in.Params["provider"] != nil {
		if pr, ok := in.Params["provider"].(string); ok && pr != "" {
			provider = pr
		}
	}
	switch provider {
	case "", "whisper", "gemini", "hybrid":
	default:
		return core.TaskOutput{}, fmt.Errorf("ai_subtitle: unknown provider %q (want whisper|gemini|hybrid)", provider)
	}
	if in.Progress != nil {
		in.Progress(5)
	}

	var srt string
	var err error
	switch provider {
	case "whisper":
		srt, err = p.runWhisper(ctx, in, srcLang)
	case "hybrid":
		// whisper transcribes to SRT in the source language, then gemini
		// translates to dstLang (or just proofreads when src == dst).
		srt, err = p.runWhisper(ctx, in, srcLang)
		if err == nil {
			if in.Progress != nil {
				in.Progress(75)
			}
			srt, err = p.refineWithGemini(ctx, in, srcLang, dstLang, srt)
		}
	case "gemini":
		audio, aerr := os.ReadFile(in.InputURI)
		if aerr != nil {
			return core.TaskOutput{}, aerr
		}
		tr := p.Cfg.Gemini.HTTP
		if tr == nil {
			tr = geminiClient{}
		}
		text, terr := tr.Transcribe(ctx, p.Cfg.Gemini.APIKey, p.Cfg.Gemini.Model, audio, dstLang)
		if terr != nil {
			return core.TaskOutput{}, terr
		}
		srt = plainToSRT(text)
	default:
		return core.TaskOutput{}, fmt.Errorf("ai_subtitle: provider %q not configured", p.Cfg.Provider)
	}
	if err != nil {
		return core.TaskOutput{}, err
	}
	if in.Progress != nil {
		in.Progress(90)
	}

	outDir := mediautil.WorkDir(in)
	base := mediautil.BaseName(in)
	out := filepath.Join(outDir, base+".srt")
	if err := mediautil.WriteFile(out, srt); err != nil {
		return core.TaskOutput{}, err
	}
	if in.Progress != nil {
		in.Progress(100)
	}
	return core.TaskOutput{Assets: []core.AssetRef{
		{Kind: "subtitle", Name: base + ".srt", URI: "local:" + out},
	}}, nil
}

func paramStr(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	if v, ok := params[key].(string); ok {
		return v
	}
	return ""
}

// whisperArgs builds the whisper-cli argument list for the given input/outbase
// and source language, honoring the configured threads/temperature/prompt.
func (p *Plugin) whisperArgs(input, base, srcLang string) []string {
	args := []string{
		"-m", p.Cfg.Whisper.ModelPath,
		"-f", input,
		"-of", base,
		"-osrt",
	}
	if srcLang != "" && srcLang != "auto" {
		args = append(args, "-l", srcLang)
	}
	if p.Cfg.Whisper.Threads > 0 {
		args = append(args, "-t", fmt.Sprintf("%d", p.Cfg.Whisper.Threads))
	}
	if p.Cfg.Whisper.Temperature != nil {
		args = append(args, "--temperature", fmt.Sprintf("%v", *p.Cfg.Whisper.Temperature))
	}
	if p.Cfg.Whisper.Prompt != "" {
		args = append(args, "--prompt", p.Cfg.Whisper.Prompt)
	}
	// -pp streams progress; -np disables the per-segment timestamps so stdout
	// stays small (stderr carries progress + final errors).
	return append(args, "-pp")
}

// runWhisper invokes a whisper.cpp-style CLI. whisper.cpp reads raw audio
// (wav/pcm/16kHz mono) but not mp4/mkv containers, so the input is first
// extracted to a 16kHz mono wav with ffmpeg — exactly the pipeline that works
// well on the CLI. The whisper command is:
// whisper-cli -m model -l srcLang -f in.wav -of outbase -osrt [-t N]
//            [--temperature T] [--prompt "..."] [-pp]
// stderr is captured so a failing run reports the real whisper message.
func (p *Plugin) runWhisper(ctx context.Context, in core.TaskInput, srcLang string) (string, error) {
	bin := p.Cfg.Whisper.BinPath
	if bin == "" {
		bin = "whisper-cli"
	}
	wav := in.InputURI + ".16k.wav"
	if err := p.extractAudio(ctx, in, wav); err != nil {
		return "", fmt.Errorf("whisper audio extract: %w", err)
	}
	base := wav + ".whisper"
	args := p.whisperArgs(wav, base, srcLang)
	cmd := exec.CommandContext(ctx, bin, args...)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("whisper stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("whisper start: %w", err)
	}
	// parse progress from stderr while the process runs, and keep a tail of
	// the log so failures report the actual whisper message.
	done := make(chan struct{})
	var tail strings.Builder
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			line := sc.Text()
			if pct := parseWhisperProgress(line); pct >= 0 && in.Progress != nil {
				// whisper reports 0..100 inclusive; clamp and scale so the
				// transcription phase occupies 5..70 of the task's progress.
				scaled := 5 + pct*0.65
				if scaled > 70 {
					scaled = 70
				}
				in.Progress(scaled)
			}
			if in.Log != nil {
				in.Log.Write([]byte(line + "\n"))
			}
			if tail.Len() < 4096 {
				tail.WriteString(line)
				tail.WriteString("\n")
			}
		}
	}()
	err = cmd.Wait()
	<-done
	if err != nil {
		return "", fmt.Errorf("whisper failed: %v (stderr: %s)", err, tail.String())
	}
	b, rerr := os.ReadFile(base + ".srt")
	if rerr != nil {
		return "", fmt.Errorf("whisper produced no srt (stderr: %s): %v", tail.String(), rerr)
	}
	return string(b), nil
}

// extractAudio converts the task input to a 16kHz mono wav via ffmpeg (through
// the injected executor when present, else a direct ffmpeg call), matching the
// input format whisper.cpp can actually decode.
func (p *Plugin) extractAudio(ctx context.Context, in core.TaskInput, out string) error {
	argv := []string{"-i", in.InputURI, "-vn", "-ac", "1", "-ar", "16000", out}
	if in.Executor != nil {
		in.Params["argv"] = argv
		if _, err := in.Executor.Run(ctx, core.Task{}, in); err != nil {
			return err
		}
		return nil
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-nostdin", "-y"}, argv...)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, b)
	}
	return nil
}

// parseWhisperProgress extracts the percentage from a whisper.cpp progress
// line ("whisper_print_progress_callback: progress =  42%"); -1 when absent.
func parseWhisperProgress(line string) float64 {
	i := strings.Index(line, "progress =")
	if i < 0 {
		return -1
	}
	rest := strings.TrimSpace(line[i+len("progress ="):])
	rest = strings.TrimSuffix(rest, "%")
	var n float64
	if _, err := fmt.Sscanf(rest, "%f", &n); err != nil {
		return -1
	}
	return n
}

// refineWithGemini sends an existing SRT to Gemini. When srcLang == dstLang it
// asks for a proofread pass; when they differ it asks for a translation, always
// preserving the timestamps and returning the SRT unchanged if the API produced
// nothing usable.
func (p *Plugin) refineWithGemini(ctx context.Context, in core.TaskInput, srcLang, dstLang, srt string) (string, error) {
	if p.Cfg.Gemini.APIKey == "" {
		return "", fmt.Errorf("ai_subtitle: hybrid requires gemini.api_key")
	}
	tr := p.Cfg.Gemini.HTTP
	if tr == nil {
		tr = geminiClient{}
	}
	refined, err := tr.RefineSRT(ctx, p.Cfg.Gemini.APIKey, p.Cfg.Gemini.Model, srcLang, dstLang, srt)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(refined) == "" {
		return srt, nil // keep whisper's output if the API returned nothing
	}
	return refined, nil
}

// geminiClient is the default Gemini API caller (real network).
type geminiClient struct{}

func (geminiClient) Transcribe(ctx context.Context, key, model string, audio []byte, lang string) (string, error) {
	if model == "" {
		model = "gemini-1.5-flash"
	}
	// Payload: base64 audio + prompt asking for verbatim transcription.
	req := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": fmt.Sprintf("Transcribe the following audio verbatim. Language hint: %s. Return only the transcript.", lang)},
					{"inline_data": map[string]any{"mime_type": "audio/mpeg", "data": b64(audio)}},
				},
			},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return postJSON(ctx, "https://generativelanguage.googleapis.com/v1beta/models/"+model+":generateContent?key="+key, body)
}

func b64(b []byte) string { return base64Std.EncodeToString(b) }
