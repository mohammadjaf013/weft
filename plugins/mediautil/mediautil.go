// Package mediautil contains small shared helpers used by media plugins
// (video, audio, thumbnail, subtitle, hls). Keeping argv construction in one
// place means the argument lists stay consistent and testable.
package mediautil

import (
	"fmt"
	"strings"
)

// Ladder defines the resolution ladder for a profile.
type Rung struct {
	Label   string // "360p"
	Width   int
	Height  int
	Bitrate string // "900k"
	Maxrate string
	Bufsize string
}

// DefaultH264Ladder mirrors the legacy ladder: 360p/480p/720p/1080p at
// 900k/1500k/3000k/6000k, CRF 21, veryfast.
var DefaultH264Ladder = []Rung{
	{Label: "360p", Width: 640, Height: 360, Bitrate: "900k", Maxrate: "1350k", Bufsize: "1800k"},
	{Label: "480p", Width: 852, Height: 480, Bitrate: "1500k", Maxrate: "2250k", Bufsize: "3000k"},
	{Label: "720p", Width: 1280, Height: 720, Bitrate: "3000k", Maxrate: "4500k", Bufsize: "6000k"},
	{Label: "1080p", Width: 1920, Height: 1080, Bitrate: "6000k", Maxrate: "9000k", Bufsize: "12000k"},
}

// LadderFromLabels picks the ladder rungs whose labels are listed (e.g.
// ["360p","720p","1080p"]), preserving ladder order. Unknown labels are
// ignored; an empty result falls back to the full default ladder.
func LadderFromLabels(labels []string) []Rung {
	want := map[string]bool{}
	for _, l := range labels {
		want[l] = true
	}
	var out []Rung
	for _, r := range DefaultH264Ladder {
		if want[r.Label] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return DefaultH264Ladder
	}
	return out
}

// H264Args builds a single ffmpeg H.264 encode command for the ladder.
// -progress parsing happens inside the executor; here we only assemble argv.
func H264Args(input, outPattern string, ladder []Rung) []string {
	args := []string{"-i", input}
	for _, r := range ladder {
		args = append(args,
			"-filter:v", fmt.Sprintf("scale=%d:%d", r.Width, r.Height),
			"-c:v", "libx264", "-crf", "21", "-preset", "veryfast",
			"-b:v", r.Bitrate, "-maxrate", r.Maxrate, "-bufsize", r.Bufsize,
			"-c:a", "aac", "-b:a", "128k",
			outPattern, // e.g. "out_%d.mp4" — but we use separate outputs below
		)
	}
	return args
}

// H264MultiArgs builds one output per ladder rung (H.264/AVC).
func H264MultiArgs(input string, ladder []Rung, outDir, base string) []string {
	return EncodeMultiArgs(input, ladder, outDir, base, "h264")
}

// EncodeMultiArgs builds one output per ladder rung for the given codec:
// "h264" → libx264, "hevc" → libx265. videoenc dispatches on its codec param
// here instead of silently ignoring it.
func EncodeMultiArgs(input string, ladder []Rung, outDir, base, codec string) []string {
	enc, crf := "libx264", "21"
	if codec == "hevc" {
		enc, crf = "libx265", "26"
	}
	args := []string{"-i", input}
	for _, r := range ladder {
		args = append(args,
			"-filter:v", fmt.Sprintf("scale=%d:%d", r.Width, r.Height),
			"-c:v", enc, "-crf", crf, "-preset", "veryfast",
			"-c:a", "aac", "-b:a", "128k",
			fmt.Sprintf("%s/%s_%s.mp4", outDir, base, r.Label),
		)
	}
	return args
}

// HLSArgs builds an HLS packager command for a given resolution.
func HLSArgs(input, outDir, name string, segSec int, bw string, r Rung) []string {
	if segSec == 0 {
		segSec = 6
	}
	return []string{
		"-i", input,
		"-filter:v", fmt.Sprintf("scale=%d:%d", r.Width, r.Height),
		"-c:v", "libx264", "-crf", "21", "-preset", "veryfast",
		"-c:a", "aac", "-b:a", "128k",
		"-hls_time", fmt.Sprintf("%d", segSec),
		"-hls_list_size", "0",
		"-hls_playlist_type", "vod",
		"-b:v", bw,
		"-hls_segment_filename", fmt.Sprintf("%s/%s_%%d.ts", outDir, name),
		fmt.Sprintf("%s/%s.m3u8", outDir, name),
	}
}

// HLSMultiArgs builds a single ffmpeg command that decodes the input once and
// writes every ladder rung as its own HLS rendition: {outDir}/{label}.m3u8
// plus {label}_NNN.ts segments, exactly like the legacy admin-convert.sh
// single-pass packager (one decode, N outputs, no wasted CPU). When the input
// has no audio stream (hasAudio=false) the audio map/codec are omitted —
// `-map 0:a:0` on a silent source is a fatal ffmpeg error.
func HLSMultiArgs(input string, ladder []Rung, outDir, base string, segSec int, hasAudio bool) []string {
	return HLSMultiArgsCodec(input, ladder, outDir, base, segSec, hasAudio, "h264")
}

// HLSMultiArgsCodec is HLSMultiArgs for an explicit codec ("h264" → libx264,
// "hevc" → libx265). The hls plugin passes the profile's codec through here so
// vod-hevc produces real HEVC renditions instead of silently encoding H.264.
func HLSMultiArgsCodec(input string, ladder []Rung, outDir, base string, segSec int, hasAudio bool, codec string) []string {
	if segSec == 0 {
		segSec = 6
	}
	enc, crf := "libx264", "21"
	if codec == "hevc" {
		enc, crf = "libx265", "26"
	}
	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-progress", "pipe:1", "-nostats",
		"-i", input, "-max_muxing_queue_size", "4096"}
	for _, r := range ladder {
		name := r.Label
		args = append(args,
			"-map", "0:v:0")
		if hasAudio {
			args = append(args, "-map", "0:a:0")
		}
		args = append(args,
			"-c:v", enc, "-preset", "veryfast", "-crf", crf,
			"-vf", fmt.Sprintf("scale=%d:%d:flags=bicubic", r.Width, r.Height),
			"-maxrate", r.Maxrate, "-bufsize", r.Bufsize, "-b:v", r.Bitrate)
		if hasAudio {
			args = append(args, "-c:a", "aac", "-b:a", "128k")
		}
		args = append(args,
			"-g", "50", "-keyint_min", "50", "-sc_threshold", "0",
			"-f", "hls",
			"-hls_time", fmt.Sprintf("%d", segSec),
			"-hls_list_size", "0",
			"-hls_playlist_type", "vod",
			"-hls_flags", "independent_segments",
			"-hls_segment_filename", fmt.Sprintf("%s/%s_%%03d.ts", outDir, name),
			fmt.Sprintf("%s/%s.m3u8", outDir, name),
		)
	}
	return args
}

// MasterPlaylist renders the master playlist (playlist.m3u8, the admin-convert.sh
// name) referencing the variant playlists for every rung of the ladder,
// matching the legacy packager's tags exactly:
//   BANDWIDTH=video+audio+20%video, AVERAGE-BANDWIDTH=video+audio,
//   RESOLUTION, FRAME-RATE (from the source), CODECS per-level.
// Written by the hls plugin itself so the playlist always matches the
// renditions that were really produced.
func MasterPlaylist(ladder []Rung, frameRate float64) string {
	return MasterPlaylistCodec(ladder, frameRate, "h264")
}

// MasterPlaylistCodec is MasterPlaylist for an explicit video codec. The
// CODECS tag reflects the actual codec ("avc1" vs "hvc1"), so a vod-hevc
// profile doesn't advertise H.264 renditions.
func MasterPlaylistCodec(ladder []Rung, frameRate float64, codec string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, r := range ladder {
		b.WriteString(StreamInfCodec(r, frameRate, codec))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("%s.m3u8\n", r.Label))
	}
	return b.String()
}

// StreamInf renders the EXT-X-STREAM-INF line for one ladder rung with the
// legacy bandwidth/CODECS values. Exposed so master rebuilders produce lines
// byte-identical to the hls plugin.
func StreamInf(r Rung, frameRate float64) string {
	return StreamInfCodec(r, frameRate, "h264")
}

// StreamInfCodec renders the EXT-X-STREAM-INF line for one ladder rung with the
// legacy bandwidth values. Exposed so master rebuilders produce lines
// byte-identical to the hls plugin. codec controls the video part of CODECS:
// "h264" → avc1.*, "hevc" → hvc1.*.
func StreamInfCodec(r Rung, frameRate float64, codec string) string {
	vb := bitrateKbps(r.Bitrate)
	ab := audioKbps(r.Label)
	avg := vb + ab
	bw := avg + vb/5
	fr := ""
	if frameRate > 0 {
		fr = fmt.Sprintf(",FRAME-RATE=%.3f", frameRate)
	}
	videoCodec := avcLevel(r.Label)
	if codec == "hevc" {
		videoCodec = hevcLevel(r.Label)
	}
	return fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d,RESOLUTION=%dx%d%s,CODECS=\"%s,mp4a.40.2\"",
		bw*1000, avg*1000, r.Width, r.Height, fr, videoCodec)
}

// audioKbps mirrors the legacy audio bitrate: 192k for 720p/1080p, 128k below.
func audioKbps(label string) int {
	if label == "720p" || label == "1080p" {
		return 192
	}
	return 128
}

// avcLevel returns the H.264 profile/level string that matches the legacy
// packager output for each rung.
func avcLevel(label string) string {
	switch label {
	case "360p":
		return "avc1.64001E"
	case "480p":
		return "avc1.64001F"
	case "720p":
		return "avc1.640020"
	case "1080p":
		return "avc1.640028"
	}
	return "avc1.640020"
}

// hevcLevel returns an HEVC (H.265) profile/level string for each rung.
func hevcLevel(label string) string {
	switch label {
	case "360p":
		return "hvc1.1.6.L93.B0"
	case "480p":
		return "hvc1.1.6.L93.B0"
	case "720p":
		return "hvc1.1.6.L120.B0"
	case "1080p":
		return "hvc1.1.6.L120.B0"
	}
	return "hvc1.1.6.L120.B0"
}

// bitrateKbps parses "3000k" → 3000; falls back to 0 for non-k values.
func bitrateKbps(s string) int {
	v := strings.TrimSuffix(s, "k")
	var n int
	fmt.Sscanf(v, "%d", &n)
	return n
}

// AudioHLSArgs builds an audio-only HLS packager command (no video stream).
func AudioHLSArgs(input, outDir, name string, segSec int, bitrate string) []string {
	if segSec == 0 {
		segSec = 6
	}
	if bitrate == "" {
		bitrate = "128k"
	}
	return []string{
		"-i", input,
		"-vn",
		"-c:a", "aac", "-b:a", bitrate,
		"-hls_time", fmt.Sprintf("%d", segSec),
		"-hls_list_size", "0",
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", fmt.Sprintf("%s/%s_%%d.ts", outDir, name),
		fmt.Sprintf("%s/%s.m3u8", outDir, name),
	}
}
