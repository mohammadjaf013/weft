package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	cfg "github.com/mohammadjaf013/weft/configs"
)

// fakeTool writes a tiny executable script that prints a version line and
// exits 0, so the doctor's ffmpeg/ffprobe checks pass on any platform (CI
// runners don't ship ffmpeg). Returns the absolute path.
func fakeTool(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "fake-tool")
	if runtime.GOOS == "windows" {
		p += ".cmd"
	}
	content := "@echo off\necho fake-tool version 1.0.0\nexit /b 0\n"
	if runtime.GOOS != "windows" {
		content = "#!/bin/sh\necho fake-tool version 1.0.0\nexit 0\n"
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDoctorOK runs doctor against a valid config and expects nil.
func TestDoctorOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weft.yaml")
	tool := fakeTool(t)
	c := cfg.Default()
	c.AI.Provider = ""
	c.Database.Path = filepath.Join(dir, "weft.db")
	c.Storage.Local.BasePath = filepath.Join(dir, "out")
	c.Network.Listen = "127.0.0.1:0" // must be free — ":0" always is
	c.Executor.FFmpegPath = tool
	c.Executor.FFprobePath = tool
	if err := writeYAML(path, c); err != nil {
		t.Fatal(err)
	}

	if err := cmdDoctor([]string{"--config", path}); err != nil {
		t.Fatalf("doctor on valid config: %v", err)
	}
}

// TestDoctorFailsOnInvalidConfig expects a non-nil error (exit != 0).
func TestDoctorFailsOnInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weft_bad.yaml")
	c := cfg.Default()
	c.AI.Provider = "whisper" // model_path empty => validation failure
	c.Database.Path = filepath.Join(dir, "weft.db")
	c.Storage.Local.BasePath = filepath.Join(dir, "out")
	c.Network.Listen = "127.0.0.1:0"
	if err := writeYAML(path, c); err != nil {
		t.Fatal(err)
	}

	err := cmdDoctor([]string{"--config", path})
	if err == nil {
		t.Fatal("doctor should fail on invalid config")
	}
	if !errors.Is(err, doctorErr) {
		t.Errorf("want doctorErr, got %v", err)
	}
}

// TestDoctorReportContainsSections ensures the report mentions the core checks.
func TestDoctorReportContainsSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weft.yaml")
	tool := fakeTool(t)
	c := cfg.Default()
	c.AI.Provider = ""
	c.Database.Path = filepath.Join(dir, "weft.db")
	c.Storage.Local.BasePath = filepath.Join(dir, "out")
	c.Network.Listen = "127.0.0.1:0"
	c.Executor.FFmpegPath = tool
	c.Executor.FFprobePath = tool
	if err := writeYAML(path, c); err != nil {
		t.Fatal(err)
	}

	old := os.Stdout
	rr, w, _ := os.Pipe()
	os.Stdout = w
	_ = cmdDoctor([]string{"--config", path})
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(rr)
	out := buf.String()

	for _, want := range []string{"Weft Doctor", "ffmpeg", "ffprobe", "Database", "Storage", "Network", "Plugins"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor report missing %q:\n%s", want, out)
		}
	}
}
