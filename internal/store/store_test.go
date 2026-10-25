package store

import (
	"path/filepath"
	"testing"

	"github.com/ekko-studio/natpilot/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	st, err := Open(path+"?_busy_timeout=5000&_foreign_keys=on", path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func TestRuleCRUD(t *testing.T) {
	st := newTestStore(t)

	id, err := st.CreateRule(&model.Rule{
		Name: "web", Host: "127.0.0.1", Port: 8080, Protocol: model.ProtoTCP,
		Tags: model.StringList{"web", "dev"}, Enabled: true, Source: "test",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	got, err := st.GetRule(id)
	if err != nil || got == nil {
		t.Fatalf("GetRule(%d): %v", id, err)
	}
	if got.Port != 8080 || got.Name != "web" || !got.Enabled {
		t.Errorf("rule round trip mismatch: %+v", got)
	}
	if len(got.Tags) != 2 {
		t.Errorf("tags = %v", got.Tags)
	}

	// The (host,port,protocol) tuple is unique.
	if _, err := st.CreateRule(&model.Rule{Host: "127.0.0.1", Port: 8080, Protocol: model.ProtoTCP}); err == nil {
		t.Errorf("expected a unique constraint violation for a duplicate rule")
	}

	got.Note = "updated"
	got.Tags = model.StringList{"ops"}
	if err := st.UpdateRule(got); err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	again, _ := st.GetRule(id)
	if again.Note != "updated" || len(again.Tags) != 1 || again.Tags[0] != "ops" {
		t.Errorf("update not persisted: %+v", again)
	}

	// Filtering by tag and enabled state.
	list, total, err := st.ListRules(RuleFilter{Tags: []string{"ops"}})
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Errorf("tag filter returned %d/%d", len(list), total)
	}
	disabled := false
	list, total, _ = st.ListRules(RuleFilter{Enabled: &disabled})
	if total != 0 {
		t.Errorf("disabled filter returned %d", total)
	}

	if err := st.DeleteRule(id); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	gone, _ := st.GetRule(id)
	if gone != nil {
		t.Errorf("rule still present after delete")
	}
}

func TestEnabledRulesAndToggle(t *testing.T) {
	st := newTestStore(t)
	a, _ := st.CreateRule(&model.Rule{Host: "127.0.0.1", Port: 1001, Protocol: model.ProtoTCP, Enabled: true})
	b, _ := st.CreateRule(&model.Rule{Host: "127.0.0.1", Port: 1002, Protocol: model.ProtoTCP, Enabled: false})

	enabled, err := st.EnabledRules()
	if err != nil {
		t.Fatalf("EnabledRules: %v", err)
	}
	if len(enabled) != 1 || enabled[0].ID != a {
		t.Fatalf("enabled rules = %+v, want just %d", enabled, a)
	}

	n, err := st.SetRulesEnabled([]int64{b}, true)
	if err != nil || n != 1 {
		t.Fatalf("SetRulesEnabled: n=%d err=%v", n, err)
	}
	enabled, _ = st.EnabledRules()
	if len(enabled) != 2 {
		t.Errorf("expected 2 enabled rules, got %d", len(enabled))
	}
}

func TestPortLifecycle(t *testing.T) {
	st := newTestStore(t)

	id, created, err := st.UpsertPort(&model.DiscoveredPort{
		Host: "127.0.0.1", Port: 3000, Protocol: model.ProtoTCP,
		Status: model.StatusNotAdded, TargetKind: model.ScanTargetLocal,
		Service: "http-alt", Process: "node",
	})
	if err != nil || !created {
		t.Fatalf("UpsertPort: created=%v err=%v", created, err)
	}

	// Upserting the same socket updates rather than duplicating.
	id2, created2, _ := st.UpsertPort(&model.DiscoveredPort{
		Host: "127.0.0.1", Port: 3000, Protocol: model.ProtoTCP, Status: model.StatusNotAdded,
	})
	if created2 || id2 != id {
		t.Errorf("second upsert created a new row (id=%d created=%v)", id2, created2)
	}

	// Adding a rule marks the port as managed.
	ruleID, _ := st.CreateRule(&model.Rule{Host: "127.0.0.1", Port: 3000, Protocol: model.ProtoTCP, Enabled: true})
	if err := st.MarkPortAdded(id, ruleID); err != nil {
		t.Fatalf("MarkPortAdded: %v", err)
	}
	p, _ := st.GetPort(id)
	if p.Status != model.StatusAdded || !p.Added || p.RuleID != ruleID {
		t.Errorf("port after add = %+v", p)
	}

	// Deleting the rule and re-reconciling turns the port back to "open".
	if err := st.DeleteRule(ruleID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if err := st.ReconcilePortStatuses(); err != nil {
		t.Fatalf("ReconcilePortStatuses: %v", err)
	}
	p, _ = st.GetPort(id)
	if p.Status != model.StatusOpen || p.Added {
		t.Errorf("port after rule removal = %+v", p)
	}

	counts, _ := st.CountPortsByStatus()
	if counts[model.StatusOpen] != 1 {
		t.Errorf("status counts = %v", counts)
	}
}

func TestMappingUpsertAndLatest(t *testing.T) {
	st := newTestStore(t)
	ruleID, _ := st.CreateRule(&model.Rule{Host: "127.0.0.1", Port: 9000, Protocol: model.ProtoTCP, Enabled: true})

	if err := st.UpsertMapping(&model.MappingRecord{
		RuleID: ruleID, ProviderName: "igd2|192.168.1.1|5000", Protocol: "tcp",
		Status: model.MapStatusFailed, Error: "no gateway",
	}); err != nil {
		t.Fatalf("UpsertMapping: %v", err)
	}
	// The same (rule, provider, protocol) tuple is updated in place.
	if err := st.UpsertMapping(&model.MappingRecord{
		RuleID: ruleID, ProviderName: "igd2|192.168.1.1|5000", Protocol: "tcp",
		Status: model.MapStatusActive, ExternalIP: "203.0.113.9", ExternalPort: 4433, LeaseSeconds: 3600,
	}); err != nil {
		t.Fatalf("UpsertMapping update: %v", err)
	}
	records, err := st.MappingsByRule()
	if err != nil {
		t.Fatalf("MappingsByRule: %v", err)
	}
	rec, ok := records[ruleID]
	if !ok {
		t.Fatalf("no mapping record for rule %d", ruleID)
	}
	if rec.Status != model.MapStatusActive || rec.ExternalPort != 4433 || rec.ExternalIP != "203.0.113.9" {
		t.Errorf("mapping record = %+v", rec)
	}
	if rec.LeaseSeconds != 3600 {
		t.Errorf("lease = %d", rec.LeaseSeconds)
	}
}

func TestPolicyAndSchedule(t *testing.T) {
	st := newTestStore(t)

	pid, err := st.CreatePolicy(&model.Policy{
		Name: "dev", PortRanges: "3000-3010", AutoAction: model.ActionWatch,
		Enabled: true, Priority: 5, Tags: model.StringList{"dev"},
	})
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	pol, _ := st.GetPolicy(pid)
	if pol == nil || pol.Name != "dev" || len(pol.Tags) != 1 {
		t.Fatalf("policy round trip = %+v", pol)
	}
	enabled, _ := st.EnabledPolicies()
	if len(enabled) != 1 {
		t.Errorf("enabled policies = %d", len(enabled))
	}

	sid, err := st.CreateSchedule(&model.Schedule{Name: "scan", Kind: "scan", Cron: "*/30 * * * *", Enabled: true})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if err := st.MarkScheduleRun(sid, "ok", model.Ptr(timeNow())); err != nil {
		t.Fatalf("MarkScheduleRun: %v", err)
	}
	sc, _ := st.GetSchedule(sid)
	if sc == nil || sc.RunCount != 1 || sc.LastStatus != "ok" {
		t.Errorf("schedule after run = %+v", sc)
	}
}

func TestUserAndSession(t *testing.T) {
	st := newTestStore(t)
	uid, err := st.CreateUser(&model.User{Username: "admin", DisplayName: "Admin", Role: "admin", Enabled: true, PasswordHash: "x"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := st.CreateUser(&model.User{Username: "admin"}); err == nil {
		t.Errorf("expected a duplicate username error")
	}
	u, _ := st.GetUserByName("admin")
	if u == nil || u.ID != uid {
		t.Fatalf("GetUserByName = %+v", u)
	}

	if err := st.CreateSession("tok123", uid, timeHour(), "127.0.0.1", "test"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, ok, _ := st.GetSession("tok123")
	if !ok || got != uid {
		t.Errorf("GetSession = %d/%v", got, ok)
	}
	if err := st.DeleteSession("tok123"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, ok, _ := st.GetSession("tok123"); ok {
		t.Errorf("session survived deletion")
	}
}

func TestSettings(t *testing.T) {
	st := newTestStore(t)
	if _, ok, _ := st.GetSetting("missing"); ok {
		t.Errorf("unexpected setting")
	}
	if err := st.SetSetting("config.mapper.dry_run", "true"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	v, ok, _ := st.GetSetting("config.mapper.dry_run")
	if !ok || v != "true" {
		t.Errorf("GetSetting = %q/%v", v, ok)
	}
	all, _ := st.AllSettings()
	if len(all) != 1 {
		t.Errorf("AllSettings = %v", all)
	}
}

func TestSettingsOverrideLayering(t *testing.T) {
	st := newTestStore(t)
	_ = st.SetSetting("config.mapper.dry_run", "true")
	_ = st.SetSetting("config.scanner.max_concurrency", "64")
	ov, err := st.AllSettings()
	if err != nil {
		t.Fatalf("AllSettings: %v", err)
	}
	if ov["config.mapper.dry_run"] != "true" || ov["config.scanner.max_concurrency"] != "64" {
		t.Errorf("overrides = %v", ov)
	}
}

// UpsertMapping used to be SELECT-then-INSERT, so the same rule/provider/protocol
// could produce two rows and later updates only touched the first one.
func TestUpsertMappingIsIdempotent(t *testing.T) {
	st := newTestStore(t)
	ruleID, err := st.CreateRule(&model.Rule{
		Name: "web", Host: "127.0.0.1", Port: 8080, Protocol: model.ProtoTCP,
		Enabled: true, Source: "test",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	rec := &model.MappingRecord{
		RuleID: ruleID, ProviderName: "igd2", Protocol: model.ProtoTCP,
		Status: model.MapStatusActive, ExternalPort: 8080, InternalPort: 8080,
		InternalHost: "192.168.1.10",
	}
	if err := st.UpsertMapping(rec); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	firstID := rec.ID
	if firstID == 0 {
		t.Fatal("first upsert did not report the row id")
	}

	rec.Status = model.MapStatusFailed
	rec.ExternalPort = 9090
	if err := st.UpsertMapping(rec); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if rec.ID != firstID {
		t.Errorf("second upsert changed the row id: %d -> %d", firstID, rec.ID)
	}

	rows, err := st.ListMappings([]int64{ruleID})
	if err != nil {
		t.Fatalf("ListMappings: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("mapping rows = %d, want 1 (duplicate rows must not be created)", len(rows))
	}
	if rows[0].Status != model.MapStatusFailed || rows[0].ExternalPort != 9090 {
		t.Errorf("row = %+v, want the updated values", rows[0])
	}
}

// Computing the next fire time of a schedule is not a run. It used to be persisted
// with MarkScheduleRun, which made a schedule that had never executed report
// run_count 1 and a last_run_at at the moment the daemon started.
func TestSetScheduleNextRunDoesNotCountARun(t *testing.T) {
	st := newTestStore(t)
	id, err := st.CreateSchedule(&model.Schedule{
		Name: "nightly", Kind: "scan", Enabled: true, Cron: "0 3 * * *",
		TargetKind: "local", PortsSpec: "8080",
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	next := timeNow().Add(6 * timeHour())
	if err := st.SetScheduleNextRun(id, model.Ptr(next)); err != nil {
		t.Fatalf("SetScheduleNextRun: %v", err)
	}
	sc, err := st.GetSchedule(id)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if sc == nil {
		t.Fatal("GetSchedule returned nil")
	}
	if sc.RunCount != 0 {
		t.Errorf("RunCount = %d after only computing the next run, want 0", sc.RunCount)
	}
	if sc.LastRunAt.Valid {
		t.Errorf("LastRunAt = %v after only computing the next run, want unset", sc.LastRunAt)
	}
	if sc.LastStatus != "" {
		t.Errorf("LastStatus = %q after only computing the next run, want empty", sc.LastStatus)
	}
	if !sc.NextRunAt.Valid {
		t.Error("NextRunAt was not persisted")
	}

	// A real run still updates all three fields.
	if err := st.MarkScheduleRun(id, "ok", model.Ptr(next)); err != nil {
		t.Fatalf("MarkScheduleRun: %v", err)
	}
	sc, _ = st.GetSchedule(id)
	if sc.RunCount != 1 || sc.LastStatus != "ok" || !sc.LastRunAt.Valid {
		t.Errorf("schedule after a real run = %+v", sc)
	}
}
