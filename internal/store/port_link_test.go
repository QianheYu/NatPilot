package store

import (
	"testing"

	"github.com/ekko-studio/natpilot/internal/model"
)

// TestMarkPortRemovedReturnsToNotAdded is the behaviour a user sees after
// deleting a rule from 映射规则: the port must come back as 未添加 (needs a
// decision) and not as 已开启 (open, never touched).
func TestMarkPortRemovedReturnsToNotAdded(t *testing.T) {
	st := newTestStore(t)

	rule := &model.Rule{
		Name: "web", Host: "192.168.1.10", Port: 8080, Protocol: model.ProtoTCP, Enabled: true,
	}
	ruleID, err := st.CreateRule(rule)
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	port := &model.DiscoveredPort{
		Host: "192.168.1.10", Port: 8080, Protocol: model.ProtoTCP,
		Status: model.StatusNotAdded, TargetKind: model.ScanTargetCIDR,
	}
	portID, _, err := st.UpsertPort(port)
	if err != nil {
		t.Fatalf("upsert port: %v", err)
	}

	if err := st.MarkPortAdded(portID, ruleID); err != nil {
		t.Fatalf("mark added: %v", err)
	}
	linked, _ := st.GetPort(portID)
	if linked.Status != model.StatusAdded {
		t.Fatalf("status = %q, want %q", linked.Status, model.StatusAdded)
	}
	if linked.RuleID != ruleID || !linked.Added {
		t.Fatalf("link not recorded: %+v", linked)
	}

	if err := st.MarkPortRemoved(portID); err != nil {
		t.Fatalf("mark removed: %v", err)
	}
	freed, _ := st.GetPort(portID)
	if freed.Status != model.StatusNotAdded {
		t.Fatalf("status = %q, want %q (未添加)", freed.Status, model.StatusNotAdded)
	}
	if freed.Added || freed.RuleID != 0 {
		t.Fatalf("link not cleared: added=%v rule_id=%d", freed.Added, freed.RuleID)
	}
}
