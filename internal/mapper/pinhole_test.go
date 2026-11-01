package mapper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/proto"
)

// TestDecideRoute pins the pure routing decision: IPv4 targets keep the NAT
// mapping path, IPv6 literals need a pinhole, and "this host" specs resolve to
// this machine's LAN IPv4 address (no router can forward a pinhole to ::1).
func TestDecideRoute(t *testing.T) {
	cases := []struct {
		host    string
		capable bool
		want    ruleRoute
	}{
		// IPv4 / names / "this host": always the historical mapping path.
		{"192.168.1.10", false, routeMapping},
		{"192.168.1.10", true, routeMapping},
		{"10.0.0.1", true, routeMapping},
		{"", true, routeMapping},
		{"   ", true, routeMapping},
		{"this", true, routeMapping},
		{"self", true, routeMapping},
		{"localhost", true, routeMapping},
		{"127.0.0.1", true, routeMapping},
		{"0.0.0.0", true, routeMapping},
		{"::1", true, routeMapping},
		{"[::1]", true, routeMapping},
		{"::", true, routeMapping},
		{"[::]", true, routeMapping},
		{"example.lan", true, routeMapping},
		// IPv4-mapped IPv6 is still IPv4 (net.IP.To4 != nil).
		{"::ffff:192.168.1.10", true, routeMapping},
		{"[::ffff:192.168.1.10]", true, routeMapping},

		// IPv6 literals: pinhole when a capable gateway exists.
		{"2001:db8::5", true, routePinhole},
		{"2001:db8::5", false, routeUnsupportedIPv6},
		{"[2001:db8::5]", true, routePinhole},
		{"[2001:db8::5]", false, routeUnsupportedIPv6},
		{"fd00::1", true, routePinhole},
		{"fe80::1%12", true, routePinhole},
	}
	for _, c := range cases {
		if got := decideRoute(c.host, c.capable); got != c.want {
			t.Errorf("decideRoute(%q, capable=%v) = %s, want %s",
				c.host, c.capable, got, c.want)
		}
	}
}

func TestCanonicalIPv6(t *testing.T) {
	for _, in := range []string{"2001:0db8:0:0:0:0:0:5", "[2001:db8::5]", " 2001:db8::5 "} {
		got, ok := canonicalIPv6(in)
		if !ok || got != "2001:db8::5" {
			t.Errorf("canonicalIPv6(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "192.168.1.5", "::ffff:192.168.1.5", "router.lan", "["} {
		if got, ok := canonicalIPv6(in); ok {
			t.Errorf("canonicalIPv6(%q) = %q, want not IPv6", in, got)
		}
	}
	// A zone id is dropped: the UPnP argument has no zone slot.
	if got, ok := canonicalIPv6("fe80::1%eth0"); !ok || got != "fe80::1" {
		t.Errorf("canonicalIPv6(zone) = %q, %v", got, ok)
	}
}

// The pinhole UniqueID has to survive the mapping record, which has no dedicated
// field; it rides in Description in a parseable marker.
func TestPinholeUniqueIDRecordRoundTrip(t *testing.T) {
	rec := model.MappingRecord{Description: pinholeDescription("web [prod]", 65535)}
	id, ok := pinholeIDFromRecord(rec)
	if !ok || id != 65535 {
		t.Fatalf("pinholeIDFromRecord = %d, %v; want 65535, true", id, ok)
	}
	if !strings.Contains(rec.Description, "web [prod]") {
		t.Errorf("description lost its human-readable part: %q", rec.Description)
	}
	for _, desc := range []string{"", "web", "natpilot [ipv6-pinhole:]", "natpilot [ipv6-pinhole:99999]"} {
		if _, ok := pinholeIDFromDescription(desc); ok {
			t.Errorf("pinholeIDFromDescription(%q) reported an id", desc)
		}
	}
}

// A failed renewal must not forget the id of the live pinhole: otherwise the
// rule could never delete it and it would linger until the lease expired.
func TestFailPinholeRulePreservesExistingUniqueID(t *testing.T) {
	m, _ := newMapperForTest(t)
	r := &model.Rule{ID: 7, Name: "v6-web", Port: 8080, Protocol: model.ProtoTCP}
	m.setRecord(7, model.MappingRecord{
		RuleID: 7, Status: model.MapStatusActive, InternalHost: "2001:db8::5", InternalPort: 8080,
		Description: pinholeDescription("v6-web", 321),
	})

	m.failPinholeRule(r, "2001:db8::5", errors.New("gateway exploded"))

	rec, ok := m.record(7)
	if !ok {
		t.Fatalf("failure was not recorded")
	}
	if rec.Status != model.MapStatusFailed {
		t.Errorf("status = %q, want failed", rec.Status)
	}
	id, ok := pinholeIDFromRecord(rec)
	if !ok || id != 321 {
		t.Errorf("unique id after a failed renewal = %d, %v; want 321 preserved", id, ok)
	}
}

// gatewayLabel / error path: a v4-only gateway must reject an IPv6 rule with an
// explanation that names the gateway and the missing service, not a bare fault.
func TestMapperIPv6WithoutPinholeGatewayFailsActionably(t *testing.T) {
	m, st := newMapperForTest(t)

	gw := gatewayInfo(proto.KindIGD2, "map-tcp", "map-udp")
	gw.Info.Name = "FakeRouter"
	gw.Info.Address = "192.168.1.1"
	m.mu.Lock()
	m.providers = []proto.DiscoveredGateway{gw}
	m.preferred = providerKey(gw.Info)
	m.mu.Unlock()

	ruleID, err := st.CreateRule(&model.Rule{
		Name: "v6-web", Host: "2001:db8::5", Port: 8080, Protocol: model.ProtoTCP, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	rule, _ := st.GetRule(ruleID)

	_, err = m.ApplyRule(context.Background(), *rule)
	if err == nil {
		t.Fatalf("expected an actionable error for an IPv6 rule on a v4-only gateway")
	}
	msg := err.Error()
	for _, want := range []string{"IPv6FirewallControl", "pinhole", "FakeRouter", "2001:db8::5"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}

	rec := m.Records()[ruleID]
	if rec.Status != model.MapStatusFailed {
		t.Errorf("record status = %q, want failed", rec.Status)
	}
	if !strings.Contains(rec.Error, "IPv6FirewallControl") {
		t.Errorf("persisted error = %q", rec.Error)
	}
}

// fakePinholeGateway is a canned WANIPv6FirewallControl:1 control point.
type fakePinholeGateway struct {
	mu          sync.Mutex
	status      int
	adds        []map[string]string
	deletes     []uint16
	nextID      uint16
	addFault    string
	deleteFault string
}

func newFakePinholeGateway() *fakePinholeGateway {
	return &fakePinholeGateway{nextID: 4242}
}

func (f *fakePinholeGateway) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	body := string(raw)
	action := strings.Trim(r.Header.Get("SOAPAction"), `"`)
	if i := strings.LastIndex(action, "#"); i >= 0 {
		action = action[i+1:]
	}
	respond := func(inner string) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%sResponse>%s</u:%sResponse></s:Body></s:Envelope>`,
			action, inner, action)
	}
	fault := func(code, desc string) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError><errorCode>%s</errorCode><errorDescription>%s</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, code, desc)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	switch action {
	case "GetFirewallStatus":
		f.status++
		respond(`<NewFirewallEnabled>1</NewFirewallEnabled><NewInboundPinholeAllowed>1</NewInboundPinholeAllowed>`)
	case "AddPinhole":
		if f.addFault != "" {
			fault(f.addFault, "add failed")
			return
		}
		call := map[string]string{
			"RemoteHost":     extractTag(body, "RemoteHost"),
			"RemotePort":     extractTag(body, "RemotePort"),
			"InternalClient": extractTag(body, "InternalClient"),
			"InternalPort":   extractTag(body, "InternalPort"),
			"Protocol":       extractTag(body, "Protocol"),
			"LeaseTime":      extractTag(body, "LeaseTime"),
		}
		f.adds = append(f.adds, call)
		respond(fmt.Sprintf("<UniqueID>%d</UniqueID>", f.nextID))
	case "DeletePinhole":
		if f.deleteFault != "" {
			fault(f.deleteFault, "delete failed")
			return
		}
		id, _ := strconv.Atoi(extractTag(body, "UniqueID"))
		f.deletes = append(f.deletes, uint16(id))
		respond("")
	default:
		// Anything else means the mapper took the NAT mapping path for a v6 rule.
		fault("401", "Invalid Action")
	}
}

func (f *fakePinholeGateway) snapshot() (adds []map[string]string, deletes []uint16, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string{}, f.adds...), append([]uint16{}, f.deletes...), f.status
}

func newPinholeIGDProvider(t *testing.T) (*proto.IGDClient, *fakePinholeGateway) {
	t.Helper()
	fake := newFakePinholeGateway()
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	client := proto.NewIGDClient(proto.IGDConfig{
		Kind:                    proto.KindIGD2,
		Address:                 "192.168.1.1",
		Port:                    80,
		ControlURL:              srv.URL + "/ctl/IPConn",
		ServiceType:             "urn:schemas-upnp-org:service:WANIPConnection:2",
		IPv6FirewallControlURL:  srv.URL + "/ctl/IPv6FC",
		IPv6FirewallServiceType: "urn:schemas-upnp-org:service:WANIPv6FirewallControl:1",
		Name:                    "FakeIPv6Router",
		Timeout:                 3 * time.Second,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client, fake
}

func injectProvider(m *Mapper, provider proto.Provider) {
	m.mu.Lock()
	m.providers = []proto.DiscoveredGateway{{Info: provider.Info(), Provider: provider}}
	m.preferred = providerKey(provider.Info())
	m.mu.Unlock()
}

// TestReconcileIPv6Pinhole is the end-to-end mapper test: an IPv6 rule must
// create a pinhole (never an AddMapping), persist the gateway id, be a no-op on
// the next pass, and delete the pinhole when the rule is disabled.
func TestReconcileIPv6Pinhole(t *testing.T) {
	provider, fake := newPinholeIGDProvider(t)
	m, st := newMapperForTest(t)
	injectProvider(m, provider)

	if !m.hasPinholeProvider() {
		t.Fatalf("injected provider was not recognised as pinhole capable")
	}

	ruleID, err := st.CreateRule(&model.Rule{
		Name: "v6-web", Host: "2001:db8::5", Port: 8080, Protocol: model.ProtoTCP, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	sum, err := m.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if sum.Created != 1 || sum.Failed != 0 {
		t.Fatalf("reconcile summary = %+v", sum)
	}

	adds, _, _ := fake.snapshot()
	if len(adds) != 1 {
		t.Fatalf("AddPinhole calls = %d, want 1 (a v6 rule must not use AddMapping)", len(adds))
	}
	want := map[string]string{
		"RemoteHost":     "",
		"RemotePort":     "0",
		"InternalClient": "2001:db8::5",
		"InternalPort":   "8080",
		"Protocol":       "6", // TCP
		"LeaseTime":      "86400",
	}
	for k, v := range want {
		if adds[0][k] != v {
			t.Errorf("AddPinhole %s = %q, want %q", k, adds[0][k], v)
		}
	}

	rec, ok := m.Records()[ruleID]
	if !ok {
		t.Fatalf("no mapping record for rule %d", ruleID)
	}
	if rec.Status != model.MapStatusActive {
		t.Errorf("status = %q, want active", rec.Status)
	}
	if rec.ExternalPort != 0 {
		t.Errorf("external port = %d, want 0 (pinholes forward the same port)", rec.ExternalPort)
	}
	if rec.InternalHost != "2001:db8::5" || rec.InternalPort != 8080 {
		t.Errorf("record target = %s:%d", rec.InternalHost, rec.InternalPort)
	}
	if rec.LeaseSeconds != 86400 {
		t.Errorf("lease = %d, want 86400", rec.LeaseSeconds)
	}
	if !rec.ExpiresAt.Valid {
		t.Errorf("pinhole record has no expiry")
	}
	id, ok := pinholeIDFromRecord(rec)
	if !ok || id != 4242 {
		t.Fatalf("persisted pinhole id = %d, %v; want 4242", id, ok)
	}

	// The id must be in the database too, otherwise a restart cannot delete it.
	rows, err := st.ListMappings([]int64{ruleID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListMappings = %d rows, %v", len(rows), err)
	}
	if got, ok := pinholeIDFromDescription(rows[0].Description); !ok || got != 4242 {
		t.Errorf("persisted description %q does not carry the pinhole id", rows[0].Description)
	}

	// An unchanged rule must not open a second pinhole.
	sum, err = m.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if sum.Created != 0 || sum.Failed != 0 {
		t.Errorf("second summary = %+v", sum)
	}
	adds, deletes, statusCalls := fake.snapshot()
	if len(adds) != 1 {
		t.Errorf("AddPinhole calls after an unchanged reconcile = %d, want 1", len(adds))
	}
	if statusCalls != 1 {
		t.Errorf("GetFirewallStatus calls = %d, want 1 (cached)", statusCalls)
	}
	if len(deletes) != 0 {
		t.Errorf("unexpected DeletePinhole calls: %v", deletes)
	}

	// Disabling the rule must delete the pinhole by its unique id.
	rule, _ := st.GetRule(ruleID)
	rule.Enabled = false
	if err := st.UpdateRule(rule); err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if _, err := m.ApplyRule(context.Background(), *rule); err != nil {
		t.Fatalf("ApplyRule(disable): %v", err)
	}
	_, deletes, _ = fake.snapshot()
	if len(deletes) != 1 || deletes[0] != 4242 {
		t.Errorf("DeletePinhole calls = %v, want [4242]", deletes)
	}
	if _, ok := m.Records()[ruleID]; ok {
		t.Errorf("local record survived pinhole removal")
	}
	rows, _ = st.ListMappings([]int64{ruleID})
	if len(rows) != 0 {
		t.Errorf("persisted mapping rows survived removal: %d", len(rows))
	}
}

// A gateway that stopped advertising the service must not make removal fail.
func TestRemovePinholeNoOpWhenGatewayLosesService(t *testing.T) {
	provider, fake := newPinholeIGDProvider(t)
	m, st := newMapperForTest(t)
	injectProvider(m, provider)

	ruleID, _ := st.CreateRule(&model.Rule{
		Name: "v6-web", Host: "2001:db8::7", Port: 9000, Protocol: model.ProtoTCP, Enabled: true,
	})
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// The gateway is re-discovered without WANIPv6FirewallControl.
	v4Only := gatewayInfo(proto.KindIGD2, "map-tcp", "map-udp")
	v4Only.Info.Name = "FakeRouter"
	injectProvider(m, fakeProvider{info: v4Only.Info})

	rule, _ := st.GetRule(ruleID)
	rule.Enabled = false
	if err := st.UpdateRule(rule); err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if _, err := m.ApplyRule(context.Background(), *rule); err != nil {
		t.Fatalf("removal must be a no-op when the service disappeared: %v", err)
	}
	if _, ok := m.Records()[ruleID]; ok {
		t.Errorf("local record survived removal")
	}
	if _, deletes, _ := fake.snapshot(); len(deletes) != 0 {
		t.Errorf("unexpected DeletePinhole calls: %v", deletes)
	}
}

// 704 NoSuchEntry means the pinhole is already gone; removal must succeed.
func TestRemovePinholeTreatsNoSuchEntryAsGone(t *testing.T) {
	provider, fake := newPinholeIGDProvider(t)
	m, st := newMapperForTest(t)
	injectProvider(m, provider)

	fake.mu.Lock()
	fake.deleteFault = "704"
	fake.mu.Unlock()

	ruleID, _ := st.CreateRule(&model.Rule{
		Name: "v6-web", Host: "2001:db8::8", Port: 9001, Protocol: model.ProtoUDP, Enabled: true,
	})
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	rule, _ := st.GetRule(ruleID)
	rule.Enabled = false
	if err := st.UpdateRule(rule); err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if _, err := m.ApplyRule(context.Background(), *rule); err != nil {
		t.Fatalf("704 must be treated as already gone, got: %v", err)
	}
	if _, ok := m.Records()[ruleID]; ok {
		t.Errorf("local record survived removal")
	}
}

// fakeProvider is a Provider with a fixed description and no live backend; used
// to simulate a gateway that lost the IPv6 service after discovery.
type fakeProvider struct {
	info proto.GatewayInfo
}

func (f fakeProvider) Info() proto.GatewayInfo { return f.info }
func (f fakeProvider) ExternalIP(context.Context) (string, error) {
	return "", proto.ErrNotSupported
}
func (f fakeProvider) AddMapping(context.Context, proto.Mapping) (proto.Result, error) {
	return proto.Result{}, proto.ErrNotSupported
}
func (f fakeProvider) DeleteMapping(context.Context, proto.Mapping) error {
	return proto.ErrNotSupported
}
func (f fakeProvider) ListMappings(context.Context) ([]proto.Mapping, error) {
	return nil, proto.ErrNotSupported
}
func (f fakeProvider) Close() error { return nil }

// Two IPv6 rules on the same port are different pinholes, not an external-port
// conflict, so conflict detection must leave both alone.
func TestConflictDetectionIgnoresIPv6Rules(t *testing.T) {
	m, st := newMapperForTest(t)
	if _, err := st.CreateRule(&model.Rule{
		Host: "2001:db8::10", Port: 5001, ExtPort: 5555, Protocol: model.ProtoTCP, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if _, err := st.CreateRule(&model.Rule{
		Host: "2001:db8::11", Port: 5002, ExtPort: 5555, Protocol: model.ProtoTCP, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	rules, _ := st.EnabledRules()
	m.detectConflicts(rules)
	if got := m.Conflicts(); len(got) != 0 {
		t.Errorf("IPv6 rules were marked as external-port conflicts: %v", got)
	}
}
