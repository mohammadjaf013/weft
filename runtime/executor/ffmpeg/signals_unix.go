//go:build !windows

package ffmpeg

import (
	"os"
	"syscall"
)

// signalStop freezes the process (SIGSTOP). The process keeps its memory and
// open files; it just stops being scheduled until signalCont is sent.
func (e *Executor) signalStop(p *os.Process) error {
	return p.Signal(syscall.SIGSTOP)
}

// signalCont resumes a stopped process (SIGCONT).
func (e *Executor) signalCont(p *os.Process) error {
	return p.Signal(syscall.SIGCONT)
}