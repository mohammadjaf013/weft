// Package masterplaylist implements the master_playlist plugin. This is the one
// place Weft intentionally does NOT shell out to ffmpeg: the Master Playlist is
// generated internally so tracks can be added/removed/replaced later without
// reprocessing video.
package masterplaylist

import (
	"context"
	"fmt"
	"strings"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

type Plugin struct{}

func (p *Plugin) Name() string { return "master_playlist" }

func (p *Plugin) Capabilities() core.Capabilities {
	return core.Capabilities{
		Name:            "master_playlist",
		SupportedInputs: []string{"mp4", "mkv", "mov", "ts"},
		SupportedKinds:  []string{"master_playlist"},
		EstimatedCPU:    0.0,
		EstimatedRAMMB:  8,
	}
}

func (p *Plugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	outDir := mediautil.WorkDir(in)
	base := mediautil.BaseName(in)
	pl := fmt.Sprintf("%s/%s_master.m3u8", outDir, base)

	// Build a master playlist referencing the variant playlist(s) this job
	// produced. TaskInput.Params["renditions"] carries per-variant info.
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	renditions, _ := in.Params["renditions"].([]map[string]any)
	if len(renditions) == 0 {
		renditions = []map[string]any{
			{"uri": base + "/" + base + ".m3u8", "bandwidth": "3000000", "resolution": "1280x720"},
		}
	}
	for _, r := range renditions {
		uri, _ := r["uri"].(string)
		bw, _ := r["bandwidth"].(string)
		res, _ := r["resolution"].(string)
		if bw == "" {
			bw = "0"
		}
		if res == "" {
			res = "0x0"
		}
		b.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%s,RESOLUTION=%s\n", bw, res))
		b.WriteString(uri + "\n")
	}

	if err := mediautil.WriteFile(pl, b.String()); err != nil {
		return core.TaskOutput{}, err
	}
	return core.TaskOutput{Assets: []core.AssetRef{
		{Kind: "playlist", Name: base + "_master.m3u8", URI: "local:" + pl},
	}}, nil
}
