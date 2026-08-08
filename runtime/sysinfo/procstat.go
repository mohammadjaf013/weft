package sysinfo

import (
	"strconv"
	"strings"
)

// parseProcStatTime extracts utime and stime (fields 14/15) from
// /proc/<pid>/stat. The comm field (2) may contain spaces or ')' so we split
// after the last ')'. Shared so the parsing is testable on any platform.
func parseProcStatTime(b []byte) (utime, stime int64, ok bool) {
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return 0, 0, false
	}
	f := strings.Fields(s[i+2:])
	// after ") " fields start at field 3 (state): f[0]=state, f[1]=ppid,
	// ... f[11]=utime, f[12]=stime
	if len(f) < 13 {
		return 0, 0, false
	}
	u, err1 := strconv.ParseInt(f[11], 10, 64)
	st, err2 := strconv.ParseInt(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return u, st, true
}

// parseProcStatPPID extracts the parent pid (field 4) from /proc/<pid>/stat.
func parseProcStatPPID(b []byte) (ppid int, ok bool) {
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return 0, false
	}
	f := strings.Fields(s[i+2:])
	if len(f) < 2 {
		return 0, false
	}
	p, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, false
	}
	return p, true
}
