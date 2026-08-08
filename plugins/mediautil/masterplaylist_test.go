package mediautil

import "testing"

func TestMasterPlaylistMatchesLegacy(t *testing.T) {
	got := MasterPlaylist(DefaultH264Ladder, 23.976)
	want := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-STREAM-INF:BANDWIDTH=1208000,AVERAGE-BANDWIDTH=1028000,RESOLUTION=640x360,FRAME-RATE=23.976,CODECS="avc1.64001E,mp4a.40.2"
360p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1928000,AVERAGE-BANDWIDTH=1628000,RESOLUTION=852x480,FRAME-RATE=23.976,CODECS="avc1.64001F,mp4a.40.2"
480p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3792000,AVERAGE-BANDWIDTH=3192000,RESOLUTION=1280x720,FRAME-RATE=23.976,CODECS="avc1.640020,mp4a.40.2"
720p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=7392000,AVERAGE-BANDWIDTH=6192000,RESOLUTION=1920x1080,FRAME-RATE=23.976,CODECS="avc1.640028,mp4a.40.2"
1080p.m3u8
`
	if got != want {
		t.Errorf("MasterPlaylist mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestMasterPlaylistNoFrameRate(t *testing.T) {
	got := MasterPlaylist([]Rung{{Label: "720p", Width: 1280, Height: 720, Bitrate: "3000k"}}, 0)
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-INDEPENDENT-SEGMENTS\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=3792000,AVERAGE-BANDWIDTH=3192000,RESOLUTION=1280x720,CODECS=\"avc1.640020,mp4a.40.2\"\n" +
		"720p.m3u8\n"
	if got != want {
		t.Errorf("MasterPlaylist without FRAME-RATE mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
