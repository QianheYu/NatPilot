package proto

import (
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// svcWANIPv6FirewallControl1 is the IGDv2 service that opens inbound IPv6
// pinholes. IPv6 has no NAT, so there is no mapping to create: the gateway
// instead opens a stateful firewall hole (a "pinhole") for one internal
// IPv6 address/port/protocol tuple.
const svcWANIPv6FirewallControlPrefix = "urn:schemas-upnp-org:service:WANIPv6FirewallControl:"

const svcWANIPv6FirewallControl1 = svcWANIPv6FirewallControlPrefix + "1"

// CapIPv6Pinhole is advertised by gateways whose device description includes
// WANIPv6FirewallControl:1. It says the service exists, not that the firewall
// currently permits pinholing; AddPinhole verifies that through
// GetFirewallStatus before it sends a request the gateway is bound to reject.
const CapIPv6Pinhole = "ipv6-pinhole"

// IPv6Pinhole is one inbound IPv6 firewall pinhole request or observation.
//
// RemoteHost/RemotePort restrict the source; leaving both empty asks for any
// source (the wildcard some gateways refuse, see UPnP error 708).
// InternalClient is an IPv6 literal without brackets, as required by the
// WANIPv6FirewallControl:1 arguments.
type IPv6Pinhole struct {
	RemoteHost     string `json:"remote_host,omitempty"`
	RemotePort     uint16 `json:"remote_port,omitempty"`
	InternalClient string `json:"internal_client"`
	InternalPort   uint16 `json:"internal_port"`
	Protocol       string `json:"protocol"` // tcp|udp
	LeaseSeconds   uint32 `json:"lease_seconds,omitempty"`
	UniqueID       uint16 `json:"unique_id,omitempty"`
}

// ProtocolNumber maps the pinhole protocol to the WANIPv6FirewallControl
// numeric value (6 = TCP, 17 = UDP).
func (p IPv6Pinhole) ProtocolNumber() (uint16, error) {
	switch NormalizeProto(p.Protocol) {
	case TCP:
		return 6, nil
	case UDP:
		return 17, nil
	default:
		return 0, fmt.Errorf("igd ipv6: unsupported pinhole protocol %q (need tcp or udp)", p.Protocol)
	}
}

// IPv6FirewallCapability is the cached answer of GetFirewallStatus plus what the
// device description told us.
//
// Advertised is true whenever the WANIPv6FirewallControl:1 service was found.
// StatusKnown is true when GetFirewallStatus returned Parseable values; only
// then do FirewallEnabled/InboundPinholeAllowed mean anything.
type IPv6FirewallCapability struct {
	Advertised             bool      `json:"advertised"`
	StatusKnown            bool      `json:"status_known"`
	FirewallEnabled        bool      `json:"firewall_enabled"`
	InboundPinholeAllowed  bool      `json:"inbound_pinhole_allowed"`
	OutboundPinholeAllowed bool      `json:"outbound_pinhole_allowed,omitempty"`
	ControlURL             string    `json:"control_url,omitempty"`
	CheckedAt              time.Time `json:"checked_at,omitempty"`
}

// PinholesSupported reports whether the gateway can open inbound pinholes right
// now. An unknown status (GetFirewallStatus failed or is not implemented) does
// not block the attempt: several firmwares implement AddPinhole but answer
// GetFirewallStatus poorly.
func (c IPv6FirewallCapability) PinholesSupported() bool {
	return c.Advertised && c.StatusKnown && c.FirewallEnabled && c.InboundPinholeAllowed
}

// IPv6FirewallProvider is implemented by protocols that can open IPv6 firewall
// pinholes. It is deliberately separate from Provider: PCP/NAT-PMP carry only
// IPv4 NAT semantics, and this capability is optional.
type IPv6FirewallProvider interface {
	// FirewallStatus returns GetFirewallStatus, cached after the first call.
	FirewallStatus(ctx context.Context) (IPv6FirewallCapability, error)
	// AddPinhole opens a pinhole and returns its id and the granted lease.
	AddPinhole(ctx context.Context, req IPv6Pinhole) (uniqueID uint16, leaseSeconds uint32, err error)
	// DeletePinhole closes a pinhole.
	DeletePinhole(ctx context.Context, uniqueID uint16) error
	// GetPinhole reads a pinhole back (best effort).
	GetPinhole(ctx context.Context, uniqueID uint16) (IPv6Pinhole, error)
}

// firewallStatusRetryTTL bounds how often a failed GetFirewallStatus is retried.
// Successful answers are cached for the lifetime of the client.
const firewallStatusRetryTTL = 30 * time.Second

// ipv6State carries the per-client IPv6 firewall endpoint and its cached status.
type ipv6State struct {
	controlURL  string
	serviceType string

	mu        sync.Mutex
	caps      IPv6FirewallCapability
	checkedAt time.Time
	lastErr   error
	lastErrAt time.Time
}

// IPv6FirewallControlURL returns the control URL of the discovered service.
func (c *IGDClient) IPv6FirewallControlURL() string {
	if c.v6 == nil {
		return ""
	}
	return c.v6.controlURL
}

// SupportsIPv6Pinhole reports whether the device description advertised
// WANIPv6FirewallControl:1. It does not probe the firewall state.
func (i GatewayInfo) SupportsIPv6Pinhole() bool {
	for _, c := range i.Capabilities {
		if c == CapIPv6Pinhole {
			return true
		}
	}
	return false
}

// FirewallStatus queries GetFirewallStatus and caches the result on the client.
//
// The first successful call is cached forever (the firewall state is expected to
// change far less often than the reconcile loop runs, and every AddPinhole would
// otherwise cost an extra round trip). Failures are cached for
// firewallStatusRetryTTL so a transient error does not wedge the client.
func (c *IGDClient) FirewallStatus(ctx context.Context) (IPv6FirewallCapability, error) {
	st := c.v6
	if st == nil || st.controlURL == "" {
		return IPv6FirewallCapability{}, fmt.Errorf("igd %s: IPv6 pinholes need %s: %w",
			c.info.Name, svcWANIPv6FirewallControl1, ErrNotSupported)
	}
	st.mu.Lock()
	if !st.checkedAt.IsZero() {
		caps := st.caps
		st.mu.Unlock()
		return caps, nil
	}
	if st.lastErr != nil && time.Since(st.lastErrAt) < firewallStatusRetryTTL {
		err := st.lastErr
		caps := st.caps
		st.mu.Unlock()
		return caps, err
	}
	st.mu.Unlock()

	caps, err := c.fetchFirewallStatus(ctx)
	st.mu.Lock()
	if err != nil {
		st.caps = caps
		st.lastErr = err
		st.lastErrAt = time.Now()
	} else {
		caps.CheckedAt = time.Now()
		st.caps = caps
		st.checkedAt = caps.CheckedAt
		st.lastErr = nil
	}
	st.mu.Unlock()
	return caps, err
}

// GetFirewallStatus is the UPnP action name for FirewallStatus, kept as an
// explicit alias so callers can use the spec vocabulary.
func (c *IGDClient) GetFirewallStatus(ctx context.Context) (IPv6FirewallCapability, error) {
	return c.FirewallStatus(ctx)
}

// fetchFirewallStatus performs the actual GetFirewallStatus SOAP call.
func (c *IGDClient) fetchFirewallStatus(ctx context.Context) (IPv6FirewallCapability, error) {
	caps := IPv6FirewallCapability{Advertised: true, ControlURL: c.v6.controlURL}
	body, err := c.soapActionAt(ctx, c.v6.controlURL, c.v6.serviceType, "GetFirewallStatus", nil)
	if err != nil {
		return caps, err
	}
	vals := soapElementMap(body)
	fw, fwOK := soapBoolValue(vals, "NewFirewallEnabled", "FirewallEnabled")
	in, inOK := soapBoolValue(vals, "NewInboundPinholeAllowed", "InboundPinholeAllowed")
	out, _ := soapBoolValue(vals, "NewOutboundPinholeAllowed", "OutboundPinholeAllowed")
	caps.FirewallEnabled = fw
	caps.InboundPinholeAllowed = in
	caps.OutboundPinholeAllowed = out
	caps.StatusKnown = fwOK && inOK
	if !caps.StatusKnown {
		return caps, fmt.Errorf("igd ipv6: GetFirewallStatus response lacks NewFirewallEnabled/NewInboundPinholeAllowed: %s",
			truncate(strings.TrimSpace(body), 200))
	}
	return caps, nil
}

// AddPinhole opens an inbound IPv6 pinhole through WANIPv6FirewallControl:1.
//
// It returns the gateway-assigned UniqueID and the granted lease. When the
// response omits the lease (the service template only mandates UniqueID), the
// requested LeaseTime is reported instead of inventing one.
func (c *IGDClient) AddPinhole(ctx context.Context, req IPv6Pinhole) (uint16, uint32, error) {
	st := c.v6
	if st == nil || st.controlURL == "" {
		return 0, 0, fmt.Errorf("igd %s: IPv6 pinholes need %s: %w",
			c.info.Name, svcWANIPv6FirewallControl1, ErrNotSupported)
	}
	if err := validatePinhole(req); err != nil {
		return 0, 0, err
	}
	protoNum, err := req.ProtocolNumber()
	if err != nil {
		return 0, 0, err
	}
	// Best effort: a definitive "firewall disabled"/"inbound not allowed" answer
	// must not be sent to the gateway as a request that is guaranteed to fail.
	if caps, cerr := c.FirewallStatus(ctx); cerr == nil && caps.StatusKnown && !caps.PinholesSupported() {
		return 0, 0, pinholeDeniedError(c.info.Name, caps)
	}

	args := [][2]string{
		{"RemoteHost", req.RemoteHost},
		{"RemotePort", strconv.FormatUint(uint64(req.RemotePort), 10)},
		{"InternalClient", req.InternalClient},
		{"InternalPort", strconv.FormatUint(uint64(req.InternalPort), 10)},
		{"Protocol", strconv.FormatUint(uint64(protoNum), 10)},
		{"LeaseTime", strconv.FormatUint(uint64(req.LeaseSeconds), 10)},
	}
	body, err := c.soapActionAt(ctx, st.controlURL, st.serviceType, "AddPinhole", args)
	if err != nil {
		return 0, 0, err
	}
	vals := soapElementMap(body)
	idText := firstValue(vals, "NewUniqueID", "UniqueID")
	if idText == "" {
		return 0, 0, fmt.Errorf("igd ipv6: AddPinhole response has no UniqueID: %s",
			truncate(strings.TrimSpace(body), 200))
	}
	id, err := strconv.ParseUint(strings.TrimSpace(idText), 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("igd ipv6: invalid UniqueID %q: %w", idText, err)
	}
	granted := req.LeaseSeconds
	if lt := firstValue(vals, "NewLeaseTime", "LeaseTime"); lt != "" {
		if v, perr := strconv.ParseUint(strings.TrimSpace(lt), 10, 32); perr == nil {
			granted = uint32(v)
		}
	}
	return uint16(id), granted, nil
}

// DeletePinhole closes a pinhole by id.
func (c *IGDClient) DeletePinhole(ctx context.Context, uniqueID uint16) error {
	st := c.v6
	if st == nil || st.controlURL == "" {
		return fmt.Errorf("igd %s: IPv6 pinholes need %s: %w",
			c.info.Name, svcWANIPv6FirewallControl1, ErrNotSupported)
	}
	_, err := c.soapActionAt(ctx, st.controlURL, st.serviceType, "DeletePinhole", [][2]string{
		{"UniqueID", strconv.FormatUint(uint64(uniqueID), 10)},
	})
	return err
}

// GetPinhole reads a pinhole back. It is best effort: firmwares that implement
// only AddPinhole/DeletePinhole return an error here, which callers may ignore.
func (c *IGDClient) GetPinhole(ctx context.Context, uniqueID uint16) (IPv6Pinhole, error) {
	st := c.v6
	if st == nil || st.controlURL == "" {
		return IPv6Pinhole{}, fmt.Errorf("igd %s: IPv6 pinholes need %s: %w",
			c.info.Name, svcWANIPv6FirewallControl1, ErrNotSupported)
	}
	body, err := c.soapActionAt(ctx, st.controlURL, st.serviceType, "GetPinhole", [][2]string{
		{"UniqueID", strconv.FormatUint(uint64(uniqueID), 10)},
	})
	if err != nil {
		return IPv6Pinhole{}, err
	}
	vals := soapElementMap(body)
	p := IPv6Pinhole{
		UniqueID:       uniqueID,
		RemoteHost:     firstValue(vals, "NewRemoteHost", "RemoteHost"),
		InternalClient: firstValue(vals, "NewInternalClient", "InternalClient"),
		Protocol:       pinholeProtocolName(firstValue(vals, "NewProtocol", "Protocol")),
	}
	p.RemotePort = uint16(atoiSafe(firstValue(vals, "NewRemotePort", "RemotePort")))
	p.InternalPort = uint16(atoiSafe(firstValue(vals, "NewInternalPort", "InternalPort")))
	p.LeaseSeconds = uint32(atoiSafe(firstValue(vals, "NewLeaseTime", "LeaseTime")))
	return p, nil
}

// validatePinhole rejects requests that the service template forbids, with a
// local error instead of a gateway round trip and an opaque UPnP fault.
func validatePinhole(req IPv6Pinhole) error {
	host := strings.TrimSpace(req.InternalClient)
	if host == "" {
		return fmt.Errorf("igd ipv6: InternalClient is required")
	}
	if strings.ContainsAny(host, "[]") {
		return fmt.Errorf("igd ipv6: InternalClient %q must be a bare IPv6 literal without brackets", req.InternalClient)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() != nil {
		return fmt.Errorf("igd ipv6: InternalClient %q is not an IPv6 address", req.InternalClient)
	}
	if req.InternalPort == 0 {
		return fmt.Errorf("igd ipv6: InternalPort must not be 0 (wildcarding is not allowed)")
	}
	if _, err := req.ProtocolNumber(); err != nil {
		return err
	}
	return nil
}

// pinholeDeniedError explains a gateway that advertises the service but has the
// firewall or inbound pinholing switched off.
func pinholeDeniedError(gateway string, caps IPv6FirewallCapability) error {
	switch {
	case !caps.FirewallEnabled:
		return fmt.Errorf("igd %s: 网关的 IPv6 防火墙处于关闭状态（GetFirewallStatus: FirewallEnabled=false）；"+
			"请在路由器管理界面启用 IPv6 防火墙后重试", gateway)
	case !caps.InboundPinholeAllowed:
		return fmt.Errorf("igd %s: 网关禁止入站 IPv6 pinhole（GetFirewallStatus: InboundPinholeAllowed=false）；"+
			"请在路由器 IPv6 防火墙设置中允许入站连接，或手动添加 pinhole 规则", gateway)
	default:
		return fmt.Errorf("igd %s: 网关未启用 IPv6 pinhole 能力", gateway)
	}
}

// pinholeProtocolName converts the numeric UPnP protocol value back to a name.
func pinholeProtocolName(v string) string {
	switch strings.TrimSpace(v) {
	case "6":
		return TCP
	case "17":
		return UDP
	default:
		return NormalizeProto(v)
	}
}

// soapElementMap returns local element name -> trimmed text for every leaf in a
// SOAP body. Local names are used so an envelope that prefixes elements
// (<u:NewUniqueID>) parses the same as a bare one, and the first occurrence wins.
func soapElementMap(body string) map[string]string {
	out := map[string]string{}
	dec := xml.NewDecoder(strings.NewReader(body))
	cur := ""
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			cur = t.Name.Local
			buf.Reset()
		case xml.CharData:
			if cur != "" {
				buf.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == cur {
				if _, seen := out[cur]; !seen {
					out[cur] = strings.TrimSpace(buf.String())
				}
				cur = ""
			}
		}
	}
	return out
}

// soapBoolValue reads the first present boolean element. UPnP uses "1"/"0"
// (some firmwares also answer "true"/"false").
func soapBoolValue(vals map[string]string, names ...string) (value, present bool) {
	for _, n := range names {
		v, ok := vals[n]
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes":
			return true, true
		case "0", "false", "no", "":
			return false, true
		default:
			return false, true
		}
	}
	return false, false
}

// firstValue returns the first non-empty value among names, so an empty
// <NewUniqueID/> does not hide a populated <UniqueID>.
func firstValue(vals map[string]string, names ...string) string {
	for _, n := range names {
		if v := vals[n]; v != "" {
			return v
		}
	}
	return ""
}
