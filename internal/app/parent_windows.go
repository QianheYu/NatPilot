//go:build windows

package app

import "golang.org/x/sys/windows"

// waitTimeout is WAIT_TIMEOUT from the Win32 wait API, spelled out so this file
// does not depend on whether the syscall package re-exports it.
const waitTimeout = 0x00000102

// processAlive reports whether pid is still running.
//
// OpenProcess with SYNCHRONIZE yields a handle that can be waited on, and a zero
// timeout answers immediately: WAIT_TIMEOUT means "still running". A pid that was
// recycled into another process is indistinguishable from the original; that is
// acceptable here because the only consequence is a shutdown that happens a
// little late rather than one that happens wrongly.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(
		windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(pid),
	)
	if err != nil {
		// Access denied means the process exists but we may not inspect it —
		// treat it as alive. Anything else (invalid parameter) means it is gone.
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer func() { _ = windows.CloseHandle(h) }()
	event, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return true
	}
	return event == waitTimeout
}
