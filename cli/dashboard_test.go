package cli

import (
	"strings"
	"testing"
	"time"
)

func TestComputeETA(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if got := computeETA(nil, 50, now); got != "-" {
		t.Errorf("nil startedAt = %q, want -", got)
	}

	started := now.Add(-10 * time.Second)
	if got := computeETA(&started, 0, now); got != "unknown" {
		t.Errorf("0%% progress = %q, want unknown", got)
	}

	started100 := now.Add(-10 * time.Second)
	if got := computeETA(&started100, 100, now); got != "done" {
		t.Errorf("100%% progress = %q, want done", got)
	}

	// 50% done after 10s elapsed -> total ~20s -> ~10s remaining
	started50 := now.Add(-10 * time.Second)
	got := computeETA(&started50, 50, now)
	if got != "10s" {
		t.Errorf("50%% after 10s elapsed = %q, want 10s", got)
	}

	// 25% done after 10s elapsed -> total ~40s -> ~30s remaining
	started25 := now.Add(-10 * time.Second)
	got = computeETA(&started25, 25, now)
	if got != "30s" {
		t.Errorf("25%% after 10s elapsed = %q, want 30s", got)
	}
}

func TestRenderQueueLine(t *testing.T) {
	if got := renderQueueLine(nil); got != "queue: empty" {
		t.Errorf("nil queue = %q, want queue: empty", got)
	}
	if got := renderQueueLine(map[string]int{"low": 0}); got != "queue: empty" {
		t.Errorf("all-zero queue = %q, want queue: empty", got)
	}
	got := renderQueueLine(map[string]int{"normal": 3, "emergency": 1, "low": 2})
	// order must be priority-band order, not map iteration order
	want := "queue: emergency=1 normal=3 low=2"
	if got != want {
		t.Errorf("queue line = %q, want %q", got, want)
	}
}

func TestRenderWorkersLine(t *testing.T) {
	if got := renderWorkersLine(nil); got != "workers: none" {
		t.Errorf("nil workers = %q, want workers: none", got)
	}
	workers := []dashWorker{{ID: "w1", Status: "busy"}, {ID: "w2", Status: "idle"}, {ID: "w3", Status: "busy"}}
	if got := renderWorkersLine(workers); got != "workers: 2/3 busy" {
		t.Errorf("workers line = %q, want workers: 2/3 busy", got)
	}
}

func TestRenderSystemLine(t *testing.T) {
	if got := renderSystemLine(dashSystem{}); got != "system: (unavailable)" {
		t.Errorf("empty system = %q, want system: (unavailable)", got)
	}
	s := dashSystem{Hostname: "node-1", NumCPU: 8, CPUPercent: 42.5, MemPercent: 60.1}
	got := renderSystemLine(s)
	for _, want := range []string{"node-1", "8 cores", "42.5%", "60.1%"} {
		if !strings.Contains(got, want) {
			t.Errorf("system line %q missing %q", got, want)
		}
	}
}

func TestRenderDetailEmpty(t *testing.T) {
	if got := renderDetail(nil); got != "" {
		t.Errorf("empty detail = %q, want empty string", got)
	}
}

func TestRenderDetailListsTasks(t *testing.T) {
	tasks := []dashTask{
		{ID: "t1", Kind: "hls", Status: "running", Progress: 42},
		{ID: "t2", Kind: "thumbnail", Status: "pending", Progress: 0},
	}
	got := renderDetail(tasks)
	for _, want := range []string{"t1", "hls", "running", "42.0%", "t2", "thumbnail", "pending"} {
		if !strings.Contains(got, want) {
			t.Errorf("detail %q missing %q", got, want)
		}
	}
}

func TestJobsToRows(t *testing.T) {
	jobs := []dashJob{
		{ID: "j1", Status: "running", Priority: "high", Profile: "vod-h264", Progress: 55},
	}
	rows := jobsToRows(jobs)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	want := []string{"j1", "running", "high", "vod-h264", "55%"}
	for i, w := range want {
		if rows[0][i] != w {
			t.Errorf("row[%d] = %q, want %q", i, rows[0][i], w)
		}
	}
}
