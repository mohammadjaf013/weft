package sysinfo

import (
	"errors"
	"testing"
	"time"
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

// TestGateSelfCPUThreshold drives the weft-own-CPU check directly (decide, not
// Allow, so the 2s cache doesn't force sleeps). The first sample only
// establishes a baseline; the second computes the usage over the window.
func TestGateSelfCPUThreshold(t *testing.T) {
	t.Run("self cpu over ceiling blocks", func(t *testing.T) {
		// f[0]=state f[1]=ppid f[2]=pgrp f[3]=session f[4]=tty f[5]=tpgid
		// f[6]=flags f[7]=minflt f[8]=cminflt f[9]=majflt f[10]=cmajflt
		// f[11]=utime f[12]=stime
		i := 0
		now := func() (float64, error) { i++; return 100000 * float64(i), nil }
		g := &Gate{MaxCPUPercent: 85, cpuNow: now}
		if !g.decide() {
			t.Fatal("first sample establishes baseline, must allow")
		}
		// fix the sampling window to exactly 1s so the math is deterministic:
		// pct = (200000-100000)/1/NumCPU*100, way over 85 for any sane machine
		g.cpuAt = g.cpuAt.Add(-time.Second)
		if g.decide() {
			t.Fatal("second sample with huge delta must block")
		}
	})
	t.Run("self cpu under ceiling allows", func(t *testing.T) {
		i := 0
		now := func() (float64, error) { i++; return 0.25 * float64(i), nil } // tiny cumulative time
		g := &Gate{MaxCPUPercent: 85, cpuNow: now}
		g.decide() // baseline
		if !g.decide() {
			t.Fatal("negligible self cpu must allow")
		}
	})
	t.Run("self cpu probe failure fails open", func(t *testing.T) {
		g := &Gate{MaxCPUPercent: 85, cpuNow: func() (float64, error) { return 0, errors.New("probe") }}
		if !g.decide() {
			t.Fatal("probe failure must fail open, not stall the queue")
		}
	})
}

// TestGateLoadThreshold drives the optional host load-average check.
func TestGateLoadThreshold(t *testing.T) {
	over := func(string) (Snapshot, error) { return Snapshot{Load1: 44}, nil }
	under := func(string) (Snapshot, error) { return Snapshot{Load1: 0.5}, nil }
	cases := []struct {
		name   string
		gate   *Gate
		wantOK bool
	}{
		{"load over threshold", &Gate{MaxLoadAvg: 4, collect: over}, false},
		{"load under threshold", &Gate{MaxLoadAvg: 4, collect: under}, true},
		{"load probe failure fails open", &Gate{MaxLoadAvg: 4, collect: func(string) (Snapshot, error) { return Snapshot{}, errors.New("probe") }}, true},
	}
	for _, c := range cases {
		if got := c.gate.Allow(); got != c.wantOK {
			t.Errorf("%s: Allow() = %v, want %v", c.name, got, c.wantOK)
		}
	}
}

// TestParseProcStatTime ensures the /proc/<pid>/stat field slicing is correct
// (comm may contain spaces or ')', so we split after the last ')').
func TestParseProcStatTime(t *testing.T) {
	// real layout after ") ": state ppid pgrp session tty tpgid flags
	// minflt cminflt majflt cmajflt utime stime rest...
	b := []byte("12345 (weft (busy) run) S 1 2 3 0 -1 4194560 10 0 0 0 12 34 0 0 0 0")
	utime, stime, ok := parseProcStatTime(b)
	if !ok {
		t.Fatal("parseProcStatTime failed")
	}
	if utime != 12 || stime != 34 {
		t.Errorf("utime=%d stime=%d, want 12 34", utime, stime)
	}
	if ppid, ok := parseProcStatPPID(b); !ok || ppid != 1 {
		t.Errorf("ppid = %d (ok=%v), want 1 true", ppid, ok)
	}
}
