// Package ffmpeg implements core.Executor by shelling out to ffmpeg/ffprobe
// via os/exec and parsing FFmpeg's native `-progress pipe:1` output. No bash
// scripts, no text scraping of stderr conventions.
package ffmpeg

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mohammadjaf013/weft/core"
)

// CommandLocator returns the paths of ffmpeg and ffprobe. Overridable for tests.
type CommandLocator struct {
	FFmpeg  string
	FFprobe string
}

func DefaultLocator() CommandLocator {
	return CommandLocator{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
}

type Executor struct {
	loc CommandLocator
	// fake short-circuits Run in tests so no external binary is needed.
	fake *fakeRunner
}

var _ core.Executor = (*Executor)(nil)

type fakeRunner struct {
	mu          sync.Mutex
	args        [][]string // recorded invocations
	result      core.Result
	err         error
	progressLog []float64
}

func New(loc CommandLocator) *Executor {
	return &Executor{loc: loc}
}

// NewFake returns an executor that records invocations and returns canned
// results — used by Runtime integration tests so no real ffmpeg is required.
func NewFake(result core.Result, err error) *Executor {
	return &Executor{loc: DefaultLocator(), fake: &fakeRunner{result: result, err: err}}
}

func (e *Executor) SetFakeProgress(progress []float64) {
	if e.fake != nil {
		e.fake.progressLog = progress
	}
}

func (e *Executor) RecordedArgs() [][]string {
	if e.fake == nil {
		return nil
	}
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	out := make([][]string, len(e.fake.args))
	copy(out, e.fake.args)
	return out
}

// Run executes the ffmpeg argument list carried in task.Params["argv"]. The
// argv entries come from a plugin's Process(); the executor only supervises.
// When the input is local and probeable, the input duration is used as the
// progress denominator so out_time_ms reports a real 0..100 during the run.
func (e *Executor) Run(ctx context.Context, task core.Task, in core.TaskInput) (core.Result, error) {
	if e.fake != nil {
		return e.fake.run(ctx, in)
	}

	argv, ok := in.Params["argv"].([]string)
	if !ok {
		return core.Result{}, fmt.Errorf("task %s: params['argv'] missing or wrong type", task.ID)
	}
	argv = append([]string{"-nostdin", "-hide_banner", "-y", "-progress", "pipe:1"}, argv...)

	durationMS := int64(-1)
	if in.InputURI != "" {
		if mi, err := e.Probe(ctx, in.InputURI); err == nil && mi.DurationSec > 0 {
			durationMS = int64(mi.DurationSec * 1000)
		}
	}

	cmd := exec.CommandContext(ctx, e.loc.FFmpeg, argv...)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return core.Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return core.Result{}, err
	}

	go e.parseProgress(stdout, in.Progress, durationMS)

	err = cmd.Wait()
	res := core.Result{ExitCode: 0}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			return res, err
		}
	}
	if res.ExitCode != 0 {
		return res, fmt.Errorf("ffmpeg exited with code %d", res.ExitCode)
	}
	return res, nil
}

// parseProgress reads ffmpeg's `-progress pipe:1` key=value stream and turns
// out_time / duration into 0..100 callbacks. progress=end means the stream
// is done. durationMS is the probe-derived input duration in ms; when it is
// unknown (-1) the percentage falls back to 100 at the end only.
//
// Note: ffmpeg's `out_time_ms` key is a long-standing misnomer — the value is
// actually MICROseconds (out_time_us in newer builds). We always divide it by
// 1000 to get real milliseconds.
func (e *Executor) parseProgress(r io.Reader, cb core.ProgressFunc, durationMS int64) {
	if cb == nil {
		io.Copy(io.Discard, r)
		return
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var outTimeMS int64 = -1
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "out_time_ms", "out_time_us":
			us, _ := strconv.ParseInt(val, 10, 64)
			outTimeMS = us / 1000
			if durationMS > 0 && outTimeMS >= 0 {
				cb(min100(outTimeMS, durationMS))
			}
		case "out_time":
			if outTimeMS < 0 {
				outTimeMS = parseTimeToMs(val)
				if durationMS > 0 && outTimeMS >= 0 {
					cb(min100(outTimeMS, durationMS))
				}
			}
		case "progress":
			if val == "end" {
				cb(100)
			}
		}
	}
}

func min100(a, b int64) float64 {
	if a >= b {
		return 100
	}
	return float64(a) / float64(b) * 100
}

var timeRe = regexp.MustCompile(`(\d+):(\d+):(\d+)(?:\.(\d+))?`)

func parseTimeToMs(s string) int64 {
	m := timeRe.FindStringSubmatch(s)
	if m == nil {
		return -1
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	sec, _ := strconv.Atoi(m[3])
	ms := int64(0)
	if m[4] != "" {
		f, _ := strconv.ParseFloat("0."+m[4], 64)
		ms = int64(f * 1000)
	}
	return int64(h)*3600000 + int64(mi)*60000 + int64(sec)*1000 + ms
}

// Probe runs ffprobe against a file and returns structured media info.
func (e *Executor) Probe(ctx context.Context, path string) (core.MediaInfo, error) {
	if e.fake != nil {
		return e.fake.result.MediaInfo, e.fake.err
	}
	cmd := exec.CommandContext(ctx, e.loc.FFprobe,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return core.MediaInfo{}, fmt.Errorf("ffprobe failed: %w", err)
	}
	return parseProbeJSON(out)
}

func parseProbeJSON(out []byte) (core.MediaInfo, error) {
	var info struct {
		Format struct {
			Duration   string `json:"duration"`
			FormatName string `json:"format_name"`
		} `json:"format"`
		Streams []struct {
			CodecType     string `json:"codec_type"`
			Width         int    `json:"width"`
			Height        int    `json:"height"`
			AvgFrameRate  string `json:"avg_frame_rate"`
			RFrameRate    string `json:"r_frame_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return core.MediaInfo{}, err
	}
	mi := core.MediaInfo{Format: info.Format.FormatName}
	mi.DurationSec, _ = strconv.ParseFloat(info.Format.Duration, 64)
	for _, s := range info.Streams {
		switch s.CodecType {
		case "video":
			mi.HasVideo = true
			mi.VideoStreams++
			if mi.Width == 0 {
				mi.Width = s.Width
				mi.Height = s.Height
			}
			if mi.FrameRate == 0 {
				mi.FrameRate = parseFrameRate(s.AvgFrameRate)
				if mi.FrameRate == 0 {
					mi.FrameRate = parseFrameRate(s.RFrameRate)
				}
			}
		case "audio":
			mi.HasAudio = true
			mi.AudioStreams++
		case "subtitle":
			mi.HasSubtitles = true
		}
	}
	return mi, nil
}

// parseFrameRate parses ffprobe's "30000/1001" or "23.976" forms. Returns 0
// when unparseable.
func parseFrameRate(s string) float64 {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	num, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	den, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil || den == 0 {
		return 0
	}
	return num / den
}

func (f *fakeRunner) run(ctx context.Context, in core.TaskInput) (core.Result, error) {
	f.mu.Lock()
	f.args = append(f.args, in.Params["argv"].([]string))
	f.mu.Unlock()
	if f.progressLog != nil {
		for _, p := range f.progressLog {
			select {
			case <-ctx.Done():
				return core.Result{}, ctx.Err()
			default:
			}
			if in.Progress != nil {
				in.Progress(p)
			}
			time.Sleep(time.Millisecond)
		}
	}
	return f.result, f.err
}
