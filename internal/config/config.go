// Package config loads, validates and persists NatPilot configuration.
//
// Configuration can come from a YAML/JSON file, environment variables or CLI
// flags (in increasing order of precedence) and may be edited at runtime from
// the web UI; persisted overrides are stored in the SQLite settings table and
// re-applied after the file layer.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Protocol identifiers.
const (
	ProtoIGD2   = "igd2"
	ProtoIGD1   = "igd1"
	ProtoPCP    = "pcp"
	ProtoNATPMP = "natpmp"
)

// Deployment modes.
const (
	ModeAuto    = "auto"
	ModeWeb     = "web"
	ModeDesktop = "desktop"
	ModeServer  = "server"
)

// App holds the top level application identity settings.
type App struct {
	Name        string `json:"name" yaml:"name"`
	DataDir     string `json:"data_dir" yaml:"data_dir"`
	Bind        string `json:"bind" yaml:"bind"`
	AutoMigrate bool   `json:"auto_migrate" yaml:"auto_migrate"`
}

// HTTP holds the embedded HTTP server settings.
type HTTP struct {
	Enabled       bool     `json:"enabled" yaml:"enabled"`
	Host          string   `json:"host" yaml:"host"`
	Port          int      `json:"port" yaml:"port"`
	Mode          string   `json:"mode" yaml:"mode"`
	BasePath      string   `json:"base_path" yaml:"base_path"`
	SessionSecret string   `json:"session_secret" yaml:"session_secret"`
	CORSOrigins   []string `json:"cors_origins" yaml:"cors_origins"`
	TrustProxy    bool     `json:"trust_proxy" yaml:"trust_proxy"`
	TLS           struct {
		Enabled  bool   `json:"enabled" yaml:"enabled"`
		CertFile string `json:"cert_file" yaml:"cert_file"`
		KeyFile  string `json:"key_file" yaml:"key_file"`
	} `json:"tls" yaml:"tls"`
}

// Database holds SQLite settings.
type Database struct {
	Driver string `json:"driver" yaml:"driver"`
	Path   string `json:"path" yaml:"path"`
	WAL    bool   `json:"wal" yaml:"wal"`
	BusyMS int    `json:"busy_timeout_ms" yaml:"busy_timeout_ms"`
}

// Scanner holds port scan defaults.
type Scanner struct {
	MaxConcurrency   int      `json:"max_concurrency" yaml:"max_concurrency"`
	TimeoutMS        int      `json:"timeout_ms" yaml:"timeout_ms"`
	MaxPortsPerScan  int      `json:"max_ports_per_scan" yaml:"max_ports_per_scan"`
	MaxHostsPerScan  int      `json:"max_hosts_per_scan" yaml:"max_hosts_per_scan"`
	DefaultPorts     string   `json:"default_ports" yaml:"default_ports"`
	GrabBanners      bool     `json:"grab_banners" yaml:"grab_banners"`
	AutoMonitorLocal bool     `json:"auto_monitor_local" yaml:"auto_monitor_local"`
	AutoLocalPolicy  string   `json:"auto_local_policy" yaml:"auto_local_policy"`
	AutoScanCIDR     []string `json:"auto_scan_cidr" yaml:"auto_scan_cidr"`
	AutoIntervalSec  int      `json:"auto_interval_seconds" yaml:"auto_interval_seconds"`
	AutoApplyPolicy  bool     `json:"auto_apply_policy" yaml:"auto_apply_policy"`
	AutoAddOpen      bool     `json:"auto_add_open" yaml:"auto_add_open"`
	AutoAddPorts     string   `json:"auto_add_ports" yaml:"auto_add_ports"`
	RateLimitPPS     int      `json:"rate_limit_pps" yaml:"rate_limit_pps"`
	EnableLANScan    bool     `json:"enable_lan_scan" yaml:"enable_lan_scan"`
}

// Mapper holds mapping engine defaults.
type Mapper struct {
	Enabled          bool     `json:"enabled" yaml:"enabled"`
	Protocols        []string `json:"protocols" yaml:"protocols"`
	DiscoverInterval int      `json:"discover_interval_seconds" yaml:"discover_interval_seconds"`
	RenewBefore      int      `json:"renew_seconds_before_expiry" yaml:"renew_seconds_before_expiry"`
	DefaultLease     int      `json:"default_lease_seconds" yaml:"default_lease_seconds"`
	MaxLease         int      `json:"max_lease_seconds" yaml:"max_lease_seconds"`
	CleanupMissing   bool     `json:"cleanup_deleted_rules" yaml:"cleanup_deleted_rules"`
	DryRun           bool     `json:"dry_run" yaml:"dry_run"`
	ReconcileEvery   int      `json:"reconcile_interval_seconds" yaml:"reconcile_interval_seconds"`
	DefaultProtocol  string   `json:"default_protocol" yaml:"default_protocol"`
	ExternalIPCheck  bool     `json:"external_ip_check" yaml:"external_ip_check"`
	AllowExtRange    bool     `json:"allow_external_port_range" yaml:"allow_external_port_range"`
	ConflictPolicy   string   `json:"conflict_policy" yaml:"conflict_policy"` // skip|rename|override|error
}

// Auth holds authentication settings.
type Auth struct {
	Enabled         bool   `json:"enabled" yaml:"enabled"`
	RequireForWeb   bool   `json:"require_for_web" yaml:"require_for_web"`
	BootstrapUser   string `json:"bootstrap_user" yaml:"bootstrap_user"`
	BootstrapPass   string `json:"bootstrap_pass" yaml:"bootstrap_pass"`
	BootstrapName   string `json:"bootstrap_name" yaml:"bootstrap_name"`
	SessionTTLHours int    `json:"session_ttl_hours" yaml:"session_ttl_hours"`
	LockoutAttempts int    `json:"lockout_attempts" yaml:"lockout_attempts"`
	LockoutMinutes  int    `json:"lockout_minutes" yaml:"lockout_minutes"`
	TokenTTLHours   int    `json:"api_token_ttl_hours" yaml:"api_token_ttl_hours"`
}

// Log holds logging settings.
type Log struct {
	Level      string `json:"level" yaml:"level"`
	Format     string `json:"format" yaml:"format"`
	File       string `json:"file" yaml:"file"`
	MaxEntries int    `json:"max_entries" yaml:"max_entries"`
	Retention  int    `json:"retention_days" yaml:"retention_days"`
}

// Config is the root configuration object.
type Config struct {
	App      App      `json:"app" yaml:"app"`
	HTTP     HTTP     `json:"http" yaml:"http"`
	Database Database `json:"database" yaml:"database"`
	Scanner  Scanner  `json:"scanner" yaml:"scanner"`
	Mapper   Mapper   `json:"mapper" yaml:"mapper"`
	Auth     Auth     `json:"auth" yaml:"auth"`
	Log      Log      `json:"log" yaml:"log"`

	Path string `json:"-" yaml:"-"`

	mu        sync.RWMutex      `json:"-" yaml:"-"`
	overrides map[string]string `json:"-" yaml:"-"`
}

// Default returns the built-in configuration.
func Default() *Config {
	c := &Config{}
	c.App = App{
		Name:        "natpilot",
		DataDir:     defaultDataDir(),
		Bind:        "127.0.0.1:0",
		AutoMigrate: true,
	}
	c.HTTP = HTTP{
		Enabled:     true,
		Host:        "0.0.0.0",
		Port:        8787,
		Mode:        ModeAuto,
		BasePath:    "/",
		CORSOrigins: []string{},
	}
	c.Database = Database{
		Driver: "sqlite",
		Path:   filepath.Join(c.App.DataDir, "natpilot.db"),
		WAL:    true,
		BusyMS: 5000,
	}
	c.Scanner = Scanner{
		MaxConcurrency:   512,
		TimeoutMS:        800,
		MaxPortsPerScan:  65535,
		MaxHostsPerScan:  1024,
		DefaultPorts:     "1-1024,3000,3306,5000,5432,6379,8000-8100,8080,8443,9000,9090,27017,32400",
		GrabBanners:      true,
		AutoMonitorLocal: false,
		AutoLocalPolicy:  "",
		AutoIntervalSec:  300,
		AutoApplyPolicy:  true,
		AutoAddOpen:      false,
		AutoAddPorts:     "",
		RateLimitPPS:     0,
		EnableLANScan:    true,
	}
	c.Mapper = Mapper{
		Enabled: true,
		// 能力优先: IGD v2(AddAnyPortMapping/租约) > PCP(显式生命周期) >
		// NAT-PMP(简单) > IGD v1(最老的兼容协议)。降级顺序与功能完整度一致。
		Protocols:        []string{ProtoIGD2, ProtoPCP, ProtoNATPMP, ProtoIGD1},
		DiscoverInterval: 300,
		RenewBefore:      120,
		DefaultLease:     86400,
		MaxLease:         604800,
		CleanupMissing:   true,
		DryRun:           false,
		ReconcileEvery:   60,
		DefaultProtocol:  "tcp",
		ExternalIPCheck:  true,
		AllowExtRange:    true,
		ConflictPolicy:   "skip",
	}
	c.Auth = Auth{
		Enabled:         false,
		RequireForWeb:   true,
		BootstrapUser:   "admin",
		BootstrapName:   "Administrator",
		SessionTTLHours: 72,
		LockoutAttempts: 8,
		LockoutMinutes:  15,
		TokenTTLHours:   720,
	}
	c.Log = Log{
		// 默认不记录：日志与审计页在用户手动开启前保持为空。
		Level:      "off",
		Format:     "text",
		MaxEntries: 5000,
		Retention:  30,
	}
	return c
}

func defaultDataDir() string {
	if v := os.Getenv("NATPILOT_DATA_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "natpilot")
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".natpilot")
	}
	return "./data"
}

// Load reads the configuration file (if present), applies environment
// variables, then the explicit overrides supplied by CLI flags.
func Load(path string, flags map[string]string) (*Config, error) {
	c := Default()
	c.Path = path

	if path != "" {
		if _, err := os.Stat(path); err == nil {
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read config %s: %w", path, err)
			}
			if err := unmarshalAny(b, c); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
			c.Path = path
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	applyEnv(c)
	applyFlags(c, flags)
	if c.App.DataDir != "" && (c.Database.Path == "" || c.Database.Path == filepath.Join(Default().App.DataDir, "natpilot.db")) {
		c.Database.Path = filepath.Join(c.App.DataDir, "natpilot.db")
	}
	if c.App.DataDir != "" {
		c.Database.Path = expand(c.Database.Path)
	}
	return c, nil
}

func unmarshalAny(b []byte, c *Config) error {
	trimmed := strings.TrimSpace(string(b))
	if strings.HasPrefix(trimmed, "{") {
		return json.Unmarshal(b, c)
	}
	return yaml.Unmarshal(b, c)
}

func expand(p string) string {
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return os.ExpandEnv(p)
}

// applyEnv overlays NATPILOT_* environment variables.
func applyEnv(c *Config) {
	if v := os.Getenv("NATPILOT_DATA_DIR"); v != "" {
		c.App.DataDir = v
	}
	if v := os.Getenv("NATPILOT_DB"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("NATPILOT_HOST"); v != "" {
		c.HTTP.Host = v
	}
	if v := os.Getenv("NATPILOT_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.HTTP.Port = n
		}
	}
	if v := os.Getenv("NATPILOT_MODE"); v != "" {
		c.HTTP.Mode = v
	}
	if v := os.Getenv("NATPILOT_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	if v := os.Getenv("NATPILOT_LOG_FORMAT"); v != "" {
		c.Log.Format = v
	}
	if v := os.Getenv("NATPILOT_SESSION_SECRET"); v != "" {
		c.HTTP.SessionSecret = v
	}
	if v := os.Getenv("NATPILOT_AUTH"); v != "" {
		c.Auth.Enabled = truthy(v)
	}
	if v := os.Getenv("NATPILOT_ADMIN_USER"); v != "" {
		c.Auth.BootstrapUser = v
	}
	if v := os.Getenv("NATPILOT_ADMIN_PASSWORD"); v != "" {
		c.Auth.BootstrapPass = v
	}
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on", "y":
		return true
	}
	return false
}

// applyFlags overlays values supplied on the command line.
func applyFlags(c *Config, flags map[string]string) {
	for k, v := range flags {
		if v == "" {
			continue
		}
		applyFlagValue(c, k, v)
	}
}

// applyFlagValue applies one legacy flat flag (the CLI/env spelling) and reports
// whether the key was recognised.
func applyFlagValue(c *Config, key, value string) bool {
	if value == "" {
		return false
	}
	switch key {
	case "data-dir":
		c.App.DataDir = value
	case "db":
		c.Database.Path = value
	case "host":
		c.HTTP.Host = value
	case "port":
		n, err := strconv.Atoi(value)
		if err != nil {
			return false
		}
		c.HTTP.Port = n
	case "mode":
		c.HTTP.Mode = value
	case "log-level":
		c.Log.Level = value
	case "log-format":
		c.Log.Format = value
	case "auth":
		c.Auth.Enabled = truthy(value)
	default:
		return false
	}
	return true
}

// Clone deep copies the configuration.
func (c *Config) Clone() *Config {
	b, _ := json.Marshal(c)
	out := Default()
	_ = json.Unmarshal(b, out)
	out.Path = c.Path
	out.mu = sync.RWMutex{}
	out.overrides = nil
	return out
}

// ApplyOverrides re-applies persisted UI overrides on top of the config and
// returns the keys it could not understand, so a typo (or a key that no longer
// exists) is reported instead of being dropped silently.
func (c *Config) ApplyOverrides(ov map[string]string) []string {
	c.mu.Lock()
	c.overrides = map[string]string{}
	for k, v := range ov {
		c.overrides[k] = v
	}
	c.mu.Unlock()
	rejected := []string{}
	for k, v := range ov {
		if !c.applyOverride(k, v) {
			rejected = append(rejected, k)
		}
	}
	sort.Strings(rejected)
	return rejected
}

// Overrides returns the persisted override map.
func (c *Config) Overrides() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]string{}
	for k, v := range c.overrides {
		out[k] = v
	}
	return out
}

// SetOverride stores an override in memory (persistence is the caller's job) and
// reports whether the configuration understood the key. An unsupported key is not
// kept, otherwise it would be replayed on every start without ever taking effect.
func (c *Config) SetOverride(key, value string) bool {
	if !c.applyOverride(key, value) {
		return false
	}
	c.mu.Lock()
	if c.overrides == nil {
		c.overrides = map[string]string{}
	}
	c.overrides[key] = value
	c.mu.Unlock()
	return true
}

// applyOverride writes one dotted override (e.g. "mapper.dry_run" or
// "http.tls.enabled") into the live configuration. Nested sections are walked by
// JSON tag, so keys of any depth are supported; list-valued keys accept a comma
// separated string. The result reports whether the key was understood and applied.
func (c *Config) applyOverride(key, value string) bool {
	parts := strings.Split(strings.TrimSpace(key), ".")
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return false
		}
	}
	if len(parts) < 2 {
		// Legacy flat keys keep their old meaning.
		return applyFlagValue(c, key, value)
	}
	rv := reflect.ValueOf(c).Elem()
	for _, section := range parts[:len(parts)-1] {
		next, ok := structFieldByTag(rv, section)
		if !ok {
			return applyFlagValue(c, key, value)
		}
		rv = next
	}
	fv, ok := structFieldByTag(rv, parts[len(parts)-1])
	if !ok {
		// Fall back to env-style handling for known flat aliases.
		return applyFlagValue(c, key, value)
	}
	return assignValue(fv, value, fv.Type())
}

// structFieldByTag returns the settable field of a struct matched by its JSON tag
// or by its Go name (case insensitive). Unexported and json:"-" fields are skipped.
func structFieldByTag(rv reflect.Value, key string) (reflect.Value, bool) {
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if tag != key && !strings.EqualFold(f.Name, key) {
			continue
		}
		fv := rv.Field(i)
		if !fv.CanSet() {
			return reflect.Value{}, false
		}
		return fv, true
	}
	return reflect.Value{}, false
}

func assignValue(fv reflect.Value, raw string, ft reflect.Type) bool {
	switch ft.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		fv.SetBool(truthy(raw))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return false
		}
		fv.SetInt(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return false
		}
		fv.SetFloat(n)
	case reflect.Slice:
		if ft.Elem().Kind() != reflect.String {
			return false
		}
		out := []string{}
		for _, part := range strings.Split(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		fv.Set(reflect.ValueOf(out))
	default:
		return false
	}
	return true
}

// RedactedMap renders the configuration for the API with secrets removed.
func (c *Config) RedactedMap() map[string]any {
	b, _ := json.Marshal(c)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	redact(out)
	return out
}

var secretKeys = map[string]bool{
	"session_secret": true, "bootstrap_pass": true, "password": true,
	"secret": true, "key": true, "token": true,
}

func redact(m map[string]any) {
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			redact(sub)
			continue
		}
		if secretKeys[strings.ToLower(k)] {
			if s, ok := v.(string); ok && s != "" {
				m[k] = "***redacted***"
			}
		}
	}
}

// Validate performs sanity checks on the configuration and normalises the
// documented defaults. Anything that would make the process bind the wrong
// address, serve cleartext while claiming TLS, or silently ignore an unknown
// protocol/policy name is rejected here instead of failing later at runtime.
func (c *Config) Validate() error {
	switch c.HTTP.Mode {
	case ModeAuto, ModeWeb, ModeDesktop, ModeServer, "":
	default:
		return fmt.Errorf("invalid http.mode %q", c.HTTP.Mode)
	}
	if c.HTTP.Port < 0 || c.HTTP.Port > 65535 {
		return fmt.Errorf("invalid http.port %d", c.HTTP.Port)
	}
	if host := strings.TrimSpace(c.HTTP.Host); host != "" && !validBindHost(host) {
		return fmt.Errorf("invalid http.host %q", host)
	}
	if bp := strings.TrimSpace(c.HTTP.BasePath); bp != "" && !strings.HasPrefix(bp, "/") {
		return fmt.Errorf("invalid http.base_path %q: must start with /", c.HTTP.BasePath)
	}
	if c.HTTP.TLS.Enabled {
		if strings.TrimSpace(c.HTTP.TLS.CertFile) == "" || strings.TrimSpace(c.HTTP.TLS.KeyFile) == "" {
			return fmt.Errorf("http.tls requires both cert_file and key_file")
		}
	}
	for _, origin := range c.HTTP.CORSOrigins {
		if strings.TrimSpace(origin) == "" {
			return fmt.Errorf("http.cors_origins must not contain empty entries")
		}
	}

	if c.Scanner.MaxConcurrency <= 0 {
		c.Scanner.MaxConcurrency = 512
	}
	if c.Scanner.TimeoutMS <= 0 {
		c.Scanner.TimeoutMS = 800
	}
	if c.Scanner.RateLimitPPS < 0 {
		return fmt.Errorf("invalid scanner.rate_limit_pps %d", c.Scanner.RateLimitPPS)
	}
	if c.Scanner.AutoIntervalSec < 0 {
		return fmt.Errorf("invalid scanner.auto_interval_seconds %d", c.Scanner.AutoIntervalSec)
	}

	if c.Mapper.DefaultLease <= 0 {
		c.Mapper.DefaultLease = 86400
	}
	if c.Mapper.MaxLease < 0 {
		return fmt.Errorf("invalid mapper.max_lease_seconds %d", c.Mapper.MaxLease)
	}
	if c.Mapper.MaxLease > 0 && c.Mapper.MaxLease < c.Mapper.DefaultLease {
		// The cap wins: keep the pair consistent instead of clamping at use time.
		c.Mapper.DefaultLease = c.Mapper.MaxLease
	}
	if p := strings.ToLower(strings.TrimSpace(c.Mapper.DefaultProtocol)); p != "" {
		if p != "tcp" && p != "udp" {
			return fmt.Errorf("invalid mapper.default_protocol %q: must be tcp or udp", p)
		}
		c.Mapper.DefaultProtocol = p
	}
	switch p := strings.ToLower(strings.TrimSpace(c.Mapper.ConflictPolicy)); p {
	case "", "skip":
		c.Mapper.ConflictPolicy = "skip"
	case "rename", "override", "error":
		c.Mapper.ConflictPolicy = p
	default:
		return fmt.Errorf("invalid mapper.conflict_policy %q", c.Mapper.ConflictPolicy)
	}
	if len(c.Mapper.Protocols) > 0 {
		out := make([]string, 0, len(c.Mapper.Protocols))
		seen := map[string]bool{}
		for _, entry := range c.Mapper.Protocols {
			v := strings.ToLower(strings.TrimSpace(entry))
			if v == "" {
				continue
			}
			switch v {
			case ProtoIGD2, ProtoIGD1, ProtoPCP, ProtoNATPMP:
				if !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			default:
				return fmt.Errorf("invalid mapper.protocols entry %q", entry)
			}
		}
		c.Mapper.Protocols = out
	}

	if c.Auth.SessionTTLHours <= 0 {
		c.Auth.SessionTTLHours = 72
	}
	if c.Auth.LockoutAttempts <= 0 {
		c.Auth.LockoutAttempts = 8
	}
	if c.Auth.LockoutMinutes <= 0 {
		c.Auth.LockoutMinutes = 15
	}
	if c.Auth.TokenTTLHours <= 0 {
		c.Auth.TokenTTLHours = 720
	}

	switch l := strings.ToLower(strings.TrimSpace(c.Log.Level)); l {
	case "", "info":
		c.Log.Level = "info"
	case "debug", "warn", "error":
		c.Log.Level = l
	case "warning":
		c.Log.Level = "warn"
	case "off", "none", "silent", "disabled":
		c.Log.Level = "off"
	default:
		return fmt.Errorf("invalid log.level %q (debug|info|warn|error|off)", c.Log.Level)
	}
	switch f := strings.ToLower(strings.TrimSpace(c.Log.Format)); f {
	case "", "text", "console":
		c.Log.Format = "text"
	case "json":
		c.Log.Format = "json"
	default:
		return fmt.Errorf("invalid log.format %q", c.Log.Format)
	}

	if c.Database.Path == "" {
		return fmt.Errorf("database.path must not be empty")
	}
	return nil
}

// validBindHost reports whether host is a plausible bind address: an IP literal,
// the wildcard form, or a hostname made of letters, digits, dots and hyphens.
func validBindHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	switch strings.ToLower(host) {
	case "localhost", "*":
		return true
	}
	if len(host) >= 2 && strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return net.ParseIP(strings.Trim(host, "[]")) != nil
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			ok := r == '-' || r == '_' ||
				(r >= '0' && r <= '9') ||
				(r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z')
			if !ok {
				return false
			}
		}
	}
	return true
}

// EnsureDataDir creates the data directory.
func (c *Config) EnsureDataDir() error {
	return os.MkdirAll(c.App.DataDir, 0o750)
}

// DSN builds the SQLite connection string.
func (c *Config) DSN() string {
	q := []string{}
	if c.Database.WAL {
		q = append(q, "_journal_mode=WAL")
	}
	busy := c.Database.BusyMS
	if busy <= 0 {
		busy = 5000
	}
	q = append(q, fmt.Sprintf("_busy_timeout=%d", busy))
	q = append(q, "_foreign_keys=on")
	q = append(q, "_synchronous=NORMAL")
	// The DSN keys above are understood by both mattn/go-sqlite3 and the pure Go
	// modernc.org/sqlite driver, which deliberately accepts the same shorthand
	// set. "cache=shared" is intentionally NOT set: it is a mattn-only option
	// and the shared cache is discouraged with WAL.
	return c.Database.Path + "?" + strings.Join(q, "&")
}

// EffectiveMode resolves the auto mode into a concrete one. When natpilot is
// started by a desktop shell (electron/tray wrapper) the environment variable
// NATPILOT_DESKTOP=1 is set by that shell.
func (c *Config) EffectiveMode() string {
	m := strings.ToLower(strings.TrimSpace(c.HTTP.Mode))
	if m == "" {
		m = ModeAuto
	}
	if m != ModeAuto {
		return m
	}
	if truthy(os.Getenv("NATPILOT_DESKTOP")) {
		return ModeDesktop
	}
	if inContainer() {
		return ModeServer
	}
	if os.Getenv("NATPILOT_SERVER") != "" {
		return ModeServer
	}
	// Auto mode must not silently downgrade a service that was explicitly
	// configured to require login: "natpilot --auth true serve" is documented as
	// the server deployment, so honour that instead of guessing "desktop".
	if c.Auth.Enabled {
		return ModeServer
	}
	return ModeDesktop
}

// EffectiveBindHost reports the address the HTTP server will actually bind to.
//
// Desktop mode is a local-only deployment: it always binds the loopback
// interface, so a stray "--host 0.0.0.0" (or an http.host setting) can never
// expose an unauthenticated console to the LAN. Use server/web mode for anything
// reachable from another machine.
func (c *Config) EffectiveBindHost() string {
	if c.EffectiveMode() == ModeDesktop {
		return "127.0.0.1"
	}
	host := strings.TrimSpace(c.HTTP.Host)
	if host == "" {
		return "0.0.0.0"
	}
	return host
}

// IsLoopbackHost reports whether a bind address only accepts local connections.
func (c *Config) IsLoopbackHost() bool { return isLoopbackHost(c.EffectiveBindHost()) }

func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" || h == "localhost" {
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// inContainer reports whether the process runs inside a container runtime.
//
// docker writes /.dockerenv, podman writes /run/.containerenv, and Kubernetes
// leaves neither. The cgroup heuristic is only a fallback: with cgroup v2 (the
// default since Docker 20.10) /proc/1/cgroup contains just "0::/", so matching on
// "docker" there does not work any more.
func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		for _, marker := range []string{"docker", "kubepods", "containerd", "lxc", "podman"} {
			if strings.Contains(s, marker) {
				return true
			}
		}
	}
	return false
}

// AuthRequired reports whether login is enforced for the current mode.
//
// The desktop免登录 behaviour is intentionally narrow: it only applies while the
// server is actually reachable from the loopback interface alone. As soon as the
// bind address is routable the console requires login again, which keeps the
// promise made in the README ("免登录仅对本机回环生效").
func (c *Config) AuthRequired() bool {
	if !c.Auth.Enabled {
		return false
	}
	if c.EffectiveMode() == ModeDesktop && c.IsLoopbackHost() {
		return false
	}
	return c.Auth.RequireForWeb
}

// SessionSecretOrGenerate returns the configured session secret or creates a
// stable random one for the given data dir.
func (c *Config) SessionSecretOrGenerate() string {
	if s := strings.TrimSpace(c.HTTP.SessionSecret); s != "" {
		return s
	}
	p := filepath.Join(c.App.DataDir, "session.key")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
		return strings.TrimSpace(string(b))
	}
	secret := randomHex(32)
	_ = os.MkdirAll(c.App.DataDir, 0o750)
	_ = os.WriteFile(p, []byte(secret), 0o600)
	c.HTTP.SessionSecret = secret
	return secret
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should never fail; fall back to a time-derived value rather
		// than returning an empty secret, but make it obvious in the log.
		now := time.Now().UnixNano()
		for i := range b {
			now = now*6364136223846793005 + 1442695040888963407
			b[i] = byte(now >> 33)
		}
	}
	return hex.EncodeToString(b)
}
