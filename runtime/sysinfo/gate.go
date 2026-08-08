package sysinfo

import (
	"errors"
	"runtime"
	"time"
)

// Gate is a resource-aware scheduling hook. It measures **weft's own** resource
// usage — the process plus the ffmpeg/ffprobe/whisper children it spawns — not
// the whole host, so a busy shared machine (other tenants, unrelated load)
// never starves the queue. The worker uses it to idle instead of piling more
// encodes onto a saturated weft.
type Gate struct {
	// MaxCPUPercent (0..100) is the ceiling for weft's own CPU usage as a
	// percentage of the machine's total cores. MaxLoadAvg is the host load
	// average ceiling. Zero values disable the respective check.
	MaxCPUPercent float64
	MaxLoadAvg    float64
	// Interval throttles sampling; defaults to 2s.
	Interval time.Duration
	// Path is the directory whose disk usage is included in the snapshot
	// (used by the load-average check). Empty uses the working directory.
	Path string

	mu      chan struct{} // 1-buffered token: sampling is single-flight
	allowOK bool
	decided time.Time

	// cpuNow returns cumulative CPU-seconds used by weft (self + children).
	// nil → the platform implementation (Linux: /proc scan; others: unsupported
	// → gate fails open). Tests override it to drive Allow deterministically.
	cpuNow  func() (float64, error)
	cpuPrev float64
	cpuAt   time.Time
	hasCPU  bool

	// collect is the load-average sampling function; nil means Collect.
	collect func(string) (Snapshot, error)
}

// platformSelfCPU returns cumulative CPU seconds consumed by this process and
// its direct children. Overridden per-platform; the default fails open.
var platformSelfCPU = func() (float64, error) {
	return 0, errors.New("self cpu sampling unsupported on this platform")
}

// Allow reports whether weft may start another job. It is always true when no
// thresholds are configured, so an unset gate never stalls the queue.
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
	if time.Since(g.decided) < iv {
		return g.allowOK
	}
	g.allowOK = g.decide()
	g.decided = time.Now()
	return g.allowOK
}

func (g *Gate) decide() bool {
	ok := true
	if g.MaxCPUPercent > 0 {
		nowCPU := g.cpuNow
		if nowCPU == nil {
			nowCPU = platformSelfCPU
		}
		cur, err := nowCPU()
		if err != nil {
			// probe failure: fail open, don't stall the pipeline
		} else if g.hasCPU && cur >= g.cpuPrev {
			dt := time.Since(g.cpuAt).Seconds()
			if dt > 0 {
				// percent of total cores used by weft over the sampling window
				pct := (cur - g.cpuPrev) / dt / float64(runtime.NumCPU()) * 100
				if pct > g.MaxCPUPercent {
					ok = false
				}
			}
		}
		g.cpuPrev, g.cpuAt, g.hasCPU = cur, time.Now(), true
	}
	if ok && g.MaxLoadAvg > 0 {
		collect := g.collect
		if collect == nil {
			collect = Collect
		}
		if s, err := collect(g.Path); err == nil && s.Load1 > g.MaxLoadAvg {
			ok = false
		}
	}
	return ok
}
