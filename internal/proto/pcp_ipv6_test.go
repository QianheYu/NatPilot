package proto

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// pcpUDPFixture answers exactly one PCP request with a canned reply and records
// the request bytes, so the IPv6 layout can be asserted end to end without a
// real PCP server.
type pcpUDPFixture struct {
	conn   *net.UDPConn
	reply  []byte
	gotCh  chan []byte
	closed chan struct{}
}

func newPCPUDPFixture(t *testing.T, reply []byte) *pcpUDPFixture {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen udp fixture: %v", err)
	}
	f := &pcpUDPFixture{conn: conn, reply: reply, gotCh: make(chan []byte, 1), closed: make(chan struct{})}
	go func() {
		defer close(f.closed)
		buf := make([]byte, 1500)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, raddr, rerr := conn.ReadFromUDP(buf)
		if rerr != nil {
			return
		}
		select {
		case f.gotCh <- append([]byte(nil), buf[:n]...):
		default:
		}
		if f.reply != nil {
			_, _ = conn.WriteToUDP(f.reply, raddr)
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return f
}

func (f *pcpUDPFixture) port() int { return f.conn.LocalAddr().(*net.UDPAddr).Port }

func (f *pcpUDPFixture) request(t *testing.T) []byte {
	t.Helper()
	select {
	case pkt := <-f.gotCh:
		return pkt
	case <-time.After(2 * time.Second):
		t.Fatalf("PCP fixture received no request")
		return nil
	}
}

func (f *pcpUDPFixture) stop() {
	_ = f.conn.Close()
	<-f.closed
}

// mapReply builds a canned MAP response with an explicit 16-byte address.
func mapReply(lifetime uint32, assignedInternal, assignedExternal uint16, address []byte) []byte {
	reply := make([]byte, pcpMapLen)
	reply[0] = pcpVersion
	reply[1] = pcpOpcodeMap | pcpResponseFlag
	reply[3] = pcpResultSuccess
	binary.BigEndian.PutUint32(reply[4:8], lifetime)
	binary.BigEndian.PutUint32(reply[8:12], 7) // epoch
	binary.BigEndian.PutUint16(reply[40:42], assignedInternal)
	binary.BigEndian.PutUint16(reply[42:44], assignedExternal)
	copy(reply[44:60], address)
	return reply
}

func fixtureClient(t *testing.T, f *pcpUDPFixture, localIP string) *PCPClient {
	t.Helper()
	c, err := NewPCPClient("127.0.0.1", localIP, 1500*time.Millisecond)
	if err != nil {
		t.Fatalf("NewPCPClient: %v", err)
	}
	c.port = f.port()
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestPCPMapPacketIPv6Layout pins the byte offsets of a v6 MAP:
//
//	40-41 internal port, 42-43 suggested external port,
//	44-59 the suggested external address (raw 16-byte v6, no ::ffff: prefix).
//
// The IPv4 packet must stay byte-identical, including the IPv4-mapped form.
func TestPCPMapPacketIPv6Layout(t *testing.T) {
	c, err := NewPCPClient("192.168.1.1", "192.168.1.50", 0)
	if err != nil {
		t.Fatalf("NewPCPClient: %v", err)
	}

	// IPv4: the historical layout, including the ::ffff: prefix at 10-11.
	v4 := c.buildMapPacketFor(3600, TCP, 8080, 9090, "", "203.0.113.7")
	if len(v4) != 60 {
		t.Fatalf("v4 packet length = %d, want 60", len(v4))
	}
	if v4[0] != pcpVersion || v4[1]&0x7f != pcpOpcodeMap || v4[36] != 6 {
		t.Errorf("v4 header bytes = %v", v4[:2])
	}
	if binary.BigEndian.Uint32(v4[4:8]) != 3600 {
		t.Errorf("v4 lifetime = %d", binary.BigEndian.Uint32(v4[4:8]))
	}
	if binary.BigEndian.Uint16(v4[40:42]) != 8080 || binary.BigEndian.Uint16(v4[42:44]) != 9090 {
		t.Errorf("v4 ports = %d/%d", binary.BigEndian.Uint16(v4[40:42]), binary.BigEndian.Uint16(v4[42:44]))
	}
	// 44-59 is the suggested external address; its bytes 10-11 are at packet
	// offsets 54-55, which must be 0xff for the IPv4-mapped form.
	if v4[54] != 0xff || v4[55] != 0xff {
		t.Errorf("v4 suggested external address lost its IPv4-mapped prefix: %v", v4[44:60])
	}
	if !bytes.Equal(v4[44:60], addr16("203.0.113.7")) {
		t.Errorf("v4 suggested external address = %v", v4[44:60])
	}

	// IPv6: the same offsets, but 44-59 is the raw 16-byte address. Bytes 10-11
	// must NOT be 0xff: 2001:db8:1::9 has 0x00 there.
	v6 := c.buildMapPacketFor(7200, UDP, 443, 0, "2001:db8::5", "2001:db8:1::9")
	if len(v6) != 60 {
		t.Fatalf("v6 packet length = %d, want 60", len(v6))
	}
	if v6[0] != pcpVersion {
		t.Errorf("v6 version = %d", v6[0])
	}
	if v6[1]&0x7f != pcpOpcodeMap {
		t.Errorf("v6 opcode = %d", v6[1]&0x7f)
	}
	if v6[36] != 17 {
		t.Errorf("v6 protocol = %d, want 17 (UDP)", v6[36])
	}
	if binary.BigEndian.Uint32(v6[4:8]) != 7200 {
		t.Errorf("v6 lifetime = %d", binary.BigEndian.Uint32(v6[4:8]))
	}
	if binary.BigEndian.Uint16(v6[40:42]) != 443 {
		t.Errorf("v6 internal port = %d", binary.BigEndian.Uint16(v6[40:42]))
	}
	if binary.BigEndian.Uint16(v6[42:44]) != 0 {
		t.Errorf("v6 suggested external port = %d, want 0", binary.BigEndian.Uint16(v6[42:44]))
	}
	if v6[54] == 0xff || v6[55] == 0xff {
		t.Errorf("v6 address was encoded with the IPv4-mapped prefix: %v", v6[44:60])
	}
	want6 := net.ParseIP("2001:db8:1::9").To16()
	if !bytes.Equal(v6[44:60], want6) {
		t.Errorf("v6 suggested external address = %v, want %v", v6[44:60], want6)
	}

	// No suggestion: the 16-byte field stays zero (server assigns).
	none := c.buildMapPacketFor(60, TCP, 22, 22, "2001:db8::5", "")
	if !bytes.Equal(none[44:60], make([]byte, 16)) {
		t.Errorf("empty suggestion is not zero: %v", none[44:60])
	}

	// The old public-signature builder is still the v4 form.
	legacy := c.buildMapPacket(60, TCP, 8080, 9090, "203.0.113.7")
	if !bytes.Equal(legacy, c.buildMapPacketFor(60, TCP, 8080, 9090, "", "203.0.113.7")) {
		t.Errorf("buildMapPacket no longer matches the v4 build")
	}
}

func TestPCPParseMapReplyIPv6(t *testing.T) {
	v6 := net.ParseIP("2001:db8:ffff::9").To16()
	ip, extPort, lifetime, err := parseMapReply(mapReply(3600, 8080, 9999, v6))
	if err != nil {
		t.Fatalf("parseMapReply: %v", err)
	}
	if ip != "2001:db8:ffff::9" {
		t.Errorf("external IP = %q, want 2001:db8:ffff::9", ip)
	}
	if extPort != 9999 {
		t.Errorf("external port = %d", extPort)
	}
	if lifetime != 3600 {
		t.Errorf("lifetime = %d", lifetime)
	}

	// The v4 reply still parses through the mapped form.
	ip, _, _, err = parseMapReply(mapReply(60, 1, 2, addr16("203.0.113.7")))
	if err != nil || ip != "203.0.113.7" {
		t.Errorf("v4 reply = %q, %v", ip, err)
	}

	// :: (all zero bytes) means "not reported".
	if ip, _, _, err := parseMapReply(mapReply(60, 0, 0, make([]byte, 16))); err != nil || ip != "" {
		t.Errorf("zero address reply = %q, %v; want empty", ip, err)
	}
}

// TestPCPAddMappingIPv6CannedResponse drives AddMapping through the real UDP
// exchange against a canned v6 response and checks the request the client sent.
func TestPCPAddMappingIPv6CannedResponse(t *testing.T) {
	f := newPCPUDPFixture(t, mapReply(3600, 8443, 9999, net.ParseIP("2001:db8:ffff::9").To16()))
	c := fixtureClient(t, f, "192.168.1.50")

	res, err := c.AddMapping(context.Background(), Mapping{
		Protocol:     TCP,
		InternalHost: "2001:db8::5",
		InternalPort: 8080,
		ExternalPort: 9090,
		LeaseSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("AddMapping(v6): %v", err)
	}
	req := f.request(t)
	f.stop()

	if got := binary.BigEndian.Uint32(req[4:8]); got != 3600 {
		t.Errorf("request lifetime = %d, want 3600", got)
	}
	if req[36] != 6 {
		t.Errorf("request protocol = %d, want 6", req[36])
	}
	if got := binary.BigEndian.Uint16(req[40:42]); got != 8080 {
		t.Errorf("request internal port = %d, want 8080", got)
	}
	if got := binary.BigEndian.Uint16(req[42:44]); got != 9090 {
		t.Errorf("request suggested external port = %d, want 9090", got)
	}
	if req[54] == 0xff && req[55] == 0xff {
		t.Errorf("request carried an IPv4-mapped address for a v6 mapping")
	}

	if res.ExternalIP != "2001:db8:ffff::9" {
		t.Errorf("result external IP = %q", res.ExternalIP)
	}
	if res.ExternalPort != 9999 {
		t.Errorf("result external port = %d, want the server-assigned 9999", res.ExternalPort)
	}
	if res.InternalHost != "2001:db8::5" {
		t.Errorf("result internal host = %q, want the v6 target", res.InternalHost)
	}
	if res.InternalPort != 8443 {
		t.Errorf("result internal port = %d, want the server-assigned 8443", res.InternalPort)
	}
	if res.LeaseSeconds != 3600 {
		t.Errorf("result lease = %d", res.LeaseSeconds)
	}
}

// A v6 suggestion must be encoded in the request's 44-59 field.
func TestPCPAddMappingIPv6SuggestsExternalAddress(t *testing.T) {
	f := newPCPUDPFixture(t, mapReply(600, 8080, 8080, net.ParseIP("2001:db8:1::9").To16()))
	c := fixtureClient(t, f, "fd00::50")

	if _, err := c.AddMapping(context.Background(), Mapping{
		Protocol:     TCP,
		InternalHost: "2001:db8::5",
		InternalPort: 8080,
		ExternalPort: 8080,
		ExternalIP:   "2001:db8:1::9",
		LeaseSeconds: 600,
	}); err != nil {
		t.Fatalf("AddMapping: %v", err)
	}
	req := f.request(t)
	f.stop()
	if !bytes.Equal(req[44:60], net.ParseIP("2001:db8:1::9").To16()) {
		t.Errorf("suggested external address = %v", req[44:60])
	}
}

// The IPv4 path stays exactly as it was: same request bytes, same result.
func TestPCPAddMappingIPv4CannedResponse(t *testing.T) {
	f := newPCPUDPFixture(t, mapReply(1200, 8080, 12345, addr16("203.0.113.7")))
	c := fixtureClient(t, f, "192.168.1.50")

	res, err := c.AddMapping(context.Background(), Mapping{
		Protocol:     TCP,
		InternalHost: "192.168.1.50",
		InternalPort: 8080,
		ExternalPort: 8080,
		ExternalIP:   "203.0.113.7",
		LeaseSeconds: 1200,
	})
	if err != nil {
		t.Fatalf("AddMapping(v4): %v", err)
	}
	req := f.request(t)
	f.stop()
	if !bytes.Equal(req[44:60], addr16("203.0.113.7")) {
		t.Errorf("v4 suggested external address = %v", req[44:60])
	}
	if req[54] != 0xff || req[55] != 0xff {
		t.Errorf("v4 request lost the IPv4-mapped prefix")
	}
	if res.ExternalIP != "203.0.113.7" || res.ExternalPort != 12345 {
		t.Errorf("v4 result = %+v", res)
	}
	if res.InternalHost != "192.168.1.50" {
		t.Errorf("v4 result internal host = %q, want the client address", res.InternalHost)
	}
}

// DeleteMapping must use the IPv6 family for an IPv6 internal address and send
// lifetime 0 with the external port that was assigned.
func TestPCPDeleteMappingIPv6(t *testing.T) {
	f := newPCPUDPFixture(t, mapReply(0, 8080, 9999, nil))
	c := fixtureClient(t, f, "fd00::50")

	if err := c.DeleteMapping(context.Background(), Mapping{
		Protocol:     UDP,
		InternalHost: "2001:db8::5",
		InternalPort: 8080,
		ExternalPort: 9999,
	}); err != nil {
		t.Fatalf("DeleteMapping(v6): %v", err)
	}
	req := f.request(t)
	f.stop()

	if got := binary.BigEndian.Uint32(req[4:8]); got != 0 {
		t.Errorf("delete lifetime = %d, want 0", got)
	}
	if req[36] != 17 {
		t.Errorf("delete protocol = %d, want 17", req[36])
	}
	if got := binary.BigEndian.Uint16(req[40:42]); got != 8080 {
		t.Errorf("delete internal port = %d", got)
	}
	if got := binary.BigEndian.Uint16(req[42:44]); got != 9999 {
		t.Errorf("delete external port = %d", got)
	}
}

func TestNewPCPClientAdvertisesIPv6(t *testing.T) {
	c, err := NewPCPClient("192.168.1.1", "192.168.1.50", 0)
	if err != nil {
		t.Fatalf("NewPCPClient: %v", err)
	}
	if c.Info().SupportsPCPIPv6() {
		t.Errorf("a v4/v4 PCP client must not advertise IPv6 MAP")
	}

	v6, err := NewPCPClient("fe80::1", "fd00::50", 0)
	if err != nil {
		t.Fatalf("NewPCPClient(v6): %v", err)
	}
	if !v6.Info().SupportsPCPIPv6() {
		t.Errorf("a PCP client over IPv6 must advertise IPv6 MAP")
	}
}

func TestPCPClientAddrForIPv6(t *testing.T) {
	c, err := NewPCPClient("192.168.1.1", "192.168.1.50", 0)
	if err != nil {
		t.Fatalf("NewPCPClient: %v", err)
	}
	addr, isV6 := c.clientAddrFor("2001:db8::5")
	if !isV6 || addr != "2001:db8::5" {
		t.Errorf("clientAddrFor(v6) = %q, %v", addr, isV6)
	}
	if got := addr16(addr); got[10] == 0xff || got[11] == 0xff {
		t.Errorf("v6 client address has an IPv4-mapped prefix: %v", got)
	}
	addr, isV6 = c.clientAddrFor("192.168.1.50")
	if isV6 || addr != "192.168.1.50" {
		t.Errorf("clientAddrFor(v4) = %q, %v", addr, isV6)
	}
}
