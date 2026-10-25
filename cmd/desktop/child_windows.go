//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createNoWindow is CREATE_NO_WINDOW.
const createNoWindow = 0x08000000

// jobHandle owns the server child. With KILL_ON_JOB_CLOSE the child is
// terminated when this process exits for any reason — including being killed
// from Task Manager, where no Go defer runs. Without it, a killed shell leaves
// an orphaned server holding the port and the SQLite file.
var jobHandle windows.Handle

// configureChild starts the server without a console window. The desktop shell
// is built as a GUI binary (-H windowsgui), so without this the console-subsystem
// server would pop up a black window of its own.
func configureChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow | windows.CREATE_BREAKAWAY_FROM_JOB,
	}
}

// attachChild puts the started server into the kill-on-close job object.
func attachChild(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("child process was not started")
	}
	if jobHandle == 0 {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return fmt.Errorf("CreateJobObject: %w", err)
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(
			h,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			windows.CloseHandle(h)
			return fmt.Errorf("SetInformationJobObject: %w", err)
		}
		jobHandle = h
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(jobHandle, ph); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}
