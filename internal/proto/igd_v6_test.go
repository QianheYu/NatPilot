package proto

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// soapFaultSpec describes a canned UPnP fault returned by the fake gateway.
type soapFaultSpec struct {
	code string
	desc string
}

type soapRequest struct {
	action string
	body   string
}

// fakeIPv6SOAP is a canned WANIPv6FirewallControl:1 control point.
type fakeIPv6SOAP struct {
	mu          sync.Mutex
	requests    []soapRequest
	statusCalls int

	statusXML   string
	addXML      string
	deleteXML   string
	getXML      string
	addFault    *soapFaultSpec
	deleteFault *soapFaultSpec
}

func newFakeIPv6SOAP() *fakeIPv6SOAP {
	return &fakeIPv6SOAP{
		statusXML: `<NewFirewallEnabled>1</NewFirewallEnabled>` +
			`<NewInboundPinholeAllowed>1</NewInboundPinholeAllowed>` +
			`<NewOutboundPinholeAllowed>1</NewOutboundPinholeAllowed>`,
		addXML: `<UniqueID>4242</UniqueID>`,
	}
}

func (f *fakeIPv6SOAP) respond(w http.ResponseWriter, action, inner string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%sResponse xmlns:u="%s">%s</u:%sResponse></s:Body></s:Envelope>`,
		action, svcWANIPv6FirewallControl1, inner, action)
}

func (f *fakeIPv6SOAP) fault(w http.ResponseWriter, spec soapFaultSpec) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError><errorCode>%s</errorCode><errorDescription>%s</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`,
		spec.code, spec.desc)
}

func (f *fakeIPv6SOAP) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	action := r.Header.Get("SOAPAction")
	action = strings.Trim(action, `"`)
	if i := strings.LastIndex(action, "#"); i >= 0 {
		action = action[i+1:]
	}

	f.mu.Lock()
	f.requests = append(f.requests, soapRequest{action: action, body: string(raw)})
	switch action {
	case "GetFirewallStatus":
		f.statusCalls++
	case "AddPinhole":
		if f.addFault != nil {
			spec := *f.addFault
			f.mu.Unlock()
			f.fault(w, spec)
			return
		}
	case "DeletePinhole":
		if f.deleteFault != nil {
			spec := *f.deleteFault
			f.mu.Unlock()
			f.fault(w, spec)
			return
		}
	}
	statusXML, addXML, getXML := f.statusXML, f.addXML, f.getXML
	f.mu.Unlock()

	switch action {
	case "GetFirewallStatus":
		f.respond(w, action, statusXML)
	case "AddPinhole":
		f.respond(w, action, addXML)
	case "DeletePinhole":
		f.respond(w, action, "")
	case "GetPinhole":
		f.respond(w, action, getXML)
	default:
		f.fault(w, soapFaultSpec{"401", "Invalid Action"})
	}
}

func (f *fakeIPv6SOAP) requestsFor(action string) []soapRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []soapRequest{}
	for _, r := range f.requests {
		if r.action == action {
			out = append(out, r)
		}
	}
	return out
}

// newIPv6TestClient wires a real IGDClient to the canned SOAP server.
func newIPv6TestClient(t *testing.T, fake *fakeIPv6SOAP) (*IGDClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	client := NewIGDClient(IGDConfig{
		Kind:                    KindIGD2,
		Address:                 "192.168.1.1",
		Port:                    80,
		ControlURL:              srv.URL + "/ctl/IPConn",
		ServiceType:             svcWANIPConnection2,
		IPv6FirewallControlURL:  srv.URL + "/ctl/IPv6FC",
		IPv6FirewallServiceType: svcWANIPv6FirewallControl1,
		Name:                    "FakeIPv6Router",
		Timeout:                 3 * time.Second,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client, srv
}

func TestDescribeGatewayCapturesIPv6FirewallControl(t *testing.T) {
	doc := `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:2</deviceType>
    <friendlyName>IPv6 Router</friendlyName>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANDevice:2</deviceType>
        <serviceList>
          <service>
            <serviceType>urn:schemas-upnp-org:service:WANIPConnection:2</serviceType>
            <controlURL>/ctl/IPConn</controlURL>
          </service>
          <service>
            <serviceType>urn:schemas-upnp-org:service:WANIPv6FirewallControl:1</serviceType>
            <controlURL>/ctl/IPv6FC</controlURL>
          </service>
        </serviceList>
      </device>
    </deviceList>
  </device>
</root>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, doc)
	}))
	defer srv.Close()

	cfg, _, err := DescribeGateway(context.Background(), srv.URL+"/rootDesc.xml", 3*time.Second)
	if err != nil {
		t.Fatalf("DescribeGateway: %v", err)
	}
	if cfg.IPv6FirewallControlURL != srv.URL+"/ctl/IPv6FC" {
		t.Errorf("IPv6 control URL = %q", cfg.IPv6FirewallControlURL)
	}
	if cfg.IPv6FirewallServiceType != svcWANIPv6FirewallControl1 {
		t.Errorf("IPv6 service type = %q", cfg.IPv6FirewallServiceType)
	}
	client := NewIGDClient(*cfg)
	defer client.Close()
	if !client.Info().SupportsIPv6Pinhole() {
		t.Errorf("gateway capabilities %v do not advertise %s", client.Info().Capabilities, CapIPv6Pinhole)
	}
	if client.IPv6FirewallControlURL() != srv.URL+"/ctl/IPv6FC" {
		t.Errorf("client IPv6 control URL = %q", client.IPv6FirewallControlURL())
	}
}

func TestDescribeGatewayWithoutIPv6ServiceDoesNotAdvertisePinholes(t *testing.T) {
	doc := `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:2</deviceType>
    <deviceList><device><serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:WANIPConnection:2</serviceType>
        <controlURL>/ctl/IPConn</controlURL>
      </service>
    </serviceList></device></deviceList>
  </device>
</root>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, doc)
	}))
	defer srv.Close()
	cfg, _, err := DescribeGateway(context.Background(), srv.URL+"/rootDesc.xml", 3*time.Second)
	if err != nil {
		t.Fatalf("DescribeGateway: %v", err)
	}
	if cfg.IPv6FirewallControlURL != "" {
		t.Errorf("IPv6 control URL = %q, want empty", cfg.IPv6FirewallControlURL)
	}
	client := NewIGDClient(*cfg)
	defer client.Close()
	if client.Info().SupportsIPv6Pinhole() {
		t.Errorf("v4-only gateway advertised %s", CapIPv6Pinhole)
	}
}

func TestFirewallStatusParsedAndCached(t *testing.T) {
	fake := newFakeIPv6SOAP()
	// Inbound pinholing disabled: the service is present but currently refuses.
	fake.statusXML = `<NewFirewallEnabled>1</NewFirewallEnabled>` +
		`<NewInboundPinholeAllowed>0</NewInboundPinholeAllowed>`
	client, _ := newIPv6TestClient(t, fake)

	caps, err := client.FirewallStatus(context.Background())
	if err != nil {
		t.Fatalf("FirewallStatus: %v", err)
	}
	if !caps.Advertised || !caps.StatusKnown {
		t.Errorf("capability = %+v, want advertised and known", caps)
	}
	if !caps.FirewallEnabled || caps.InboundPinholeAllowed {
		t.Errorf("capability = %+v, want FirewallEnabled=true InboundPinholeAllowed=false", caps)
	}
	if caps.PinholesSupported() {
		t.Errorf("PinholesSupported() = true with inbound pinholing disabled")
	}
	// Second call must come from the cache.
	if _, err := client.FirewallStatus(context.Background()); err != nil {
		t.Fatalf("second FirewallStatus: %v", err)
	}
	fake.mu.Lock()
	calls := fake.statusCalls
	fake.mu.Unlock()
	if calls != 1 {
		t.Errorf("GetFirewallStatus calls = %d, want 1 (cached)", calls)
	}

	// A definitive denial must not produce an AddPinhole request at all.
	_, _, err = client.AddPinhole(context.Background(), IPv6Pinhole{
		InternalClient: "2001:db8::5", InternalPort: 8080, Protocol: TCP, LeaseSeconds: 3600,
	})
	if err == nil {
		t.Fatalf("AddPinhole succeeded with inbound pinholing disabled")
	}
	if !strings.Contains(err.Error(), "InboundPinholeAllowed") {
		t.Errorf("error %q does not explain the disabled inbound pinhole permission", err)
	}
	if got := len(fake.requestsFor("AddPinhole")); got != 0 {
		t.Errorf("sent %d AddPinhole requests despite a known denial", got)
	}
}

func TestAddPinholeArgsAndUniqueIDRoundTrip(t *testing.T) {
	fake := newFakeIPv6SOAP()
	fake.addXML = `<NewUniqueID>65535</NewUniqueID><NewLeaseTime>3600</NewLeaseTime>`
	client, _ := newIPv6TestClient(t, fake)

	id, lease, err := client.AddPinhole(context.Background(), IPv6Pinhole{
		RemoteHost:     "",
		RemotePort:     0,
		InternalClient: "2001:db8::5",
		InternalPort:   8443,
		Protocol:       "tcp",
		LeaseSeconds:   3600,
	})
	if err != nil {
		t.Fatalf("AddPinhole: %v", err)
	}
	if id != 65535 {
		t.Errorf("unique id = %d, want 65535 (uint16 max must round-trip)", id)
	}
	if lease != 3600 {
		t.Errorf("lease = %d, want 3600", lease)
	}

	reqs := fake.requestsFor("AddPinhole")
	if len(reqs) != 1 {
		t.Fatalf("AddPinhole requests = %d, want 1", len(reqs))
	}
	body := reqs[0].body
	for _, want := range []string{
		"<RemoteHost></RemoteHost>",
		"<RemotePort>0</RemotePort>",
		"<InternalClient>2001:db8::5</InternalClient>",
		"<InternalPort>8443</InternalPort>",
		"<Protocol>6</Protocol>",
		"<LeaseTime>3600</LeaseTime>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("AddPinhole body missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, `xmlns:u="`+svcWANIPv6FirewallControl1+`"`) {
		t.Errorf("AddPinhole body uses the wrong service type:\n%s", body)
	}

	// UDP must be encoded as protocol 17.
	if _, _, err := client.AddPinhole(context.Background(), IPv6Pinhole{
		InternalClient: "2001:db8::5", InternalPort: 5353, Protocol: "udp", LeaseSeconds: 60,
	}); err != nil {
		t.Fatalf("UDP AddPinhole: %v", err)
	}
	reqs = fake.requestsFor("AddPinhole")
	if len(reqs) != 2 {
		t.Fatalf("AddPinhole requests = %d, want 2", len(reqs))
	}
	if !strings.Contains(reqs[1].body, "<Protocol>17</Protocol>") {
		t.Errorf("UDP AddPinhole did not use protocol 17:\n%s", reqs[1].body)
	}
	// GetFirewallStatus is cached, so both AddPinhole calls cost one status call.
	fake.mu.Lock()
	statusCalls := fake.statusCalls
	fake.mu.Unlock()
	if statusCalls != 1 {
		t.Errorf("GetFirewallStatus calls = %d, want 1", statusCalls)
	}
}

func TestAddPinholeLeaseFallsBackToRequested(t *testing.T) {
	fake := newFakeIPv6SOAP()
	fake.addXML = `<NewUniqueID>7</NewUniqueID>` // no lease in response
	client, _ := newIPv6TestClient(t, fake)

	_, lease, err := client.AddPinhole(context.Background(), IPv6Pinhole{
		InternalClient: "2001:db8::7", InternalPort: 443, Protocol: TCP, LeaseSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("AddPinhole: %v", err)
	}
	if lease != 1800 {
		t.Errorf("lease = %d, want the requested 1800 when the response omits it", lease)
	}
}

func TestAddPinholeFault702IsActionable(t *testing.T) {
	fake := newFakeIPv6SOAP()
	fake.addFault = &soapFaultSpec{code: "702", desc: "FirewallDisabled"}
	client, _ := newIPv6TestClient(t, fake)

	_, _, err := client.AddPinhole(context.Background(), IPv6Pinhole{
		InternalClient: "2001:db8::5", InternalPort: 8080, Protocol: TCP, LeaseSeconds: 3600,
	})
	if err == nil {
		t.Fatalf("expected the 702 fault to surface")
	}
	msg := err.Error()
	if !strings.Contains(msg, "upnp error 702") {
		t.Errorf("error %q does not keep the raw UPnP code", msg)
	}
	if !strings.Contains(msg, "IPv6 防火墙") || !strings.Contains(msg, "FirewallDisabled") {
		t.Errorf("error %q is not actionable", msg)
	}
	if code, ok := UPnPErrorCode(err); !ok || code != "702" {
		t.Errorf("UPnPErrorCode = %q, %v; want 702, true", code, ok)
	}
}

func TestPinholeErrorHints(t *testing.T) {
	cases := []struct{ code, want string }{
		{"701", "pinhole 表已满"},
		{"702", "IPv6 防火墙已关闭"},
		{"703", "入站 IPv6 pinhole"},
		{"704", "pinhole 不存在"},
		{"705", "TCP(6) 或 UDP(17)"},
		{"706", "具体的内网端口"},
		{"707", "明确指定 TCP 或 UDP"},
		{"708", "来源 IPv6 地址"},
		{"709", "探测包"},
	}
	for _, c := range cases {
		err := upnpError("AddPinhole", c.code, "spec description")
		if err == nil {
			t.Fatalf("code %s: nil error", c.code)
		}
		msg := err.Error()
		if !strings.Contains(msg, "upnp error "+c.code+": spec description") {
			t.Errorf("code %s: raw fault missing from %q", c.code, msg)
		}
		if !strings.Contains(msg, c.want) {
			t.Errorf("code %s: hint %q missing from %q", c.code, c.want, msg)
		}
	}
}

func TestAddPinholeValidationAndUnsupported(t *testing.T) {
	fake := newFakeIPv6SOAP()
	client, _ := newIPv6TestClient(t, fake)

	for name, req := range map[string]IPv6Pinhole{
		"brackets":       {InternalClient: "[2001:db8::5]", InternalPort: 80, Protocol: TCP},
		"ipv4":           {InternalClient: "192.168.1.5", InternalPort: 80, Protocol: TCP},
		"missing client": {InternalPort: 80, Protocol: TCP},
		"wildcard port":  {InternalClient: "2001:db8::5", InternalPort: 0, Protocol: TCP},
		"bad protocol":   {InternalClient: "2001:db8::5", InternalPort: 80, Protocol: "sctp"},
	} {
		if _, _, err := client.AddPinhole(context.Background(), req); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	if got := len(fake.requestsFor("AddPinhole")); got != 0 {
		t.Errorf("invalid requests reached the gateway: %d", got)
	}

	// No service in the description: the error must name the expected service.
	v4Only := NewIGDClient(IGDConfig{
		Kind: KindIGD2, Address: "192.168.1.1", ControlURL: "http://192.168.1.1/ctl/IPConn",
		ServiceType: svcWANIPConnection2, Name: "v4-only",
	})
	defer v4Only.Close()
	if _, _, err := v4Only.AddPinhole(context.Background(), IPv6Pinhole{
		InternalClient: "2001:db8::5", InternalPort: 80, Protocol: TCP,
	}); !errors.Is(err, ErrNotSupported) || !strings.Contains(err.Error(), svcWANIPv6FirewallControl1) {
		t.Errorf("unsupported AddPinhole error = %v", err)
	}
	if _, err := v4Only.FirewallStatus(context.Background()); !errors.Is(err, ErrNotSupported) {
		t.Errorf("unsupported FirewallStatus error = %v", err)
	}
}

func TestDeletePinholeAndGetPinholeRoundTrip(t *testing.T) {
	fake := newFakeIPv6SOAP()
	fake.getXML = `<NewRemoteHost></NewRemoteHost><NewRemotePort>0</NewRemotePort>` +
		`<NewInternalClient>2001:db8::5</NewInternalClient><NewInternalPort>8080</NewInternalPort>` +
		`<NewProtocol>17</NewProtocol><NewLeaseTime>90</NewLeaseTime>`
	client, _ := newIPv6TestClient(t, fake)

	if err := client.DeletePinhole(context.Background(), 65535); err != nil {
		t.Fatalf("DeletePinhole: %v", err)
	}
	reqs := fake.requestsFor("DeletePinhole")
	if len(reqs) != 1 {
		t.Fatalf("DeletePinhole requests = %d", len(reqs))
	}
	if !strings.Contains(reqs[0].body, "<UniqueID>65535</UniqueID>") {
		t.Errorf("DeletePinhole body does not carry the uint16 id:\n%s", reqs[0].body)
	}

	pin, err := client.GetPinhole(context.Background(), 65535)
	if err != nil {
		t.Fatalf("GetPinhole: %v", err)
	}
	if pin.UniqueID != 65535 || pin.InternalClient != "2001:db8::5" || pin.InternalPort != 8080 {
		t.Errorf("GetPinhole = %+v", pin)
	}
	if pin.Protocol != UDP {
		t.Errorf("GetPinhole protocol = %q, want udp", pin.Protocol)
	}
	if pin.LeaseSeconds != 90 {
		t.Errorf("GetPinhole lease = %d, want 90", pin.LeaseSeconds)
	}
}

func TestGetPinholeNamespacedElements(t *testing.T) {
	fake := newFakeIPv6SOAP()
	// Some firmwares prefix the response elements; local-name parsing must cope.
	fake.addXML = `<u:UniqueID>9</u:UniqueID>`
	client, _ := newIPv6TestClient(t, fake)

	id, _, err := client.AddPinhole(context.Background(), IPv6Pinhole{
		InternalClient: "2001:db8::9", InternalPort: 80, Protocol: TCP, LeaseSeconds: 60,
	})
	if err != nil {
		t.Fatalf("AddPinhole: %v", err)
	}
	if id != 9 {
		t.Errorf("unique id = %d, want 9", id)
	}
}
