package proto

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

// PCP (RFC 6887) constants and packet layout.
//
// Common header (24 bytes):
//
//	0: version (=2)
//	1: R(1) | opcode(7)
//	2: reserved
//	3: result code (0 in requests)
//	4-7: requested lifetime
//	8-11: reserved (epoch in responses)
//	12-23: reserved
//
// MAP opcode data (36 bytes):
//
//	24-35: mapping nonce (12)
//	36: protocol (6=TCP, 17=UDP)
//	37-39: reserved
//	40-41: internal port
//	42-43: suggested/assigned external port
//	44-59: suggested/assigned external address (16 bytes)
//
// MAP carries no internal-address field (RFC 6887 §11.2): the internal address
// is the source address of the PCP request itself. The 16-byte field at 44-59
// is the *external* address, and it is family specific: an IPv4 address is
// encoded in the IPv4-mapped form (::ffff:a.b.c.d), an IPv6 address occupies
// all 16 bytes with no ::ffff: prefix. That is why every packet builder and
// reply parser here takes the mapping's address family into account.
const (
	pcpPort           = 5351
	pcpVersion        = 2
	pcpOpcodeAnnounce = 0
	pcpOpcodeMap      = 1
	pcpResponseFlag   = 0x80

	pcpResultSuccess               = 0
	pcpResultUnsupportedVersion    = 1
	pcpResultNotAuthorized         = 2
	pcpResultMalformedRequest      = 3
	pcpResultUnsupportedOpcode     = 4
	pcpResultExcessiveRemotePeers  = 6
	pcpResultAddressMismatch       = 7
	pcpResultNoResources           = 8
	pcpResultUnsupportedProtocol   = 9
	pcpResultUserQuotaExceeded     = 10
	pcpResultCannotProvideExtAddr  = 11
	pcpResultAddressFamilyMismatch = 12

	pcpHeaderLen = 24
	pcpMapLen    = 60
)

var pcpResultNames = map[byte]string{
	0:  "success",
	1:  "unsupported version",
	2:  "not authorized",
	3:  "malformed request",
	4:  "unsupported opcode",
	5:  "not authorized",
	6:  "excessive remote peers",
	7:  "address mismatch",
	8:  "no resources",
	9:  "unsupported protocol",
	10: "user quota exceeded",
	11: "cannot provide external address",
	12: "address family mismatch",
}

// CapPCPIPv6 is advertised by a PCP client that can carry an IPv6 MAP: its PCP
// transport (the client address or the server address) is IPv6, so the request
// source address — the PCP "internal address" — can be the host's IPv6 address.
// A PCP server reached over IPv4 only cannot vouch for an IPv6 client address,
// so it does not advertise this capability.
const CapPCPIPv6 = "pcp-ipv6"

// PCPClient implements the Port Control Protocol.
type PCPClient struct {
	info       GatewayInfo
	gatewayIP  string
	localIP    string
	timeout    time.Duration
	nonce      []byte
	externalIP string
	epoch      uint32
	// port is the PCP server port; a field so tests can point the client at a
	// local UDP fixture instead of the well-known 5351.
	port int
}

// NewPCPClient builds a PCP client for the gateway at gatewayIP.
//
// localIP is this host's address on the router's LAN. It decides the address
// family of the client address (the PCP "internal address"): IPv4 stays in the
// historical IPv4-mapped form, IPv6 is carried as a plain 16-byte address. When
// either end of the PCP transport is IPv6 the client advertises CapPCPIPv6, so
// callers can route IPv6 mappings to it.
func NewPCPClient(gatewayIP, localIP string, timeout time.Duration) (*PCPClient, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	nonce, err := randomNonce()
	if err != nil {
		return nil, err
	}
	caps := []string{"map-tcp", "map-udp", "external-ip", "delete", "pcp-map"}
	if isIPv6Address(localIP) || isIPv6Address(gatewayIP) {
		caps = append(caps, CapPCPIPv6)
	}
	return &PCPClient{
		gatewayIP: gatewayIP,
		localIP:   localIP,
		timeout:   timeout,
		nonce:     nonce,
		port:      pcpPort,
		info: GatewayInfo{
			Kind:         KindPCP,
			Name:         "PCP server",
			Address:      gatewayIP,
			Port:         pcpPort,
			Capabilities: caps,
		},
	}, nil
}

// Info returns gateway details.
func (c *PCPClient) Info() GatewayInfo { return c.info }

// Close releases resources.
func (c *PCPClient) Close() error { return nil }

// SupportsPCPIPv6 reports whether a discovered gateway can carry an IPv6 MAP
// (PCP reachable over IPv6; see CapPCPIPv6).
func (i GatewayInfo) SupportsPCPIPv6() bool {
	for _, c := range i.Capabilities {
		if c == CapPCPIPv6 {
			return true
		}
	}
	return false
}

// isIPv6Address reports whether s is an IPv6 literal (IPv4-mapped addresses
// count as IPv4).
func isIPv6Address(s string) bool {
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(s), "[]"))
	return ip != nil && ip.To4() == nil && ip.To16() != nil
}

// clientAddr16 renders the requester address in 16 byte PCP form.
func (c *PCPClient) clientAddr16() []byte {
	return addr16(c.localIP)
}

// clientAddrFor picks the address to put in the MAP client role: a concrete
// IPv6 internal host wins (that is the address the mapping is for), everything
// else keeps the historical behaviour of using this host's local address.
// It returns the canonical address and whether it is IPv6.
func (c *PCPClient) clientAddrFor(internalHost string) (string, bool) {
	host := strings.TrimSpace(strings.Trim(strings.TrimSpace(internalHost), "[]"))
	if isIPv6Address(host) {
		return net.ParseIP(host).To16().String(), true
	}
	return c.localIP, isIPv6Address(c.localIP)
}

// addr16 renders any IPv4 or IPv6 literal in the PCP 16-byte address form.
//
// IPv4 uses the IPv4-mapped form (::ffff:a.b.c.d) exactly as before; IPv6 is
// copied verbatim, so bytes 10-11 are not forced to 0xff (the bug a hardcoded
// prefix would cause). The zero value is returned for unparseable input.
func addr16(s string) []byte {
	out := make([]byte, 16)
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(s), "[]"))
	if ip == nil {
		return out
	}
	if v4 := ip.To4(); v4 != nil {
		out[10], out[11] = 0xff, 0xff
		copy(out[12:16], v4)
		return out
	}
	copy(out, ip.To16())
	return out
}

// v4To16 is kept as the historical spelling used by the v4 packet tests; it now
// renders either family correctly.
func v4To16(s string) []byte { return addr16(s) }

// exchange sends a PCP packet and returns the reply.
func (c *PCPClient) exchange(ctx context.Context, pkt []byte) ([]byte, error) {
	return c.exchangeFrom(ctx, pkt, "")
}

// exchangeFrom sends a PCP packet, optionally sourcing it from localIP.
//
// The source address is the PCP "internal address" (MAP has no field for it),
// so an IPv6 mapping needs an IPv6 socket. The socket is pinned to localIP only
// when the server is IPv6 too; a v4-only PCP server cannot carry an IPv6 client
// address, and the packet is then sent over the historical v4 path unchanged.
func (c *PCPClient) exchangeFrom(ctx context.Context, pkt []byte, localIP string) ([]byte, error) {
	gw := net.ParseIP(c.gatewayIP)
	raddr := &net.UDPAddr{IP: gw, Port: c.port}
	network := "udp"
	var laddr *net.UDPAddr
	if gw != nil && gw.To4() == nil {
		network = "udp6"
		if isIPv6Address(localIP) {
			laddr = &net.UDPAddr{IP: net.ParseIP(localIP)}
		}
	}
	conn, err := net.DialUDP(network, laddr, raddr)
	if err != nil {
		return nil, fmt.Errorf("pcp dial: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(c.timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	// Retransmit a couple of times, but never extend the original deadline: a
	// dead candidate must cost one probe budget, not a multiple of it. During
	// discovery several candidates are probed in parallel, so stretching this
	// loop is what used to make discovery take minutes.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if time.Until(deadline) <= 20*time.Millisecond {
			break
		}
		if _, err := conn.Write(pkt); err != nil {
			lastErr = err
			continue
		}
		buf := make([]byte, 1500)
		n, err := conn.Read(buf)
		if err == nil && n >= pcpHeaderLen {
			return buf[:n], nil
		}
		lastErr = err
		if lastErr == nil {
			lastErr = fmt.Errorf("pcp: short reply (%d bytes)", n)
		}
		_ = conn.SetDeadline(deadline)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no response from PCP server %s", c.gatewayIP)
	}
	return nil, lastErr
}

// Announce sends an ANNOUNCE to confirm a PCP server is listening and to learn
// the current epoch.
func (c *PCPClient) Announce(ctx context.Context) error {
	pkt := make([]byte, pcpHeaderLen)
	pkt[0] = pcpVersion
	pkt[1] = pcpOpcodeAnnounce
	reply, err := c.exchange(ctx, pkt)
	if err != nil {
		return err
	}
	if reply[0] != pcpVersion && reply[0] != 0 {
		return fmt.Errorf("pcp: unsupported version %d", reply[0])
	}
	if reply[1]&0x7f != pcpOpcodeAnnounce {
		return fmt.Errorf("pcp: unexpected opcode %d in announce reply", reply[1]&0x7f)
	}
	if reply[3] != pcpResultSuccess {
		return pcpError(reply[3])
	}
	c.epoch = binary.BigEndian.Uint32(reply[8:12])
	return nil
}

// ExternalIP learns the external address with a zero-lifetime MAP request
// (RFC 6887 §11.1).
func (c *PCPClient) ExternalIP(ctx context.Context) (string, error) {
	// Some servers reject lifetime 0; try a short 60s mapping as a fallback.
	for _, lifetime := range []uint32{0, 60} {
		pkt := c.buildMapPacket(lifetime, TCP, 0, 0, "")
		reply, err := c.exchange(ctx, pkt)
		if err != nil {
			continue
		}
		ip, _, _, rerr := parseMapReply(reply)
		if rerr != nil {
			continue
		}
		if ip != "" {
			c.externalIP = ip
			c.info.ExternalIP = ip
			// A fallback mapping created here is deliberately released.
			if lifetime > 0 {
				del := c.buildMapPacket(0, TCP, 0, 0, "")
				_, _ = c.exchange(ctx, del)
			}
			return ip, nil
		}
	}
	return "", fmt.Errorf("pcp: gateway did not report an external address")
}

// buildMapPacket constructs a MAP request for this host's address (historical
// signature, IPv4 callers unchanged).
func (c *PCPClient) buildMapPacket(lifetime uint32, proto string, internalPort, externalPort int, extIP string) []byte {
	return c.buildMapPacketFor(lifetime, proto, internalPort, externalPort, "", extIP)
}

// buildMapPacketFor constructs a MAP request. internalHost selects the address
// family of the mapping (an IPv6 literal makes it a v6 mapping); extIP, when
// non-empty, is the suggested external address and is encoded in that same
// family's 16-byte form.
func (c *PCPClient) buildMapPacketFor(lifetime uint32, proto string, internalPort, externalPort int, internalHost, extIP string) []byte {
	pkt := make([]byte, pcpMapLen)
	pkt[0] = pcpVersion
	pkt[1] = pcpOpcodeMap
	binary.BigEndian.PutUint32(pkt[4:8], lifetime)
	copy(pkt[24:36], c.nonce)
	pkt[36] = 6 // TCP
	if NormalizeProto(proto) == UDP {
		pkt[36] = 17
	}
	binary.BigEndian.PutUint16(pkt[40:42], uint16(internalPort))
	binary.BigEndian.PutUint16(pkt[42:44], uint16(externalPort))
	if extIP != "" {
		// 44-59 is the suggested *external* address; a v6 mapping carries a raw
		// 16-byte v6 address here, a v4 mapping the IPv4-mapped form.
		copy(pkt[44:60], addr16(extIP))
	}
	_ = internalHost // the internal address is the packet source, not a field
	return pkt
}

// parseMapReply extracts the external address/ports and lifetime from a MAP
// reply. The address may be IPv4-mapped or plain IPv6; an all-zero field means
// "the server did not report one".
func parseMapReply(reply []byte) (extIP string, extPort, lifetime int, err error) {
	if len(reply) < pcpMapLen {
		return "", 0, 0, fmt.Errorf("pcp: short map reply (%d bytes)", len(reply))
	}
	if reply[1]&0x7f != pcpOpcodeMap {
		return "", 0, 0, fmt.Errorf("pcp: unexpected reply opcode %d", reply[1]&0x7f)
	}
	if code := reply[3]; code != pcpResultSuccess {
		return "", 0, 0, pcpError(code)
	}
	lifetime = int(binary.BigEndian.Uint32(reply[4:8]))
	extPort = int(binary.BigEndian.Uint16(reply[42:44]))
	return parseMapReplyAddress(reply[44:60]), extPort, lifetime, nil
}

// parseMapReplyAddress decodes the 16-byte external address of a MAP reply.
//
// IPv4 is detected through To4 (the IPv4-mapped form) and rendered as a.b.c.d;
// anything else that is a real IPv6 address is rendered in canonical v6 form.
// The unspecified address (0.0.0.0 or ::) yields an empty string.
func parseMapReplyAddress(raw []byte) string {
	if len(raw) < 16 {
		return ""
	}
	ip := net.IP(append([]byte(nil), raw[:16]...))
	if v4 := ip.To4(); v4 != nil {
		if v4.IsUnspecified() {
			return ""
		}
		return v4.String()
	}
	v6 := ip.To16()
	if v6 == nil || v6.IsUnspecified() {
		return ""
	}
	return v6.String()
}

// AddMapping creates a PCP MAP mapping.
//
// m.InternalHost may be an IPv6 literal: the mapping is then created for that
// address (the client address in the PCP request), the suggested external
// address uses a raw 16-byte v6 form, and the reply's IPv6 external address is
// parsed back. For IPv4 requests every byte of the packet stays exactly as it
// was before IPv6 support was added.
func (c *PCPClient) AddMapping(ctx context.Context, m Mapping) (Result, error) {
	proto := NormalizeProto(m.Protocol)
	if proto != TCP && proto != UDP {
		return Result{}, fmt.Errorf("pcp: unsupported protocol %q", m.Protocol)
	}
	lifetime := uint32(m.LeaseSeconds)
	if lifetime == 0 {
		lifetime = 7200
	}
	internalAddr, isV6 := c.clientAddrFor(m.InternalHost)
	pkt := c.buildMapPacketFor(lifetime, proto, m.InternalPort, m.ExternalPort, internalAddr, m.ExternalIP)
	reply, err := c.exchangeFrom(ctx, pkt, internalAddr)
	if err != nil {
		return Result{}, err
	}
	extIP, extPort, gotLease, err := parseMapReply(reply)
	if err != nil {
		return Result{}, err
	}
	c.epoch = binary.BigEndian.Uint32(reply[8:12])
	assignedInternal := int(binary.BigEndian.Uint16(reply[40:42]))
	if extIP == "" {
		extIP = c.externalIP
	}
	if extPort == 0 {
		extPort = m.ExternalPort
	}
	if extIP != "" {
		c.externalIP = extIP
		c.info.ExternalIP = extIP
	}
	// Report the address the mapping really is for. IPv4 keeps the historical
	// value (this host's address); an IPv6 mapping reports the requested host.
	reportedHost := c.localIP
	if isV6 {
		reportedHost = internalAddr
	}
	return Result{
		ExternalIP:    extIP,
		ExternalPort:  extPort,
		InternalHost:  reportedHost,
		InternalPort:  assignedInternal,
		Protocol:      proto,
		LeaseSeconds:  gotLease,
		GatewayKind:   KindPCP,
		GatewayName:   c.info.Name,
		AppliedAt:     time.Now(),
		RawStatusCode: int(reply[3]),
	}, nil
}

// DeleteMapping removes a PCP mapping by requesting lifetime 0. An IPv6
// mapping is deleted with the same client address family it was created with.
func (c *PCPClient) DeleteMapping(ctx context.Context, m Mapping) error {
	proto := NormalizeProto(m.Protocol)
	ext := m.ExternalPort
	if ext == 0 {
		ext = m.InternalPort
	}
	internalAddr, _ := c.clientAddrFor(m.InternalHost)
	pkt := c.buildMapPacketFor(0, proto, m.InternalPort, ext, internalAddr, "")
	reply, err := c.exchangeFrom(ctx, pkt, internalAddr)
	if err != nil {
		return err
	}
	if len(reply) >= 4 {
		if code := reply[3]; code != pcpResultSuccess {
			return pcpError(code)
		}
	}
	return nil
}

// ListMappings is not supported by PCP (no enumeration opcode).
func (c *PCPClient) ListMappings(ctx context.Context) ([]Mapping, error) {
	return nil, ErrNotSupported
}

func pcpError(code byte) error {
	name, ok := pcpResultNames[code]
	if !ok {
		name = fmt.Sprintf("result code %d", code)
	}
	return fmt.Errorf("pcp: gateway returned %s", name)
}
