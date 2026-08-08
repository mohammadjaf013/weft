//go:build !linux

package sysinfo

import (
	"os"
	"path/filepath"
	"runtime"
)

// fillPlatform on non-Linux hosts reports CPU count and hostname; memory/disk
// figures are zero (the production target is Linux, where sysinfo_linux.go
// provides the full picture).
func fillPlatform(s *Snapshot, path string) error {
	host, _ := os.Hostname()
	s.Hostname = host
	s.NumCPU = runtime.NumCPU()
	if path == "" {
		path = "."
	}
	// Best-effort working-directory disk info is unavailable via stdlib here.
	_ = filepath.Clean(path)
	return nil
}
