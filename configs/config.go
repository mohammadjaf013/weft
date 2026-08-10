// Package config loads and validates weft.yaml. Config errors are startup
// errors — the Agent refuses to come up in a broken state.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Scheduler struct {
		Mode          string  `yaml:"mode"`
		MaxCPUPercent float64 `yaml:"max_cpu_percent"`
		MaxLoadAvg    float64 `yaml:"max_load_average"`
		// MaxEstimatedCPUCores/MaxEstimatedRAMMB cap the SUM of in-flight
		// tasks' declared Capabilities() cost (not measured usage — see
		// runtime/worker.Budget). 0 = unlimited for that dimension.
		MaxEstimatedCPUCores float64 `yaml:"max_estimated_cpu_cores"`
		MaxEstimatedRAMMB    int     `yaml:"max_estimated_ram_mb"`
	} `yaml:"scheduler"`
	Workers struct {
		Min int `yaml:"min"`
		Max int `yaml:"max"`
		// LeaseTTLSeconds is how long a worker may hold a task lease before the
		// expirer requeues it (crash recovery).
		LeaseTTLSeconds int `yaml:"lease_ttl_seconds"`
	} `yaml:"workers"`
	Queue struct {
		Priorities []string `yaml:"priorities"`
	} `yaml:"queue"`
	Workflow struct {
		DefaultTimeoutSeconds int `yaml:"default_timeout_seconds"`
	} `yaml:"workflow"`
	Plugins struct {
		Enabled []string `yaml:"enabled"`
	} `yaml:"plugins"`
	Network struct {
		Listen string `yaml:"listen"`
		TLS    string `yaml:"tls"`
	} `yaml:"network"`
	Security struct {
		APIKeys        bool   `yaml:"api_keys"`
		KeyHash        string `yaml:"key_hash"`
		WebhookSigning string `yaml:"webhook_signing"`
		AdminAPIKey    string `yaml:"admin_api_key"`
	} `yaml:"security"`
	Database struct {
		Path string `yaml:"path"`
	} `yaml:"database"`
	Webhooks []WebhookCfg `yaml:"webhooks"`
	AI       AICfg        `yaml:"ai_subtitle"`
	Cron     CronCfg      `yaml:"cron"`
	Storage  struct {
		Local struct {
			BasePath string `yaml:"base_path"`
		} `yaml:"local"`
	} `yaml:"storage"`
	Executor struct {
		FFmpegPath  string `yaml:"ffmpeg_path"`
		FFprobePath string `yaml:"ffprobe_path"`
	} `yaml:"executor"`
}

type WebhookCfg struct {
	ID             string   `yaml:"id"`
	URL            string   `yaml:"url"`
	Secret         string   `yaml:"secret"`
	Events         []string `yaml:"events"`
	MaxRetries     int      `yaml:"max_retries"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
}

type AICfg struct {
	Provider string `yaml:"provider"`
	Whisper  struct {
		ModelPath   string   `yaml:"model_path"`
		Language    string   `yaml:"language"`    // source language of the audio, e.g. en, tr
		Threads     int      `yaml:"threads"`     // -t CPU threads for whisper-cli
		Temperature *float64 `yaml:"temperature"` // --temperature, e.g. 0.0 for deterministic output
		Prompt      string   `yaml:"prompt"`      // --prompt initial context (names, setting, vocabulary)
		BinPath     string   `yaml:"bin_path"`    // default whisper-cli
	} `yaml:"whisper"`
	Gemini struct {
		APIKey   string `yaml:"api_key"`
		Model    string `yaml:"model"`
		Language string `yaml:"language"`
	} `yaml:"gemini"`
	AutoGenerate struct {
		Enabled         bool     `yaml:"enabled"`
		OnlyIfMissing   bool     `yaml:"only_if_missing"`
		TargetLanguages []string `yaml:"target_languages"`
	} `yaml:"auto_generate"`
}

type CronCfg struct {
	Cleanup struct {
		Schedule           string `yaml:"schedule"`
		RetentionHours     int    `yaml:"retention_hours"`
		EventRetentionDays int    `yaml:"event_retention_days"`
		// DeleteFiles, when true, makes the retention pruner also delete each
		// pruned job's local source file and any leftover work/cache dirs, not
		// just its DB rows. Off by default: existing deployments that rely on
		// retention_hours purely for DB/history cleanup must opt in explicitly
		// before source media starts getting deleted on a schedule.
		DeleteFiles bool `yaml:"delete_files"`
	} `yaml:"cleanup"`
	Benchmark struct {
		Schedule string `yaml:"schedule"`
	} `yaml:"benchmark"`
	HealthScan struct {
		Schedule string `yaml:"schedule"`
	} `yaml:"health_scan"`
}

// Default returns a working local configuration.
func Default() *Config {
	c := &Config{}
	c.Scheduler.Mode = "dynamic"
	c.Scheduler.MaxCPUPercent = 85
	c.Workers.Min = 1
	c.Workers.Max = 0 // auto
	c.Workers.LeaseTTLSeconds = 300
	c.Queue.Priorities = []string{"emergency", "high", "normal", "low", "background"}
	c.Workflow.DefaultTimeoutSeconds = 3600
	c.Plugins.Enabled = []string{"ffmpeg-video", "ffmpeg-audio", "subtitle", "thumbnail", "hls", "master_playlist", "master_update", "upload", "poster_upload", "storage-local", "storage-ssh", "storage-s3", "ai-subtitle"}
	c.Network.Listen = "127.0.0.1:8443"
	c.Network.TLS = "off"
	c.Security.APIKeys = true
	c.Security.KeyHash = "argon2id"
	c.Security.WebhookSigning = "hmac-sha256"
	c.Database.Path = "weft.db"
	c.AI.Provider = "whisper"
	c.AI.AutoGenerate.Enabled = true
	c.AI.AutoGenerate.OnlyIfMissing = true
	c.AI.AutoGenerate.TargetLanguages = []string{"fa", "en"}
	c.Storage.Local.BasePath = "./data"
	c.Executor.FFmpegPath = "ffmpeg"
	c.Executor.FFprobePath = "ffprobe"
	c.Cron.Cleanup.Schedule = "0 3 * * *"
	c.Cron.Cleanup.RetentionHours = 72
	c.Cron.Cleanup.EventRetentionDays = 30
	c.Cron.Benchmark.Schedule = "0 4 * * 0"
	c.Cron.HealthScan.Schedule = "*/5 * * * *"
	return c
}

func Load(path string) (*Config, error) {
	c := Default()
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("invalid config schema: %w", err)
	}
	return c, nil
}

// Validate checks the rules that must hold before the Agent starts. Returns a
// list of problems; non-empty means the Agent refuses to start.
func (c *Config) Validate() []string {
	var problems []string
	if c.Network.Listen == "" {
		problems = append(problems, "network.listen is required")
	}
	switch c.AI.Provider {
	case "whisper":
		if c.AI.Whisper.ModelPath == "" {
			problems = append(problems, "ai_subtitle.provider=whisper but ai_subtitle.whisper.model_path is empty")
		} else if fi, err := os.Stat(c.AI.Whisper.ModelPath); err != nil || fi.IsDir() {
			problems = append(problems, fmt.Sprintf("ai_subtitle.whisper.model_path %q is not readable", c.AI.Whisper.ModelPath))
		}
	case "gemini":
		if c.AI.Gemini.APIKey == "" {
			problems = append(problems, "ai_subtitle.provider=gemini but ai_subtitle.gemini.api_key is empty (use env GEMINI_API_KEY)")
		}
	case "":
		// no AI subtitle configured — fine
	default:
		problems = append(problems, fmt.Sprintf("ai_subtitle.provider %q is invalid (want whisper|gemini)", c.AI.Provider))
	}
	return problems
}

// DefaultTimeout returns the workflow timeout as a duration.
func (c *Config) DefaultTimeout() time.Duration {
	if c.Workflow.DefaultTimeoutSeconds <= 0 {
		return time.Hour
	}
	return time.Duration(c.Workflow.DefaultTimeoutSeconds) * time.Second
}
