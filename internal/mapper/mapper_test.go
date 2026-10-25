package mapper

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/netutil"
	"github.com/ekko-studio/natpilot/internal/proto"
	"github.com/ekko-studio/natpilot/internal/store"
)

// fakeIGD is a minimal UPnP IGD v2 control point used to exercise the real
// mapping path without a physical router.
type fakeIGD struct {
	mu       sync.Mutex
	mappings map[string]string // "proto/extport" -> internal client
	calls    int
}

func newFakeIGD() *fakeIGD { return &fakeIGD{mappings: map[string]string{}} }

func (f *fakeIGD) description(host string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:2</deviceType>
    <friendlyName>Fake IGD</friendlyName>
    <manufacturer>TestCo</manufacturer>
    <modelName>FakeRouter</modelName>
    <UDN>uuid:test</UDN>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANDevice:2</deviceType>
        <serviceList>
          <service>
            <serviceType>urn:schemas-upnp-org:service:WANIPConnection:2</serviceType>
            <controlURL>http://%s/ctl/IPConn</controlURL>
          </service>
        </serviceList>
      </device>
    </deviceList>
  </device>
</root>`, host)
}

func (f *fakeIGD) handler(host string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "rootDesc"):
			w.Header().Set("Content-Type", "text/xml")
			fmt.Fprint(w, f.description(host))
			return
		}
		action := r.Header.Get("SOAPAction")
		body := make([]byte, 8192)
		n, _ := r.Body.Read(body)
		payload := string(body[:n])
		f.mu.Lock()
		f.calls++
		f.mu.Unlock()

		respond := func(inner string) {
			w.Header().Set("Content-Type", "text/xml")
			fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:Response>%s</u:Response></s:Body></s:Envelope>`, inner)
		}
		soapFault := func(code, desc string) {
			w.Header().Set("Content-Type", "text/xml")
			fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError><errorCode>%s</errorCode><errorDescription>%s</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, code, desc)
		}

		switch {
		case strings.Contains(action, "GetExternalIPAddress"):
			respond("<NewExternalIPAddress>203.0.113.55</NewExternalIPAddress>")
		case strings.Contains(action, "AddAnyPortMapping"):
			ext := extractTag(payload, "NewExternalPort")
			protoName := extractTag(payload, "NewProtocol")
			client := extractTag(payload, "NewInternalClient")
			f.mu.Lock()
			f.mappings[protoName+"/"+ext] = client
			f.mu.Unlock()
			respond("<NewReservedPort>" + ext + "</NewReservedPort>")
		case strings.Contains(action, "AddPortMapping"):
			ext := extractTag(payload, "NewExternalPort")
			protoName := extractTag(payload, "NewProtocol")
			client := extractTag(payload, "NewInternalClient")
			f.mu.Lock()
			f.mappings[protoName+"/"+ext] = client
			f.mu.Unlock()
			respond("")
		case strings.Contains(action, "DeletePortMapping"):
			ext := extractTag(payload, "NewExternalPort")
			protoName := extractTag(payload, "NewProtocol")
			f.mu.Lock()
			key := protoName + "/" + ext
			_, existed := f.mappings[key]
			delete(f.mappings, key)
			f.mu.Unlock()
			if !existed {
				soapFault("714", "NoSuchEntryInArray")
				return
			}
			respond("")
		case strings.Contains(action, "GetGenericPortMappingEntry"):
			idx := extractTag(payload, "NewPortMappingIndex")
			if idx != "0" {
				soapFault("713", "SpecifiedArrayIndexInvalid")
				return
			}
			f.mu.Lock()
			entry := ""
			for k, v := range f.mappings {
				parts := strings.SplitN(k, "/", 2)
				entry = "<NewRemoteHost></NewRemoteHost><NewExternalPort>" + parts[1] +
					"</NewExternalPort><NewProtocol>" + parts[0] +
					"</NewProtocol><NewInternalPort>" + parts[1] +
					"</NewInternalPort><NewInternalClient>" + v +
					"</NewInternalClient><NewEnabled>1</NewEnabled>" +
					"<NewPortMappingDescription>natpilot</NewPortMappingDescription>" +
					"<NewLeaseDuration>0</NewLeaseDuration>"
				break
			}
			f.mu.Unlock()
			if entry == "" {
				soapFault("713", "SpecifiedArrayIndexInvalid")
				return
			}
			respond(entry)
		default:
			soapFault("401", "Invalid Action")
		}
	}
}

func extractTag(body, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	i := strings.Index(body, open)
	if i < 0 {
		return ""
	}
	i += len(open)
	j := strings.Index(body[i:], closeTag)
	if j < 0 {
		return ""
	}
	return body[i : i+j]
}

// newFakeIGDProvider stands up the fake device and returns a connected client.
func newFakeIGDProvider(t *testing.T) (*proto.IGDClient, *fakeIGD) {
	t.Helper()
	fake := newFakeIGD()
	// The control URL must point at the test server, so the server is created
	// first and the description is rendered with its address.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "rootDesc") {
			w.Header().Set("Content-Type", "text/xml")
			fmt.Fprint(w, fake.description(strings.TrimPrefix(srv.URL, "http://")))
			return
		}
		fake.handler(strings.TrimPrefix(srv.URL, "http://"))(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg, _, err := proto.DescribeGateway(context.Background(), srv.URL+"/rootDesc.xml", 3*time.Second)
	if err != nil {
		t.Fatalf("DescribeGateway: %v", err)
	}
	if cfg == nil || cfg.Kind != proto.KindIGD2 {
		t.Fatalf("expected IGD v2, got %+v", cfg)
	}
	return proto.NewIGDClient(*cfg), fake
}

func newMapperForTest(t *testing.T) (*Mapper, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	st, err := store.Open(path+"?_busy_timeout=5000&_foreign_keys=on", path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := config.Default()
	cfg.App.DataDir = dir
	return New(st, cfg, nil), st
}

func TestIsLocalHostSpec(t *testing.T) {
	for _, h := range []string{"", " ", "this", "self", "localhost", "LOCALHOST", "127.0.0.1", "127.0.0.5", "::1", "[::1]", "0.0.0.0", "::"} {
		if !isLocalHostSpec(h) {
			t.Errorf("isLocalHostSpec(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"192.168.1.20", "10.0.0.5", "fd00::1", "example.lan"} {
		if isLocalHostSpec(h) {
			t.Errorf("isLocalHostSpec(%q) = true, want false", h)
		}
	}
}

// A rule stored without an explicit host used to keep 127.0.0.1, which no router
// can forward to; it must be translated to this machine's LAN address.
func TestEffectiveHostReplacesLoopback(t *testing.T) {
	want := "127.0.0.1"
	if ip, err := netutil.PrimaryIPv4(); err == nil && ip != "" {
		want = ip
	}
	for _, spec := range []string{"", "this", "self", "localhost", "127.0.0.1", "::1", "0.0.0.0"} {
		r := &model.Rule{Host: spec}
		if got := effectiveHost(nil, r); got != want {
			t.Errorf("effectiveHost(%q) = %q, want %q", spec, got, want)
		}
	}
	if got := effectiveHost(nil, &model.Rule{Host: "192.168.1.20"}); got != "192.168.1.20" {
		t.Errorf("explicit LAN host rewritten to %q", got)
	}
}

// Disabled rules are never reconciled, so their live gateway mapping can only be
// torn down if the record is restored at start-up. Enabled rules are deliberately
// skipped so that every start re-provisions them from scratch.
func TestLoadRecordsRestoresOnlyDisabledRules(t *testing.T) {
	m, st := newMapperForTest(t)

	disabledID, err := st.CreateRule(&model.Rule{Host: "192.168.1.20", Port: 8080, Protocol: "tcp", Name: "off"})
	if err != nil {
		t.Fatalf("create disabled rule: %v", err)
	}
	enabledID, err := st.CreateRule(&model.Rule{Host: "192.168.1.21", Port: 8081, Protocol: "tcp", Name: "on", Enabled: true})
	if err != nil {
		t.Fatalf("create enabled rule: %v", err)
	}
	for _, id := range []int64{disabledID, enabledID} {
		if err := st.UpsertMapping(&model.MappingRecord{
			RuleID: id, ProviderName: "igd2", Protocol: "tcp",
			Status: model.MapStatusActive, ExternalPort: 9080, InternalHost: "192.168.1.20", InternalPort: 8080,
		}); err != nil {
			t.Fatalf("upsert mapping: %v", err)
		}
	}

	m.LoadRecords()

	if _, ok := m.record(disabledID); !ok {
		t.Errorf("disabled rule mapping was not restored, so cleanup can never find it")
	}
	if _, ok := m.record(enabledID); ok {
		t.Errorf("enabled rule mapping must not be restored (it is re-provisioned on reconcile)")
	}
}

func TestReconcileAgainstFakeIGD(t *testing.T) {
	provider, fake := newFakeIGDProvider(t)
	m, st := newMapperForTest(t)

	// Inject the provider directly so no network discovery is required.
	m.mu.Lock()
	m.providers = []proto.DiscoveredGateway{{Info: provider.Info(), Provider: provider}}
	m.preferred = providerKey(provider.Info())
	m.mu.Unlock()

	ruleID, err := st.CreateRule(&model.Rule{
		Name: "web", Host: "127.0.0.1", Port: 8080, Protocol: model.ProtoTCP, Enabled: true, Source: "manual",
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

	fake.mu.Lock()
	_, staged := fake.mappings["TCP/8080"]
	fake.mu.Unlock()
	if !staged {
		t.Fatalf("gateway did not receive the mapping: %v", fake.mappings)
	}

	rec, ok := m.Records()[ruleID]
	if !ok {
		t.Fatalf("no mapping record for rule %d", ruleID)
	}
	if rec.Status != model.MapStatusActive {
		t.Errorf("status = %q, want active", rec.Status)
	}
	if rec.ExternalIP != "203.0.113.55" {
		t.Errorf("external IP = %q", rec.ExternalIP)
	}
	if rec.ExternalPort != 8080 {
		t.Errorf("external port = %d", rec.ExternalPort)
	}
	if !strings.HasPrefix(rec.ProviderName, "igd2") {
		t.Errorf("provider = %q", rec.ProviderName)
	}

	// A second reconcile with no rule change is a no-op (no new gateway calls
	// beyond the lease check).
	before := fake.calls
	sum, err = m.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if sum.Created != 0 || sum.Failed != 0 {
		t.Errorf("second summary = %+v (an unchanged mapping must not be recreated)", sum)
	}
	if fake.calls != before {
		t.Errorf("gateway was contacted again (%d -> %d) for an unchanged rule", before, fake.calls)
	}

	// Disabling the rule tears the mapping down on the gateway.
	rule, _ := st.GetRule(ruleID)
	rule.Enabled = false
	if err := st.UpdateRule(rule); err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if _, err := m.ApplyRule(context.Background(), *rule); err != nil {
		t.Fatalf("ApplyRule(disable): %v", err)
	}
	fake.mu.Lock()
	_, stillThere := fake.mappings["TCP/8080"]
	fake.mu.Unlock()
	if stillThere {
		t.Errorf("mapping survived rule disable: %v", fake.mappings)
	}
}

func TestRemoveMappingOnRuleDelete(t *testing.T) {
	provider, fake := newFakeIGDProvider(t)
	m, st := newMapperForTest(t)
	m.mu.Lock()
	m.providers = []proto.DiscoveredGateway{{Info: provider.Info(), Provider: provider}}
	m.preferred = providerKey(provider.Info())
	m.mu.Unlock()

	ruleID, _ := st.CreateRule(&model.Rule{Host: "127.0.0.1", Port: 4444, Protocol: model.ProtoUDP, Enabled: true})
	rule, _ := st.GetRule(ruleID)
	if _, err := m.ApplyRule(context.Background(), *rule); err != nil {
		t.Fatalf("ApplyRule: %v", err)
	}
	fake.mu.Lock()
	_, ok := fake.mappings["UDP/4444"]
	fake.mu.Unlock()
	if !ok {
		t.Fatalf("UDP mapping was not created: %v", fake.mappings)
	}

	if err := m.RemoveMapping(context.Background(), *rule); err != nil {
		t.Fatalf("RemoveMapping: %v", err)
	}
	fake.mu.Lock()
	_, still := fake.mappings["UDP/4444"]
	fake.mu.Unlock()
	if still {
		t.Errorf("mapping not removed: %v", fake.mappings)
	}
	if _, ok := m.Records()[ruleID]; ok {
		t.Errorf("local record survived removal")
	}
}

func TestConflictDetectionSkipsDuplicateExternalPort(t *testing.T) {
	provider, fake := newFakeIGDProvider(t)
	m, st := newMapperForTest(t)
	m.mu.Lock()
	m.providers = []proto.DiscoveredGateway{{Info: provider.Info(), Provider: provider}}
	m.preferred = providerKey(provider.Info())
	m.mu.Unlock()

	// Two enabled rules that both want external port 5555.
	a, _ := st.CreateRule(&model.Rule{Host: "127.0.0.1", Port: 5001, ExtPort: 5555, Protocol: model.ProtoTCP, Enabled: true})
	b, _ := st.CreateRule(&model.Rule{Host: "127.0.0.1", Port: 5002, ExtPort: 5555, Protocol: model.ProtoTCP, Enabled: true})

	sum, err := m.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if sum.Failed != 1 {
		t.Errorf("expected exactly one conflict failure, got %+v", sum)
	}
	if sum.Created != 1 {
		t.Errorf("expected the first rule to be mapped, got %+v", sum)
	}
	conflicts := m.Conflicts()
	// Only the later duplicate is marked; the first rule keeps the port.
	if len(conflicts) != 1 {
		t.Errorf("conflict map = %v, want only the duplicate marked", conflicts)
	}
	if conflicts[b] != a {
		t.Errorf("conflicts = %v, want %d -> %d (owner)", conflicts, b, a)
	}
	if _, selfMarked := conflicts[a]; selfMarked {
		t.Errorf("the owning rule must not be marked as conflicting: %v", conflicts)
	}
	// Only one mapping must exist on the gateway.
	fake.mu.Lock()
	count := len(fake.mappings)
	fake.mu.Unlock()
	if count != 1 {
		t.Errorf("gateway holds %d mappings, want 1 (%v)", count, fake.mappings)
	}
	recs := m.Records()
	if recs[a].Status != model.MapStatusActive {
		t.Errorf("rule %d status = %q, want active", a, recs[a].Status)
	}
	if recs[b].Status != model.MapStatusConflict {
		t.Errorf("rule %d status = %q, want conflict", b, recs[b].Status)
	}
}

func TestDiscoverReturnsErrWhenNothingPresent(t *testing.T) {
	m, _ := newMapperForTest(t)
	// No providers and no reachable gateway: mapping must fail cleanly with a
	// descriptive error rather than panicking.
	rule, _ := m.store.CreateRule(&model.Rule{Host: "127.0.0.1", Port: 7777, Protocol: model.ProtoTCP, Enabled: true})
	r, _ := m.store.GetRule(rule)
	_, err := m.ApplyRule(context.Background(), *r)
	if err == nil {
		t.Fatalf("expected an error when no gateway is available")
	}
	if !strings.Contains(err.Error(), "gateway") {
		t.Errorf("error = %v, want a gateway related message", err)
	}
	rec := m.Records()[rule]
	if rec.Status != model.MapStatusFailed {
		t.Errorf("status = %q, want failed", rec.Status)
	}
}

func gatewayInfo(kind string, caps ...string) proto.DiscoveredGateway {
	return proto.DiscoveredGateway{Info: proto.GatewayInfo{
		Kind:         kind,
		Address:      "192.168.1.1",
		Port:         1900,
		Capabilities: caps,
	}}
}

// The old implementation returned true from both switch branches, so every rule
// went to the preferred gateway regardless of what it could actually carry.
func TestSupportsFiltersByCapability(t *testing.T) {
	tcpOnly := gatewayInfo(proto.KindIGD2, "map-tcp")
	if !supports(tcpOnly, "tcp") {
		t.Errorf("tcp-only gateway should support tcp")
	}
	if supports(tcpOnly, "udp") {
		t.Errorf("tcp-only gateway must not claim udp support")
	}
	if !supports(tcpOnly, "TCP") {
		t.Errorf("protocol matching must be case insensitive")
	}

	both := gatewayInfo(proto.KindIGD2, "map-tcp", "map-udp")
	if !supports(both, "udp") {
		t.Errorf("dual-protocol gateway should support udp")
	}

	// No capability list at all: fall back to what the transport speaks.
	bare := gatewayInfo(proto.KindNATPMP)
	if !supports(bare, "tcp") || !supports(bare, "udp") {
		t.Errorf("bare NAT-PMP gateway should support tcp and udp")
	}
	if supports(bare, "") != true {
		t.Errorf("empty protocol must not be filtered")
	}

	// Unknown protocol names have no capability vocabulary: let the gateway decide.
	if !supports(tcpOnly, "sctp") {
		t.Errorf("unknown protocols must not be silently dropped")
	}

	// Unknown kind without capabilities cannot be assumed to work.
	if supports(proto.DiscoveredGateway{Info: proto.GatewayInfo{Kind: "mystery"}}, "tcp") {
		t.Errorf("unknown gateway kind must not be assumed capable")
	}
}

// A preferred gateway that cannot carry the protocol must not be tried first.
func TestCandidatesSkipsIncapablePreferredGateway(t *testing.T) {
	m, _ := newMapperForTest(t)

	tcpPreferred := gatewayInfo(proto.KindIGD2, "map-tcp")
	udpCapable := gatewayInfo(proto.KindPCP, "map-tcp", "map-udp")

	m.mu.Lock()
	m.providers = []proto.DiscoveredGateway{tcpPreferred, udpCapable}
	m.preferred = providerKey(tcpPreferred.Info)
	m.mu.Unlock()

	udpCandidates := m.candidates("udp")
	if len(udpCandidates) != 1 {
		t.Fatalf("udp candidates = %d, want 1", len(udpCandidates))
	}
	if udpCandidates[0].Info.Kind != proto.KindPCP {
		t.Errorf("udp candidate = %q, want the PCP gateway", udpCandidates[0].Info.Kind)
	}

	tcpCandidates := m.candidates("tcp")
	if len(tcpCandidates) != 2 {
		t.Fatalf("tcp candidates = %d, want 2", len(tcpCandidates))
	}
	if providerKey(tcpCandidates[0].Info) != m.preferred {
		t.Errorf("tcp candidates must lead with the preferred gateway")
	}
}
