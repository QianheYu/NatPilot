package scanner

import (
	"testing"

	"github.com/ekko-studio/natpilot/internal/model"
)

// TestMatchPolicyByMAC is the core of host monitoring: a policy bound to a MAC
// applies to that host's ports only, regardless of the IP it currently has.
func TestMatchPolicyByMAC(t *testing.T) {
	ports := []model.Policy{
		{ID: 1, Name: "camera", MAC: "aa:bb:cc:dd:ee:ff", AutoAction: model.ActionAdd, Priority: 10, Enabled: true},
		{ID: 2, Name: "global", AutoAction: model.ActionWatch, Priority: 1, Enabled: true},
	}

	otherHost := &model.DiscoveredPort{Host: "192.168.1.50", Port: 22, Protocol: "tcp"}
	boundHost := &model.DiscoveredPort{Host: "192.168.1.60", Port: 22, Protocol: "tcp"}

	// A port on a different machine falls through to the global policy.
	if got := matchPolicy(ports, otherHost, "11:22:33:44:55:66"); got == nil || got.Name != "global" {
		t.Fatalf("other host matched %v, want the global policy", got)
	}

	// The bound machine matches by MAC even though the policy has no IP.
	if got := matchPolicy(ports, boundHost, "AA-BB-CC-DD-EE-FF"); got == nil || got.Name != "camera" {
		t.Fatalf("bound host matched %v, want the camera policy", got)
	}

	// Without a resolvable MAC the host-bound policy must not apply to anybody:
	// falling back to "any host" would silently map ports everywhere.
	if got := matchPolicy(ports, boundHost, ""); got == nil || got.Name != "global" {
		t.Fatalf("unknown MAC matched %v, want the global policy", got)
	}
}

// TestHostBoundPolicyIgnoresTargetKind makes sure the binding wins over the
// match_target_kind heuristics (local/cidr/remote), which are meaningless for a
// policy that follows one specific device.
func TestHostBoundPolicyIgnoresTargetKind(t *testing.T) {
	pol := &model.Policy{Name: "pinned", MAC: "aa:bb:cc:dd:ee:01", MatchTarget: model.ScanTargetLocal}
	remote := &model.DiscoveredPort{Host: "10.0.0.9", Port: 8080, Protocol: "tcp", TargetKind: model.ScanTargetCIDR}

	if policyTargetMatches(pol, remote, "aa:bb:cc:dd:ee:01") != true {
		t.Fatal("host-bound policy must match its host even for a CIDR-discovered port")
	}
	if policyTargetMatches(pol, remote, "aa:bb:cc:dd:ee:02") != false {
		t.Fatal("host-bound policy must not match another MAC")
	}
}
