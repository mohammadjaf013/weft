package sysinfo

import (
	"errors"
	"testing"
)

func TestPercent(t *testing.T) {
	cases := []struct {
		used, total, want float64
	}{
		{500, 1000, 50},
		{0, 1000, 0},
		{100, 0, 0},
		{100, 50, 200}, // clamps not applied here; caller's math
	}
	for _, c := range cases {
		if got := pct(c.used, c.total); got != c.want {
			t.Errorf("pct(%v, %v) = %v, want %v", c.used, c.total, got, c.want)
		}
	}
}

func TestSnapshotShape(t *testing.T) {
	s, err := Collect(".")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if s.NumCPU == 0 {
		t.Error("NumCPU is 0")
	}
	if s.Hostname == "" {
		t.Error("Hostname empty")
	}
	if s.At.IsZero() {
		t.Error("At zero")
	}
	// Fields must not be negative / insane on any platform.
	if s.MemPct < 0 || s.MemPct > 100.1 {
		t.Errorf("MemPct = %v out of range", s.MemPct)
	}
	if s.DiskPct < 0 || s.DiskPct > 100.1 {
		t.Errorf("DiskPct = %v out of range", s.DiskPct)
	}
}

// TestGateDisabledAlwaysAllows ensures a zero-threshold gate never stalls the
// queue (the common config has no scheduler thresholds set).
func TestGateDisabledAlwaysAllows(t *testing.T) {
	g := &Gate{}
	for i := 0; i < 5; i++ {
		if !g.Allow() {
			t.Fatal("disabled gate must always allow")
		}
	}
}

// TestGateExtremeThresholdBlocks injects a saturating snapshot so the gate must
// refuse work, and a healthy snapshot so it must allow.
func TestGateExtremeThresholdBlocks(t *testing.T) {
	over := func(string) (Snapshot, error) {
		return Snapshot{NumCPU: 4, Load1: 12, CPUPercent: 300}, nil
	}
	under := func(string) (Snapshot, error) {
		return Snapshot{NumCPU: 4, Load1: 0.5, CPUPercent: 12.5}, nil
	}
	cases := []struct {
		name   string
		gate   *Gate
		wantOK bool
	}{
		{"cpu over threshold", &Gate{MaxCPUPercent: 85, collect: over}, false},
		{"cpu under threshold", &Gate{MaxCPUPercent: 85, collect: under}, true},
		{"load over threshold", &Gate{MaxLoadAvg: 4, collect: over}, false},
		{"load under threshold", &Gate{MaxLoadAvg: 4, collect: under}, true},
		{"probe failure fails open", &Gate{MaxCPUPercent: 85, collect: func(string) (Snapshot, error) { return Snapshot{}, errors.New("probe") }}, true},
	}
	for _, c := range cases {
		if got := c.gate.Allow(); got != c.wantOK {
			t.Errorf("%s: Allow() = %v, want %v", c.name, got, c.wantOK)
		}
	}
}
