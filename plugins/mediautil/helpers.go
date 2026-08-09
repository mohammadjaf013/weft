package mediautil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mohammadjaf013/weft/core"
)

// WorkRoot is the base directory under which per-task work dirs are created.
// Defaults to "." (repo/daemon CWD); tests override it to a temp dir.
var WorkRoot = "."

// WorkDir returns the per-task output directory derived from the task id.
func WorkDir(in core.TaskInput) string {
	return filepath.Join(WorkRoot, "work", string(in.TaskID))
}

// EnsureWorkDir creates the per-task work directory (and parents). Media
// plugins call this before handing an output path to ffmpeg — ffmpeg refuses
// to create output directories itself.
func EnsureWorkDir(in core.TaskInput) (string, error) {
	dir := WorkDir(in)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// WriteFile creates parent dirs and writes content (used for generated assets
// like VTT previews that ffmpeg doesn't produce).
func WriteFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// Duration probes the input and returns its length in seconds (0 if unknown).
func Duration(in core.TaskInput) float64 {
	if in.Executor == nil || in.InputURI == "" {
		return 0
	}
	mi, err := in.Executor.Probe(context.Background(), in.InputURI)
	if err != nil {
		return 0
	}
	return mi.DurationSec
}

// HasAudio reports whether the probed input contains an audio stream. On probe
// failure it returns false (the caller can fall back to extension checks).
func HasAudio(in core.TaskInput) bool {
	if in.Executor == nil || in.InputURI == "" {
		return false
	}
	mi, err := in.Executor.Probe(context.Background(), in.InputURI)
	if err != nil {
		return false
	}
	return mi.HasAudio
}

// FrameRate returns the probed source video frame rate (0 if unknown), used to
// tag the master playlist's FRAME-RATE attribute like the legacy packager.
func FrameRate(in core.TaskInput) float64 {
	if in.Executor == nil || in.InputURI == "" {
		return 0
	}
	mi, err := in.Executor.Probe(context.Background(), in.InputURI)
	if err != nil {
		return 0
	}
	return mi.FrameRate
}

// BaseName derives a stable file base from the input ref: s3://in/movie.mp4 → "movie".
func BaseName(in core.TaskInput) string {
	ref := in.InputRef
	// split on both separators so a Windows-style ref ("G:\dir\a.mp4") yields
	// the same base on a unix host
	if i := strings.LastIndexAny(ref, `/\`); i >= 0 {
		ref = ref[i+1:]
	}
	base := strings.TrimSuffix(ref, filepath.Ext(ref))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = string(in.TaskID)
	}
	return base
}

// Ext returns the lowercased file extension of a ref/path without the dot:
// s3://in/movie.MP4 → "mp4".
func Ext(ref string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(ref)), ".")
}

// Glob returns the files in dir matching the pattern (e.g. "song_*.ts"),
// sorted. Missing dir or no matches yields nil.
func Glob(dir, pattern string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil
	}
	return matches
}

// ParamFloat reads a float64 param by name, returning def when absent or
// unparsable.
func ParamFloat(params map[string]any, name string, def float64) float64 {
	v, ok := params[name]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return def
		}
		return f
	}
	return def
}

// ParamBool reads a bool param by name, returning def when absent or
// unparsable. Task params round-trip through JSON (decoded into map[string]any
// as a real bool), so this is mostly a defensive default rather than a
// multi-type coercion like ParamFloat.
func ParamBool(params map[string]any, name string, def bool) bool {
	v, ok := params[name]
	if !ok {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

// TrimFromParams derives a Trim from "trim_start"/"trim_end" task params.
// trim_start skips that many seconds from the beginning; trim_end keeps the
// clip but cuts that many seconds off the end (requires the probed duration).
// Returns zero Trim when neither param is set. Returns an error — instead of
// silently producing an untrimmed (or empty) clip — when trim_end is
// requested but the source duration couldn't be probed, or when the
// requested window leaves nothing to encode; the caller must fail the task
// rather than proceed as if trimming had succeeded.
func TrimFromParams(params map[string]any, duration float64) (Trim, error) {
	start := ParamFloat(params, "trim_start", 0)
	end := ParamFloat(params, "trim_end", 0)
	if start <= 0 && end <= 0 {
		return Trim{}, nil
	}
	if end > 0 && duration <= 0 {
		return Trim{}, fmt.Errorf("trim_end=%.2f requested but the source duration could not be probed", end)
	}
	keep := duration - start - end
	if end > 0 && keep <= 0 {
		return Trim{}, fmt.Errorf("trim_start=%.2f trim_end=%.2f leaves no content in a %.2fs source", start, end, duration)
	}
	if keep < 0 {
		keep = 0
	}
	return Trim{Start: start, Keep: keep}, nil
}
