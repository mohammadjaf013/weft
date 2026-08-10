package thumbnail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	ffexec "github.com/mohammadjaf013/weft/runtime/executor/ffmpeg"
)

func TestProcessProducesPosterSpriteVTT(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t1",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params:   map[string]any{},
		Executor: fake,
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) < 3 {
		t.Fatalf("expected >=3 assets, got %d", len(out.Assets))
	}
	if out.Assets[0].Kind != "thumbnail" {
		t.Errorf("asset[0].Kind = %q, want thumbnail", out.Assets[0].Kind)
	}
	if out.Assets[1].Kind != "sprite" {
		t.Errorf("asset[1].Kind = %q, want sprite", out.Assets[1].Kind)
	}
	if out.Assets[2].Kind != "vtt" {
		t.Errorf("asset[2].Kind = %q, want vtt", out.Assets[2].Kind)
	}
	args := fake.RecordedArgs()
	if len(args) != 3 {
		t.Fatalf("expected 3 ffmpeg execs, got %d", len(args))
	}
	// The generated VTT must actually exist on disk.
	if _, err := os.Stat(mediautil.WorkDir(in) + "/movie_preview.vtt"); err != nil {
		t.Errorf("generated vtt missing: %v", err)
	}
}

// TestPosterAndSpriteUseImage2Update is the regression test for a production
// bug: modern ffmpeg's image2 muxer refuses to write a single frame to a
// non-pattern filename ("...c731781eaf_poster.jpg") without -update 1 —
// -frames:v 1 alone isn't enough — and errors with "does not contain an
// image sequence pattern or a pattern is invalid". Both single-frame outputs
// (poster, sprite) must pass -update 1 right alongside -frames:v 1.
func TestPosterAndSpriteUseImage2Update(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "tupd",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params:   map[string]any{},
		Executor: fake,
	}
	if _, err := (&Plugin{}).Process(context.Background(), in); err != nil {
		t.Fatalf("Process: %v", err)
	}
	args := fake.RecordedArgs()
	if len(args) != 3 {
		t.Fatalf("expected 3 ffmpeg execs (poster, sprite, stills), got %d", len(args))
	}
	// poster (args[0]) and sprite (args[1]) are single-frame outputs to a
	// non-pattern filename — both need -update 1.
	for i, label := range []string{"poster", "sprite"} {
		if !hasFlagPair(args[i], "-update", "1") {
			t.Errorf("%s argv missing -update 1: %v", label, args[i])
		}
	}
	// stills (args[2]) writes a %03d pattern — -update is not needed there.
}

func hasFlagPair(argv []string, flag, value string) bool {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) && argv[i+1] == value {
			return true
		}
	}
	return false
}

func TestProcessWithRealStills(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t2",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params:   map[string]any{},
		Executor: fake,
	}
	// Simulate ffmpeg having produced numbered stills.
	work := mediautil.WorkDir(in)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s/movie_%03d.jpg", work, i), []byte("jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// Every still jpg must be reported as an asset in thumbnails/.
	var stills []core.AssetRef
	for _, a := range out.Assets {
		if a.Kind == "thumbnail" && a.Dir == "thumbnails" && a.Name != "movie_poster.jpg" {
			stills = append(stills, a)
		}
	}
	if len(stills) == 0 {
		t.Fatalf("expected per-second stills in assets, got none")
	}
	// The stills argv must write numbered jpgs (movie_001.jpg style).
	found := false
	for _, args := range fake.RecordedArgs() {
		for _, a := range args {
			if strings.Contains(a, "movie_%03d.jpg") {
				found = true
			}
		}
	}
	if !found {
		t.Error("stills argv missing numbered jpg pattern")
	}
}

func TestProcessCustomThumbCount(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t3",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params: map[string]any{
			"thumb_count": float64(5),
			"thumb_size":  "1080x1080",
		},
		Executor: fake,
	}
	work := mediautil.WorkDir(in)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s/movie_thumb_%02d.jpg", work, i), []byte("jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) != 5 {
		t.Fatalf("custom mode should produce exactly 5 thumbnails, got %d", len(out.Assets))
	}
	for _, a := range out.Assets {
		if a.Kind != "thumbnail" || a.Dir != "thumbnails" {
			t.Errorf("asset = %+v, want kind=thumbnail dir=thumbnails", a)
		}
	}
	args := fake.RecordedArgs()
	if len(args) != 1 {
		t.Fatalf("custom mode should run a single ffmpeg, got %d", len(args))
	}
	var scale, frames bool
	for _, a := range args[0] {
		if strings.Contains(a, "scale=1080:1080") {
			scale = true
		}
		if a == "-frames:v" {
			frames = true
		}
	}
	if !scale {
		t.Errorf("custom size missing scale=1080:1080: %v", args[0])
	}
	if !frames {
		t.Errorf("custom count missing -frames:v: %v", args[0])
	}
}

func TestProcessSingleThumbAt(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t3b",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params: map[string]any{
			"thumb_at":   float64(83),
			"thumb_size": "640x360",
		},
		Executor: fake,
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) != 1 {
		t.Fatalf("thumb_at mode should produce exactly 1 asset, got %d", len(out.Assets))
	}
	if out.Assets[0].Kind != "thumbnail" || out.Assets[0].Dir != "thumbnails" {
		t.Errorf("asset = %+v, want kind=thumbnail dir=thumbnails", out.Assets[0])
	}
	args := fake.RecordedArgs()
	if len(args) != 1 {
		t.Fatalf("thumb_at mode should run a single ffmpeg, got %d", len(args))
	}
	var seek, frames, scale bool
	for i, a := range args[0] {
		if a == "-ss" && i+1 < len(args[0]) && args[0][i+1] == "83.000" {
			seek = true
		}
		if a == "-frames:v" {
			frames = true
		}
		if strings.Contains(a, "scale=640:360") {
			scale = true
		}
	}
	if !seek {
		t.Errorf("thumb_at missing -ss 83.000: %v", args[0])
	}
	if !frames {
		t.Errorf("thumb_at missing -frames:v: %v", args[0])
	}
	// regression: image2 muxer needs -update 1 to write a single frame to a
	// non-pattern filename (thumb_at.jpg) — see TestPosterAndSpriteUseImage2Update.
	if !hasFlagPair(args[0], "-update", "1") {
		t.Errorf("thumb_at missing -update 1: %v", args[0])
	}
	if !scale {
		t.Errorf("thumb_at missing scale=640:360: %v", args[0])
	}
}

// realMasterPlaylist mirrors mediautil.MasterPlaylistCodec's actual output
// shape (see the production log this is a regression test for): four
// renditions named "{label}.m3u8" in ascending order, with subtitle
// EXT-X-MEDIA lines interspersed before the stream-infs.
const realMasterPlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="فارسی",DEFAULT=NO,AUTOSELECT=YES,FORCED=NO,LANGUAGE="fa",URI="subtitle/fa/فارسی.vtt"
#EXT-X-STREAM-INF:BANDWIDTH=1208000,AVERAGE-BANDWIDTH=1028000,RESOLUTION=640x360,FRAME-RATE=23.976,CODECS="avc1.64001E,mp4a.40.2",SUBTITLES="subs"
360p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1928000,AVERAGE-BANDWIDTH=1628000,RESOLUTION=852x480,FRAME-RATE=23.976,CODECS="avc1.64001F,mp4a.40.2",SUBTITLES="subs"
480p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3792000,AVERAGE-BANDWIDTH=3192000,RESOLUTION=1280x720,FRAME-RATE=23.976,CODECS="avc1.640020,mp4a.40.2"
720p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=7392000,AVERAGE-BANDWIDTH=6192000,RESOLUTION=1920x1080,FRAME-RATE=23.976,CODECS="avc1.640028,mp4a.40.2"
1080p.m3u8
`

func TestResolveHLSRenditionPrefers1080p(t *testing.T) {
	dir := t.TempDir()
	master := filepath.Join(dir, "playlist.m3u8")
	if err := os.WriteFile(master, []byte(realMasterPlaylist), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolveHLSRendition(master)
	want := filepath.Join(dir, "1080p.m3u8")
	if got != want {
		t.Errorf("resolveHLSRendition = %q, want %q", got, want)
	}
}

func TestResolveHLSRenditionFallsBackWhen1080pMissing(t *testing.T) {
	m3u8 := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1928000,RESOLUTION=852x480
480p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3792000,RESOLUTION=1280x720
720p.m3u8
`
	dir := t.TempDir()
	master := filepath.Join(dir, "playlist.m3u8")
	if err := os.WriteFile(master, []byte(m3u8), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolveHLSRendition(master)
	want := filepath.Join(dir, "720p.m3u8")
	if got != want {
		t.Errorf("resolveHLSRendition = %q, want %q (720p, since 1080p isn't in the ladder)", got, want)
	}
}

func TestResolveHLSRenditionPassesThroughNonMaster(t *testing.T) {
	// Not a master playlist: no #EXT-X-STREAM-INF lines (already a single
	// rendition, e.g. someone pointed straight at 720p.m3u8).
	rendition := `#EXTM3U
#EXT-X-VERSION:3
#EXTINF:6.0,
seg000.ts
#EXT-X-ENDLIST
`
	dir := t.TempDir()
	path := filepath.Join(dir, "720p.m3u8")
	if err := os.WriteFile(path, []byte(rendition), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveHLSRendition(path); got != path {
		t.Errorf("resolveHLSRendition(rendition-level playlist) = %q, want unchanged %q", got, path)
	}

	// Not even an m3u8.
	mp4 := filepath.Join(dir, "movie.mp4")
	if got := resolveHLSRendition(mp4); got != mp4 {
		t.Errorf("resolveHLSRendition(mp4) = %q, want unchanged", got)
	}
}

// TestProcessThumbAtRetargetsMasterPlaylist is the regression test for a
// production failure: pointing --thumb-at at an already-published job's
// master playlist.m3u8 (fetched via source_server input resolution) made
// ffmpeg fail with "could not seek to position 10.000" / "does not contain
// any stream", because a master playlist has no duration or decodable
// stream of its own. Process must retarget the ffmpeg -i argument at one
// real rendition (preferring 1080p) before running.
func TestProcessThumbAtRetargetsMasterPlaylist(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	srcDir := t.TempDir()
	master := filepath.Join(srcDir, "playlist.m3u8")
	if err := os.WriteFile(master, []byte(realMasterPlaylist), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t3c",
		InputRef: "s3://in/movie.mp4",
		InputURI: master,
		Params:   map[string]any{"thumb_at": float64(10)},
		Executor: fake,
	}
	if _, err := (&Plugin{}).Process(context.Background(), in); err != nil {
		t.Fatalf("Process: %v", err)
	}
	args := fake.RecordedArgs()
	if len(args) != 1 {
		t.Fatalf("expected a single ffmpeg exec, got %d", len(args))
	}
	want := filepath.Join(srcDir, "1080p.m3u8")
	found := false
	for i, a := range args[0] {
		if a == "-i" && i+1 < len(args[0]) {
			if args[0][i+1] != want {
				t.Errorf("-i argument = %q, want %q (not the master playlist)", args[0][i+1], want)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no -i argument found in argv: %v", args[0])
	}
}

func TestProcessCustomThumbOriginalSize(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	fake := ffexec.NewFake(core.Result{ExitCode: 0}, nil)
	in := core.TaskInput{
		TaskID:   "t4",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params: map[string]any{
			"thumb_count": float64(1),
			"thumb_size":  "original",
		},
		Executor: fake,
	}
	work := mediautil.WorkDir(in)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(work+"/movie_thumb_01.jpg", []byte("jpeg"), 0o644)
	if _, err := (&Plugin{}).Process(context.Background(), in); err != nil {
		t.Fatalf("Process: %v", err)
	}
	args := fake.RecordedArgs()[0]
	for _, a := range args {
		if strings.Contains(a, "scale=") {
			t.Errorf("original size must not scale: %v", args)
		}
	}
}

func TestProcessTrimCoordinated(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	// MediaInfo with a known duration so TrimFromParams can compute the window.
	fake := ffexec.NewFake(core.Result{ExitCode: 0, MediaInfo: core.MediaInfo{HasVideo: true, DurationSec: 100}}, nil)
	in := core.TaskInput{
		TaskID:   "t5",
		InputRef: "s3://in/movie.mp4",
		InputURI: "local:work/movie.mp4",
		Params: map[string]any{
			"trim_start": float64(50),
			"trim_end":   float64(10),
		},
		Executor: fake,
	}
	work := mediautil.WorkDir(in)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(work+"/movie_poster.jpg", []byte("jpeg"), 0o644)
	if _, err := (&Plugin{}).Process(context.Background(), in); err != nil {
		t.Fatalf("Process: %v", err)
	}
	// every default ffmpeg run must carry the -ss seek and -t window
	args := fake.RecordedArgs()
	if len(args) < 3 {
		t.Fatalf("expected >=3 execs, got %d", len(args))
	}
	for _, a := range args {
		if !containsAny(a, "-ss", "50.000") {
			t.Errorf("trimmed run missing -ss 50.000: %v", a)
		}
		if !containsAny(a, "-t", "40.000") {
			t.Errorf("trimmed run missing -t 40.000: %v", a)
		}
	}
}

func containsAny(xs []string, want, want2 string) bool {
	for _, x := range xs {
		if x == want || x == want2 {
			return true
		}
	}
	return false
}
