// Package subtitle implements the subtitle plugin: SRT→WebVTT conversion via
// ffmpeg, plus subtitle HLS packaging.
package subtitle

import (
	"context"
	"fmt"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

type Plugin struct{}

func (p *Plugin) Name() string { return "subtitle" }

func (p *Plugin) Capabilities() core.Capabilities {
	return core.Capabilities{
		Name:            "subtitle",
		SupportedInputs: []string{"srt", "vtt", "ass", "ssa", "mp4", "mkv"},
		SupportedKinds:  []string{"subtitle"},
		EstimatedCPU:    0.2,
		EstimatedRAMMB:  64,
	}
}

func (p *Plugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if in.InputURI == "" {
		return core.TaskOutput{}, fmt.Errorf("subtitle: no input resolved for %s", in.InputRef)
	}
	// Graceful skip: an input without a subtitle track has nothing to convert.
	// The subtitle task is "extract and package existing subtitles", not "invent
	// them" — a missing track must not fail the whole job.
	if in.Executor != nil {
		if mi, err := in.Executor.Probe(ctx, in.InputURI); err == nil && !mi.HasSubtitles {
			return core.TaskOutput{}, nil
		}
	}
	outDir, err := mediautil.EnsureWorkDir(in)
	if err != nil {
		return core.TaskOutput{}, err
	}
	base := mediautil.BaseName(in)
	// The operator may override the output base name with a "name" param so a
	// re-submitted subtitle lands on the same file the master playlist already
	// references (e.g. movie.fa.srt → name: movie → movie.vtt).
	if n, _ := in.Params["name"].(string); n != "" {
		base = n
	}

	// Destination subdirectory under the job root: subtitle/<lang>/. The lang
	// is a per-task param from jobs create --lang; unknown/empty falls back to
	// "und" (ISO 639-2 undetermined) so subtitles still land in one place.
	lang, _ := in.Params["lang"].(string)
	if lang == "" {
		lang = "und"
	}

	// Convert to WebVTT.
	vtt := fmt.Sprintf("%s/%s.vtt", outDir, base)
	args := []string{
		"-i", in.InputURI,
		"-c:s", "webvtt",
		vtt,
	}
	in.Params["argv"] = args
	if _, err := in.Executor.Run(ctx, core.Task{}, in); err != nil {
		return core.TaskOutput{}, err
	}

	dir := "subtitle/" + lang
	return core.TaskOutput{Assets: []core.AssetRef{
		{Kind: "subtitle", Name: base + ".vtt", URI: "local:" + vtt, Dir: dir},
	}}, nil
}
