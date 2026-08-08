//go:build windows

package ffmpeg

import (
	"fmt"
	"os"
)

// Windows has no SIGSTOP/SIGCONT equivalent exposed in the standard library.
// Pausing a process here is a no-op: the job state still flips, but the child
// process keeps running. The production target is Linux, where signals_unix.go
// implements the real freeze/resume.
func (e *Executor) signalStop(p *os.Process) error {
	return fmt.Errorf("process pause not supported on Windows")
}

func (e *Executor) signalCont(p *os.Process) error {
	return fmt.Errorf("process resume not supported on Windows")
}