//go:build !windows

// Package proc holds the operating-system specific parts of process handling: liveness checks,
// starting a worker in its own process group, and killing a whole process tree.
package proc

import (
	"os"
	"syscall"
	"time"
)

// Alive reports whether a process with this id exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// WorkerAttrs makes the worker the leader of its own process group, so the whole tree can be signalled.
func WorkerAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// DetachAttrs starts a process in a new session so it survives the parent and its terminal.
func DetachAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// KillTree asks the process group led by pid to stop, waits up to grace, then kills whatever is left.
func KillTree(pid int, grace time.Duration) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// Interrupt asks one process (not its tree) to stop.
func Interrupt(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
