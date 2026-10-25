//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// configureChild makes the server receive SIGTERM when this process dies, so a
// crashed or killed shell never leaves the server holding the port.
func configureChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

// attachChild has nothing to do: Pdeathsig is set before the child starts.
func attachChild(cmd *exec.Cmd) error { return nil }
