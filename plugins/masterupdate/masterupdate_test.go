package masterupdate

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	"github.com/mohammadjaf013/weft/plugins/storage/local"
)

const baseMaster = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720
movie/720p/movie.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
movie/1080p/movie.m3u8
`

func TestUpdatePlaylistAddsSubtitle(t *testing.T) {
	out, err := updatePlaylist(baseMaster, "subtitle", "fa", "subtitle/fa/movie.vtt", false, false)
	if err != nil {
		t.Fatalf("updatePlaylist: %v", err)
	}
	for _, want := range []string{
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="فارسی",DEFAULT=NO,AUTOSELECT=YES,FORCED=NO,LANGUAGE="fa",URI="subtitle/fa/movie.vtt"`,
		`SUBTITLES="subs"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("playlist missing %q:\n%s", want, out)
		}
	}
	// Media line must come before the stream infs.
	mi := strings.Index(out, `TYPE=SUBTITLES`)
	si := strings.Index(out, `#EXT-X-STREAM-INF`)
	if mi < 0 || si < 0 || mi > si {
		t.Errorf("media line must precede stream-inf (media=%d stream=%d):\n%s", mi, si, out)
	}
}

func TestUpdatePlaylistReplacesSameLang(t *testing.T) {
	master := baseMaster + `
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="فارسی",DEFAULT=YES,AUTOSELECT=YES,FORCED=NO,LANGUAGE="fa",URI="subtitle/fa/movie.vtt"`
	out, err := updatePlaylist(master, "subtitle", "fa", "subtitle/fa/movie.vtt", false, false)
	if err != nil {
		t.Fatalf("updatePlaylist: %v", err)
	}
	if n := strings.Count(out, `TYPE=SUBTITLES`); n != 1 {
		t.Errorf("expected exactly 1 subtitle media line after replace, got %d:\n%s", n, out)
	}
}

func TestUpdatePlaylistAddsTwoLangs(t *testing.T) {
	out, err := updatePlaylist(baseMaster, "subtitle", "fa", "subtitle/fa/movie.vtt", false, false)
	if err != nil {
		t.Fatal(err)
	}
	out, err = updatePlaylist(out, "subtitle", "en", "subtitle/en/movie.vtt", false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{`LANGUAGE="fa"`, `LANGUAGE="en"`} {
		if !strings.Contains(out, lang) {
			t.Errorf("missing %s:\n%s", lang, out)
		}
	}
	if n := strings.Count(out, `TYPE=SUBTITLES`); n != 2 {
		t.Errorf("expected 2 subtitle media lines, got %d:\n%s", n, out)
	}
}

// TestUpdatePlaylistSubtitleAppliesToAllRenditions is the regression test for
// a production bug: SUBTITLES="subs" was only being appended to the first
// EXT-X-STREAM-INF line seen (a "firstStream" latch in updatePlaylist), so on
// a real 4-rendition ladder only 360p ended up tagged after adding "fa", and
// only 480p after subsequently adding "en" -- 720p/1080p never got the
// group reference and so never offered subtitles to the player.
const fourRungMaster = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=1208000,RESOLUTION=640x360
360p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1928000,RESOLUTION=852x480
480p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3792000,RESOLUTION=1280x720
720p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=7392000,RESOLUTION=1920x1080
1080p.m3u8
`

func TestUpdatePlaylistSubtitleAppliesToAllRenditions(t *testing.T) {
	out, err := updatePlaylist(fourRungMaster, "subtitle", "fa", "subtitle/fa/movie.vtt", false, false)
	if err != nil {
		t.Fatalf("updatePlaylist: %v", err)
	}
	if n := strings.Count(out, `SUBTITLES="subs"`); n != 4 {
		t.Errorf("expected SUBTITLES=\"subs\" on all 4 renditions, got %d:\n%s", n, out)
	}

	// Adding a second language must not duplicate the attribute or leave any
	// rendition behind.
	out, err = updatePlaylist(out, "subtitle", "en", "subtitle/en/movie.vtt", false, false)
	if err != nil {
		t.Fatalf("updatePlaylist: %v", err)
	}
	if n := strings.Count(out, `SUBTITLES="subs"`); n != 4 {
		t.Errorf("expected SUBTITLES=\"subs\" on all 4 renditions after 2nd lang, got %d:\n%s", n, out)
	}
}

// TestUpdatePlaylistAudioAppliesToAllRenditions mirrors the subtitle
// regression above for the "audio" track kind -- same updatePlaylist code
// path, same bug, same fix.
func TestUpdatePlaylistAudioAppliesToAllRenditions(t *testing.T) {
	out, err := updatePlaylist(fourRungMaster, "audio", "fa", "audio/fa/movie.m3u8", false, false)
	if err != nil {
		t.Fatalf("updatePlaylist: %v", err)
	}
	if n := strings.Count(out, `AUDIO="audio"`); n != 4 {
		t.Errorf("expected AUDIO=\"audio\" on all 4 renditions, got %d:\n%s", n, out)
	}
}

// TestUpdatePlaylistDefaultCanBeToggledByResend verifies that re-running
// updatePlaylist for the same language with a different `default` value
// replaces the previous EXT-X-MEDIA line (via the existing same-language
// replace semantics) rather than adding a duplicate -- so removing a
// previously-set DEFAULT=YES just means calling subtitle-add again with
// --default omitted/false for the same --lang.
func TestUpdatePlaylistDefaultCanBeToggledByResend(t *testing.T) {
	out, err := updatePlaylist(fourRungMaster, "subtitle", "fa", "subtitle/fa/movie.vtt", false, true)
	if err != nil {
		t.Fatalf("updatePlaylist: %v", err)
	}
	if !strings.Contains(out, `LANGUAGE="fa"`) || !strings.Contains(out, "DEFAULT=YES") {
		t.Fatalf("expected fa subtitle DEFAULT=YES after first send:\n%s", out)
	}

	out, err = updatePlaylist(out, "subtitle", "fa", "subtitle/fa/movie.vtt", false, false)
	if err != nil {
		t.Fatalf("updatePlaylist: %v", err)
	}
	if n := strings.Count(out, `LANGUAGE="fa"`); n != 1 {
		t.Errorf("expected exactly 1 fa media line after resend, got %d:\n%s", n, out)
	}
	if strings.Contains(out, "DEFAULT=YES") {
		t.Errorf("expected DEFAULT=NO after resending with default=false:\n%s", out)
	}
	if !strings.Contains(out, "DEFAULT=NO") {
		t.Errorf("expected DEFAULT=NO present after resend:\n%s", out)
	}
}

func TestUpdatePlaylistAudioGroup(t *testing.T) {
	out, err := updatePlaylist(baseMaster, "audio", "fa", "audio/fa/movie.m3u8", false, false)
	if err != nil {
		t.Fatalf("updatePlaylist: %v", err)
	}
	for _, want := range []string{
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="فارسی",DEFAULT=NO,AUTOSELECT=YES,FORCED=NO,LANGUAGE="fa",URI="audio/fa/movie.m3u8"`,
		`AUDIO="audio"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("playlist missing %q:\n%s", want, out)
		}
	}
}

// TestUpdatePlaylistForcedDefaultFlags is the regression test for D4: before
// the fix, DEFAULT/FORCED were hardcoded to NO regardless of what the caller
// requested.
func TestUpdatePlaylistForcedDefaultFlags(t *testing.T) {
	out, err := updatePlaylist(baseMaster, "subtitle", "fa", "subtitle/fa/movie.vtt", true, true)
	if err != nil {
		t.Fatalf("updatePlaylist: %v", err)
	}
	want := `#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="فارسی",DEFAULT=YES,AUTOSELECT=YES,FORCED=YES,LANGUAGE="fa",URI="subtitle/fa/movie.vtt"`
	if !strings.Contains(out, want) {
		t.Errorf("playlist missing %q:\n%s", want, out)
	}

	// default (unset) case must still be NO/NO.
	outDefault, err := updatePlaylist(baseMaster, "subtitle", "fa", "subtitle/fa/movie.vtt", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outDefault, "DEFAULT=NO") || !strings.Contains(outDefault, "FORCED=NO") {
		t.Errorf("unset forced/default must render NO/NO:\n%s", outDefault)
	}
}

// TestProcessAppliesForcedDefaultParams verifies Process reads the
// forced/default task params (as set by buildTasks from --forced/--default)
// and threads them through to updatePlaylist.
func TestProcessAppliesForcedDefaultParams(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	dir := t.TempDir()
	st, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref := core.AssetRef{Kind: "playlist", Name: "playlist.m3u8"}
	if err := st.Save(context.Background(), ref, strings.NewReader(baseMaster)); err != nil {
		t.Fatalf("seed master: %v", err)
	}
	in := core.TaskInput{
		TaskID:  "t2",
		Kind:    "update_master",
		Params:  map[string]any{"lang": "fa", "name": "movie", "track": "subtitle", "forced": true, "default": true},
		Storage: st,
	}
	if _, err := (&Plugin{}).Process(context.Background(), in); err != nil {
		t.Fatalf("Process: %v", err)
	}
	rc, err := st.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "DEFAULT=YES") || !strings.Contains(string(b), "FORCED=YES") {
		t.Errorf("stored master missing forced/default flags:\n%s", b)
	}
}

func TestProcessUpdatesStoredMaster(t *testing.T) {
	mediautil.WorkRoot = t.TempDir()
	dir := t.TempDir()
	st, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ref := core.AssetRef{Kind: "playlist", Name: "playlist.m3u8"}
	if err := st.Save(context.Background(), ref, strings.NewReader(baseMaster)); err != nil {
		t.Fatalf("seed master: %v", err)
	}

	in := core.TaskInput{
		TaskID:  "t1",
		Kind:    "update_master",
		Params:  map[string]any{"lang": "fa", "name": "movie", "track": "subtitle"},
		Storage: st,
	}
	out, err := (&Plugin{}).Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(out.Assets) != 0 {
		t.Errorf("expected no upload assets (master written directly), got %d", len(out.Assets))
	}
	rc, err := st.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	var sb strings.Builder
	buf := make([]byte, 1024)
	for {
		n, _ := rc.Read(buf)
		if n == 0 {
			break
		}
		sb.Write(buf[:n])
	}
	if !strings.Contains(sb.String(), `LANGUAGE="fa"`) || !strings.Contains(sb.String(), `SUBTITLES="subs"`) {
		t.Errorf("stored master not updated:\n%s", sb.String())
	}
}

func TestUpdatePlaylistUnknownKind(t *testing.T) {
	if _, err := updatePlaylist(baseMaster, "bogus", "fa", "x", false, false); err == nil {
		t.Fatal("expected error for unknown track kind")
	}
}
