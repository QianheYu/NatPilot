// Package oscmd runs operating system utilities with a timeout, trying a list
// of candidate paths in order.
//
// It exists because the darwin and windows code paths obtain network state from
// system tools (route, netstat, arp, lsof, tasklist) rather than from /proc, so
// the same tiny "run this, give me stdout" helper is needed in several places.
package oscmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Run executes the first resolvable candidate with args and returns its stdout.
//
// Candidates are tried in order so a caller can list both the canonical
// absolute path ("/sbin/route") and a PATH lookup ("route"). The context bounds
// the whole call and timeout bounds each individual attempt. Stderr is captured
// and folded into the error, because these tools report their diagnostics there.
func Run(ctx context.Context, timeout time.Duration, candidates []string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	var lastErr error
	for _, cand := range candidates {
		path, err := resolve(cand)
		if err != nil {
			lastErr = err
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(cctx, path, args...)
		configureCommand(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err = cmd.Run()
		cancel()
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg != "" {
				lastErr = fmt.Errorf("%s %s: %w (%s)", cand, strings.Join(args, " "), err, msg)
			} else {
				lastErr = fmt.Errorf("%s %s: %w", cand, strings.Join(args, " "), err)
			}
			continue
		}
		return stdout.String(), nil
	}
	if lastErr == nil {
		lastErr = errors.New("no candidate command available")
	}
	return "", lastErr
}

// resolve turns a candidate into an executable path, accepting either a bare
// name looked up on PATH or an explicit path that exists on disk.
func resolve(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	if _, err := os.Stat(name); err == nil {
		return name, nil
	}
	return "", fmt.Errorf("%s not found", name)
}
