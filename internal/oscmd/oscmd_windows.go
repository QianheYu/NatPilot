//go:build windows

package oscmd

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW. Every utility invoked from here (netstat,
// tasklist, route, arp) is a console program; without this flag the desktop
// build flashes a console window each time it enumerates sockets or routes.
const createNoWindow = 0x08000000

// configureCommand stops the child process from allocating a console.
func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
