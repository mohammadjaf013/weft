// Package thumbnail implements the thumbnail plugin: poster extraction,
// sprite generation, per-N-seconds stills, and VTT previews.
package thumbnail

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

type Plugin struct{}

func (p *Plugin) Name() string { return "thumbnail" }

func (p *Plugin) Capabilities() core.Capabilities {
	return core.Capabilities{
		Name:            "thumbnail",
		SupportedInputs: []string{"mp4", "mkv", "mov", "ts"},
		SupportedKinds:  []string{"thumbnail"},
		EstimatedCPU:    0.2,
		EstimatedRAMMB:  64,
	}
}

func (p *Plugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if in.InputURI == "" {
		return core.TaskOutput{}, fmt.Errorf("thumbnail: no input resolved for %s", in.InputRef)
	}
	outDir, err := mediautil.EnsureWorkDir(in)
	if err != nil {
		return core.TaskOutput{}, err
	}
	base := mediautil.BaseName(in)

	// Optional coordinated trim: when the HLS pipeline trimmed the clip, the
	// thumbnails must come from the same window so they match the renditions.
	trim := mediautil.TrimFromParams(in.Params, mediautil.Duration(in))

	// Custom mode: when thumb_count is set, produce exactly that many
	// evenly-spaced thumbnails at thumb_size ("1080x1080" or "original")
	// instead of the default poster/sprite/stills set.
	if count, _ := in.Params["thumb_count"].(float64); count > 0 {
		return p.stillThumbs(ctx, in, outDir, base, trim, int(count))
	}

	poster := fmt.Sprintf("%s/%s_poster.jpg", outDir, base)
	sprite := fmt.Sprintf("%s/%s_sprite.jpg", outDir, base)
	vtt := fmt.Sprintf("%s/%s_preview.vtt", outDir, base)

	// Poster at 10% of duration.
	posterArgs := []string{"-i", in.InputURI,
		"-vf", "thumbnail=100,scale=480:270",
		"-frames:v", "1",
		poster,
	}
	in.Params["argv"] = trimInsert(trim, posterArgs)
	if _, err := in.Executor.Run(ctx, core.Task{}, in); err != nil {
		return core.TaskOutput{}, err
	}

	// Sprite: a 5x5 grid of frames spread across the whole clip. fps is set so
	// ~25 frames are produced regardless of duration (fps=1/60 would yield zero
	// frames for short inputs).
	spriteFPS := "1"
	if dur := mediautil.Duration(in); dur > 0 {
		fps := 25.0 / dur
		if fps < 1 {
			fps = 1
		}
		spriteFPS = fmt.Sprintf("%g", fps)
	}
	spriteArgs := []string{"-i", in.InputURI,
		"-vf", fmt.Sprintf("fps=%s,tile=5x5,scale=1280:720", spriteFPS),
		"-frames:v", "1",
		sprite,
	}
	in.Params["argv"] = trimInsert(trim, spriteArgs)
	if _, err := in.Executor.Run(ctx, core.Task{}, in); err != nil {
		return core.TaskOutput{}, err
	}

	// Per-N-second stills, one jpg every `every` seconds of the clip, like the
	// legacy converter (e.g. every 5s → base_001.jpg, base_002.jpg, …). Short
	// clips fall back to 1fps so at least one frame is produced.
	stills := ""
	if dur := mediautil.Duration(in); dur > 0 {
		fps := 1.0 / everySeconds
		if dur <= everySeconds {
			fps = 1
		}
		stills = fmt.Sprintf("fps=%g", fps)
	} else {
		stills = "fps=1"
	}
	stillsPattern := fmt.Sprintf("%s/%s_%%03d.jpg", outDir, base)
	stillsArgs := []string{"-i", in.InputURI,
		"-vf", stills + ",scale=480:270",
		stillsPattern,
	}
	in.Params["argv"] = trimInsert(trim, stillsArgs)
	if _, err := in.Executor.Run(ctx, core.Task{}, in); err != nil {
		return core.TaskOutput{}, err
	}

	// VTT preview file (generated, not from ffmpeg).
	vttContent := "WEBVTT\n\n00:00:00.000 --> 00:00:10.000\nautoadvance\n\n"
	if err := mediautil.WriteFile(vtt, vttContent); err != nil {
		return core.TaskOutput{}, err
	}

	assets := []core.AssetRef{
		{Kind: "thumbnail", Name: base + "_poster.jpg", URI: "local:" + poster, Dir: "thumbnails"},
		{Kind: "sprite", Name: base + "_sprite.jpg", URI: "local:" + sprite, Dir: "thumbnails"},
		{Kind: "vtt", Name: base + "_preview.vtt", URI: "local:" + vtt, Dir: "thumbnails"},
	}
	for _, f := range mediautil.Glob(outDir, base+"_*.jpg") {
		if strings.HasSuffix(f, "_poster.jpg") || strings.HasSuffix(f, "_sprite.jpg") {
			continue
		}
		assets = append(assets, core.AssetRef{Kind: "thumbnail", Name: filepath.Base(f), URI: "local:" + f, Dir: "thumbnails"})
	}
	return core.TaskOutput{Assets: assets}, nil
}

// stillThumbs produces exactly count evenly-spaced thumbnails from the (trimmed)
// clip at the requested size: "1080x1080" scales both dimensions, "original"
// keeps the source resolution, any other value falls back to the source.
func (p *Plugin) stillThumbs(ctx context.Context, in core.TaskInput, outDir, base string, trim mediautil.Trim, count int) (core.TaskOutput, error) {
	dur := mediautil.Duration(in)
	window := dur
	if trim.Active() {
		window = trim.Keep
	}
	fps := "1"
	if window > 0 {
		fps = fmt.Sprintf("%g", float64(count)/window)
	}
	vf := "fps=" + fps
	if sz, _ := in.Params["thumb_size"].(string); sz != "" && sz != "original" {
		w, h := parseSize(sz)
		if w > 0 && h > 0 {
			vf += fmt.Sprintf(",scale=%d:%d", w, h)
		}
	}
	pattern := fmt.Sprintf("%s/%s_thumb_%%02d.jpg", outDir, base)
	args := []string{"-i", in.InputURI,
		"-vf", vf,
		"-frames:v", fmt.Sprintf("%d", count),
		pattern,
	}
	in.Params["argv"] = trimInsert(trim, args)
	if _, err := in.Executor.Run(ctx, core.Task{}, in); err != nil {
		return core.TaskOutput{}, err
	}

	assets := []core.AssetRef{}
	for _, f := range mediautil.Glob(outDir, base+"_thumb_*.jpg") {
		assets = append(assets, core.AssetRef{Kind: "thumbnail", Name: filepath.Base(f), URI: "local:" + f, Dir: "thumbnails"})
	}
	return core.TaskOutput{Assets: assets}, nil
}

// trimInsert splices the input seek and output duration options into an argv
// list that starts with "-i <input>".
func trimInsert(trim mediautil.Trim, argv []string) []string {
	if !trim.Active() {
		return argv
	}
	out := append([]string{}, trim.InputArgs()...)
	out = append(out, argv...)
	// -t is an output option: it must be placed before the output file, so we
	// append it just before the last element (the output path).
	if args := trim.OutputArgs(); len(args) > 0 {
		if len(out) > 1 {
			tail := out[len(out)-1]
			out = append(out[:len(out)-1], args...)
			out = append(out, tail)
		}
	}
	return out
}

// parseSize parses "1080x1080" → (1080, 1080); returns (0,0) on bad input.
func parseSize(s string) (int, int) {
	var w, h int
	if _, err := fmt.Sscanf(s, "%dx%d", &w, &h); err != nil {
		return 0, 0
	}
	return w, h
}

// everySeconds is the interval between per-second stills (legacy behavior).
const everySeconds = 5
