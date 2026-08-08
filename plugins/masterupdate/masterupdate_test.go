package masterupdate

import (
	"context"
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
	out, err := updatePlaylist(baseMaster, "subtitle", "fa", "subtitle/fa/movie.vtt")
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
	out, err := updatePlaylist(master, "subtitle", "fa", "subtitle/fa/movie.vtt")
	if err != nil {
		t.Fatalf("updatePlaylist: %v", err)
	}
	if n := strings.Count(out, `TYPE=SUBTITLES`); n != 1 {
		t.Errorf("expected exactly 1 subtitle media line after replace, got %d:\n%s", n, out)
	}
}

func TestUpdatePlaylistAddsTwoLangs(t *testing.T) {
	out, err := updatePlaylist(baseMaster, "subtitle", "fa", "subtitle/fa/movie.vtt")
	if err != nil {
		t.Fatal(err)
	}
	out, err = updatePlaylist(out, "subtitle", "en", "subtitle/en/movie.vtt")
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

func TestUpdatePlaylistAudioGroup(t *testing.T) {
	out, err := updatePlaylist(baseMaster, "audio", "fa", "audio/fa/movie.m3u8")
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
	if _, err := updatePlaylist(baseMaster, "bogus", "fa", "x"); err == nil {
		t.Fatal("expected error for unknown track kind")
	}
}
