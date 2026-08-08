package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

// doctorErr is returned when any check fails, so `weft doctor` exits non-zero
// (like flutter doctor) without implying the report was not printed.
var doctorErr = errors.New("doctor: one or more checks failed")

// checkResult is a single doctor line. Status is one of ok|warn|fail|skip.
type checkResult struct {
	status  string
	title   string
	details []string
}

const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
	statusSkip = "skip"
)

// doctorCheck runs one check and returns its result.
type doctorCheck func(c *cfg.Config) checkResult

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "weft.yaml", "path to config file")
	_ = fs.Parse(args)

	var c *cfg.Config
	c, _ = cfg.Load(*configPath) // load errors are reported by the config check

	fmt.Printf("Weft Doctor %s\n", core.Version)
	fmt.Println()

	results := []checkResult{
		checkBinary(c),
		checkConfig(c, *configPath),
		checkExecTool(c, "ffmpeg", c.Executor.FFmpegPath, "executor.ffmpeg_path"),
		checkExecTool(c, "ffprobe", c.Executor.FFprobePath, "executor.ffprobe_path"),
		checkAI(c),
		checkDatabase(c),
		checkStorage(c),
		checkNetwork(c),
		checkPlugins(c),
	}

	failed := 0
	for _, r := range results {
		printResult(r)
		if r.status == statusFail {
			failed++
		}
	}

	fmt.Println()
	fmt.Printf("doctor finished: %d passed, %d failed, %d warned\n",
		len(results)-failed, failed, warned(results))
	if failed > 0 {
		return doctorErr
	}
	return nil
}

func warned(results []checkResult) int {
	n := 0
	for _, r := range results {
		if r.status == statusWarn {
			n++
		}
	}
	return n
}

// printResult renders one line, flutter-doctor style.
func printResult(r checkResult) {
	var mark string
	switch r.status {
	case statusOK:
		mark = "[✓]"
	case statusWarn:
		mark = "[!]"
	case statusFail:
		mark = "[✗]"
	default:
		mark = "[–]"
	}
	fmt.Printf("%s %s\n", mark, r.title)
	for _, d := range r.details {
		fmt.Printf("    • %s\n", d)
	}
}

// --- individual checks ---

func checkBinary(_ *cfg.Config) checkResult {
	return checkResult{status: statusOK, title: "Weft binary", details: []string{"version " + core.Version}}
}

func checkConfig(c *cfg.Config, path string) checkResult {
	r := checkResult{title: "Configuration"}
	if c == nil {
		r.status = statusFail
		r.details = []string{fmt.Sprintf("%s could not be loaded", path)}
		return r
	}
	probs := c.Validate()
	if len(probs) > 0 {
		r.status = statusFail
		r.details = probs
		return r
	}
	r.status = statusOK
	r.details = []string{fmt.Sprintf("%s loaded and valid", path)}
	return r
}

// checkExecTool verifies a binary is resolvable (either on PATH or an absolute
// path from config) and reports its --version line.
func checkExecTool(c *cfg.Config, name, configured, field string) checkResult {
	r := checkResult{title: name}
	if c == nil {
		r.status = statusSkip
		r.details = []string{"skipped (no config)"}
		return r
	}
	binary := configured
	if binary == "" {
		binary = name
	}
	binPath, err := exec.LookPath(binary)
	if err != nil {
		if configured != "" {
			if _, serr := os.Stat(configured); serr == nil {
				binPath = configured
				err = nil
			}
		}
	}
	if err != nil {
		r.status = statusFail
		r.details = []string{fmt.Sprintf("%s not found (config %s=%q)", name, field, configured)}
		return r
	}
	ver := versionLine(binPath)
	r.status = statusOK
	r.details = []string{fmt.Sprintf("%s found at %s", name, binPath)}
	if ver != "" {
		r.details = append(r.details, ver)
	}
	return r
}

func versionLine(bin string) string {
	cmd := exec.Command(bin, "-version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	first := strings.SplitN(string(out), "\n", 2)[0]
	return strings.TrimSpace(first)
}

func checkAI(c *cfg.Config) checkResult {
	r := checkResult{title: "AI subtitle provider"}
	if c == nil {
		r.status = statusSkip
		return r
	}
	switch c.AI.Provider {
	case "whisper":
		if c.AI.Whisper.ModelPath == "" {
			r.status = statusWarn
			r.details = []string{"provider=whisper but whisper.model_path is empty (ai-subtitle disabled)"}
			return r
		}
		if fi, err := os.Stat(c.AI.Whisper.ModelPath); err != nil {
			r.status = statusWarn
			r.details = []string{fmt.Sprintf("whisper model %q not readable: %v", c.AI.Whisper.ModelPath, err)}
			return r
		} else if fi.IsDir() {
			r.status = statusWarn
			r.details = []string{fmt.Sprintf("whisper model %q is a directory, want a file", c.AI.Whisper.ModelPath)}
			return r
		}
		r.status = statusOK
		r.details = []string{fmt.Sprintf("whisper model at %s", c.AI.Whisper.ModelPath)}
	case "gemini":
		if c.AI.Gemini.APIKey == "" {
			r.status = statusWarn
			r.details = []string{"provider=gemini but gemini.api_key is empty (ai-subtitle disabled)"}
			return r
		}
		r.status = statusOK
		r.details = []string{fmt.Sprintf("gemini model %q", orDefault(c.AI.Gemini.Model, "gemini-2.0-flash"))}
	case "":
		r.status = statusSkip
		r.details = []string{"no provider configured — ai-subtitle disabled"}
	default:
		r.status = statusWarn
		r.details = []string{fmt.Sprintf("unknown provider %q", c.AI.Provider)}
	}
	return r
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func checkDatabase(c *cfg.Config) checkResult {
	r := checkResult{title: "Database"}
	if c == nil {
		r.status = statusSkip
		return r
	}
	if err := os.MkdirAll(filepath.Dir(c.Database.Path), 0o755); err != nil {
		r.status = statusFail
		r.details = []string{fmt.Sprintf("cannot create db directory: %v", err)}
		return r
	}
	// Opening the real store proves the schema migrates on this machine.
	store, err := sqlite.Open(c.Database.Path)
	if err != nil {
		r.status = statusFail
		r.details = []string{fmt.Sprintf("cannot open %s: %v", c.Database.Path, err)}
		return r
	}
	defer store.Close()
	// A trivial read proves the file is a working database.
	_, err = store.ListJobs(context.Background(), core.JobFilter{})
	if err != nil {
		r.status = statusFail
		r.details = []string{fmt.Sprintf("database read failed: %v", err)}
		return r
	}
	r.status = statusOK
	r.details = []string{fmt.Sprintf("%s opens and schema migrates", c.Database.Path)}
	return r
}

func checkStorage(c *cfg.Config) checkResult {
	r := checkResult{title: "Storage"}
	if c == nil {
		r.status = statusSkip
		return r
	}
	base := c.Storage.Local.BasePath
	if base == "" {
		r.status = statusFail
		r.details = []string{"storage.local.base_path is empty"}
		return r
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		r.status = statusFail
		r.details = []string{fmt.Sprintf("cannot create %s: %v", base, err)}
		return r
	}
	probe := filepath.Join(base, ".weft-doctor-write-test")
	f, err := os.Create(probe)
	if err != nil {
		r.status = statusFail
		r.details = []string{fmt.Sprintf("%s is not writable: %v", base, err)}
		return r
	}
	f.Close()
	os.Remove(probe)
	r.status = statusOK
	r.details = []string{fmt.Sprintf("%s writable", base)}
	return r
}

func checkNetwork(c *cfg.Config) checkResult {
	r := checkResult{title: "Network"}
	if c == nil {
		r.status = statusSkip
		return r
	}
	ln, err := net.Listen("tcp", c.Network.Listen)
	if err != nil {
		r.status = statusWarn
		r.details = []string{
			fmt.Sprintf("%s is not free: %v", c.Network.Listen, err),
			"another process (perhaps weft) is already listening",
		}
		return r
	}
	ln.Close()
	r.status = statusOK
	r.details = []string{fmt.Sprintf("listen %s is free", c.Network.Listen)}
	return r
}

func checkPlugins(c *cfg.Config) checkResult {
	r := checkResult{title: "Plugins"}
	if c == nil {
		r.status = statusSkip
		return r
	}
	valid := map[string]bool{
		"ffmpeg-video": true, "ffmpeg-audio": true, "subtitle": true,
		"thumbnail": true, "hls": true, "master_playlist": true,
		"upload": true, "ai-subtitle": true, "storage-local": true,
		"storage-ssh": true, "storage-s3": true,
	}
	if len(c.Plugins.Enabled) == 0 {
		r.status = statusFail
		r.details = []string{"plugins.enabled is empty"}
		return r
	}
	var unknown []string
	for _, p := range c.Plugins.Enabled {
		if !valid[p] {
			unknown = append(unknown, p)
		}
	}
	r.status = statusOK
	r.details = []string{fmt.Sprintf("%d plugin(s) enabled: %s", len(c.Plugins.Enabled), strings.Join(c.Plugins.Enabled, ", "))}
	if len(unknown) > 0 {
		r.status = statusWarn
		r.details = append(r.details, fmt.Sprintf("unknown plugin names: %s", strings.Join(unknown, ", ")))
	}
	return r
}
