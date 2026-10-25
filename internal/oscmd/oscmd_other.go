//go:build !windows

package oscmd

import "os/exec"

// configureCommand is a no-op outside windows, where child processes do not
// allocate consoles.
func configureCommand(cmd *exec.Cmd) {}
