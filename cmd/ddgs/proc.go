package main

// Process helpers for the `ddgs api -d` detach path. POSIX-only: this port
// targets Linux/macOS/BSD, Windows is not supported (see README).

import (
	"path/filepath"
	"strconv"
	"syscall"
)

// detachedSysProcAttr puts the child in a new session so it survives the CLI
// exiting — the equivalent of cli.py detaching uvicorn from the terminal.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// childGone reports whether a process has already exited. Signal 0 sends
// nothing; it only checks existence. EPERM means the process exists but is
// owned by someone else, so it counts as alive.
func childGone(pid int) bool {
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

// isNoSuchProcess reports whether err means the process does not exist.
func isNoSuchProcess(err error) bool {
	return err == syscall.ESRCH
}

func dirOf(p string) string { return filepath.Dir(p) }

func addrOf(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}
