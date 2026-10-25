package app

import (
	"os"
	"testing"
	"time"
)

func TestParentPIDUnset(t *testing.T) {
	t.Setenv(ParentPIDEnv, "")
	if pid, ok := ParentPID(); ok || pid != 0 {
		t.Fatalf("empty env: got (%d, %v), want (0, false)", pid, ok)
	}

	t.Setenv(ParentPIDEnv, "   ")
	if _, ok := ParentPID(); ok {
		t.Fatal("blank env must not yield a pid")
	}
}

func TestParentPIDParses(t *testing.T) {
	t.Setenv(ParentPIDEnv, " 4321 ")
	pid, ok := ParentPID()
	if !ok || pid != 4321 {
		t.Fatalf("got (%d, %v), want (4321, true)", pid, ok)
	}
}

func TestParentPIDRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"abc", "0", "-5", "12x"} {
		t.Setenv(ParentPIDEnv, raw)
		if pid, ok := ParentPID(); ok {
			t.Fatalf("env %q: got (%d, true), want a rejection", raw, pid)
		}
	}
}

func TestProcessAliveSelf(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("the test process itself must be reported as alive")
	}
}

// TestProcessAliveDeadPID spawns nothing: pid values beyond the OS maximum are
// never valid, so they must read as dead on every platform.
func TestProcessAliveDeadPID(t *testing.T) {
	if processAlive(1 << 30) {
		t.Fatal("a pid far above the maximum must be reported as dead")
	}
}

func TestWatchParentNoEnvIsNoop(t *testing.T) {
	t.Setenv(ParentPIDEnv, "")
	called := make(chan struct{}, 1)
	stop := WatchParent(func() { called <- struct{}{} })
	stop()
	stop() // double stop must not panic
	select {
	case <-called:
		t.Fatal("onGone must not fire when no parent pid is configured")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestWatchParentFiresWhenParentVanishes(t *testing.T) {
	// A pid that cannot exist makes the very first poll fire, so the test does
	// not have to wait for a real parent to die.
	t.Setenv(ParentPIDEnv, "1073741824")
	called := make(chan struct{}, 1)
	stop := WatchParent(func() { called <- struct{}{} })
	defer stop()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("onGone was not called for a pid that cannot exist")
	}
}
