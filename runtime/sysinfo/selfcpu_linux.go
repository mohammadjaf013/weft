//go:build linux

package sysinfo

import (
	"os"
	"strconv"
)

// init registers the Linux self-CPU sampler. It sums the CPU time of this
// process and its direct children (the ffmpeg/ffprobe/whisper/ssh processes
// weft spawns) from /proc/<pid>/stat, so the resource gate reflects weft's own
// load rather than the whole shared host.
func init() {
	platformSelfCPU = linuxSelfCPU
}

// linuxSelfCPU returns cumulative CPU seconds (user + system) consumed by this
// process and its direct children.
func linuxSelfCPU() (float64, error) {
	myPID := os.Getpid()
	total := 0.0
	addProc := func(pid int) {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return
		}
		utime, stime, ok := parseProcStatTime(b)
		if !ok {
			return
		}
		total += (float64(utime) + float64(stime)) / clkTck()
	}
	addProc(myPID)
	// scan /proc for direct children (ppid == our pid)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return total, err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == myPID {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		if ppid, ok := parseProcStatPPID(b); ok && ppid == myPID {
			addProc(pid)
		}
	}
	return total, nil
}

// clkTck returns the number of clock ticks per second. Linux USER_HZ is 100 on
// virtually all architectures (the kernel exports SC_CLK_TCK=100 for userland);
// fall back to 100 if the value is somehow invalid.
func clkTck() float64 {
	if v, err := os.ReadFile("/proc/sys/kernel/clock_tick_rate"); err == nil {
		if n, err := strconv.Atoi(string(v)); err == nil && n > 0 {
			return float64(n)
		}
	}
	return 100
}
