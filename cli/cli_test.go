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

// TestParseRemotePicksUpAdminKey ensures a remote command run from a directory
// that has no weft.yaml still discovers one in a well-known install location
// and reads security.admin_api_key from it — the token that 401s without.
func TestParseRemotePicksUpAdminKey(t *testing.T) {
	dir := t.TempDir()
	conf := "network:\n  listen: 127.0.0.1:9443\nsecurity:\n  api_keys: true\n  admin_api_key: secret-token-123\n"
	if err := os.WriteFile(filepath.Join(dir, "weft.yaml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	// run the CLI from an empty dir so the cwd has no config; point it at the
	// discovered dir by simulating findConfig via a --config flag to that dir
	fs := remoteFlagSet("weft")
	rf, err := parseRemote(fs, []string{"--config", filepath.Join(dir, "weft.yaml"), "jobs", "get", "j1"})
	if err != nil {
		t.Fatal(err)
	}
	if rf.key != "secret-token-123" {
		t.Errorf("key = %q, want secret-token-123 (read from config)", rf.key)
	}
	if rf.api != "http://127.0.0.1:9443" {
		t.Errorf("api = %q, want http://127.0.0.1:9443", rf.api)
	}
	// --key flag must win over the config file
	fs2 := remoteFlagSet("weft")
	rf2, err := parseRemote(fs2, []string{"--key", "explicit", "--config", filepath.Join(dir, "weft.yaml"), "jobs", "get", "j1"})
	if err != nil {
		t.Fatal(err)
	}
	if rf2.key != "explicit" {
		t.Errorf("explicit key = %q, want explicit", rf2.key)
	}
}

func TestJobsCreateBodySrcLang(t *testing.T) {
	body := jobsCreateBody("/mnt/in.mp4", "ai-subtitle", "high", 2, "fa", "tr", "movie", "series", true, "hybrid", 0, 0, 0, "")
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
	minimal := jobsCreateBody("in.mp4", "vod-h264", "normal", 0, "", "", "", "", false, "", 0, 0, 0, "")
	if _, ok := minimal["src_lang"]; ok {
		t.Errorf("empty src_lang must be omitted: %v", minimal)
	}
	if _, ok := minimal["lang"]; ok {
		t.Errorf("empty lang must be omitted: %v", minimal)
	}
	if _, ok := minimal["delete_source"]; ok {
		t.Errorf("false delete_source must be omitted: %v", minimal)
	}

	// trim + custom thumbnail params
	trimmed := jobsCreateBody("in.mp4", "vod-h264", "normal", 0, "", "", "", "", false, "", 50, 10, 5, "1080x1080")
	if trimmed["trim_start"] != float64(50) || trimmed["trim_end"] != float64(10) {
		t.Errorf("trim params = %v", trimmed)
	}
	if trimmed["thumb_count"] != 5 || trimmed["thumb_size"] != "1080x1080" {
		t.Errorf("thumb params = %v", trimmed)
	}
}
