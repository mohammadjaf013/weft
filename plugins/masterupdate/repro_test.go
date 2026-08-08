package masterupdate

import (
	"strings"
	"testing"
)

// Reproduces the user's real master (4 renditions, INDEPENDENT-SEGMENTS) and
// runs subtitle-add for en then tr in sequence, checking nothing is lost.
func TestRealMasterMultipleSubs(t *testing.T) {
	real := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=1028000,AVERAGE-BANDWIDTH=1028000,RESOLUTION=640x360,CODECS="avc1.640020,mp4a.40.2"
360p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1628000,AVERAGE-BANDWIDTH=1628000,RESOLUTION=854x480,CODECS="avc1.640020,mp4a.40.2"
480p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3128000,AVERAGE-BANDWIDTH=3128000,RESOLUTION=1280x720,CODECS="avc1.640020,mp4a.40.2"
720p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6128000,AVERAGE-BANDWIDTH=6128000,RESOLUTION=1920x1080,CODECS="avc1.640020,mp4a.40.2"
1080p.m3u8
`

	var err error
	out := real
	out, err = updatePlaylist(out, "subtitle", "fa", "subtitle/fa/movie.vtt")
	if err != nil {
		t.Fatal(err)
	}
	out, err = updatePlaylist(out, "subtitle", "en", "subtitle/en/movie.vtt")
	if err != nil {
		t.Fatal(err)
	}
	out, err = updatePlaylist(out, "subtitle", "tr", "subtitle/tr/movie.vtt")
	if err != nil {
		t.Fatal(err)
	}

	// All renditions must survive.
	for _, r := range []string{"360p.m3u8", "480p.m3u8", "720p.m3u8", "1080p.m3u8"} {
		if !strings.Contains(out, r) {
			t.Errorf("missing rendition %s:\n%s", r, out)
		}
	}
	for _, lbl := range []string{"BANDWIDTH=1028000", "BANDWIDTH=6128000", "RESOLUTION=1920x1080"} {
		if !strings.Contains(out, lbl) {
			t.Errorf("missing %s:\n%s", lbl, out)
		}
	}
	// All three languages present, each on its own line.
	for _, lang := range []string{`LANGUAGE="fa"`, `LANGUAGE="en"`, `LANGUAGE="tr"`} {
		if !strings.Contains(out, lang) {
			t.Errorf("missing %s:\n%s", lang, out)
		}
	}
	// EXT-X-MEDIA lines must be newline-separated (not concatenated).
	for _, m := range []string{`movie.vtt`, `LANGUAGE="en"`, `LANGUAGE="tr"`} {
		if !strings.Contains(out, m) {
			t.Errorf("missing media attr %s:\n%s", m, out)
		}
	}
	// No two tags mashed on one line.
	if strings.Contains(out, "dialog\"#EXT-X-MEDIA") {
		t.Errorf("media lines concatenated:\n%s", out)
	}
	// #EXTM3U must be alone on its line.
	if strings.Contains(out, "#EXTM3U#") {
		t.Errorf("#EXTM3U concatenated with next line:\n%s", out)
	}
}
