package model

// Mapping status values.
const (
	MapStatusPending  = "pending"
	MapStatusActive   = "active"
	MapStatusFailed   = "failed"
	MapStatusConflict = "conflict"
	MapStatusDisabled = "disabled"
	MapStatusRemoved  = "removed"
)

// Protocol values for a mapping rule.
const (
	ProtoTCP = "tcp"
	ProtoUDP = "udp"
	ProtoAny = "any"
)

// PortAction values.
const (
	ActionAdd    = "add"
	ActionIgnore = "ignore"
	ActionDelete = "delete"
	ActionWatch  = "watch"
)

// Scan source kinds.
const (
	ScanTargetLocal  = "local"
	ScanTargetCIDR   = "cidr"
	ScanTargetRemote = "remote"
	// ScanTargetMAC targets hosts by hardware address instead of by network:
	// the address is looked up when the scan starts, so a device that got a new
	// DHCP lease is still scanned.
	ScanTargetMAC = "mac"
)

// Scan run status values.
const (
	ScanStatusRunning = "running"
	ScanStatusDone    = "done"
	ScanStatusFailed  = "failed"
	ScanStatusCancel  = "canceled"
)

// Discovered port status values (the user-facing lifecycle).
const (
	StatusAdded      = "added"       // present in the mapping rule list
	StatusOpen       = "open"        // listening locally but not managed
	StatusClosed     = "closed"      // known but not currently listening
	StatusNotAdded   = "not_added"   // open, never added, awaiting a decision
	StatusSourceGone = "source_gone" // rule exists but its source port disappeared
)

// Rule is a managed host+port mapping entry. A rule is only acted upon when
// Enabled is true.
type Rule struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Host        string     `json:"host"` // target host/IP the mapping points at (LAN side)
	Port        int        `json:"port"` // target internal port
	ExtPort     int        `json:"ext_port"`
	Protocol    string     `json:"protocol"`
	Tags        StringList `json:"tags"`
	Note        string     `json:"note"`
	Enabled     bool       `json:"enabled"`
	AutoCreated bool       `json:"auto_created"`
	Source      string     `json:"source"` // manual|scan|import|api
	CreatedAt   Time       `json:"created_at"`
	UpdatedAt   Time       `json:"updated_at"`

	// Join / computed fields (not persisted).
	ExternalIP    string  `json:"external_ip,omitempty"`
	ExternalPort  int     `json:"external_port,omitempty"`
	Status        string  `json:"status,omitempty"`
	LastError     string  `json:"last_error,omitempty"`
	ProviderName  string  `json:"provider,omitempty"`
	LeaseExpires  int     `json:"lease_expires_at,omitempty"`
	LeaseSeconds  int     `json:"lease_seconds,omitempty"`
	ObservedOpen  bool    `json:"observed_open,omitempty"`
	ConflictOf    int64   `json:"conflict_of,omitempty"`
	SuccessCount  int     `json:"success_count"`
	FailCount     int     `json:"fail_count"`
	ExternalHost  string  `json:"-"`
	InternalDevIP string  `json:"internal_ip,omitempty"`
	ReverseLookup string  `json:"reverse_dns,omitempty"`
	Score         float64 `json:"score,omitempty"`
}

// Clone returns a shallow copy of the rule.
func (r Rule) Clone() Rule {
	c := r
	c.Tags = append(StringList{}, r.Tags...)
	return c
}

// DiscoveredPort is one observation produced by a scan or bound to a rule.
type DiscoveredPort struct {
	ID         int64      `json:"id"`
	ScanID     int64      `json:"scan_id"`
	RuleID     int64      `json:"rule_id,omitempty"`
	Host       string     `json:"host"`
	Port       int        `json:"port"`
	Protocol   string     `json:"protocol"`
	Status     string     `json:"status"`
	Action     string     `json:"action"`
	Added      bool       `json:"added"`
	Ignored    bool       `json:"ignored"`
	Process    string     `json:"process,omitempty"`
	PID        int        `json:"pid,omitempty"`
	Banner     string     `json:"banner,omitempty"`
	Service    string     `json:"service,omitempty"`
	LatencyMS  int        `json:"latency_ms,omitempty"`
	Tags       StringList `json:"tags"`
	Note       string     `json:"note,omitempty"`
	TargetKind string     `json:"target_kind,omitempty"`
	TargetRef  string     `json:"target_ref,omitempty"`
	FirstSeen  Time       `json:"first_seen"`
	LastSeen   Time       `json:"last_seen"`
}

// ScanRun records a scan execution.
type ScanRun struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name,omitempty"`
	TargetKind  string   `json:"target_kind"`
	TargetRef   string   `json:"target_ref"`
	PortsSpec   string   `json:"ports_spec"`
	Trigger     string   `json:"trigger"` // manual|schedule|api
	Status      string   `json:"status"`
	Progress    int      `json:"progress"`
	Total       int      `json:"total"`
	Open        int      `json:"open_count"`
	NewCount    int      `json:"new_count"`
	ErrorCount  int      `json:"error_count"`
	Message     string   `json:"message,omitempty"`
	StartedAt   Time     `json:"started_at"`
	FinishedAt  NullTime `json:"finished_at"`
	DurationMS  int64    `json:"duration_ms"`
	PolicyID    int64    `json:"policy_id,omitempty"`
	HostsCount  int      `json:"hosts_count"`
	Engine      string   `json:"engine,omitempty"`
	ResultsNote string   `json:"results_note,omitempty"`
}

// Policy is a saved default strategy applied automatically to a scan's results
// (whitelist/blacklist ports, port ranges, protocol, auto add/ignore, tagging).
type Policy struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Enabled       bool       `json:"enabled"`
	MatchTarget   string     `json:"match_target_kind"` // local|cidr|remote|any
	PortRanges    string     `json:"port_ranges"`       // e.g. "22,8000-8100,3000"
	ExcludeRanges string     `json:"exclude_ranges"`
	Protocol      string     `json:"protocol"`
	AutoAction    string     `json:"auto_action"` // add|watch|ignore
	AutoAdd       bool       `json:"auto_add"`
	Tags          StringList `json:"tags"`
	Note          string     `json:"note"`
	MinPort       int        `json:"min_port"`
	MaxPort       int        `json:"max_port"`
	RequireName   string     `json:"require_name"`
	Priority      int        `json:"priority"`
	CreatedAt     Time       `json:"created_at"`
	UpdatedAt     Time       `json:"updated_at"`

	// MAC binds this policy to one host. Addresses handed out by DHCP change,
	// so a host-bound policy is keyed by the hardware address and the IP is
	// resolved from the hosts table at match time. Empty means a global policy.
	MAC string `json:"mac,omitempty"`

	// MACPatterns matches several devices by hardware address (full address or
	// OUI prefix, comma separated). This is the "MAC" match kind: a policy that
	// follows a group of devices no matter which addresses DHCP gives them.
	MACPatterns string `json:"mac_patterns,omitempty"`

	// Computed at read time (not persisted): where the bound host currently is.
	HostIP   string `json:"host_ip,omitempty"`
	HostName string `json:"host_name,omitempty"`
}

// Schedule defines recurring execution of a scan or a maintenance task.
type Schedule struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"` // scan|reconcile|cleanup|discover
	Enabled    bool     `json:"enabled"`
	Cron       string   `json:"cron"`
	TargetKind string   `json:"target_kind"`
	TargetRef  string   `json:"target_ref"`
	PortsSpec  string   `json:"ports_spec"`
	PolicyID   int64    `json:"policy_id,omitempty"`
	LastRunAt  NullTime `json:"last_run_at"`
	NextRunAt  NullTime `json:"next_run_at"`
	LastStatus string   `json:"last_status,omitempty"`
	RunCount   int      `json:"run_count"`
	CreatedAt  Time     `json:"created_at"`
	UpdatedAt  Time     `json:"updated_at"`
}

// MappingRecord is the persisted state of a mapping attempt for a rule.
type MappingRecord struct {
	ID           int64    `json:"id"`
	RuleID       int64    `json:"rule_id"`
	ProviderName string   `json:"provider_name"`
	Protocol     string   `json:"protocol"`
	Status       string   `json:"status"`
	ExternalIP   string   `json:"external_ip,omitempty"`
	ExternalPort int      `json:"external_port,omitempty"`
	InternalHost string   `json:"internal_host,omitempty"`
	InternalPort int      `json:"internal_port,omitempty"`
	LeaseSeconds int      `json:"lease_seconds,omitempty"`
	Description  string   `json:"description,omitempty"`
	Error        string   `json:"error,omitempty"`
	CreatedAt    Time     `json:"created_at"`
	UpdatedAt    Time     `json:"updated_at"`
	ExpiresAt    NullTime `json:"expires_at"`
}

// Gateway is a discovered NAT gateway with its supported capabilities.
type Gateway struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Kind         string     `json:"kind"` // igd2|igd1|pcp|natpmp|upnp
	Address      string     `json:"address"`
	Port         int        `json:"port"`
	Location     string     `json:"location,omitempty"`
	ControlURL   string     `json:"control_url,omitempty"`
	ServiceType  string     `json:"service_type,omitempty"`
	ExternalIP   string     `json:"external_ip,omitempty"`
	ModelName    string     `json:"model_name,omitempty"`
	Manufacturer string     `json:"manufacturer,omitempty"`
	Preferred    bool       `json:"preferred"`
	Enabled      bool       `json:"enabled"`
	LastSeen     Time       `json:"last_seen"`
	LastError    string     `json:"last_error,omitempty"`
	Capabilities StringList `json:"capabilities"`
}

// Host is a discovered network peer.
type Host struct {
	ID        int64      `json:"id"`
	IP        string     `json:"ip"`
	MAC       string     `json:"mac,omitempty"`
	Hostname  string     `json:"hostname,omitempty"`
	Vendor    string     `json:"vendor,omitempty"`
	Kind      string     `json:"kind,omitempty"`
	Interface string     `json:"interface,omitempty"`
	IsLocal   bool       `json:"is_local"`
	SeenCount int        `json:"seen_count"`
	OpenPorts StringList `json:"open_ports,omitempty"`
	Tags      StringList `json:"tags"`
	Note      string     `json:"note,omitempty"`
	FirstSeen Time       `json:"first_seen"`
	LastSeen  Time       `json:"last_seen"`
	Source    string     `json:"source,omitempty"`
}

// User account for web/GUI deployments.
type User struct {
	ID           int64    `json:"id"`
	Username     string   `json:"username"`
	DisplayName  string   `json:"display_name"`
	Email        string   `json:"email,omitempty"`
	Role         string   `json:"role"` // admin|operator|viewer
	Enabled      bool     `json:"enabled"`
	PasswordHash string   `json:"-"`
	CreatedAt    Time     `json:"created_at"`
	UpdatedAt    Time     `json:"updated_at"`
	LastLoginAt  NullTime `json:"last_login_at"`
}

// AuditEntry records a security relevant action.
type AuditEntry struct {
	ID        int64  `json:"id"`
	At        Time   `json:"at"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Object    string `json:"object"`
	Detail    string `json:"detail,omitempty"`
	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	Success   bool   `json:"success"`
}

// SystemEvent is a structured, user-visible system log entry.
type SystemEvent struct {
	ID       int64   `json:"id"`
	At       Time    `json:"at"`
	Level    string  `json:"level"`
	Category string  `json:"category"`
	Message  string  `json:"message"`
	Meta     JSONMap `json:"meta,omitempty"`
	Read     bool    `json:"read"`
}

// Setting is a persisted key/value configuration overrides.
type Setting struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt Time   `json:"updated_at"`
}

// Stats is the dashboard summary payload.
type Stats struct {
	Rules       int            `json:"rules"`
	RulesActive int            `json:"rules_active"`
	Enabled     int            `json:"rules_enabled"`
	Mapped      int            `json:"mapped"`
	MappingsTot int            `json:"mappings_total"`
	Failures    int            `json:"failures"`
	Conflicts   int            `json:"conflicts"`
	PortsOpen   int            `json:"ports_open"`
	GoneSources int            `json:"gone_sources"`
	Hosts       int            `json:"hosts"`
	Gateways    int            `json:"gateways"`
	ScansToday  int            `json:"scans_today"`
	Schedules   int            `json:"schedules"`
	ByProvider  map[string]int `json:"by_provider"`
	ByProtocol  map[string]int `json:"by_protocol"`
	ByStatus    map[string]int `json:"by_status"`
	ByTag       map[string]int `json:"by_tag"`
	RecentScans []ScanRun      `json:"recent_scans"`
	UpdatedAt   Time           `json:"updated_at"`
}
