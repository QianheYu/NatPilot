package proto

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
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
//	44-59: external address (16 bytes, IPv4-mapped form)
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

// PCPClient implements the Port Control Protocol.
type PCPClient struct {
	info       GatewayInfo
	gatewayIP  string
	localIP    string
	timeout    time.Duration
	nonce      []byte
	externalIP string
	epoch      uint32
}

// NewPCPClient builds a PCP client for the gateway at gatewayIP.
func NewPCPClient(gatewayIP, localIP string, timeout time.Duration) (*PCPClient, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	nonce, err := randomNonce()
	if err != nil {
		return nil, err
	}
	return &PCPClient{
		gatewayIP: gatewayIP,
		localIP:   localIP,
		timeout:   timeout,
		nonce:     nonce,
		info: GatewayInfo{
			Kind:         KindPCP,
			Name:         "PCP server",
			Address:      gatewayIP,
			Port:         pcpPort,
			Capabilities: []string{"map-tcp", "map-udp", "external-ip", "delete", "pcp-map"},
		},
	}, nil
}

// Info returns gateway details.
func (c *PCPClient) Info() GatewayInfo { return c.info }

// Close releases resources.
func (c *PCPClient) Close() error { return nil }

// clientAddr16 renders the requester address in 16 byte PCP form.
func (c *PCPClient) clientAddr16() []byte {
	out := make([]byte, 16)
	ip := net.ParseIP(c.localIP)
	if ip == nil {
		return out
	}
	if v4 := ip.To4(); v4 != nil {
		out[10], out[11] = 0xff, 0xff // IPv4-mapped IPv6 ::ffff:a.b.c.d
		copy(out[12:16], v4)
		return out
	}
	copy(out, ip.To16())
	return out
}

func v4To16(s string) []byte {
	out := make([]byte, 16)
	ip := net.ParseIP(s)
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

// exchange sends a PCP packet and returns the reply.
func (c *PCPClient) exchange(ctx context.Context, pkt []byte) ([]byte, error) {
	raddr := &net.UDPAddr{IP: net.ParseIP(c.gatewayIP), Port: pcpPort}
	conn, err := net.DialUDP("udp", nil, raddr)
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

// buildMapPacket constructs a MAP request.
func (c *PCPClient) buildMapPacket(lifetime uint32, proto string, internalPort, externalPort int, extIP string) []byte {
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
		copy(pkt[44:60], v4To16(extIP))
	}
	return pkt
}

// parseMapReply extracts the external address/ports and lifetime from a MAP
// reply.
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
	ip16 := net.IP(reply[44:60])
	if v4 := ip16.To4(); v4 != nil && !v4.IsUnspecified() {
		return v4.String(), extPort, lifetime, nil
	}
	return "", extPort, lifetime, nil
}

// AddMapping creates a PCP MAP mapping.
func (c *PCPClient) AddMapping(ctx context.Context, m Mapping) (Result, error) {
	proto := NormalizeProto(m.Protocol)
	if proto != TCP && proto != UDP {
		return Result{}, fmt.Errorf("pcp: unsupported protocol %q", m.Protocol)
	}
	lifetime := uint32(m.LeaseSeconds)
	if lifetime == 0 {
		lifetime = 7200
	}
	pkt := c.buildMapPacket(lifetime, proto, m.InternalPort, m.ExternalPort, m.ExternalIP)
	reply, err := c.exchange(ctx, pkt)
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
	return Result{
		ExternalIP:    extIP,
		ExternalPort:  extPort,
		InternalHost:  c.localIP,
		InternalPort:  assignedInternal,
		Protocol:      proto,
		LeaseSeconds:  gotLease,
		GatewayKind:   KindPCP,
		GatewayName:   c.info.Name,
		AppliedAt:     time.Now(),
		RawStatusCode: int(reply[3]),
	}, nil
}

// DeleteMapping removes a PCP mapping by requesting lifetime 0.
func (c *PCPClient) DeleteMapping(ctx context.Context, m Mapping) error {
	proto := NormalizeProto(m.Protocol)
	ext := m.ExternalPort
	if ext == 0 {
		ext = m.InternalPort
	}
	pkt := c.buildMapPacket(0, proto, m.InternalPort, ext, "")
	reply, err := c.exchange(ctx, pkt)
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
