// Package hls implements the HLS packaging plugin: single-pass segmentation of
// a video into a multi-rendition HLS VOD set (variant playlists + .ts segments
// + a master playlist), or audio-only HLS when the input has no video stream.
// The ffmpeg command decodes once and writes every rung — same shape as the
// legacy admin-convert.sh packager.
package hls

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

type Plugin struct{}

func (p *Plugin) Name() string { return "hls" }

func (p *Plugin) Capabilities() core.Capabilities {
	return core.Capabilities{
		Name:            "hls",
		SupportedInputs: []string{"mp4", "mkv", "mov", "ts", "mp3", "m4a", "wav", "flac", "opus", "aac"},
		SupportedKinds:  []string{"hls"},
		EstimatedCPU:    1.5,
		EstimatedRAMMB:  256,
	}
}

func (p *Plugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if in.InputURI == "" {
		return core.TaskOutput{}, fmt.Errorf("hls: no input resolved for %s", in.InputRef)
	}
	outDir, err := mediautil.EnsureWorkDir(in)
	if err != nil {
		return core.TaskOutput{}, err
	}
	outDir = outDir + "/hls"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return core.TaskOutput{}, err
	}
	base := mediautil.BaseName(in)
	// The operator may override the output base name with a "name" param so a
	// re-submitted dub/subtitle lands on the same files the master references.
	if n, _ := in.Params["name"].(string); n != "" {
		base = n
	}

	// Audio-only input (mp3 and friends) → audio HLS; otherwise a full
	// multi-rendition video HLS set.
	hasAudio := mediautil.HasAudio(in)
	if isAudioOnly(in) {
		in.Params["argv"] = mediautil.AudioHLSArgs(in.InputURI, outDir, base, 6, "")
	} else {
		ladder := mediautil.DefaultH264Ladder
		if l, ok := in.Params["ladder"].([]string); ok && len(l) > 0 {
			ladder = mediautil.LadderFromLabels(l)
		}
		codec := "h264"
		if c, _ := in.Params["codec"].(string); c == "hevc" {
			codec = "hevc"
		}
		// single-pass: decode once, write every rendition + a master playlist
		in.Params["argv"] = mediautil.HLSMultiArgsCodec(in.InputURI, ladder, outDir, base, 6, hasAudio, codec)
		frameRate := mediautil.FrameRate(in)
		if err := mediautil.WriteFile(filepath.Join(outDir, "playlist.m3u8"), mediautil.MasterPlaylistCodec(ladder, frameRate, codec)); err != nil {
			return core.TaskOutput{}, err
		}
	}
	if _, err := in.Executor.Run(ctx, core.Task{}, in); err != nil {
		return core.TaskOutput{}, err
	}

	// Every produced file is an asset: the master playlist, each variant
	// playlist, and every .ts segment. The upload plugin moves them all into
	// the same destination dir so the relative URIs keep resolving.
	assets := []core.AssetRef{}
	seen := map[string]bool{}
	// Audio HLS (mp3 and friends) is kept separate in an audio/ subfolder so
	// it never collides with video renditions; video HLS stays in the root.
	// A lang param (dub-add) further scopes it to audio/<lang>/.
	dir := ""
	if isAudioOnly(in) {
		dir = "audio"
		if lang, _ := in.Params["lang"].(string); lang != "" {
			dir += "/" + lang
		}
	}
	for _, f := range mediautil.Glob(outDir, "playlist.m3u8") {
		assets = append(assets, core.AssetRef{Kind: "playlist", Name: filepath.Base(f), URI: "local:" + f, Dir: dir})
		seen[filepath.Base(f)] = true
	}
	// variant playlists (360p.m3u8 …) and segments (360p_000.ts …). The audio
	// case emits base.m3u8, which is also a *.m3u8 file.
	for _, f := range mediautil.Glob(outDir, "*.m3u8") {
		if seen[filepath.Base(f)] {
			continue // master already collected
		}
		assets = append(assets, core.AssetRef{Kind: "video", Name: filepath.Base(f), URI: "local:" + f, Dir: dir})
	}
	for _, f := range mediautil.Glob(outDir, "*.ts") {
		assets = append(assets, core.AssetRef{Kind: "video", Name: filepath.Base(f), URI: "local:" + f, Dir: dir})
	}
	return core.TaskOutput{Assets: assets}, nil
}

// isAudioOnly reports whether the input has no video stream, based on ffprobe.
// On probe failure it falls back to the file extension.
func isAudioOnly(in core.TaskInput) bool {
	if in.Executor != nil {
		if mi, err := in.Executor.Probe(context.Background(), in.InputURI); err == nil {
			return !mi.HasVideo
		}
	}
	switch ext := mediautil.Ext(in.InputURI); ext {
	case "mp3", "m4a", "wav", "flac", "opus", "aac", "ogg":
		return true
	}
	return false
}
