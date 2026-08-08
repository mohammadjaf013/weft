package mediautil

import (
	"context"
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
