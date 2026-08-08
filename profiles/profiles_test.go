package profiles

import (
	"testing"

	"github.com/mohammadjaf013/weft/core"
)

func TestGetKnownAndUnknown(t *testing.T) {
	p, err := Get("vod-h264")
	if err != nil {
		t.Fatalf("Get(vod-h264): %v", err)
	}
	if p.Name != "vod-h264" {
		t.Errorf("name = %q", p.Name)
	}
	if _, err := Get("nope"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
	if len(List()) != len(Profiles) {
		t.Errorf("List() returned %d, want %d", len(List()), len(Profiles))
	}
}

func TestBuildTaskGraphWiresDeps(t *testing.T) {
	p, _ := Get("vod-h264")
	tasks, err := p.BuildTaskGraph("job-1")
	if err != nil {
		t.Fatalf("BuildTaskGraph: %v", err)
	}
	if len(tasks) != len(p.TaskKinds) {
		t.Fatalf("expected %d tasks, got %d", len(p.TaskKinds), len(tasks))
	}
	byKind := map[string]core.Task{}
	for _, tk := range tasks {
		if tk.JobID != "job-1" {
			t.Errorf("task %s job = %q", tk.ID, tk.JobID)
		}
		byKind[tk.Kind] = tk
	}

	// hls is the root: no dependencies.
	h := byKind["hls"]
	if len(h.DependsOn) != 0 {
		t.Errorf("hls deps = %v, want none", h.DependsOn)
	}
	// thumbnail and subtitle depend on hls.
	th := byKind["thumbnail"]
	if len(th.DependsOn) != 1 || th.DependsOn[0] != h.ID {
		t.Errorf("thumbnail deps = %v, want [%s]", th.DependsOn, h.ID)
	}
	sub := byKind["subtitle"]
	if len(sub.DependsOn) != 1 || sub.DependsOn[0] != h.ID {
		t.Errorf("subtitle deps = %v, want [%s]", sub.DependsOn, h.ID)
	}
	// upload depends on hls, thumbnail, and subtitle.
	up := byKind["upload"]
	if len(up.DependsOn) != 3 {
		t.Errorf("upload deps = %v, want 3", up.DependsOn)
	}
	got := map[core.TaskID]bool{}
	for _, d := range up.DependsOn {
		got[d] = true
	}
	if !got[h.ID] || !got[th.ID] || !got[sub.ID] {
		t.Errorf("upload deps = %v, want [%s %s %s]", up.DependsOn, h.ID, th.ID, sub.ID)
	}
}

func TestBuildTaskGraphSimpleProfiles(t *testing.T) {
	// audio: audio_encode -> upload
	p, _ := Get("audio")
	tasks, _ := p.BuildTaskGraph("job-2")
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	var enc, up core.Task
	for _, tk := range tasks {
		if tk.Kind == "audio_encode" {
			enc = tk
		}
		if tk.Kind == "upload" {
			up = tk
		}
	}
	if len(up.DependsOn) != 1 || up.DependsOn[0] != enc.ID {
		t.Errorf("upload deps = %v, want [%s]", up.DependsOn, enc.ID)
	}
}

func TestBuildTaskGraphAudioHLS(t *testing.T) {
	// audio-hls: audio_encode -> upload, hls -> upload (parallel, both on source)
	p, err := Get("audio-hls")
	if err != nil {
		t.Fatalf("Get(audio-hls): %v", err)
	}
	tasks, _ := p.BuildTaskGraph("job-ah")
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	byKind := map[string]core.Task{}
	for _, tk := range tasks {
		byKind[tk.Kind] = tk
	}
	enc, hasEnc := byKind["audio_encode"]
	if !hasEnc {
		t.Fatal("missing audio_encode task")
	}
	h, hasH := byKind["hls"]
	if !hasH {
		t.Fatal("missing hls task")
	}
	up, hasUp := byKind["upload"]
	if !hasUp {
		t.Fatal("missing upload task")
	}
	// audio_encode and hls are roots (they both operate on the source input).
	if len(enc.DependsOn) != 0 {
		t.Errorf("audio_encode deps = %v, want none", enc.DependsOn)
	}
	if len(h.DependsOn) != 0 {
		t.Errorf("hls deps = %v, want none", h.DependsOn)
	}
	// upload depends on both.
	if len(up.DependsOn) != 2 {
		t.Errorf("upload deps = %v, want 2", up.DependsOn)
	}
	got := map[core.TaskID]bool{}
	for _, d := range up.DependsOn {
		got[d] = true
	}
	if !got[enc.ID] || !got[h.ID] {
		t.Errorf("upload deps = %v, want both [%s %s]", up.DependsOn, enc.ID, h.ID)
	}
}

func TestTaskIDsUnique(t *testing.T) {
	p, _ := Get("vod-h264")
	tasks, _ := p.BuildTaskGraph("job-3")
	seen := map[core.TaskID]bool{}
	for _, tk := range tasks {
		if seen[tk.ID] {
			t.Errorf("duplicate task id %s", tk.ID)
		}
		seen[tk.ID] = true
	}
}
