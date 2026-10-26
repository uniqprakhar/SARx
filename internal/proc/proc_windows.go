//go:build windows

// Package proc holds the operating-system specific parts of process handling: liveness checks,
// starting a worker in its own process group, and killing a whole process tree.
package proc

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
	createNewProcessGroup          = 0x00000200
	detachedProcess                = 0x00000008
)

// Alive reports whether a process with this id exists and has not exited.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// WorkerAttrs starts the worker in its own process group.
func WorkerAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// DetachAttrs starts a process with no console so it survives the parent.
func DetachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup, HideWindow: true}
}

// KillTree ends the process and everything it started. Windows has no process groups to signal, so
// this uses taskkill, which ships with every Windows install. The grace period is not used.
func KillTree(pid int, grace time.Duration) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// Interrupt ends one process. Windows cannot deliver a polite signal to an arbitrary process.
func Interrupt(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid)).Run()
}
