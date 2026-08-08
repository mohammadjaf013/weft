package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Network.Listen != "127.0.0.1:8443" {
		t.Errorf("listen = %q", c.Network.Listen)
	}
	if c.Workers.Min != 1 {
		t.Errorf("workers.min = %d", c.Workers.Min)
	}
	if len(c.Plugins.Enabled) == 0 {
		t.Error("no default plugins")
	}
	if c.DefaultTimeout() != time.Hour {
		t.Errorf("default timeout = %v, want 1h", c.DefaultTimeout())
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weft.yaml")
	content := `
network:
  listen: "0.0.0.0:9000"
workers:
  min: 3
ai_subtitle:
  provider: "gemini"
  gemini:
    api_key: "test-key"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Network.Listen != "0.0.0.0:9000" {
		t.Errorf("listen = %q", c.Network.Listen)
	}
	if c.Workers.Min != 3 {
		t.Errorf("workers.min = %d", c.Workers.Min)
	}
	if c.AI.Provider != "gemini" {
		t.Errorf("ai provider = %q", c.AI.Provider)
	}
	// Load keeps defaults for fields not present.
	if c.Database.Path != "weft.db" {
		t.Errorf("database.path = %q", c.Database.Path)
	}
}

func TestLoadWhisperTuning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weft.yaml")
	content := `
ai_subtitle:
  provider: "whisper"
  whisper:
    model_path: "/opt/weft/models/ggml-medium.bin"
    language: "en"
    threads: 8
    temperature: 0.0
    prompt: "Spider-Man, Peter Parker, Zendaya, Green Goblin"
    bin_path: "/usr/local/bin/whisper-cli"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.AI.Whisper.ModelPath != "/opt/weft/models/ggml-medium.bin" {
		t.Errorf("model_path = %q", c.AI.Whisper.ModelPath)
	}
	if c.AI.Whisper.Language != "en" {
		t.Errorf("language = %q", c.AI.Whisper.Language)
	}
	if c.AI.Whisper.Threads != 8 {
		t.Errorf("threads = %d", c.AI.Whisper.Threads)
	}
	if c.AI.Whisper.Temperature == nil || *c.AI.Whisper.Temperature != 0.0 {
		t.Errorf("temperature = %v, want 0.0", c.AI.Whisper.Temperature)
	}
	if c.AI.Whisper.Prompt != "Spider-Man, Peter Parker, Zendaya, Green Goblin" {
		t.Errorf("prompt = %q", c.AI.Whisper.Prompt)
	}
	if c.AI.Whisper.BinPath != "/usr/local/bin/whisper-cli" {
		t.Errorf("bin_path = %q", c.AI.Whisper.BinPath)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("network: [unclosed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid yaml")
	}
}

func TestValidate(t *testing.T) {
	// whisper without model_path
	c := Default()
	c.AI.Provider = "whisper"
	c.AI.Whisper.ModelPath = ""
	if probs := c.Validate(); len(probs) == 0 {
		t.Error("expected validation error for whisper without model_path")
	}

	// gemini without api_key
	c = Default()
	c.AI.Provider = "gemini"
	if probs := c.Validate(); len(probs) == 0 {
		t.Error("expected validation error for gemini without api_key")
	}

	// invalid provider
	c = Default()
	c.AI.Provider = "claude"
	if probs := c.Validate(); len(probs) == 0 {
		t.Error("expected validation error for invalid provider")
	}

	// whisper with readable model passes (but no model file exists by default)
	c = Default()
	c.AI.Provider = "whisper"
	c.AI.Whisper.ModelPath = filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(c.AI.Whisper.ModelPath, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if probs := c.Validate(); len(probs) != 0 {
		t.Errorf("unexpected validation problems: %v", probs)
	}

	// empty listen
	c = Default()
	c.Network.Listen = ""
	if probs := c.Validate(); len(probs) == 0 {
		t.Error("expected validation error for empty listen")
	}
}

func TestDefaultTimeoutCustom(t *testing.T) {
	c := Default()
	c.Workflow.DefaultTimeoutSeconds = 300
	if got := c.DefaultTimeout(); got != 300*time.Second {
		t.Errorf("timeout = %v", got)
	}
	c.Workflow.DefaultTimeoutSeconds = 0
	if got := c.DefaultTimeout(); got != time.Hour {
		t.Errorf("timeout = %v", got)
	}
}
