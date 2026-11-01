// Command natpilot is a NAT port-mapping control plane: it manages host+port
// mapping rules, talks UPnP IGD v2/v1, PCP and NAT-PMP to the local gateway,
// scans for candidate ports and exposes everything through a CLI, a REST API
// and an embedded web UI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ekko-studio/natpilot/internal/api"
	"github.com/ekko-studio/natpilot/internal/app"
	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/netutil"
	"github.com/ekko-studio/natpilot/internal/store"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

const usage = `NatPilot - NAT port mapping control plane

Usage:
  natpilot [global flags] <command> [command flags] [args]

Commands:
  serve        Run the HTTP server (API + web UI) and the mapping engine
  rule         Manage the host/port mapping list (list, add, rm, enable, disable, set)
  ports        Inspect and act on discovered ports (list, add, ignore, delete, clear)
  scan         Run a port scan (local, cidr)
  gateway      Discover and inspect NAT gateways (list, discover, preferred)
  mapping      Show live mappings (list, reconcile, cleanup, external-ip)
  policy       Manage auto-monitor policies (list, add, rm)
  schedule     Manage recurring jobs (list, add, rm)
  user         Manage accounts (list, add, rm, token, passwd)
  events       Inspect the system event log (list, clear)
  config       Show or initialise configuration (print, path, init, set)
  version      Print version information
  help         Show this help

Global flags:
  --config PATH      configuration file (YAML or JSON)
  --data-dir PATH    data directory (default ~/.natpilot)
  --db PATH          SQLite database path
  --host ADDR        bind address for serve (default 0.0.0.0)
  --port N           bind port for serve (default 8787)
  --mode MODE        auto|web|server|desktop (default auto)
  --auth BOOL        require login (web/server modes)
  --log-level LEVEL  debug|info|warn|error|off (default off = 不记录)
  --log-format FMT   text|json
  --json             machine readable output

Examples:
  natpilot serve --port 8787 --auth true
  natpilot gateway discover
  natpilot rule add --host 127.0.0.1 --port 8080 --protocol tcp --tags web,dev --enable
  natpilot scan local --ports 1-1024 --add-open
  natpilot scan cidr 192.168.1.0/24 --ports 22,80,443
`

func run(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
		return 0
	}
	// Split global flags from the subcommand.
	globalFlags := map[string]string{}
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		key, val, hasVal := splitFlag(a)
		if isBoolGlobal(key) && !hasVal {
			globalFlags[key] = "true"
			i++
			continue
		}
		if hasVal {
			globalFlags[key] = val
			i++
			continue
		}
		if i+1 < len(args) {
			globalFlags[key] = args[i+1]
			i += 2
			continue
		}
		i++
	}
	rest := args[i:]
	if len(rest) == 0 {
		fmt.Print(usage)
		return 0
	}
	cmd := rest[0]
	sub := rest[1:]

	asJSON := globalFlags["json"] == "true" || globalFlags["format"] == "json"
	flags := map[string]string{}
	for k, v := range globalFlags {
		switch k {
		case "config", "data-dir", "db", "host", "port", "mode", "auth",
			"log-level", "log-format":
			flags[k] = v
		}
	}

	ctx, cancel := signalContext()
	defer cancel()

	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "version", "--version", "-v":
		if asJSON {
			printJSON(map[string]any{"version": version, "commit": commit, "built": buildDate, "product": product})
			return 0
		}
		fmt.Println(versionString())
		return 0
	case "serve", "run", "http":
		return cmdServe(ctx, flags, sub)
	case "rule", "rules":
		return withApp(ctx, flags, func(a *app.App) int { return cmdRule(ctx, a, sub, asJSON) })
	case "ports", "port":
		return withApp(ctx, flags, func(a *app.App) int { return cmdPorts(ctx, a, sub, asJSON) })
	case "scan":
		return withApp(ctx, flags, func(a *app.App) int { return cmdScan(ctx, a, sub, asJSON) })
	case "gateway", "gateways":
		return withApp(ctx, flags, func(a *app.App) int { return cmdGateway(ctx, a, sub, asJSON) })
	case "mapping", "mappings":
		return withApp(ctx, flags, func(a *app.App) int { return cmdMapping(ctx, a, sub, asJSON) })
	case "policy", "policies":
		return withApp(ctx, flags, func(a *app.App) int { return cmdPolicy(ctx, a, sub, asJSON) })
	case "schedule", "schedules":
		return withApp(ctx, flags, func(a *app.App) int { return cmdSchedule(ctx, a, sub, asJSON) })
	case "user", "users":
		return withApp(ctx, flags, func(a *app.App) int { return cmdUser(ctx, a, sub, asJSON) })
	case "events", "logs":
		return withApp(ctx, flags, func(a *app.App) int { return cmdEvents(ctx, a, sub, asJSON) })
	case "config":
		return cmdConfig(flags, sub, asJSON)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fmt.Print(usage)
		return 2
	}
}

// serveFlagNames lists the global flags that may also be given after "serve".
var serveFlagNames = []string{
	"config", "data-dir", "db", "host", "port", "mode", "auth", "log-level", "log-format",
}

// parseServeFlags accepts the global flags positioned after the subcommand, so
// that "natpilot serve --host 127.0.0.1 --port 8787" (the form used by the
// README and the systemd unit) works like "natpilot --host ... serve".
func parseServeFlags(args []string, flags map[string]string) error {
	if len(args) == 0 {
		return nil
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	targets := make(map[string]*string, len(serveFlagNames))
	for _, name := range serveFlagNames {
		targets[name] = fs.String(name, "", "")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, p := range targets {
		if v := strings.TrimSpace(*p); v != "" {
			flags[name] = v
		}
	}
	return nil
}

func splitFlag(a string) (key, val string, hasVal bool) {
	a = strings.TrimLeft(a, "-")
	if i := strings.Index(a, "="); i >= 0 {
		return a[:i], a[i+1:], true
	}
	return a, "", false
}

func isBoolGlobal(k string) bool {
	switch k {
	case "json", "help", "version", "verbose", "quiet":
		return true
	}
	return false
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		logging.Info("main", "shutdown signal received")
		cancel()
	}()
	return ctx, cancel
}

// withApp builds the application (without background loops) and runs fn.
func withApp(ctx context.Context, flags map[string]string, fn func(*app.App) int) int {
	a, err := app.Build(ctx, app.Options{ConfigPath: flags["config"], Flags: flags, Version: version})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer a.Shutdown()
	return fn(a)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// ---- serve ----

func cmdServe(ctx context.Context, flags map[string]string, args []string) int {
	// Accept the global flags after the subcommand too, so the documented
	// "natpilot serve --host 127.0.0.1 --port 8787" form (used by the README and
	// the systemd unit) behaves like "natpilot --host ... serve".
	if err := parseServeFlags(args, flags); err != nil {
		return 2
	}
	// A desktop shell sets NATPILOT_PARENT_PID: if that process disappears (crash,
	// "结束任务"), cancel the context so this server shuts down instead of
	// lingering as an orphan holding the loopback port.
	ctx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	stopParentWatch := app.WatchParent(cancelServe)
	defer stopParentWatch()

	a, err := app.Build(ctx, app.Options{ConfigPath: flags["config"], Flags: flags, Version: version})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer a.Shutdown()

	mode := a.Config.EffectiveMode()
	host := a.Config.EffectiveBindHost()
	if req := strings.TrimSpace(a.Config.HTTP.Host); req != "" && !strings.EqualFold(req, host) {
		reason := "bind address normalised"
		if mode == config.ModeDesktop {
			reason = "desktop mode is local-only; use --mode server to listen on the LAN"
		}
		logging.Warn("main", "bind address overridden", "mode", mode, "requested", req, "effective", host, "reason", reason)
	}
	addr := net.JoinHostPort(host, strconv.Itoa(a.Config.HTTP.Port))

	handler := api.NewServer(a.API)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0, // SSE streams must not be cut off
		IdleTimeout:       120 * time.Second,
	}

	a.Run(ctx)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot listen on %s: %v\n", addr, err)
		return 1
	}
	actual := ln.Addr().String()
	fmt.Printf("%s\n", versionString())
	fmt.Printf("  mode         : %s\n", mode)
	// listening 打印内核实际绑定的地址/端口（0.0.0.0 就是 0.0.0.0），
	// 不再改写成 127.0.0.1 —— 那样会让"到底监听了哪个地址"看不出来。
	listenNote := ""
	if h, _, err := net.SplitHostPort(actual); err == nil && (h == "0.0.0.0" || h == "::") {
		listenNote = "   (all interfaces)"
	}
	fmt.Printf("  listening    : http://%s%s\n", actual, listenNote)
	for _, line := range reachableURLs(actual) {
		fmt.Printf("  %-13s: %s\n", line.label, line.url)
	}
	fmt.Printf("  data dir     : %s\n", a.Config.App.DataDir)
	fmt.Printf("  database     : %s\n", a.Config.Database.Path)
	fmt.Printf("  auth         : %v\n", a.Auth.Enabled())
	fmt.Printf("  protocols    : %s\n", strings.Join(a.Config.Mapper.Protocols, ", "))
	fmt.Printf("  dry run      : %v\n", a.Config.Mapper.DryRun)
	fmt.Printf("\nPress Ctrl+C to stop.\n")

	errCh := make(chan error, 1)
	go func() {
		if a.Config.HTTP.TLS.Enabled {
			errCh <- srv.ServeTLS(ln, a.Config.HTTP.TLS.CertFile, a.Config.HTTP.TLS.KeyFile)
			return
		}
		errCh <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		logging.Info("main", "shutting down HTTP server")
		sctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		return 0
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
			return 1
		}
		return 0
	}
}

// reachableURLs turns the bound address into the URLs a user can actually open.
//
// A wildcard bind (0.0.0.0 / [::]) is not clickable but is the important fact to
// report, so "listening" keeps it verbatim and this helper adds the concrete
// loopback and LAN addresses underneath.
func reachableURLs(actual string) []struct{ label, url string } {
	host, port, err := net.SplitHostPort(actual)
	if err != nil {
		return nil
	}
	var out []struct{ label, url string }
	wildcard := host == "0.0.0.0" || host == "::" || host == ""
	local := net.JoinHostPort("127.0.0.1", port)
	if !isLoopbackLiteral(host) {
		out = append(out, struct{ label, url string }{"local url", "http://" + local})
	}
	if wildcard {
		ifaces, err := netutil.Interfaces()
		if err != nil {
			return out
		}
		// v4 与 v6 分开限流：v4 地址通常排在前面，共用计数会让 v6 永远轮不到。
		// link-local v6（fe80::/10）需要 %zone 才能访问，打印出来用户也点不开，
		// 所以只列全局/ULA 地址。
		seen4, seen6 := 0, 0
		for _, i := range ifaces {
			if i.Loopback || !i.Up || i.IP == "" {
				continue
			}
			if i.IsV6() {
				if i.Scoped || seen6 >= 2 {
					continue
				}
				out = append(out, struct{ label, url string }{"lan6 url", "http://" + netutil.JoinHostPort(i.IP, mustPort(port))})
				seen6++
				continue
			}
			if seen4 >= 4 {
				continue
			}
			out = append(out, struct{ label, url string }{"lan url", "http://" + net.JoinHostPort(i.IP, port)})
			seen4++
		}
	}
	return out
}

func isLoopbackLiteral(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// ---- helpers shared by CLI commands ----

func parseKV(args []string) (map[string]string, []string) {
	kv := map[string]string{}
	rest := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			rest = append(rest, a)
			continue
		}
		key, val, has := splitFlag(a)
		if has {
			kv[key] = val
			continue
		}
		// Support the "--key value" form when the value is not another flag.
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && flagTakesValue(key) {
			kv[key] = args[i+1]
			i++
			continue
		}
		kv[key] = "true"
	}
	return kv, rest
}

// flagTakesValue reports whether a CLI flag consumes a following argument.
func flagTakesValue(key string) bool {
	switch key {
	case "json", "verbose", "quiet", "force", "help", "add-open", "wait", "apply-policy", "open-only":
		return false
	}
	return true
}

func flagBool(kv map[string]string, key string, def bool) bool {
	v, ok := kv[key]
	if !ok {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func flagInt(kv map[string]string, key string, def int) int {
	v, ok := kv[key]
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func flagList(kv map[string]string, key string) []string {
	v := strings.TrimSpace(kv[key])
	if v == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fatal(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	return 1
}

var _ = sort.Strings
var _ = store.RuleFilter{}
var _ = model.Rule{}

// mustPort converts a numeric port string (already validated by SplitHostPort).
func mustPort(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
