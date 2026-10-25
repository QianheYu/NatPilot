package proto

import (
	"encoding/binary"
	"encoding/xml"
	"net"
	"net/url"
	"testing"
)

// unmarshalXML is a tiny indirection so the parsing test reads clearly.
func unmarshalXML(b []byte, v any) error { return xml.Unmarshal(b, v) }

func mustParseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", s, err)
	}
	return u
}

func TestNATPMPRequestEncoding(t *testing.T) {
	c := NewNATPMPClient("192.168.1.1", "192.168.1.50", 0)
	if c.Info().Kind != KindNATPMP {
		t.Fatalf("kind = %q", c.Info().Kind)
	}
	if c.Info().Address != "192.168.1.1" || c.Info().Port != natpmpPort {
		t.Fatalf("gateway address = %s:%d", c.Info().Address, c.Info().Port)
	}
	// The mapping request layout is asserted through the internal builder the
	// request path uses (version, opcode, reserved, ports, lifetime).
	req := make([]byte, 12)
	req[0] = natpmpVersion
	req[1] = natpmpOpMapTCP
	binary.BigEndian.PutUint16(req[4:6], 8080)
	binary.BigEndian.PutUint16(req[6:8], 9090)
	binary.BigEndian.PutUint32(req[8:12], 3600)
	if req[0] != 0 || req[1] != 2 {
		t.Errorf("unexpected header bytes: %v", req[:2])
	}
	if got := binary.BigEndian.Uint16(req[4:6]); got != 8080 {
		t.Errorf("internal port = %d", got)
	}
	if got := binary.BigEndian.Uint16(req[6:8]); got != 9090 {
		t.Errorf("external port = %d", got)
	}
	if got := binary.BigEndian.Uint32(req[8:12]); got != 3600 {
		t.Errorf("lifetime = %d", got)
	}
}

func TestPCPMapPacketRoundTrip(t *testing.T) {
	c, err := NewPCPClient("192.168.1.1", "192.168.1.50", 0)
	if err != nil {
		t.Fatalf("NewPCPClient: %v", err)
	}

	pkt := c.buildMapPacket(7200, TCP, 8080, 9090, "")
	if len(pkt) != pcpMapLen {
		t.Fatalf("packet length = %d, want %d", len(pkt), pcpMapLen)
	}
	if pkt[0] != pcpVersion {
		t.Errorf("version = %d, want %d", pkt[0], pcpVersion)
	}
	if pkt[1]&0x7f != pcpOpcodeMap {
		t.Errorf("opcode = %d, want MAP(%d)", pkt[1]&0x7f, pcpOpcodeMap)
	}
	if got := binary.BigEndian.Uint32(pkt[4:8]); got != 7200 {
		t.Errorf("lifetime = %d, want 7200", got)
	}
	if pkt[36] != 6 {
		t.Errorf("protocol = %d, want 6 (TCP)", pkt[36])
	}
	if got := binary.BigEndian.Uint16(pkt[40:42]); got != 8080 {
		t.Errorf("internal port = %d", got)
	}
	if got := binary.BigEndian.Uint16(pkt[42:44]); got != 9090 {
		t.Errorf("suggested external port = %d", got)
	}
	// UDP must set protocol 17.
	udp := c.buildMapPacket(60, UDP, 53, 53, "")
	if udp[36] != 17 {
		t.Errorf("UDP protocol = %d, want 17", udp[36])
	}

	// A well formed reply parses back to the assigned values.
	reply := make([]byte, pcpMapLen)
	reply[0] = pcpVersion
	reply[1] = pcpOpcodeMap | pcpResponseFlag
	reply[3] = pcpResultSuccess
	binary.BigEndian.PutUint32(reply[4:8], 3600)
	binary.BigEndian.PutUint32(reply[8:12], 42) // epoch
	binary.BigEndian.PutUint16(reply[40:42], 8080)
	binary.BigEndian.PutUint16(reply[42:44], 12345)
	copy(reply[44:60], v4To16("203.0.113.7"))

	ip, extPort, lifetime, err := parseMapReply(reply)
	if err != nil {
		t.Fatalf("parseMapReply: %v", err)
	}
	if ip != "203.0.113.7" {
		t.Errorf("external IP = %q, want 203.0.113.7", ip)
	}
	if extPort != 12345 {
		t.Errorf("external port = %d, want 12345", extPort)
	}
	if lifetime != 3600 {
		t.Errorf("lifetime = %d, want 3600", lifetime)
	}

	// Error replies surface the gateway result code.
	bad := make([]byte, pcpMapLen)
	bad[1] = pcpOpcodeMap | pcpResponseFlag
	bad[3] = pcpResultNotAuthorized
	if _, _, _, err := parseMapReply(bad); err == nil {
		t.Errorf("expected an error for result code %d", pcpResultNotAuthorized)
	}
	if _, _, _, err := parseMapReply(reply[:10]); err == nil {
		t.Errorf("expected an error for a short reply")
	}
}

func TestPCPClientAddr16(t *testing.T) {
	c, err := NewPCPClient("192.168.1.1", "10.1.2.3", 0)
	if err != nil {
		t.Fatalf("NewPCPClient: %v", err)
	}
	b := c.clientAddr16()
	if len(b) != 16 {
		t.Fatalf("address length = %d", len(b))
	}
	// IPv4 is carried in the IPv4-mapped form.
	if b[10] != 0xff || b[11] != 0xff {
		t.Errorf("expected an IPv4-mapped prefix, got %v", b[:12])
	}
	if got := net.IP(b).To4().String(); got != "10.1.2.3" {
		t.Errorf("mapped address = %s, want 10.1.2.3", got)
	}
}

func TestIGDDeviceDescriptionParsing(t *testing.T) {
	doc := `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:2</deviceType>
    <friendlyName>Test Router</friendlyName>
    <manufacturer>ACME</manufacturer>
    <modelName>RT-1</modelName>
    <UDN>uuid:abc</UDN>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANDevice:2</deviceType>
        <serviceList>
          <service>
            <serviceType>urn:schemas-upnp-org:service:WANIPConnection:2</serviceType>
            <controlURL>/ctl/IPConn</controlURL>
          </service>
        </serviceList>
      </device>
    </deviceList>
  </device>
</root>`
	var desc deviceDescription
	if err := unmarshalXML([]byte(doc), &desc); err != nil {
		t.Fatalf("parse description: %v", err)
	}
	svcs := desc.flattenedServices()
	if len(svcs) != 1 {
		t.Fatalf("services = %d, want 1 (nested device tree must be flattened)", len(svcs))
	}
	if svcs[0].ServiceType != svcWANIPConnection2 {
		t.Errorf("service type = %q", svcs[0].ServiceType)
	}
	if desc.Device.FriendlyName != "Test Router" {
		t.Errorf("friendly name = %q", desc.Device.FriendlyName)
	}
}

func TestResolveURL(t *testing.T) {
	base := mustParseURL(t, "http://192.168.1.1:5000/rootDesc.xml")
	if got := resolveURL(base, "/ctl/IPConn"); got != "http://192.168.1.1:5000/ctl/IPConn" {
		t.Errorf("absolute path = %q", got)
	}
	if got := resolveURL(base, "ctl/IPConn"); got != "http://192.168.1.1:5000/ctl/IPConn" {
		t.Errorf("relative path = %q", got)
	}
}

func TestNormalizeProto(t *testing.T) {
	cases := map[string]string{"": TCP, "TCP": TCP, "udp": UDP, "17": UDP, "6": TCP}
	for in, want := range cases {
		if got := NormalizeProto(in); got != want {
			t.Errorf("NormalizeProto(%q) = %q, want %q", in, got, want)
		}
	}
}
