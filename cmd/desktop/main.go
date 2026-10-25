// Command natpilot-desktop is a lightweight launcher for the desktop use case.
//
// It is intentionally minimal: it picks a free loopback port, starts
// `natpilot serve` in desktop mode (no login), waits for the health endpoint and
// opens the console in the system browser. It runs in the foreground and stops
// on Ctrl+C, which also stops the server it spawned.
//
// The full desktop edition — native window, system tray, close-to-tray, quit from
// the tray menu — is the Electron app in ./desktop. That is the recommended way
// to run NatPilot on a desktop, because Electron provides the tray on Windows,
// macOS and Linux from a single implementation.
//
// Build:  go build -o bin/natpilot-desktop ./cmd/desktop
// Run:    ./bin/natpilot-desktop [--server ./bin/natpilot] [--no-open]
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	serverBin := flag.String("server", "", "path to the natpilot server binary (default: alongside this executable, then PATH)")
	addr := flag.String("addr", "127.0.0.1:0", "loopback address for the embedded server")
	dataDir := flag.String("data-dir", "", "data directory (default: user config dir)")
	browser := flag.String("browser", "", "browser or webview executable to use (empty = system default)")
	noOpen := flag.Bool("no-open", false, "do not open a window (headless smoke test)")
	extra := flag.String("args", "", "extra arguments passed to the server (space separated)")
	flag.Parse()

	host, port, err := splitAddr(*addr)
	if err != nil {
		fatalf("%v", err)
	}
	// Reserve a free port when 0 was requested.
	if port == "0" {
		ln, lerr := net.Listen("tcp", host+":0")
		if lerr != nil {
			fatalf("reserve port: %v", lerr)
		}
		port = fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
		_ = ln.Close()
	}
	baseURL := fmt.Sprintf("http://%s", net.JoinHostPort(host, port))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	args := []string{"--host", host, "--port", port, "--mode", "desktop"}
	if *dataDir != "" {
		args = append(args, "--data-dir", *dataDir)
	}
	if strings.TrimSpace(*extra) != "" {
		args = append(args, strings.Fields(*extra)...)
	}
	args = append(args, "serve")

	selfPath := currentExecutable()
	lookup := resolveServerBin(*serverBin, selfPath, runtime.GOOS, runtime.GOARCH, fileExists)
	if lookup.Local {
		fmt.Printf("natpilot-desktop: using server %s\n", lookup.Path)
	}

	cmd := exec.CommandContext(ctx, lookup.Path, args...)
	cmd.Env = append(os.Environ(), "NATPILOT_DESKTOP=1")
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	cmd.Stdin = nil
	configureChild(cmd)
	if err := cmd.Start(); err != nil {
		if !lookup.Local {
			// Nothing local was found, so a PATH lookup was attempted and failed.
			// Explain what was probed instead of leaking a bare exec error.
			fatalf("%s", lookup.hint(filepath.Base(selfPath)))
		}
		fatalf("start server %q: %v", lookup.Path, err)
	}
	if err := attachChild(cmd); err != nil {
		// Lifecycle hardening is best effort: the normal quit path still stops
		// the server explicitly.
		fmt.Fprintf(os.Stderr, "natpilot-desktop: could not tie the server to this process: %v\n", err)
	}

	// Relay the server output.
	//
	// The relays must consume the pipes for the whole lifetime of the process.
	// An earlier version pushed lines into a bounded channel that nobody read
	// after startup; once 200 lines had accumulated the relay blocked, the OS
	// pipe filled up and the server froze on its next log write.
	var sawListening atomic.Bool
	pump := func(r io.Reader, w io.Writer) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintln(w, line)
			if strings.Contains(line, "listening") {
				sawListening.Store(true)
			}
		}
	}
	go pump(stdout, os.Stdout)
	go pump(stderr, os.Stderr)

	// Wait for readiness.
	ready := false
	deadline := time.Now().Add(20 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, rerr := client.Get(baseURL + "/api/health")
		if rerr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
		if sawListening.Load() {
			// The banner is printed just before the listener starts serving; a
			// short pause avoids racing the first /api/health request.
			time.Sleep(200 * time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !ready {
		fmt.Fprintln(os.Stderr, "natpilot-desktop: server did not become ready in time")
	} else {
		fmt.Printf("natpilot-desktop: UI available at %s\n", baseURL)
	}

	// Reopening the console is shared by the startup path and the tray menu.
	openConsole := func() {
		open := *browser
		if open == "" {
			open = defaultBrowser()
		}
		if open == "" {
			fmt.Printf("No browser detected; open %s manually.\n", baseURL)
			return
		}
		bArgs := []string{baseURL}
		// Chromium-based shells accept app mode for a chromeless window.
		if isChromium(open) {
			bArgs = []string{fmt.Sprintf("--app=%s", baseURL), "--window-size=1440,900"}
		}
		if err := exec.Command(open, bArgs...).Start(); err != nil {
			fmt.Fprintf(os.Stderr, "natpilot-desktop: could not open %s: %v\n", open, err)
			fmt.Printf("Open %s manually.\n", baseURL)
		}
	}

	if !*noOpen {
		openConsole()
	}

	// This binary is a lightweight launcher: it runs in the foreground and stops
	// on Ctrl+C / SIGTERM. The tray-resident desktop edition is the Electron app
	// in ./desktop — it owns the window and the tray on Windows, macOS and Linux,
	// and shuts this server down when it quits.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
	case <-ctx.Done():
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		_ = cmd.Process.Kill()
	}
}

func splitAddr(a string) (string, string, error) {
	i := strings.LastIndex(a, ":")
	if i < 0 {
		return "", "", fmt.Errorf("invalid address %q", a)
	}
	return a[:i], a[i+1:], nil
}

func defaultBrowser() string {
	candidates := map[string][]string{
		"darwin":  {"open", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
		"windows": {"chrome.exe", "msedge.exe", "explorer.exe"},
		"linux":   {"google-chrome", "chromium", "chromium-browser", "microsoft-edge", "xdg-open"},
	}
	for _, c := range candidates[runtime.GOOS] {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return ""
}

func isChromium(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(base, "chrome") || strings.Contains(base, "chromium") || strings.Contains(base, "edge")
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "natpilot-desktop: "+format+"\n", a...)
	os.Exit(1)
}
