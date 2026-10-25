//go:build windows

package scanner

import (
	"context"
	"errors"
	"time"

	"github.com/ekko-studio/natpilot/internal/oscmd"
)

// listLocalListeners enumerates the sockets this machine is listening on using
// the built-in `netstat -ano`, which reports the owning PID for every socket.
// `tasklist` then resolves those PIDs to image names.
func listLocalListeners() ([]LocalListener, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := oscmd.Run(ctx, 10*time.Second,
		[]string{"netstat.exe", "netstat", `C:\Windows\System32\netstat.exe`}, "-ano")
	if err != nil {
		return nil, err
	}
	listeners := parseWinNetstat(out)
	if len(listeners) == 0 {
		return nil, errors.New("netstat reported no listening sockets")
	}

	// Process names are a best-effort enrichment; a failure here must not lose
	// the port list itself.
	if tl, terr := oscmd.Run(ctx, 10*time.Second,
		[]string{"tasklist.exe", "tasklist", `C:\Windows\System32\tasklist.exe`},
		"/FO", "CSV", "/NH"); terr == nil {
		names := parseTasklist(tl)
		for i := range listeners {
			if n, ok := names[listeners[i].PID]; ok && n != "" {
				listeners[i].Process = n
			}
		}
	}
	return dedupeListeners(listeners), nil
}
