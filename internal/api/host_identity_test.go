package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ekko-studio/natpilot/internal/auth"
	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/mapper"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/store"
)

func newIdentityTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
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
	cfg.HTTP.SessionSecret = "test-session-secret-0123456789"
	cfg.Log.Level = "off"
	cfg.SetAuthEnabled(false)
	app := &App{Store: st, Config: cfg, Auth: auth.New(st, cfg), Mapper: mapper.New(st, cfg, nil)}
	return NewServer(app), st
}

func doJSON(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestPatchHostIdentityPersistsAcrossDiscovery: a name corrected by the user
// must stick to the MAC and survive a later discovery observation that carries
// no hostname (the case where a PTR lookup fails or a source has no name).
func TestPatchHostIdentityPersistsAcrossDiscovery(t *testing.T) {
	srv, st := newIdentityTestServer(t)

	host := &model.Host{
		IP: "192.168.1.130", MAC: "aa:bb:cc:dd:ee:30", Hostname: "router", Source: "arp",
	}
	if err := st.UpsertHost(host); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	// Reverse DNS must not run for a host that already has a name: the helper
	// returns "" and UpsertHost therefore keeps the stored one.
	if got := srv.hostnameIfUnknown(context.Background(), host.IP, host.MAC); got != "" {
		t.Fatalf("hostnameIfUnknown returned %q for a named host", got)
	}

	rec := doJSON(t, srv, http.MethodPatch, "/api/hosts/"+strconv.FormatInt(host.ID, 10),
		`{"hostname":"office-router","note":"living room","tags":["edge","lab"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/hosts/%d = %d: %s", host.ID, rec.Code, rec.Body.String())
	}
	var patched struct {
		OK   bool       `json:"ok"`
		Host model.Host `json:"host"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode patch response: %v", err)
	}
	if !patched.OK {
		t.Fatalf("patch response = %s", rec.Body.String())
	}
	if patched.Host.Hostname != "office-router" || patched.Host.Note != "living room" {
		t.Fatalf("patch response host = %+v", patched.Host)
	}

	got, err := st.GetHostByID(host.ID)
	if err != nil || got == nil {
		t.Fatalf("GetHostByID: %v", err)
	}
	if got.Hostname != "office-router" || got.Note != "living room" {
		t.Fatalf("patch not persisted: %+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "edge" || got.Tags[1] != "lab" {
		t.Fatalf("patched tags = %v", got.Tags)
	}

	// A later discovery observation with no hostname/note/tags must not erase
	// the corrected identity.
	if err := st.UpsertHost(&model.Host{IP: host.IP, MAC: host.MAC, Source: "arp"}); err != nil {
		t.Fatalf("later discovery: %v", err)
	}
	got, _ = st.GetHostByID(host.ID)
	if got.Hostname != "office-router" || got.Note != "living room" {
		t.Fatalf("identity erased by discovery: %+v", got)
	}
	if len(got.Tags) != 2 {
		t.Fatalf("tags erased by discovery: %v", got.Tags)
	}

	// Partial patches only change the fields that are present.
	rec = doJSON(t, srv, http.MethodPatch, "/api/hosts/"+strconv.FormatInt(host.ID, 10),
		`{"note":"rack"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("partial PATCH = %d: %s", rec.Code, rec.Body.String())
	}
	got, _ = st.GetHostByID(host.ID)
	if got.Hostname != "office-router" || got.Note != "rack" || len(got.Tags) != 2 {
		t.Fatalf("partial patch clobbered other fields: %+v", got)
	}
}

// TestPatchHostIdentityUnknownHost covers the route's 404 path.
func TestPatchHostIdentityUnknownHost(t *testing.T) {
	srv, _ := newIdentityTestServer(t)
	rec := doJSON(t, srv, http.MethodPatch, "/api/hosts/9999", `{"hostname":"ghost"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PATCH unknown host = %d, want 404", rec.Code)
	}
}

// TestPortAndRuleResponsesCarryHostIdentity checks the JSON contract added to
// /api/ports and /api/rules: host_id, family and the joined MAC/hostname.
func TestPortAndRuleResponsesCarryHostIdentity(t *testing.T) {
	srv, st := newIdentityTestServer(t)

	host := &model.Host{
		IP: "192.168.1.131", MAC: "aa:bb:cc:dd:ee:31", Hostname: "camera", Source: "arp",
	}
	if err := st.UpsertHost(host); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	if _, _, err := st.UpsertPort(&model.DiscoveredPort{
		Host: host.IP, Port: 554, Protocol: model.ProtoTCP,
		Status: model.StatusNotAdded, TargetKind: model.ScanTargetLocal,
	}); err != nil {
		t.Fatalf("seed port: %v", err)
	}
	if _, err := st.CreateRule(&model.Rule{
		Name: "camera", Host: host.IP, Port: 554, Protocol: model.ProtoTCP,
		Enabled: true, Source: "test",
	}); err != nil {
		t.Fatalf("seed rule: %v", err)
	}

	rec := doJSON(t, srv, http.MethodGet, "/api/ports", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/ports = %d: %s", rec.Code, rec.Body.String())
	}
	var portsResp struct {
		Ports []model.DiscoveredPort `json:"ports"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &portsResp); err != nil {
		t.Fatalf("decode ports: %v", err)
	}
	if len(portsResp.Ports) != 1 {
		t.Fatalf("ports = %+v", portsResp.Ports)
	}
	p := portsResp.Ports[0]
	if p.HostID != host.ID || p.Family != model.FamilyIPv4 || p.MAC != host.MAC || p.Hostname != host.Hostname {
		t.Fatalf("port identity = host_id %d family %q mac %q hostname %q", p.HostID, p.Family, p.MAC, p.Hostname)
	}

	rec = doJSON(t, srv, http.MethodGet, "/api/rules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/rules = %d: %s", rec.Code, rec.Body.String())
	}
	var rulesResp struct {
		Rules []model.Rule `json:"rules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rulesResp); err != nil {
		t.Fatalf("decode rules: %v", err)
	}
	if len(rulesResp.Rules) != 1 {
		t.Fatalf("rules = %+v", rulesResp.Rules)
	}
	r := rulesResp.Rules[0]
	if r.HostID != host.ID || r.Family != model.FamilyIPv4 || r.MAC != host.MAC || r.Hostname != host.Hostname {
		t.Fatalf("rule identity = host_id %d family %q mac %q hostname %q", r.HostID, r.Family, r.MAC, r.Hostname)
	}
}
