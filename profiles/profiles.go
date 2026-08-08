// Package profiles defines the media processing profiles (DAG templates) and
// the job builder that turns a profile into a task graph. Adding a profile is
// data, not code.
package profiles

import (
	"fmt"

	"github.com/mohammadjaf013/weft/core"
)

// Profile describes a workflow template: the task kinds it produces and the
// dependency edges between them.
type Profile struct {
	Name          string
	Description   string
	DefaultOutput string
	// TaskKinds in the order they appear in the graph.
	TaskKinds []string
	// Edges: parent -> child. A task is created per kind per node.
	Edges []Edge
	// Params applied to each task kind.
	Params map[string]map[string]any
}

type Edge struct {
	From string
	To   string
}

var Profiles = map[string]Profile{
	"vod-h264": {
		Name:        "vod-h264",
		Description: "H.264 HLS: 360p/480p/720p/1080p renditions, thumbnails, subtitle, master playlist, upload",
		TaskKinds:   []string{"hls", "thumbnail", "subtitle", "upload"},
		Edges: []Edge{
			{From: "hls", To: "thumbnail"},
			{From: "hls", To: "subtitle"},
			{From: "thumbnail", To: "upload"},
			{From: "subtitle", To: "upload"},
			{From: "hls", To: "upload"},
		},
		Params: map[string]map[string]any{
			"hls": {"ladder": []string{"360p", "480p", "720p", "1080p"}},
		},
	},
	"vod-hevc": {
		Name:        "vod-hevc",
		Description: "HEVC HLS: 360p/480p/720p/1080p renditions, thumbnails, subtitle, master playlist, upload",
		TaskKinds:   []string{"hls", "thumbnail", "subtitle", "upload"},
		Edges: []Edge{
			{From: "hls", To: "thumbnail"},
			{From: "hls", To: "subtitle"},
			{From: "thumbnail", To: "upload"},
			{From: "subtitle", To: "upload"},
			{From: "hls", To: "upload"},
		},
		Params: map[string]map[string]any{
			"hls": {"ladder": []string{"360p", "480p", "720p", "1080p"}, "codec": "hevc"},
		},
	},
	"audio": {
		Name:        "audio",
		Description: "Audio encode + upload",
		TaskKinds:   []string{"audio_encode", "upload"},
		Edges: []Edge{
			{From: "audio_encode", To: "upload"},
		},
	},
	"audio-hls": {
		Name:        "audio-hls",
		Description: "Audio HLS (m3u8 + ts segments) + m4a encode + upload",
		TaskKinds:   []string{"audio_encode", "hls", "upload"},
		Edges: []Edge{
			{From: "audio_encode", To: "upload"},
			{From: "hls", To: "upload"},
		},
	},
	"thumbnail": {
		Name:        "thumbnail",
		Description: "Thumbnail + sprite + upload",
		TaskKinds:   []string{"thumbnail", "upload"},
		Edges: []Edge{
			{From: "thumbnail", To: "upload"},
		},
	},
	// subtitle-add converts a standalone SRT/VTT/ASS input, updates the published
	// master playlist to reference the new subtitle track (EXT-X-MEDIA), then
	// uploads both. Re-running with the same lang replaces the previous track.
	"subtitle-add": {
		Name:        "subtitle-add",
		Description: "Add/replace a subtitle track to an already-published video: input is the SRT/VTT file, --lang selects the language, --name is the movie base name",
		TaskKinds:   []string{"subtitle", "update_master", "upload"},
		Edges: []Edge{
			{From: "subtitle", To: "update_master"},
			{From: "update_master", To: "upload"},
		},
	},
	// dub-add adds/replaces an audio track on an already-published video. The
	// input is a standalone audio file (mp3/m4a/aac); it is packaged into audio
	// HLS under audio/<lang>/, then update_master attaches the audio group to
	// the master playlist before upload.
	"dub-add": {
		Name:        "dub-add",
		Description: "Add/replace a dubbed audio track on an already-published video: input is the audio file, --lang selects the language, --name is the movie base name",
		TaskKinds:   []string{"hls", "update_master", "upload"},
		Edges: []Edge{
			{From: "hls", To: "update_master"},
			{From: "update_master", To: "upload"},
		},
		Params: map[string]map[string]any{
			"update_master": {"track": "audio"},
		},
	},
	"ai-subtitle": {
		Name:        "ai-subtitle",
		Description: "AI subtitle generation (whisper/gemini) + upload",
		TaskKinds:   []string{"ai_subtitle", "upload"},
		Edges: []Edge{
			{From: "ai_subtitle", To: "upload"},
		},
	},
}

func Get(name string) (Profile, error) {
	p, ok := Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown profile %q", name)
	}
	return p, nil
}

func List() []Profile {
	out := make([]Profile, 0, len(Profiles))
	for _, p := range Profiles {
		out = append(out, p)
	}
	return out
}

// BuildTaskGraph turns a profile into a core.Task list with unique IDs,
// wiring DependsOn from the profile edges.
func (p Profile) BuildTaskGraph(jobID core.JobID) ([]core.Task, error) {
	// kind -> list of task ids
	taskIDs := map[string][]core.TaskID{}
	for _, k := range p.TaskKinds {
		id := core.TaskID(fmt.Sprintf("task_%s", core.NewID(string(k))))
		taskIDs[k] = append(taskIDs[k], id)
	}

	var tasks []core.Task
	for _, k := range p.TaskKinds {
		ids := taskIDs[k]
		for _, id := range ids {
			t := core.Task{ID: id, JobID: jobID, Kind: k, Status: core.TaskPending}
			if params, ok := p.Params[k]; ok {
				t.Params = params
			}
			tasks = append(tasks, t)
		}
	}

	// wire deps: for each edge, all nodes of To depend on all nodes of From.
	for _, e := range p.Edges {
		for _, to := range taskIDs[e.To] {
			for _, from := range taskIDs[e.From] {
				// find the To task and append dep
				for i := range tasks {
					if tasks[i].ID == to {
						tasks[i].DependsOn = append(tasks[i].DependsOn, from)
					}
				}
			}
		}
	}
	return tasks, nil
}
