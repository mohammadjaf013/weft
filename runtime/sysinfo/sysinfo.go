// Package sysinfo reports host resource usage (CPU, memory, disk). The
// platform-specific implementation lives in sysinfo_linux.go / sysinfo_other.go;
// both share this snapshot type.
package sysinfo

import "time"

// Snapshot is a point-in-time view of host resources.
type Snapshot struct {
	// CPU
	NumCPU    int     `json:"num_cpu"`
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`
	CPUPercent float64 `json:"cpu_percent"` // 0..100
	// Memory (bytes)
	MemTotal uint64 `json:"mem_total"`
	MemUsed  uint64 `json:"mem_used"`
	MemAvail uint64 `json:"mem_avail"`
	MemPct   float64 `json:"mem_percent"` // 0..100
	// Disk for the given path
	DiskTotal uint64 `json:"disk_total"`
	DiskUsed  uint64 `json:"disk_used"`
	DiskAvail uint64 `json:"disk_avail"`
	DiskPct   float64 `json:"disk_percent"` // 0..100

	Hostname string    `json:"hostname"`
	Uptime   int64     `json:"uptime_seconds"`
	At       time.Time `json:"at"`
}

// Collect gathers host usage. path is the directory whose disk usage should
// be reported (empty uses the process working directory).
func Collect(path string) (Snapshot, error) {
	s := Snapshot{At: time.Now()}
	if err := fillPlatform(&s, path); err != nil {
		return s, err
	}
	s.CPUPercent = pct(s.Load1, float64(s.NumCPU))
	s.MemPct = pct(float64(s.MemUsed), float64(s.MemTotal))
	s.DiskPct = pct(float64(s.DiskUsed), float64(s.DiskTotal))
	return s, nil
}

func pct(used, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return used / total * 100
}
