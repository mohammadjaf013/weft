package sysinfo

import "time"

// Gate is a resource-aware scheduling hook backed by Collect. It samples the
// host at most every Interval (cheap: /proc reads are fast but not free) and
// reports whether the host is under the configured thresholds. The worker uses
// it to idle instead of piling more encodes onto a saturated machine.
type Gate struct {
	// MaxCPUPercent (0..100) and MaxLoadAvg (load average, typically scaled to
	// NumCPU). Zero values disable the respective check.
	MaxCPUPercent float64
	MaxLoadAvg    float64
	// Interval throttles sampling; defaults to 2s.
	Interval time.Duration
	// Path is the directory whose disk usage is included in the snapshot
	// (not used by the gate itself today). Empty uses the working directory.
	Path string

	mu       chan struct{} // 1-buffered token: sampling is single-flight
	last     Snapshot
	lastTime time.Time
	hasLast  bool

	// collect is the sampling function; nil means Collect. Tests override it
	// to drive Allow() deterministically without touching the real host.
	collect func(string) (Snapshot, error)
}

// Allow reports whether the host may start another job. It is always true when
// no thresholds are configured, so an unset gate never stalls the queue.
func (g *Gate) Allow() bool {
	if g.MaxCPUPercent <= 0 && g.MaxLoadAvg <= 0 {
		return true
	}
	iv := g.Interval
	if iv <= 0 {
		iv = 2 * time.Second
	}
	if g.mu == nil {
		g.mu = make(chan struct{}, 1)
		g.mu <- struct{}{}
	}
	select {
	case <-g.mu:
		defer func() { g.mu <- struct{}{} }()
	default:
		// another goroutine is sampling; keep the cached decision
	}
	if g.hasLast && time.Since(g.lastTime) < iv {
		return g.under(g.last)
	}
	collect := g.collect
	if collect == nil {
		collect = Collect
	}
	s, err := collect(g.Path)
	if err != nil {
		return true // probe failure: fail open, don't stall the pipeline
	}
	g.last, g.lastTime, g.hasLast = s, time.Now(), true
	return g.under(s)
}

func (g *Gate) under(s Snapshot) bool {
	if g.MaxCPUPercent > 0 && s.CPUPercent > g.MaxCPUPercent {
		return false
	}
	if g.MaxLoadAvg > 0 && s.Load1 > g.MaxLoadAvg {
		return false
	}
	return true
}
