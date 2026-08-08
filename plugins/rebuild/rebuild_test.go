package rebuild

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/plugins/storage/local"
)

func TestDiscoverFullSet(t *testing.T) {
	files := []string{
		"360p.m3u8",
		"480p.m3u8",
		"720p.m3u8",
		"1080p.m3u8",
		"720p_000.ts",
		"subtitle/fa/movie.vtt",
		"subtitle/en/movie.vtt",
		"audio/tr/movie.m3u8",
		"audio/tr/movie_000.ts",
		"playlist.m3u8", // regenerated
	}
	p, err := Discover(files)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(p.Renditions) != 4 {
		t.Fatalf("renditions = %v, want 4", p.Renditions)
	}
	if got := p.Renditions[0]; got != "360p" {
		t.Errorf("renditions[0] = %q, want 360p", got)
	}
	if len(p.Subtitles) != 2 || p.Subtitles[0].Lang != "en" || p.Subtitles[1].Lang != "fa" {
		t.Errorf("subtitles = %+v, want en,fa", p.Subtitles)
	}
	if len(p.Audios) != 1 || p.Audios[0].Lang != "tr" {
		t.Errorf("audios = %+v, want tr", p.Audios)
	}
	m := p.Master
	for _, want := range []string{
		"#EXTM3U",
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs"`,
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio"`,
		`SUBTITLES="subs"`,
		`AUDIO="audio"`,
		"BANDWIDTH=",
		"1080p.m3u8",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("master missing %q\n%s", want, m)
		}
	}
}

func TestDiscoverNoSubs(t *testing.T) {
	p, err := Discover([]string{"360p.m3u8", "720p.m3u8"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(p.Subtitles) != 0 || len(p.Audios) != 0 {
		t.Fatalf("want no tracks, got subs=%d auds=%d", len(p.Subtitles), len(p.Audios))
	}
	if strings.Contains(p.Master, "SUBTITLES=") || strings.Contains(p.Master, "AUDIO=") {
		t.Errorf("master should not reference empty groups\n%s", p.Master)
	}
}

func TestDiscoverNoRenditions(t *testing.T) {
	if _, err := Discover([]string{"subtitle/fa/a.vtt"}); err == nil {
		t.Fatal("Discover with no renditions must error")
	}
}

func TestRebuildWritesMaster(t *testing.T) {
	dir := t.TempDir()
	st, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{"360p.m3u8", "720p.m3u8", "subtitle/fa/x.vtt", "audio/tr/x.m3u8"}
	for _, f := range seed {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("#EXTM3U\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Rebuild(context.Background(), st, "h264")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if len(p.Renditions) != 2 || len(p.Subtitles) != 1 || len(p.Audios) != 1 {
		t.Fatalf("plan = %+v", p)
	}
	b, err := os.ReadFile(filepath.Join(dir, "playlist.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "subtitle/fa/x.vtt") {
		t.Errorf("master missing subtitle uri:\n%s", b)
	}
	if !strings.Contains(string(b), "audio/tr/x.m3u8") {
		t.Errorf("master missing audio uri:\n%s", b)
	}
	if !strings.Contains(string(b), "avc1.") {
		t.Errorf("master should advertise avc1 codec for h264:\n%s", b)
	}
}

func TestRebuildWritesHevcMaster(t *testing.T) {
	dir := t.TempDir()
	st, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "360p.m3u8"), []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Rebuild(context.Background(), st, "hevc")
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if !strings.Contains(p.Master, "hvc1.") {
		t.Errorf("master should advertise hvc1 codec for hevc:\n%s", p.Master)
	}
}
