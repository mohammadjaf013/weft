// Package thumbnail implements the thumbnail plugin: poster extraction,
// sprite generation, per-N-seconds stills, and VTT previews.
package thumbnail

import (
	"context"
	"fmt"
	"os"
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
	// A master HLS playlist (e.g. a published job's playlist.m3u8, grabbed
	// via source_server input fetching) has no single duration and no
	// directly decodable stream of its own -- ffmpeg can't -ss seek into it
	// ("could not seek to position ...") and errors with "does not contain
	// any stream". Retarget to one real rendition sub-playlist instead.
	in.InputURI = resolveHLSRendition(in.InputURI)
	outDir, err := mediautil.EnsureWorkDir(in)
	if err != nil {
		return core.TaskOutput{}, err
	}
	base := mediautil.BaseName(in)

	// Optional coordinated trim: when the HLS pipeline trimmed the clip, the
	// thumbnails must come from the same window so they match the renditions.
	trim, err := mediautil.TrimFromParams(in.Params, mediautil.Duration(in))
	if err != nil {
		return core.TaskOutput{}, fmt.Errorf("thumbnail: %w", err)
	}

	// Custom mode: when thumb_count is set, produce exactly that many
	// evenly-spaced thumbnails at thumb_size ("1080x1080" or "original")
	// instead of the default poster/sprite/stills set.
	if count, _ := in.Params["thumb_count"].(float64); count > 0 {
		return p.stillThumbs(ctx, in, outDir, base, trim, int(count))
	}
	// Single-timestamp mode: when thumb_at is set, capture exactly one frame
	// at that offset ("grab the thumbnail at 00:01:23") instead of the
	// default set or the N-evenly-spaced thumb_count mode.
	if at, _ := in.Params["thumb_at"].(float64); at > 0 {
		return p.singleThumb(ctx, in, outDir, base, at)
	}

	poster := fmt.Sprintf("%s/%s_poster.jpg", outDir, base)
	sprite := fmt.Sprintf("%s/%s_sprite.jpg", outDir, base)
	vtt := fmt.Sprintf("%s/%s_preview.vtt", outDir, base)

	// Poster at 10% of duration. -update 1 is required on modern ffmpeg
	// builds: the image2 muxer refuses to write a single frame to a
	// non-pattern filename ("...does not contain an image sequence
	// pattern...") without it — -frames:v 1 alone isn't enough.
	posterArgs := []string{"-i", in.InputURI,
		"-vf", "thumbnail=100,scale=480:270",
		"-frames:v", "1",
		"-update", "1",
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
		"-update", "1",
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

// singleThumb captures exactly one frame at the given offset (seconds) — the
// "grab the thumbnail at 00:01:23" case, distinct from stillThumbs' N
// evenly-spaced frames. Seeks against the ORIGINAL source timeline (ignores
// any coordinated trim window) since the caller gave an explicit absolute
// timestamp, not a trim-relative one.
func (p *Plugin) singleThumb(ctx context.Context, in core.TaskInput, outDir, base string, at float64) (core.TaskOutput, error) {
	dest := fmt.Sprintf("%s/%s_thumb_at.jpg", outDir, base)
	// -update 1: same reason as the poster/sprite outputs above — the image2
	// muxer needs it to write a single frame to a non-pattern filename.
	args := []string{"-ss", fmt.Sprintf("%.3f", at), "-i", in.InputURI, "-frames:v", "1", "-update", "1"}
	if sz, _ := in.Params["thumb_size"].(string); sz != "" && sz != "original" {
		if w, h := parseSize(sz); w > 0 && h > 0 {
			args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", w, h))
		}
	}
	args = append(args, dest)
	in.Params["argv"] = args
	if _, err := in.Executor.Run(ctx, core.Task{}, in); err != nil {
		return core.TaskOutput{}, err
	}
	return core.TaskOutput{Assets: []core.AssetRef{
		{Kind: "thumbnail", Name: base + "_thumb_at.jpg", URI: "local:" + dest, Dir: "thumbnails"},
	}}, nil
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

// hlsVariant is one #EXT-X-STREAM-INF entry from a master playlist: the
// rendition sub-playlist it points at, plus its RESOLUTION width (0 if the
// attribute was missing) for the no-labeled-match fallback.
type hlsVariant struct {
	uri   string
	width int
}

// hlsRenditionPreference is weft's own rendition-label convention (see
// mediautil.MasterPlaylistCodec's ladder rungs) in best-first order, used to
// pick a rendition when grabbing a thumbnail from an already-published HLS
// master playlist: prefer 1080p, fall back to 720p, and so on.
var hlsRenditionPreference = []string{"1080p", "720p", "480p", "360p"}

// resolveHLSRendition rewrites a master HLS playlist input to one real
// rendition sub-playlist. ffmpeg cannot -ss seek into a master playlist (it
// has no duration of its own, only references to per-variant sub-playlists),
// so a thumbnail request against one fails with "could not seek to position
// ..." / "does not contain any stream". Any input that isn't a master
// playlist (not .m3u8, or an .m3u8 already at the rendition level with no
// #EXT-X-STREAM-INF lines -- e.g. someone already pointed at 720p.m3u8
// directly) passes through unchanged.
func resolveHLSRendition(inputPath string) string {
	if !strings.EqualFold(filepath.Ext(inputPath), ".m3u8") {
		return inputPath
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return inputPath // let ffmpeg surface the real "file not found" error
	}
	variants := parseMasterVariants(string(data))
	if len(variants) == 0 {
		return inputPath
	}
	return filepath.Join(filepath.Dir(inputPath), bestHLSVariant(variants))
}

// parseMasterVariants extracts the rendition URI (and RESOLUTION width, if
// present) following each #EXT-X-STREAM-INF line. Returns nil for a
// playlist with no such lines (i.e. not a master playlist).
func parseMasterVariants(m3u8 string) []hlsVariant {
	lines := strings.Split(strings.ReplaceAll(m3u8, "\r\n", "\n"), "\n")
	var out []hlsVariant
	for i, raw := range lines {
		l := strings.TrimSpace(raw)
		if !strings.HasPrefix(l, "#EXT-X-STREAM-INF:") || i+1 >= len(lines) {
			continue
		}
		uri := strings.TrimSpace(lines[i+1])
		if uri == "" || strings.HasPrefix(uri, "#") {
			continue
		}
		w := 0
		if idx := strings.Index(l, "RESOLUTION="); idx >= 0 {
			var h int
			fmt.Sscanf(l[idx+len("RESOLUTION="):], "%dx%d", &w, &h)
		}
		out = append(out, hlsVariant{uri: uri, width: w})
	}
	return out
}

// bestHLSVariant picks by hlsRenditionPreference first (matching weft's own
// "{label}.m3u8" naming), then falls back to the widest RESOLUTION for a
// non-standard ladder.
func bestHLSVariant(variants []hlsVariant) string {
	for _, label := range hlsRenditionPreference {
		for _, v := range variants {
			if strings.Contains(strings.ToLower(v.uri), label) {
				return v.uri
			}
		}
	}
	best := variants[0]
	for _, v := range variants[1:] {
		if v.width > best.width {
			best = v
		}
	}
	return best.uri
}
