package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if c.HTTP.Port != 8787 {
		t.Errorf("default port = %d", c.HTTP.Port)
	}
	if len(c.Mapper.Protocols) != 4 || c.Mapper.Protocols[0] != ProtoIGD2 {
		t.Errorf("default protocol order = %v (newest must come first)", c.Mapper.Protocols)
	}
}

func TestApplyOverrideNestedKeys(t *testing.T) {
	c := Default()
	c.ApplyOverrides(map[string]string{
		"mapper.dry_run":             "true",
		"scanner.max_concurrency":    "64",
		"scanner.default_ports":      "22,80",
		"mapper.protocols":           "pcp,natpmp",
		"log.level":                  "debug",
		"scanner.auto_monitor_local": "true",
	})

	if !c.Mapper.DryRun {
		t.Errorf("mapper.dry_run override not applied")
	}
	if c.Scanner.MaxConcurrency != 64 {
		t.Errorf("scanner.max_concurrency = %d, want 64", c.Scanner.MaxConcurrency)
	}
	if c.Scanner.DefaultPorts != "22,80" {
		t.Errorf("scanner.default_ports = %q", c.Scanner.DefaultPorts)
	}
	if len(c.Mapper.Protocols) != 2 || c.Mapper.Protocols[0] != "pcp" {
		t.Errorf("mapper.protocols = %v", c.Mapper.Protocols)
	}
	if c.Log.Level != "debug" {
		t.Errorf("log.level = %q", c.Log.Level)
	}
	if !c.Scanner.AutoMonitorLocal {
		t.Errorf("scanner.auto_monitor_local not applied")
	}
}

func TestLoadFromFileAndFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "natpilot.yaml")
	body := "http:\n  port: 9999\nlog:\n  level: warn\nmapper:\n  dry_run: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	c, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.HTTP.Port != 9999 {
		t.Errorf("file port = %d, want 9999", c.HTTP.Port)
	}
	if c.Log.Level != "warn" {
		t.Errorf("file log level = %q", c.Log.Level)
	}
	if !c.Mapper.DryRun {
		t.Errorf("file dry_run not applied")
	}
	// Unset fields keep their defaults.
	if c.Scanner.MaxConcurrency != 512 {
		t.Errorf("default not preserved: max_concurrency = %d", c.Scanner.MaxConcurrency)
	}

	// CLI flags win over the file.
	c, err = Load(path, map[string]string{"port": "8123", "log-level": "error"})
	if err != nil {
		t.Fatalf("Load with flags: %v", err)
	}
	if c.HTTP.Port != 8123 {
		t.Errorf("flag port = %d, want 8123", c.HTTP.Port)
	}
	if c.Log.Level != "error" {
		t.Errorf("flag log level = %q", c.Log.Level)
	}
}

func TestAuthRequiredByMode(t *testing.T) {
	c := Default()
	c.Auth.Enabled = true
	c.Auth.RequireForWeb = true

	c.HTTP.Mode = ModeServer
	if !c.AuthRequired() {
		t.Errorf("server mode with auth enabled must require login")
	}
	c.HTTP.Mode = ModeDesktop
	if c.AuthRequired() {
		t.Errorf("desktop mode must never require login")
	}
	c.HTTP.Mode = ModeWeb
	if !c.AuthRequired() {
		t.Errorf("web mode with auth enabled must require login")
	}
	c.Auth.Enabled = false
	c.HTTP.Mode = ModeServer
	if c.AuthRequired() {
		t.Errorf("auth disabled must not require login")
	}
}

func TestSessionSecretStable(t *testing.T) {
	dir := t.TempDir()
	c := Default()
	c.App.DataDir = dir
	first := c.SessionSecretOrGenerate()
	if len(first) < 16 {
		t.Fatalf("generated secret too short: %q", first)
	}
	second := c.SessionSecretOrGenerate()
	if first != second {
		t.Errorf("session secret is not stable across calls")
	}
	// A fresh config in the same data dir reads the persisted secret.
	c2 := Default()
	c2.App.DataDir = dir
	c2.HTTP.SessionSecret = ""
	if got := c2.SessionSecretOrGenerate(); got != first {
		t.Errorf("secret not persisted: %q != %q", got, first)
	}
}

func TestRedaction(t *testing.T) {
	c := Default()
	c.HTTP.SessionSecret = "supersecret"
	c.Auth.BootstrapPass = "hunter2"
	m := c.RedactedMap()
	httpSec, _ := m["http"].(map[string]any)
	if httpSec["session_secret"] != "***redacted***" {
		t.Errorf("session secret not redacted: %v", httpSec["session_secret"])
	}
	authSec, _ := m["auth"].(map[string]any)
	if authSec["bootstrap_pass"] != "***redacted***" {
		t.Errorf("bootstrap password not redacted: %v", authSec["bootstrap_pass"])
	}
}

// Desktop mode is documented as a local-only deployment: it must never publish an
// unauthenticated console on a routable interface, whatever http.host says.
func TestEffectiveBindHostDesktopIsLoopbackOnly(t *testing.T) {
	t.Setenv("NATPILOT_DESKTOP", "")
	t.Setenv("NATPILOT_SERVER", "")

	c := Default()

	c.HTTP.Mode = ModeDesktop
	for _, host := range []string{"0.0.0.0", "", "192.168.1.5", "::"} {
		c.HTTP.Host = host
		if got := c.EffectiveBindHost(); got != "127.0.0.1" {
			t.Errorf("desktop bind host for %q = %q, want 127.0.0.1", host, got)
		}
	}

	c.HTTP.Mode = ModeServer
	c.HTTP.Host = ""
	if got := c.EffectiveBindHost(); got != "0.0.0.0" {
		t.Errorf("server bind host for empty host = %q, want 0.0.0.0", got)
	}
	c.HTTP.Host = "192.168.1.5"
	if got := c.EffectiveBindHost(); got != "192.168.1.5" {
		t.Errorf("server bind host = %q, want 192.168.1.5", got)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost", "::1", "", "127.0.0.53"} {
		if !isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"0.0.0.0", "192.168.1.5", "::", "example.lan"} {
		if isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = true, want false", h)
		}
	}
}

func TestAuthRequired(t *testing.T) {
	t.Setenv("NATPILOT_DESKTOP", "")
	t.Setenv("NATPILOT_SERVER", "")

	c := Default()
	c.Auth.Enabled = true
	c.Auth.RequireForWeb = true

	c.HTTP.Mode = ModeDesktop
	c.HTTP.Host = "0.0.0.0"
	if c.AuthRequired() {
		t.Errorf("desktop bound to loopback should not require login")
	}

	c.HTTP.Mode = ModeServer
	if !c.AuthRequired() {
		t.Errorf("server mode must require login")
	}

	c.HTTP.Mode = ModeWeb
	if !c.AuthRequired() {
		t.Errorf("web mode must require login")
	}

	c.Auth.Enabled = false
	if c.AuthRequired() {
		t.Errorf("auth disabled must not require login")
	}
}

// The README documents "natpilot --auth true serve" as the login-protected server
// deployment. Auto mode used to resolve that to desktop, which silently disabled
// authentication.
func TestEffectiveModeWithAuthImpliesServer(t *testing.T) {
	t.Setenv("NATPILOT_DESKTOP", "")
	t.Setenv("NATPILOT_SERVER", "")
	if inContainer() {
		t.Skip("running in a container: auto mode always resolves to server")
	}

	c := Default()
	c.HTTP.Mode = ModeAuto

	c.Auth.Enabled = false
	if got := c.EffectiveMode(); got != ModeDesktop {
		t.Fatalf("auto mode without auth = %q, want %q", got, ModeDesktop)
	}

	c.Auth.Enabled = true
	if got := c.EffectiveMode(); got != ModeServer {
		t.Fatalf("auto mode with auth enabled = %q, want %q", got, ModeServer)
	}
}

func TestValidateRejectsBadHost(t *testing.T) {
	for _, host := range []string{"not a host", "http://x", "192.168.1.5:8080", "a..b"} {
		c := Default()
		c.HTTP.Host = host
		if err := c.Validate(); err == nil {
			t.Errorf("Validate() accepted invalid http.host %q", host)
		}
	}
	for _, host := range []string{"0.0.0.0", "127.0.0.1", "::", "::1", "[::1]", "localhost", "nas.local"} {
		c := Default()
		c.HTTP.Host = host
		if err := c.Validate(); err != nil {
			t.Errorf("Validate() rejected valid http.host %q: %v", host, err)
		}
	}
}

func TestValidateRequiresTLSPair(t *testing.T) {
	c := Default()
	c.HTTP.TLS.Enabled = true
	c.HTTP.TLS.CertFile = "/tmp/cert.pem"
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() accepted http.tls without key_file")
	}
	c.HTTP.TLS.KeyFile = "/tmp/key.pem"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() rejected a complete TLS pair: %v", err)
	}
}

func TestValidateNormalizesEnumsAndLists(t *testing.T) {
	c := Default()
	c.Mapper.Protocols = []string{"IGD2", "igd2", " pcp ", "natpmp", ""}
	c.Mapper.ConflictPolicy = "  RENAME "
	c.Mapper.DefaultProtocol = "UDP"
	c.Log.Level = "warning"
	c.Log.Format = "console"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() failed: %v", err)
	}
	want := []string{"igd2", "pcp", "natpmp"}
	if len(c.Mapper.Protocols) != len(want) {
		t.Fatalf("protocols = %v, want %v", c.Mapper.Protocols, want)
	}
	for i := range want {
		if c.Mapper.Protocols[i] != want[i] {
			t.Fatalf("protocols = %v, want %v", c.Mapper.Protocols, want)
		}
	}
	if c.Mapper.ConflictPolicy != "rename" {
		t.Errorf("conflict_policy = %q, want rename", c.Mapper.ConflictPolicy)
	}
	if c.Mapper.DefaultProtocol != "udp" {
		t.Errorf("default_protocol = %q, want udp", c.Mapper.DefaultProtocol)
	}
	if c.Log.Level != "warn" || c.Log.Format != "text" {
		t.Errorf("log = %q/%q, want warn/text", c.Log.Level, c.Log.Format)
	}
}

func TestValidateRejectsUnknownEnums(t *testing.T) {
	c := Default()
	c.Mapper.Protocols = []string{"upnp"}
	if err := c.Validate(); err == nil {
		t.Error("Validate() accepted an unknown mapper.protocols entry")
	}

	c = Default()
	c.Mapper.ConflictPolicy = "yolo"
	if err := c.Validate(); err == nil {
		t.Error("Validate() accepted an unknown conflict_policy")
	}

	c = Default()
	c.Mapper.DefaultProtocol = "sctp"
	if err := c.Validate(); err == nil {
		t.Error("Validate() accepted an unknown default_protocol")
	}

	c = Default()
	c.Log.Level = "verbose"
	if err := c.Validate(); err == nil {
		t.Error("Validate() accepted an unknown log.level")
	}
}

func TestValidateKeepsLeasePairConsistent(t *testing.T) {
	c := Default()
	c.Mapper.MaxLease = 600
	c.Mapper.DefaultLease = 86400
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() failed: %v", err)
	}
	if c.Mapper.DefaultLease != 600 {
		t.Errorf("default lease = %d, want it clamped to max 600", c.Mapper.DefaultLease)
	}
}

// applyOverride used to understand only two levels, so a nested key such as
// http.tls.enabled was dropped without any signal.
func TestApplyOverrideNestedDepth(t *testing.T) {
	c := Default()
	if rejected := c.ApplyOverrides(map[string]string{
		"http.tls.enabled": "true",
		"mapper.dry_run":   "true",
		"nope.nothing":     "1",
	}); len(rejected) != 1 || rejected[0] != "nope.nothing" {
		t.Fatalf("rejected = %v, want [nope.nothing]", rejected)
	}
	if !c.HTTP.TLS.Enabled {
		t.Error("http.tls.enabled was not applied through two nested levels")
	}
	if !c.Mapper.DryRun {
		t.Error("mapper.dry_run was not applied")
	}
}

// SetOverride must not keep a key the configuration cannot understand, otherwise
// it would be replayed on every start without ever taking effect.
func TestSetOverrideRejectsUnknownKey(t *testing.T) {
	c := Default()
	if c.SetOverride("mapper.nope", "1") {
		t.Fatal("SetOverride() accepted an unknown key")
	}
	if v, ok := c.Overrides()["mapper.nope"]; ok {
		t.Fatalf("unknown override was stored: %q", v)
	}
	if !c.SetOverride("mapper.dry_run", "true") {
		t.Fatal("SetOverride() rejected a valid key")
	}
	if !c.Mapper.DryRun {
		t.Error("valid override was not applied")
	}
}
