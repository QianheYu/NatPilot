package store

import (
	"testing"

	"github.com/ekko-studio/natpilot/internal/model"
)

// TestRuleProviderRoundTrip ensures the gateway pin survives storage, which is the
// whole point of the feature: a rule must keep pointing at the chosen gateway.
func TestRuleProviderRoundTrip(t *testing.T) {
	st := newTestStore(t)
	pin := "igd2|192.168.1.1|1900"
	id, err := st.CreateRule(&model.Rule{Name: "pinned", Host: "127.0.0.1", Port: 8080, Protocol: "tcp", Enabled: true, Provider: pin})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	got, err := st.GetRule(id)
	if err != nil || got == nil {
		t.Fatalf("get rule: %v %v", got, err)
	}
	if got.Provider != pin {
		t.Fatalf("provider = %q, want %q", got.Provider, pin)
	}

	// Clearing the pin must work too (back to automatic).
	got.Provider = ""
	if err := st.UpdateRule(got); err != nil {
		t.Fatalf("update rule: %v", err)
	}
	again, _ := st.GetRule(id)
	if again.Provider != "" {
		t.Fatalf("provider = %q, want empty after clearing", again.Provider)
	}
}
