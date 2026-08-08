package mediautil

import (
	"testing"

	"github.com/mohammadjaf013/weft/core"
)

func TestBaseName(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"s3://in/movie.mp4", "movie"},
		{"local:/abs/path/pod.mp3", "pod"},
		{`G:\develop\weft\rtest\sample.mp4`, "sample"}, // Windows path
		{"film.mkv", "film"},
		{"nested/dir/video.mp4", "video"},
		{"", "task_x"}, // no ref -> task id fallback
	}
	for _, c := range cases {
		in := core.TaskInput{InputRef: c.ref, TaskID: "task_x"}
		if got := BaseName(in); got != c.want {
			t.Errorf("BaseName(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}
