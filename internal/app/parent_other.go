//go:build !windows

package app

import "syscall"

// processAlive reports whether pid is still running. Signal 0 runs the existence
// and permission checks without delivering anything; EPERM means the process is
// there but owned by somebody else.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}
