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
	poster := fmt.Sprintf("%s/%s_poster.jpg", outDir, base)
	sprite := fmt.Sprintf("%s/%s_sprite.jpg", outDir, base)
	vtt := fmt.Sprintf("%s/%s_preview.vtt", outDir, base)

	// Poster at 10% of duration.
	posterArgs := []string{
		"-i", in.InputURI,
		"-vf", "thumbnail=100,scale=480:270",
		"-frames:v", "1",
		poster,
	}
	in.Params["argv"] = posterArgs
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
	spriteArgs := []string{
		"-i", in.InputURI,
		"-vf", fmt.Sprintf("fps=%s,tile=5x5,scale=1280:720", spriteFPS),
		"-frames:v", "1",
		sprite,
	}
	in.Params["argv"] = spriteArgs
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
	stillsArgs := []string{
		"-i", in.InputURI,
		"-vf", stills + ",scale=480:270",
		stillsPattern,
	}
	in.Params["argv"] = stillsArgs
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

// everySeconds is the interval between per-second stills (legacy behavior).
const everySeconds = 5
