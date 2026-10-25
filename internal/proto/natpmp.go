package proto

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// NAT-PMP (RFC 6886) constants.
const (
	natpmpPort       = 5351
	natpmpVersion    = 0
	natpmpOpPublicIP = 0
	natpmpOpMapUDP   = 1
	natpmpOpMapTCP   = 2
	natpmpRespFlag   = 0x80
	natpmpResultOK   = 0
)

var natpmpResultNames = map[uint16]string{
	0: "success",
	1: "unsupported version",
	2: "not authorized / refusing",
	3: "network failure",
	4: "out of resources",
	5: "unsupported opcode",
}

// NATPMPClient implements the NAT Port Mapping Protocol.
type NATPMPClient struct {
	info       GatewayInfo
	gatewayIP  string
	timeout    time.Duration
	localIP    string
	externalIP string
}

// NewNATPMPClient builds a client for the gateway at gatewayIP.
func NewNATPMPClient(gatewayIP, localIP string, timeout time.Duration) *NATPMPClient {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &NATPMPClient{
		gatewayIP: gatewayIP,
		localIP:   localIP,
		timeout:   timeout,
		info: GatewayInfo{
			Kind:         KindNATPMP,
			Name:         "NAT-PMP gateway",
			Address:      gatewayIP,
			Port:         natpmpPort,
			Capabilities: []string{"map-tcp", "map-udp", "external-ip", "delete"},
		},
	}
}

// Info returns gateway details.
func (c *NATPMPClient) Info() GatewayInfo { return c.info }

// Close releases resources (nothing to release for NAT-PMP).
func (c *NATPMPClient) Close() error { return nil }

// request sends a NAT-PMP request of the given opcode and returns the reply.
func (c *NATPMPClient) request(ctx context.Context, opcode byte, internalPort, externalPort int, lifetime uint32) ([]byte, error) {
	raddr := &net.UDPAddr{IP: net.ParseIP(c.gatewayIP), Port: natpmpPort}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, fmt.Errorf("natpmp dial: %w", err)
	}
	defer conn.Close()

	// The public address request is only 2 bytes; mapping requests are 12.
	var req []byte
	if opcode == natpmpOpPublicIP {
		req = make([]byte, 2)
		req[0] = natpmpVersion
		req[1] = natpmpOpPublicIP
	} else {
		req = make([]byte, 12)
		req[0] = natpmpVersion
		req[1] = opcode
		// bytes 2-3 reserved
		binary.BigEndian.PutUint16(req[4:6], uint16(internalPort))
		binary.BigEndian.PutUint16(req[6:8], uint16(externalPort))
		binary.BigEndian.PutUint32(req[8:12], lifetime)
	}

	deadline := time.Now().Add(c.timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	// RFC 6886 requires retransmission with doubling intervals; three tries.
	// The absolute deadline is never extended, so a dead gateway costs exactly
	// one probe budget.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if time.Until(deadline) <= 20*time.Millisecond {
			break
		}
		if _, err := conn.Write(req); err != nil {
			lastErr = err
			continue
		}
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err == nil && n >= 4 {
			return buf[:n], nil
		}
		lastErr = err
		_ = conn.SetDeadline(deadline)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no response from NAT-PMP gateway %s", c.gatewayIP)
	}
	return nil, lastErr
}

// ExternalIP queries the public address (opcode 0).
func (c *NATPMPClient) ExternalIP(ctx context.Context) (string, error) {
	reply, err := c.request(ctx, natpmpOpPublicIP, 0, 0, 0)
	if err != nil {
		return "", err
	}
	if len(reply) < 12 {
		return "", fmt.Errorf("natpmp: short public address reply (%d bytes)", len(reply))
	}
	if code := binary.BigEndian.Uint16(reply[2:4]); code != natpmpResultOK {
		return "", natpmpError(code)
	}
	ip := net.IPv4(reply[8], reply[9], reply[10], reply[11]).String()
	c.externalIP = ip
	c.info.ExternalIP = ip
	return ip, nil
}

// AddMapping creates or refreshes a NAT-PMP mapping.
func (c *NATPMPClient) AddMapping(ctx context.Context, m Mapping) (Result, error) {
	proto := NormalizeProto(m.Protocol)
	opcode := byte(natpmpOpMapTCP)
	if proto == UDP {
		opcode = natpmpOpMapUDP
	}
	lifetime := uint32(m.LeaseSeconds)
	if lifetime == 0 {
		lifetime = 3600
	}
	reply, err := c.request(ctx, opcode, m.InternalPort, m.ExternalPort, lifetime)
	if err != nil {
		return Result{}, err
	}
	if len(reply) < 16 {
		return Result{}, fmt.Errorf("natpmp: short map reply (%d bytes)", len(reply))
	}
	if code := binary.BigEndian.Uint16(reply[2:4]); code != natpmpResultOK {
		return Result{}, natpmpError(code)
	}
	assigned := int(binary.BigEndian.Uint16(reply[10:12]))
	gotLifetime := int(binary.BigEndian.Uint32(reply[12:16]))
	extIP := c.externalIP
	if extIP == "" {
		extIP, _ = c.ExternalIP(ctx)
	}
	return Result{
		ExternalIP:   extIP,
		ExternalPort: assigned,
		InternalPort: m.InternalPort,
		Protocol:     proto,
		LeaseSeconds: gotLifetime,
		GatewayKind:  KindNATPMP,
		GatewayName:  c.info.Name,
		AppliedAt:    time.Now(),
	}, nil
}

// DeleteMapping removes a mapping by requesting a zero lifetime.
func (c *NATPMPClient) DeleteMapping(ctx context.Context, m Mapping) error {
	proto := NormalizeProto(m.Protocol)
	opcode := byte(natpmpOpMapTCP)
	if proto == UDP {
		opcode = natpmpOpMapUDP
	}
	ext := m.ExternalPort
	if ext == 0 {
		ext = m.InternalPort
	}
	reply, err := c.request(ctx, opcode, m.InternalPort, ext, 0)
	if err != nil {
		return err
	}
	if len(reply) >= 4 {
		if code := binary.BigEndian.Uint16(reply[2:4]); code != natpmpResultOK {
			return natpmpError(code)
		}
	}
	return nil
}

// ListMappings is not supported by NAT-PMP.
func (c *NATPMPClient) ListMappings(ctx context.Context) ([]Mapping, error) {
	return nil, ErrNotSupported
}

func natpmpError(code uint16) error {
	name, ok := natpmpResultNames[code]
	if !ok {
		name = fmt.Sprintf("result code %d", code)
	}
	return fmt.Errorf("natpmp: gateway returned %s", name)
}

// randomNonce returns a random 12 byte mapping nonce (shared with PCP).
func randomNonce() ([]byte, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
