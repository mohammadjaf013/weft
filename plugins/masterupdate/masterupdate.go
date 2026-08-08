// Package masterupdate implements the update_master task: it edits the
// published master playlist in place to add/replace a subtitle or audio track
// (EXT-X-MEDIA) for an already-published video. Re-running with the same
// language replaces that track instead of stacking duplicates.
package masterupdate

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

type Plugin struct{}

func (p *Plugin) Name() string { return "master_update" }

func (p *Plugin) Capabilities() core.Capabilities {
	return core.Capabilities{
		Name:            "master_update",
		SupportedInputs: []string{"m3u8", "vtt", "mp3", "m4a"},
		SupportedKinds:  []string{"update_master"},
		EstimatedCPU:    0.0,
		EstimatedRAMMB:  8,
	}
}

func (p *Plugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if in.Storage == nil {
		return core.TaskOutput{}, fmt.Errorf("master_update: task %s has no destination storage", in.TaskID)
	}
	base := mediautil.BaseName(in)
	if n, _ := in.Params["name"].(string); n != "" {
		base = n
	}
	lang, _ := in.Params["lang"].(string)
	if lang == "" {
		lang = "und"
	}
	kind, _ := in.Params["track"].(string)
	if kind == "" {
		kind = "subtitle"
	}

	// The published master playlist is always named playlist.m3u8 (the hls
	// plugin writes it as the master for the whole ladder).
	ref := core.AssetRef{Kind: "playlist", Name: "playlist.m3u8"}
	rc, err := in.Storage.Open(ctx, ref)
	if err != nil {
		return core.TaskOutput{}, fmt.Errorf("master_update: open master %s: %w", ref.Name, err)
	}
	b, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return core.TaskOutput{}, err
	}

	uri, _ := in.Params["uri"].(string)
	if uri == "" {
		if kind == "subtitle" {
			uri = fmt.Sprintf("subtitle/%s/%s.vtt", lang, base)
		} else {
			uri = fmt.Sprintf("audio/%s/%s.m3u8", lang, base)
		}
	}

	updated, err := updatePlaylist(string(b), kind, lang, uri)
	if err != nil {
		return core.TaskOutput{}, err
	}
	if err := in.Storage.Save(ctx, ref, strings.NewReader(updated)); err != nil {
		return core.TaskOutput{}, fmt.Errorf("master_update: save master %s: %w", ref.Name, err)
	}
	// No assets: the master is written directly to the published storage, so a
	// later upload task must not try to re-copy it from a local path.
	return core.TaskOutput{}, nil
}

// updatePlaylist adds or replaces an EXT-X-MEDIA track for the given language.
// Subtitle tracks go in the "subs" group; audio tracks in "audio". The group
// is referenced from every EXT-X-STREAM-INF line.
func updatePlaylist(master, kind, lang, uri string) (string, error) {
	group := "subs"
	if kind == "audio" {
		group = "audio"
	}
	var mediaType string
	switch kind {
	case "subtitle":
		mediaType = "SUBTITLES"
	case "audio":
		mediaType = "AUDIO"
	default:
		return "", fmt.Errorf("master_update: unsupported track kind %q", kind)
	}

	// Drop the existing track for the same language (replace semantics).
	media := make([]string, 0, 4)
	var lines []string
	for _, raw := range splitLines(master) {
		l := strings.TrimSpace(raw)
		if isMediaLine(l) {
			kv := parseMediaAttrs(l)
			if kv["TYPE"] == mediaType && kv["LANGUAGE"] == lang {
				continue // replaced
			}
			media = append(media, l)
			continue
		}
		lines = append(lines, l)
	}

	newMedia := fmt.Sprintf(
		`#EXT-X-MEDIA:TYPE=%s,GROUP-ID="%s",NAME="%s",DEFAULT=NO,AUTOSELECT=YES,FORCED=NO,LANGUAGE="%s",URI="%s"`,
		mediaType, group, langName(lang), lang, uri,
	)
	if kind == "subtitle" {
		newMedia += `,CHARACTERISTICS="public.accessibility.transcribes-spoken-dialog"`
	}
	media = append(media, newMedia)

	var b strings.Builder
	mediaWritten := false
	firstStream := true
	for _, l := range lines {
		if strings.HasPrefix(l, "#EXT-X-STREAM-INF:") {
			if !mediaWritten {
				writeMedia(&b, media)
				mediaWritten = true
			}
			attr := groupAttr(l, mediaType, group)
			if firstStream && attr != "" {
				l = attr
				firstStream = false
			}
			b.WriteString(l)
			b.WriteString("\n")
			continue
		}
		b.WriteString(l)
		b.WriteString("\n")
	}
	if !mediaWritten {
		writeMedia(&b, media)
	}
	return b.String(), nil
}

func writeMedia(b *strings.Builder, media []string) {
	for _, m := range media {
		b.WriteString(m)
		b.WriteString("\n")
	}
}

// groupAttr appends the group reference (SUBTITLES="subs" / AUDIO="audio") to
// an EXT-X-STREAM-INF line if it does not already carry one.
func groupAttr(streamInf, mediaType, group string) string {
	needle := mediaType + "="
	if strings.Contains(streamInf, needle) {
		return ""
	}
	return strings.TrimSuffix(streamInf, "\n") + "," + needle + `"` + group + `"`
}

// splitLines normalizes CRLF and splits on newlines, dropping empty lines.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

func isMediaLine(l string) bool {
	return strings.HasPrefix(strings.TrimSpace(l), "#EXT-X-MEDIA:")
}

func parseMediaAttrs(line string) map[string]string {
	kv := map[string]string{}
	rest := strings.TrimPrefix(strings.TrimSpace(line), "#EXT-X-MEDIA:")
	for rest != "" {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		var key, val string
		eq := strings.Index(rest, "=")
		if eq < 0 {
			break
		}
		key = strings.TrimSpace(rest[:eq])
		rest = rest[eq+1:]
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				break
			}
			val = rest[1 : 1+end]
			rest = rest[2+end:]
		} else {
			end := strings.Index(rest, ",")
			if end < 0 {
				val = rest
				rest = ""
			} else {
				val = rest[:end]
				rest = rest[end:]
			}
		}
		rest = strings.TrimPrefix(rest, ",")
		kv[key] = val
	}
	return kv
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
