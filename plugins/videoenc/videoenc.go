// Package videoenc implements the video_encode plugin: H.264/HEVC encode using
// the injected Executor. It builds ffmpeg argv, runs it, and reports the
// produced assets.
package videoenc

import (
	"context"
	"fmt"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

type Plugin struct{}

func (p *Plugin) Name() string { return "ffmpeg-video" }

func (p *Plugin) Capabilities() core.Capabilities {
	return core.Capabilities{
		Name:            "ffmpeg-video",
		SupportedInputs: []string{"mp4", "mkv", "mov", "avi", "mpeg", "ts", "webm"},
		SupportedKinds:  []string{"video_encode"},
		EstimatedCPU:    3.0,
		EstimatedRAMMB:  512,
	}
}

func (p *Plugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if in.InputURI == "" {
		return core.TaskOutput{}, fmt.Errorf("video_encode: no input resolved for %s", in.InputRef)
	}
	codec := "h264"
	if c, ok := in.Params["codec"].(string); ok && c != "" {
		codec = c
	}
	switch codec {
	case "h264", "hevc":
	default:
		return core.TaskOutput{}, fmt.Errorf("video_encode: unsupported codec %q (want h264|hevc)", codec)
	}
	outDir, err := mediautil.EnsureWorkDir(in)
	if err != nil {
		return core.TaskOutput{}, err
	}
	base := mediautil.BaseName(in)
	ladder := mediautil.DefaultH264Ladder
	args := mediautil.EncodeMultiArgs(in.InputURI, ladder, outDir, base, codec)

	in.Params["argv"] = args

	res, err := in.Executor.Run(ctx, core.Task{}, in)
	if err != nil {
		return core.TaskOutput{}, err
	}
	_ = res

	assets := make([]core.AssetRef, 0, len(ladder))
	for _, r := range ladder {
		assets = append(assets, core.AssetRef{
			Kind: "video",
			Name: fmt.Sprintf("%s_%s.mp4", base, r.Label),
			URI:  fmt.Sprintf("local:%s/%s_%s.mp4", outDir, base, r.Label),
		})
	}
	return core.TaskOutput{Assets: assets}, nil
}
