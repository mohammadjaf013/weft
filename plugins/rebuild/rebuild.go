// Package rebuild scans a destination storage directory and rebuilds the
// master playlist (playlist.m3u8) from whatever is actually published there:
// variant playlists at the root (360p.m3u8 …), subtitles under
// subtitle/<lang>/<name>.vtt and dubs under audio/<lang>/<name>.m3u8. It is the
// recovery path for a master playlist that was lost, truncated, or corrupted —
// instead of replaying the whole job it regenerates the master from files.
package rebuild

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

// Track is one subtitle or dub found under a destination dir.
type Track struct {
	Lang string `json:"lang"`
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// Plan describes what a rebuild will (or did) write.
type Plan struct {
	Renditions []string `json:"renditions"` // variant base names, e.g. 360p
	Subtitles  []Track  `json:"subtitles"`
	Audios     []Track  `json:"audios"`
	Master     string   `json:"master"` // final playlist content
}

// Discover inspects the file list of a storage root and returns the rebuild
// plan without writing anything.
func Discover(files []string) (Plan, error) {
	labels := map[string]bool{}
	var subs, auds []Track
	for _, f := range files {
		f = strings.TrimPrefix(f, "./")
		f = strings.TrimSuffix(f, "/")
		if f == "" {
			continue
		}
		switch {
		case f == "playlist.m3u8":
			continue // regenerated from scratch
		case strings.HasSuffix(f, ".m3u8") && !strings.Contains(f, "/"):
			// variant at the root: "720p.m3u8" → 720p
			label := strings.TrimSuffix(f, ".m3u8")
			if isLadderLabel(label) {
				labels[label] = true
			}
		case strings.HasPrefix(f, "subtitle/") && strings.HasSuffix(f, ".vtt"):
			if lang, name := splitLangName(f); lang != "" {
				subs = append(subs, Track{Lang: lang, URI: f, Name: name})
			}
		case strings.HasPrefix(f, "audio/") && strings.HasSuffix(f, ".m3u8"):
			if lang, name := splitLangName(f); lang != "" {
				auds = append(auds, Track{Lang: lang, URI: f, Name: name})
			}
		}
	}

	order := []string{"360p", "480p", "720p", "1080p"}
	var rends []string
	for _, l := range order {
		if labels[l] {
			rends = append(rends, l)
		}
	}
	// any extra ladder labels not in the default order, sorted
	var extra []string
	for l := range labels {
		if !contains(rends, l) {
			extra = append(extra, l)
		}
	}
	sort.Strings(extra)
	rends = append(rends, extra...)

	if len(rends) == 0 {
		return Plan{}, fmt.Errorf("rebuild: no HLS variant playlists found at the storage root (want e.g. 360p.m3u8)")
	}
	sortTracks(subs)
	sortTracks(auds)

	p := Plan{
		Renditions: rends,
		Subtitles:  subs,
		Audios:     auds,
		Master:     Render(rends, subs, auds, "h264"),
	}
	return p, nil
}

// Render builds a complete master playlist from the discovered pieces. codec
// controls the video CODECS tag ("h264" → avc1.*, "hevc" → hvc1.*).
func Render(rends []string, subs, auds []Track, codec string) string {
	ladder := mediautil.LadderFromLabels(rends)
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, t := range subs {
		b.WriteString(mediaLine("SUBTITLES", "subs", t))
	}
	for _, t := range auds {
		b.WriteString(mediaLine("AUDIO", "audio", t))
	}
	subGroup, audGroup := "subs", "audio"
	if len(subs) == 0 {
		subGroup = ""
	}
	if len(auds) == 0 {
		audGroup = ""
	}
	for _, r := range ladder {
		line := mediautil.StreamInfCodec(r, 0, codec)
		if subGroup != "" {
			line += ",SUBTITLES=\"" + subGroup + "\""
		}
		if audGroup != "" {
			line += ",AUDIO=\"" + audGroup + "\""
		}
		b.WriteString(line)
		b.WriteString("\n")
		b.WriteString(r.Label)
		b.WriteString(".m3u8\n")
	}
	return b.String()
}

// mediaLine hardcodes DEFAULT=NO/FORCED=NO — known limitation, not a bug to
// fix here: rebuild reconstructs the master by scanning storage for files
// that already exist (Track carries only what a directory listing can infer:
// lang/URI), with no durable per-track metadata store to recover a
// previously-requested forced/default flag from. masterupdate.go (the
// subtitle-add/dub-add path, which DOES have real request params at the
// moment a track is added) is where forced/default are actually honored.
func mediaLine(typ, group string, t Track) string {
	line := fmt.Sprintf(
		`#EXT-X-MEDIA:TYPE=%s,GROUP-ID="%s",NAME="%s",DEFAULT=NO,AUTOSELECT=YES,FORCED=NO,LANGUAGE="%s",URI="%s"`,
		typ, group, langName(t.Lang), t.Lang, t.URI,
	)
	if typ == "SUBTITLES" {
		line += `,CHARACTERISTICS="public.accessibility.transcribes-spoken-dialog"`
	}
	return line + "\n"
}

// Rebuild regenerates the master playlist in place for the given storage and
// returns the resulting plan. codec selects the video CODECS tag in the master
// ("h264" → avc1.*, "hevc" → hvc1.*); empty defaults to h264.
func Rebuild(ctx context.Context, st core.Storage, codec string) (Plan, error) {
	if codec != "hevc" {
		codec = "h264"
	}
	files, err := st.List(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("rebuild: list storage: %w", err)
	}
	p, err := Discover(files)
	if err != nil {
		return Plan{}, err
	}
	p.Master = Render(p.Renditions, p.Subtitles, p.Audios, codec)
	ref := core.AssetRef{Kind: "playlist", Name: "playlist.m3u8"}
	if err := st.Save(ctx, ref, strings.NewReader(p.Master)); err != nil {
		return Plan{}, fmt.Errorf("rebuild: write master: %w", err)
	}
	return p, nil
}

func isLadderLabel(label string) bool {
	for _, r := range mediautil.DefaultH264Ladder {
		if r.Label == label {
			return true
		}
	}
	return false
}

func splitLangName(f string) (lang, name string) {
	parts := strings.Split(f, "/")
	if len(parts) < 3 {
		return "", ""
	}
	lang = parts[1]
	base := parts[len(parts)-1]
	base = strings.TrimSuffix(base, ".vtt")
	base = strings.TrimSuffix(base, ".m3u8")
	return lang, base
}

func sortTracks(t []Track) {
	sort.Slice(t, func(i, j int) bool { return t[i].Lang < t[j].Lang })
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func langName(lang string) string {
	m := map[string]string{
		"fa": "فارسی", "en": "English", "ar": "العربية", "tr": "Türkçe",
		"ru": "Русский", "de": "Deutsch", "fr": "Français", "es": "Español",
		"it": "Italiano", "zh": "中文", "hi": "हिन्दी", "ur": "اردو", "ku": "Kurdî",
	}
	if v, ok := m[lang]; ok {
		return v
	}
	return lang
}
