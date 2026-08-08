// Package audioenc implements the audio_encode plugin.
package audioenc

import (
	"context"
	"fmt"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

type Plugin struct{}

func (p *Plugin) Name() string { return "ffmpeg-audio" }

func (p *Plugin) Capabilities() core.Capabilities {
	return core.Capabilities{
		Name:            "ffmpeg-audio",
		SupportedInputs: []string{"aac", "mp3", "wav", "flac", "opus", "mp4", "mkv"},
		SupportedKinds:  []string{"audio_encode"},
		EstimatedCPU:    0.8,
		EstimatedRAMMB:  128,
	}
}

func (p *Plugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if in.InputURI == "" {
		return core.TaskOutput{}, fmt.Errorf("audio_encode: no input resolved for %s", in.InputRef)
	}
	outDir, err := mediautil.EnsureWorkDir(in)
	if err != nil {
		return core.TaskOutput{}, err
	}
	base := mediautil.BaseName(in)
	out := fmt.Sprintf("%s/%s.m4a", outDir, base)
	args := []string{
		"-i", in.InputURI,
		"-c:a", "aac", "-b:a", "128k",
		out,
	}
	in.Params["argv"] = args
	if _, err := in.Executor.Run(ctx, core.Task{}, in); err != nil {
		return core.TaskOutput{}, err
	}
	return core.TaskOutput{Assets: []core.AssetRef{
		{Kind: "audio", Name: base + ".m4a", URI: "local:" + out},
	}}, nil
}
