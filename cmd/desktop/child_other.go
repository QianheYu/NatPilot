//go:build !windows && !linux

package main

import "os/exec"

// configureChild is a no-op: neither macOS nor the fallback platforms expose a
// parent-death signal. The shell still stops the server on its normal shutdown
// path (quit channel → context cancel → cmd.Wait).
func configureChild(cmd *exec.Cmd) {}

// attachChild is a no-op on this platform.
func attachChild(cmd *exec.Cmd) error { return nil }
