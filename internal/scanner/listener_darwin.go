//go:build darwin

package scanner

import (
	"context"
	"errors"
	"time"

	"github.com/ekko-studio/natpilot/internal/oscmd"
)

// listLocalListeners enumerates the sockets this machine is listening on.
//
// `netstat -an` is the primary source because it needs no privileges and lists
// every socket on the system. `lsof` is then used to attribute process names and
// PIDs, which netstat cannot report; lsof without root only sees the current
// user's processes, so the enrichment is best effort by design.
func listLocalListeners() ([]LocalListener, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	netstat := []string{"/usr/sbin/netstat", "/bin/netstat", "netstat"}
	out := []LocalListener{}
	var lastErr error
	for _, args := range [][]string{{"-an", "-p", "tcp"}, {"-an", "-p", "udp"}} {
		s, err := oscmd.Run(ctx, 8*time.Second, netstat, args...)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, parseMacNetstat(s)...)
	}

	lsof := []string{"/usr/sbin/lsof", "/usr/bin/lsof", "lsof"}
	lsofFound := false
	for _, args := range [][]string{{"-nP", "-iTCP", "-sTCP:LISTEN"}, {"-nP", "-iUDP"}} {
		s, err := oscmd.Run(ctx, 8*time.Second, lsof, args...)
		if err != nil {
			continue
		}
		got := parseLsofListeners(s)
		if len(got) > 0 {
			lsofFound = true
			out = append(out, got...)
		}
	}

	if len(out) == 0 {
		if lastErr == nil {
			lastErr = errors.New("netstat and lsof reported no listening sockets")
		}
		return nil, lastErr
	}
	if !lsofFound {
		// Ports are complete but unattributed; the scan still works, it just
		// cannot name the owning process.
		return dedupeListeners(out), nil
	}
	return dedupeListeners(out), nil
}
