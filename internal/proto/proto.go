// Package proto implements the NAT traversal / port mapping protocols:
//
//   - UPnP IGD v2 (WANIPConnection:2, AddAnyPortMapping + DeletePortMapping)
//   - UPnP IGD v1 (WANIPConnection:1 / WANPPPConnection:1)
//   - PCP      (RFC 6887, including the MAP opcode)
//   - NAT-PMP  (RFC 6886)
//   - UPnP     (SSDP discovery + device description parsing)
//
// New protocols are preferred; the legacy ones stay available for compatibility
// and are only used when a newer one is unavailable or fails.
package proto

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Protocol kinds.
const (
	KindIGD2   = "igd2"
	KindIGD1   = "igd1"
	KindPCP    = "pcp"
	KindNATPMP = "natpmp"
)

// Transport protocols.
const (
	TCP = "tcp"
	UDP = "udp"
)

// ErrNotSupported is returned when a gateway cannot serve a request.
var ErrNotSupported = errors.New("operation not supported by gateway")

// ErrNoGateway is returned when no usable gateway was discovered.
var ErrNoGateway = errors.New("no NAT gateway available")

// Mapping is a port mapping request or observation. An empty InternalHost means
// "this host", the gateway substitutes the requester address.
type Mapping struct {
	Protocol     string `json:"protocol"`
	InternalHost string `json:"internal_host,omitempty"`
	InternalPort int    `json:"internal_port"`
	ExternalPort int    `json:"external_port,omitempty"`
	ExternalIP   string `json:"external_ip,omitempty"`
	LeaseSeconds int    `json:"lease_seconds,omitempty"`
	Description  string `json:"description,omitempty"`
	RemoteHost   string `json:"remote_host,omitempty"` // PCP/IGD remote host restriction
	Enabled      bool   `json:"enabled"`
}

// Result is the outcome of a successful mapping operation.
type Result struct {
	ExternalIP    string    `json:"external_ip"`
	ExternalPort  int       `json:"external_port"`
	InternalHost  string    `json:"internal_host"`
	InternalPort  int       `json:"internal_port"`
	Protocol      string    `json:"protocol"`
	LeaseSeconds  int       `json:"lease_seconds"`
	GatewayKind   string    `json:"gateway_kind"`
	GatewayName   string    `json:"gateway_name"`
	AppliedAt     time.Time `json:"applied_at"`
	RawStatusCode int       `json:"raw_status_code,omitempty"`
	Detail        string    `json:"detail,omitempty"`
}

// ExpiresAt returns the expiry of the lease (zero when infinite).
func (r Result) ExpiresAt() time.Time {
	if r.LeaseSeconds <= 0 {
		return time.Time{}
	}
	return r.AppliedAt.Add(time.Duration(r.LeaseSeconds) * time.Second)
}

// GatewayInfo describes a discovered NAT gateway and its capabilities.
type GatewayInfo struct {
	Kind         string    `json:"kind"`
	Name         string    `json:"name"`
	Address      string    `json:"address"`
	Port         int       `json:"port"`
	Location     string    `json:"location,omitempty"`
	ControlURL   string    `json:"control_url,omitempty"`
	ServiceType  string    `json:"service_type,omitempty"`
	ExternalIP   string    `json:"external_ip,omitempty"`
	ModelName    string    `json:"model_name,omitempty"`
	Manufacturer string    `json:"manufacturer,omitempty"`
	UDN          string    `json:"udn,omitempty"`
	Capabilities []string  `json:"capabilities"`
	Interfaces   []string  `json:"interfaces,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
	Provider     Provider  `json:"-"`
}

// Provider is implemented by every protocol backend.
type Provider interface {
	// Info returns the gateway description.
	Info() GatewayInfo
	// ExternalIP reports the WAN address as known by the gateway.
	ExternalIP(ctx context.Context) (string, error)
	// AddMapping creates or refreshes a mapping.
	AddMapping(ctx context.Context, m Mapping) (Result, error)
	// DeleteMapping removes a mapping.
	DeleteMapping(ctx context.Context, m Mapping) error
	// ListMappings enumerates existing mappings where the protocol allows it.
	ListMappings(ctx context.Context) ([]Mapping, error)
	// Close releases protocol resources.
	Close() error
}

// NormalizeProto maps a user supplied protocol string to tcp/udp.
func NormalizeProto(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "udp", "17":
		return UDP
	case "tcp", "6", "":
		return TCP
	default:
		return strings.ToLower(strings.TrimSpace(p))
	}
}

// FormatMapping renders a mapping for logs.
func FormatMapping(m Mapping) string {
	host := m.InternalHost
	if host == "" {
		host = "self"
	}
	return fmt.Sprintf("%s %s:%d -> :%d (%ds)",
		strings.ToUpper(m.Protocol), host, m.InternalPort, m.ExternalPort, m.LeaseSeconds)
}
