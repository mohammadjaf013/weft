package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHoistGlobalFlags(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"none",
			[]string{"jobs", "list"},
			[]string{"jobs", "list"},
		},
		{
			"key-before-subcommand",
			[]string{"--key", "tok", "jobs", "list"},
			[]string{"jobs", "list", "--key", "tok"},
		},
		{
			"mixed-before-and-after",
			[]string{"--config", "a.yaml", "jobs", "list", "--key", "tok"},
			[]string{"jobs", "list", "--config", "a.yaml", "--key", "tok"},
		},
		{
			"key-equals-form",
			[]string{"--key=tok", "storage", "list"},
			[]string{"storage", "list", "--key=tok"},
		},
		{
			"preserves-positional-flags",
			[]string{"jobs", "create", "in.mp4", "--profile", "vod"},
			[]string{"jobs", "create", "in.mp4", "--profile", "vod"},
		},
	}
	for _, c := range cases {
		got := hoistGlobalFlags(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: hoist(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

func TestFindConfigExplicitWins(t *testing.T) {
	if got := findConfig("my.yaml"); got != "my.yaml" {
		t.Errorf("explicit config = %q, want my.yaml", got)
	}
}

func TestFindConfigFallsBackToCWD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weft.yaml")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if got := findConfig(""); got != "weft.yaml" {
		t.Errorf("findConfig in cwd = %q, want weft.yaml", got)
	}
}

func TestFindConfigNoMatchReturnsDefault(t *testing.T) {
	if got := findConfig(""); got != "weft.yaml" {
		t.Errorf("findConfig = %q, want default weft.yaml", got)
	}
}

func TestJobsCreateBodySrcLang(t *testing.T) {
	body := jobsCreateBody("/mnt/in.mp4", "ai-subtitle", "high", 2, "fa", "tr", "movie", "series", true, "hybrid")
	want := map[string]any{
		"input_ref":      "/mnt/in.mp4",
		"profile":        "ai-subtitle",
		"priority":       "high",
		"destination_id": 2,
		"lang":           "fa",
		"src_lang":       "tr",
		"name":           "movie",
		"path":           "series",
		"delete_source":  true,
		"provider":       "hybrid",
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("jobsCreateBody = %v, want %v", body, want)
	}

	// empty optional flags must be omitted entirely
	minimal := jobsCreateBody("in.mp4", "vod-h264", "normal", 0, "", "", "", "", false, "")
	if _, ok := minimal["src_lang"]; ok {
		t.Errorf("empty src_lang must be omitted: %v", minimal)
	}
	if _, ok := minimal["lang"]; ok {
		t.Errorf("empty lang must be omitted: %v", minimal)
	}
	if _, ok := minimal["delete_source"]; ok {
		t.Errorf("false delete_source must be omitted: %v", minimal)
	}
}
