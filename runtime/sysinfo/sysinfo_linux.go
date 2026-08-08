//go:build linux

package sysinfo

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func fillPlatform(s *Snapshot, path string) error {
	host, _ := os.Hostname()
	s.Hostname = host
	s.NumCPU = numCPU()

	if err := readLoadAvg(s); err != nil {
		return err
	}
	if err := readMeminfo(s); err != nil {
		return err
	}
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(abs, &st); err != nil {
		return err
	}
	bs := st.Bsize
	s.DiskTotal = uint64(st.Blocks) * uint64(bs)
	s.DiskAvail = uint64(st.Bavail) * uint64(bs)
	if s.DiskTotal >= s.DiskAvail {
		s.DiskUsed = s.DiskTotal - s.DiskAvail
	}
	return nil
}

func numCPU() int {
	n := 0
	b, err := os.ReadFile("/proc/cpuinfo")
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "processor") {
				n++
			}
		}
	}
	if n == 0 {
		n = runtime.NumCPU()
	}
	return n
}

func readLoadAvg(s *Snapshot) error {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return err
	}
	f := strings.Fields(string(b))
	if len(f) >= 3 {
		s.Load1, _ = strconv.ParseFloat(f[0], 64)
		s.Load5, _ = strconv.ParseFloat(f[1], 64)
		s.Load15, _ = strconv.ParseFloat(f[2], 64)
	}
	// uptime is field 4? No — /proc/loadavg: 1min 5min 15min running/total lastpid.
	// Read /proc/uptime separately.
	if b2, err := os.ReadFile("/proc/uptime"); err == nil {
		if u, err := strconv.ParseFloat(strings.Fields(string(b2))[0], 64); err == nil {
			s.Uptime = int64(u)
		}
	}
	return nil
}

func readMeminfo(s *Snapshot) error {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	kb := func(v uint64) uint64 { return v * 1024 }
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			s.MemTotal = kb(parseMemKB(line))
		case strings.HasPrefix(line, "MemAvailable:"):
			s.MemAvail = kb(parseMemKB(line))
		}
	}
	if s.MemTotal > 0 {
		if s.MemAvail > s.MemTotal {
			s.MemAvail = s.MemTotal
		}
		s.MemUsed = s.MemTotal - s.MemAvail
	}
	return sc.Err()
}

func parseMemKB(line string) uint64 {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	v, _ := strconv.ParseUint(f[1], 10, 64)
	return v
}
