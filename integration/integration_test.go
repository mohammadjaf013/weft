// Package integration runs the real media pipeline: actual ffmpeg/ffprobe,
// the real plugin set, real sqlite, real upload to local storage. These tests
// are skipped when ffmpeg is not on PATH, so `go test ./...` stays green on
// machines without media tooling.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/daemon"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

// hasTool reports whether a binary is resolvable on PATH.
func hasTool(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if !hasTool("ffmpeg") || !hasTool("ffprobe") {
		t.Skip("ffmpeg/ffprobe not on PATH; skipping real-media integration test")
	}
}

// makeSample creates a tiny real mp4 (testsrc + sine) and returns its path.
func makeSample(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "sample.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=15:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make sample: %v\n%s", err, b)
	}
	return out
}

func postJSON(t *testing.T, url, path string, body any) (map[string]any, int) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out, resp.StatusCode
}

func waitCompleted(t *testing.T, baseURL, id string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/jobs/" + id)
		if err != nil {
			t.Fatalf("GET job: %v", err)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if out["status"] == "completed" {
			return out
		}
		if s, _ := out["status"].(string); s == "failed" {
			t.Fatalf("job %s failed: %v", id, out["error"])
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("job %s did not complete in %v", id, timeout)
	return nil
}

// TestRealVODPipeline is the fully-real run: real ffmpeg encodes a 4-rung H.264
// ladder + thumbnails, real master playlist, real upload, verified with ffprobe.
func TestRealVODPipeline(t *testing.T) {
	requireFFmpeg(t)

	root := t.TempDir()
	mediautil.WorkRoot = root
	uploadBase := filepath.Join(root, "out")

	input := makeSample(t, root)
	c := cfg.Default()
	c.Database.Path = filepath.Join(root, "weft.db")
	c.Network.Listen = "127.0.0.1:0"
	c.Security.APIKeys = false
	c.AI.Provider = ""
	c.AI.AutoGenerate.Enabled = false
	c.Storage.Local.BasePath = uploadBase
	c.Workers.Min = 1
	c.Plugins.Enabled = []string{"hls", "thumbnail", "subtitle", "upload"}

	d, err := daemon.Open(c, nil)
	if err != nil {
		t.Fatalf("daemon open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer d.Store().Close()
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()

	addr := waitAddr(t, d)
	baseURL := "http://" + addr

	// Real job through the real API.
	out, code := postJSON(t, baseURL, "/jobs", map[string]any{
		"input_ref": input, // local path; daemon's default resolver handles it
		"profile":   "vod-h264",
		"priority":  "normal",
	})
	if code != http.StatusCreated {
		t.Fatalf("create job: %d %v", code, out)
	}
	id := out["id"].(string)

	waitCompleted(t, baseURL, id, 90*time.Second)

	// Every expected asset landed directly under the upload base root, since
	// the operator's path is the literal final location (no job-id subfolder).
	jobDir := uploadBase
	expect := map[string]string{
		"360p.m3u8":                  "", // variant playlists + master at root
		"480p.m3u8":                  "",
		"720p.m3u8":                  "",
		"1080p.m3u8":                 "",
		"360p_000.ts":                "",
		"1080p_000.ts":               "",
		"_poster.jpg":                "thumbnails",
		"_sprite.jpg":                "thumbnails",
		"_preview.vtt":               "thumbnails",
		"playlist.m3u8":              "",
	}
	for suffix, dir := range expect {
		found := false
		_ = filepath.Walk(jobDir, func(p string, fi os.FileInfo, _ error) error {
			if fi.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(jobDir, p)
			if err != nil {
				return nil
			}
			relDir := filepath.ToSlash(filepath.Dir(rel))
			if relDir == "." {
				relDir = ""
			}
			if relDir != dir {
				return nil
			}
			if strings.HasSuffix(filepath.Base(rel), suffix) {
				found = true
			}
			return nil
		})
		if !found {
			t.Errorf("missing uploaded asset %s%s (matching *%s) in %s", dir+"/", "<name>", suffix, jobDir)
		}
	}
	// thumbnails must live under a thumbnails/ subfolder, not the job root
	if _, err := os.Stat(filepath.Join(jobDir, "thumbnails")); err != nil {
		t.Errorf("expected a thumbnails/ subfolder: %v", err)
	}

	// The encoded ladder must really be h264 at the right resolutions.
	probe := func(path, suffix string, wantW, wantH int) {
		t.Helper()
		var match string
		_ = filepath.Walk(jobDir, func(p string, fi os.FileInfo, _ error) error {
			if !fi.IsDir() && strings.HasSuffix(p, suffix) {
				match = p
			}
			return nil
		})
		if match == "" {
			t.Fatalf("no *%s to probe", suffix)
		}
		cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", match)
		b, err := cmd.Output()
		if err != nil {
			t.Fatalf("ffprobe %s: %v", match, err)
		}
		parts := strings.Split(strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0]), ",")
		if len(parts) != 3 || parts[0] != "h264" {
			t.Fatalf("%s codec = %v, want h264", match, parts)
		}
		if parts[1] != fmt.Sprintf("%d", wantW) || parts[2] != fmt.Sprintf("%d", wantH) {
			t.Errorf("%s resolution = %vx%v, want %dx%d", match, parts[1], parts[2], wantW, wantH)
		}
	}
	probe(jobDir, "360p_000.ts", 640, 360)
	probe(jobDir, "720p_000.ts", 1280, 720)
	probe(jobDir, "1080p_000.ts", 1920, 1080)

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("serve did not shut down")
	}
}

// TestVODPipelineDestPath verifies that a job's path lands under a custom
// subdirectory of the destination root (one server, many folders).
func TestVODPipelineDestPath(t *testing.T) {
	requireFFmpeg(t)

	root := t.TempDir()
	mediautil.WorkRoot = root
	uploadBase := filepath.Join(root, "out")

	input := makeSample(t, root)
	c := cfg.Default()
	c.Database.Path = filepath.Join(root, "weft.db")
	c.Network.Listen = "127.0.0.1:0"
	c.Security.APIKeys = false
	c.AI.Provider = ""
	c.AI.AutoGenerate.Enabled = false
	c.Storage.Local.BasePath = uploadBase
	c.Workers.Min = 1
	c.Plugins.Enabled = []string{"hls", "thumbnail", "subtitle", "upload"}

	d, err := daemon.Open(c, nil)
	if err != nil {
		t.Fatalf("daemon open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer d.Store().Close()
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()

	addr := waitAddr(t, d)
	baseURL := "http://" + addr

	out, code := postJSON(t, baseURL, "/jobs", map[string]any{
		"input_ref": input,
		"profile":   "vod-h264",
		"path":      "series",
	})
	if code != http.StatusCreated {
		t.Fatalf("create job: %d %v", code, out)
	}
	id := out["id"].(string)

	waitCompleted(t, baseURL, id, 90*time.Second)

	// Output must be uploadBase/series/... (the path is the literal final
	// folder, no <job-id> component anymore).
	jobDir := filepath.Join(uploadBase, "series")
	if _, err := os.Stat(filepath.Join(jobDir, "playlist.m3u8")); err != nil {
		t.Fatalf("expected playlist under %s: %v", jobDir, err)
	}
	if _, err := os.Stat(filepath.Join(uploadBase, id)); !os.IsNotExist(err) {
		t.Errorf("output leaked into storage root %s (want only under series/)", uploadBase)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("serve did not shut down")
	}
}

// TestVODPipelineDeleteSource verifies that a job created with delete_source
// removes its source file from disk once it completes, and cleans the work dir.
func TestVODPipelineDeleteSource(t *testing.T) {
	requireFFmpeg(t)

	root := t.TempDir()
	mediautil.WorkRoot = root

	input := makeSample(t, root)
	uploadBase := filepath.Join(root, "out")

	c := cfg.Default()
	c.Database.Path = filepath.Join(root, "weft.db")
	c.Network.Listen = "127.0.0.1:0"
	c.Security.APIKeys = false
	c.AI.Provider = ""
	c.AI.AutoGenerate.Enabled = false
	c.Storage.Local.BasePath = uploadBase
	c.Workers.Min = 1
	c.Plugins.Enabled = []string{"hls", "thumbnail", "subtitle", "upload"}

	d, err := daemon.Open(c, nil)
	if err != nil {
		t.Fatalf("daemon open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer d.Store().Close()
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()

	addr := waitAddr(t, d)
	baseURL := "http://" + addr

	out, code := postJSON(t, baseURL, "/jobs", map[string]any{
		"input_ref":    input,
		"profile":      "vod-h264",
		"delete_source": true,
	})
	if code != http.StatusCreated {
		t.Fatalf("create job: %d %v", code, out)
	}
	id := out["id"].(string)

	waitCompleted(t, baseURL, id, 90*time.Second)

	// Source file must be gone after completion.
	if _, err := os.Stat(input); !os.IsNotExist(err) {
		t.Errorf("source %s still exists after delete_source job", input)
	}
	// Work dirs must be cleaned (cleaner runs async after completion event).
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "work")); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("work dir still present after cleanup")
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("serve did not shut down")
	}
}

// TestSubtitleAddToExistingUploads a standalone SRT, updates the published
// master playlist to reference it, and uploads both — the subtitle track
// becomes selectable in the player.
func TestSubtitleAddToExisting(t *testing.T) {
	requireFFmpeg(t)

	root := t.TempDir()
	mediautil.WorkRoot = root
	uploadBase := filepath.Join(root, "out")

	srt := filepath.Join(root, "fa.srt")
	srtContent := "1\n00:00:01,000 --> 00:00:03,000\nسلام\n\n"
	if err := os.WriteFile(srt, []byte(srtContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed an already-published video: a master playlist the operator wants to
	// extend with a Persian subtitle.
	movieDir := filepath.Join(uploadBase, "Series-Test", "movie1")
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	masterContent := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720
movie/720p/movie.m3u8
`
	if err := os.WriteFile(filepath.Join(movieDir, "playlist.m3u8"), []byte(masterContent), 0o644); err != nil {
		t.Fatal(err)
	}

	c := cfg.Default()
	c.Database.Path = filepath.Join(root, "weft.db")
	c.Network.Listen = "127.0.0.1:0"
	c.Security.APIKeys = false
	c.AI.Provider = ""
	c.AI.AutoGenerate.Enabled = false
	c.Storage.Local.BasePath = uploadBase
	c.Workers.Min = 1
	c.Plugins.Enabled = []string{"subtitle", "master_update", "upload"}

	d, err := daemon.Open(c, nil)
	if err != nil {
		t.Fatalf("daemon open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer d.Store().Close()
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()

	addr := waitAddr(t, d)
	baseURL := "http://" + addr

	out, code := postJSON(t, baseURL, "/jobs", map[string]any{
		"input_ref": srt,
		"profile":   "subtitle-add",
		"lang":      "fa",
		"name":      "movie",
		"path":      "Series-Test/movie1",
	})
	if code != http.StatusCreated {
		t.Fatalf("create job: %d %v", code, out)
	}
	id := out["id"].(string)
	waitCompleted(t, baseURL, id, 60*time.Second)

	// The converted subtitle must land under <dest>/subtitle/fa/.
	subDir := filepath.Join(uploadBase, "Series-Test", "movie1", "subtitle", "fa")
	if _, err := os.Stat(filepath.Join(subDir, "movie.vtt")); err != nil {
		t.Fatalf("expected movie.vtt under %s: %v", subDir, err)
	}
	b, err := os.ReadFile(filepath.Join(subDir, "movie.vtt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "WEBVTT") {
		t.Errorf("converted vtt missing WEBVTT header: %s", b)
	}

	// The master must now carry an EXT-X-MEDIA subtitle track for fa.
	master, err := os.ReadFile(filepath.Join(movieDir, "playlist.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	ms := string(master)
	if !strings.Contains(ms, `TYPE=SUBTITLES`) || !strings.Contains(ms, `LANGUAGE="fa"`) {
		t.Errorf("master not updated with fa subtitle track:\n%s", ms)
	}
	if !strings.Contains(ms, `SUBTITLES="subs"`) {
		t.Errorf("master stream-inf missing subtitle group:\n%s", ms)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("serve did not shut down")
	}
}

// TestSubtitleAddReplace verifies re-submitting the same language replaces the
// previous track instead of adding a duplicate EXT-X-MEDIA line.
func TestSubtitleAddReplace(t *testing.T) {
	requireFFmpeg(t)

	root := t.TempDir()
	mediautil.WorkRoot = root
	uploadBase := filepath.Join(root, "out")

	srt := filepath.Join(root, "fa.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:03,000\nسلام\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	movieDir := filepath.Join(uploadBase, "Series-Test", "movie1")
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	masterContent := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720
movie/720p/movie.m3u8
`
	if err := os.WriteFile(filepath.Join(movieDir, "playlist.m3u8"), []byte(masterContent), 0o644); err != nil {
		t.Fatal(err)
	}

	c := cfg.Default()
	c.Database.Path = filepath.Join(root, "weft.db")
	c.Network.Listen = "127.0.0.1:0"
	c.Security.APIKeys = false
	c.AI.Provider = ""
	c.AI.AutoGenerate.Enabled = false
	c.Storage.Local.BasePath = uploadBase
	c.Workers.Min = 1
	c.Plugins.Enabled = []string{"subtitle", "master_update", "upload"}

	d, err := daemon.Open(c, nil)
	if err != nil {
		t.Fatalf("daemon open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer d.Store().Close()
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()

	addr := waitAddr(t, d)
	baseURL := "http://" + addr

	for i := 0; i < 2; i++ {
		out, code := postJSON(t, baseURL, "/jobs", map[string]any{
			"input_ref": srt,
			"profile":   "subtitle-add",
			"lang":      "fa",
			"name":      "movie",
			"path":      "Series-Test/movie1",
		})
		if code != http.StatusCreated {
			t.Fatalf("create job %d: %d %v", i, code, out)
		}
		waitCompleted(t, baseURL, out["id"].(string), 60*time.Second)
	}

	master, err := os.ReadFile(filepath.Join(movieDir, "playlist.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	ms := string(master)
	if n := strings.Count(ms, `TYPE=SUBTITLES`); n != 1 {
		t.Errorf("expected exactly 1 subtitle track after re-submit, got %d:\n%s", n, ms)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("serve did not shut down")
	}
}

// TestDubAddPackagesAudioUploads it under audio/<lang>/ and attaches an audio
// group to the published master playlist.
func TestDubAdd(t *testing.T) {
	requireFFmpeg(t)

	root := t.TempDir()
	mediautil.WorkRoot = root
	uploadBase := filepath.Join(root, "out")

	audio := filepath.Join(root, "dub.mp3")
	if _, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=2", "-y", audio).Output(); err != nil {
		t.Fatalf("make audio: %v", err)
	}

	movieDir := filepath.Join(uploadBase, "Series-Test", "movie1")
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	masterContent := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720
movie/720p/movie.m3u8
`
	if err := os.WriteFile(filepath.Join(movieDir, "playlist.m3u8"), []byte(masterContent), 0o644); err != nil {
		t.Fatal(err)
	}

	c := cfg.Default()
	c.Database.Path = filepath.Join(root, "weft.db")
	c.Network.Listen = "127.0.0.1:0"
	c.Security.APIKeys = false
	c.AI.Provider = ""
	c.AI.AutoGenerate.Enabled = false
	c.Storage.Local.BasePath = uploadBase
	c.Workers.Min = 1
	c.Plugins.Enabled = []string{"hls", "master_update", "upload"}

	d, err := daemon.Open(c, nil)
	if err != nil {
		t.Fatalf("daemon open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer d.Store().Close()
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()

	addr := waitAddr(t, d)
	baseURL := "http://" + addr

	out, code := postJSON(t, baseURL, "/jobs", map[string]any{
		"input_ref": audio,
		"profile":   "dub-add",
		"lang":      "fa",
		"name":      "movie",
		"path":      "Series-Test/movie1",
	})
	if code != http.StatusCreated {
		t.Fatalf("create job: %d %v", code, out)
	}
	waitCompleted(t, baseURL, out["id"].(string), 120*time.Second)

	audioDir := filepath.Join(movieDir, "audio", "fa")
	if _, err := os.Stat(filepath.Join(audioDir, "movie.m3u8")); err != nil {
		t.Fatalf("expected audio playlist under %s: %v", audioDir, err)
	}

	master, err := os.ReadFile(filepath.Join(movieDir, "playlist.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	ms := string(master)
	if !strings.Contains(ms, `TYPE=AUDIO`) || !strings.Contains(ms, `LANGUAGE="fa"`) {
		t.Errorf("master not updated with fa audio track:\n%s", ms)
	}
	if !strings.Contains(ms, `AUDIO="audio"`) {
		t.Errorf("master stream-inf missing audio group:\n%s", ms)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("serve did not shut down")
	}
}

func waitAddr(t *testing.T, d *daemon.Daemon) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for d.Addr == "" {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not bind")
		}
		time.Sleep(30 * time.Millisecond)
	}
	return d.Addr
}

// TestRebuildMaster scans an existing media directory whose master playlist was
// lost or corrupted, regenerates playlist.m3u8 from the files actually present,
// and verifies renditions + a subtitle track are wired back in.
func TestRebuildMaster(t *testing.T) {
	root := t.TempDir()
	mediautil.WorkRoot = root
	uploadBase := filepath.Join(root, "out")

	// Seed an already-published directory: real renditions + subtitle files,
	// but a truncated/corrupted master playlist (the old-binary bug).
	movieDir := filepath.Join(uploadBase, "Series-Test", "movie1")
	for _, d := range []string{
		filepath.Join(movieDir, "subtitle", "fa"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"720p.m3u8":               "#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:2,\n720p_000.ts\n",
		"720p_000.ts":             "fake-segment",
		"1080p.m3u8":              "#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:2,\n1080p_000.ts\n",
		"1080p_000.ts":            "fake-segment",
		"subtitle/fa/movie.vtt":   "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nسلام\n",
		"playlist.m3u8":           "#EXTM3U\n#EXT-X-VERSION:3\nbroken-glued-line¼rkÃ§e",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(movieDir, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	c := cfg.Default()
	c.Database.Path = filepath.Join(root, "weft.db")
	c.Network.Listen = "127.0.0.1:0"
	c.Security.APIKeys = false
	c.AI.Provider = ""
	c.AI.AutoGenerate.Enabled = false
	c.Storage.Local.BasePath = uploadBase
	c.Workers.Min = 1
	c.Plugins.Enabled = []string{"subtitle", "master_update", "upload"}

	d, err := daemon.Open(c, nil)
	if err != nil {
		t.Fatalf("daemon open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer d.Store().Close()
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()

	addr := waitAddr(t, d)
	baseURL := "http://" + addr

	out, code := postJSON(t, baseURL, "/storage/rebuild-master", map[string]any{
		"destination_id": 0,
		"path":           "Series-Test/movie1",
	})
	if code != http.StatusOK {
		t.Fatalf("rebuild-master = %d %v", code, out)
	}
	if got := out["status"]; got != "ok" {
		t.Fatalf("status = %v, want ok", got)
	}
	if rend, _ := out["renditions"].([]any); len(rend) != 2 {
		t.Errorf("renditions = %v, want 2 (720p, 1080p)", rend)
	}

	master, err := os.ReadFile(filepath.Join(movieDir, "playlist.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	ms := string(master)
	for _, want := range []string{"#EXT-X-STREAM-INF", "720p.m3u8", "1080p.m3u8"} {
		if !strings.Contains(ms, want) {
			t.Errorf("rebuilt master missing %q:\n%s", want, ms)
		}
	}
	if strings.Contains(ms, "¼rkÃ§e") {
		t.Errorf("rebuilt master still contains mojibake from corrupted input:\n%s", ms)
	}
	if !strings.Contains(ms, `TYPE=SUBTITLES`) || !strings.Contains(ms, `LANGUAGE="fa"`) {
		t.Errorf("rebuilt master missing fa subtitle track:\n%s", ms)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("serve did not shut down")
	}
}
