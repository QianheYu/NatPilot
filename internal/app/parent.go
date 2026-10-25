package app

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ekko-studio/natpilot/internal/logging"
)

// ParentPIDEnv names the environment variable that carries the pid of the
// process which launched this server. Only the desktop shells (the Electron
// main process and cmd/desktop) set it; a plain `natpilot serve` never does, so
// nothing changes for server deployments.
const ParentPIDEnv = "NATPILOT_PARENT_PID"

// parentPollInterval is how often the desktop parent is re-checked. The desktop
// shell already stops the server explicitly on a normal quit; this watcher only
// covers the paths where it cannot: a crash or "结束任务" from the task manager,
// which would otherwise leave the Go child holding its loopback port forever.
const parentPollInterval = 2 * time.Second

// WatchParent calls onGone once the process named by ParentPIDEnv has exited.
//
// It returns a stop function that must be called on normal shutdown so the
// watcher goroutine does not outlive the server. When ParentPIDEnv is unset or
// malformed the watcher is simply not started and the stop function is a no-op.
func WatchParent(onGone func()) (stop func()) {
	if onGone == nil {
		return func() {}
	}
	pid, ok := ParentPID()
	if !ok {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(parentPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if processAlive(pid) {
					continue
				}
				logging.Warn("main", "desktop parent process is gone; shutting down", "pid", pid)
				onGone()
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// ParentPID reads ParentPIDEnv and reports whether it holds a usable pid.
func ParentPID() (int, bool) {
	raw := strings.TrimSpace(os.Getenv(ParentPIDEnv))
	if raw == "" {
		return 0, false
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
