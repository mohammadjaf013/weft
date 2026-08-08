package sysinfo

import "testing"

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
