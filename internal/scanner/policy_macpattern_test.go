package scanner

import (
	"testing"

	"github.com/ekko-studio/natpilot/internal/model"
)

// TestPolicyMACPatterns covers the "MAC 范围" match kind: a policy can follow a
// group of devices by hardware address (exact or OUI prefix) instead of a subnet,
// which is what keeps it correct when DHCP moves the addresses around.
func TestPolicyMACPatterns(t *testing.T) {
	pol := &model.Policy{
		Name:        "vendor cameras",
		MatchTarget: model.ScanTargetMAC,
		MACPatterns: "aa:bb:cc, 11:22:33:44:55:66",
		Enabled:     true,
	}
	port := &model.DiscoveredPort{Host: "192.168.1.40", Port: 554, Protocol: "tcp", TargetKind: model.ScanTargetCIDR}

	if !policyTargetMatches(pol, port, "aa:bb:cc:00:11:22") {
		t.Fatal("OUI prefix should match a device of that vendor")
	}
	if !policyTargetMatches(pol, port, "11-22-33-44-55-66") {
		t.Fatal("exact address should match regardless of separators")
	}
	if policyTargetMatches(pol, port, "99:88:77:66:55:44") {
		t.Fatal("unrelated MAC must not match")
	}
	if policyTargetMatches(pol, port, "") {
		t.Fatal("a port whose MAC is unknown must not match a MAC rule")
	}
}

// TestMACMatchKindWithoutPatternsFailsClosed guards against a "match everything"
// fallback: a mac-kind policy with no usable pattern would otherwise apply to
// every host on the network.
func TestMACMatchKindWithoutPatternsFailsClosed(t *testing.T) {
	pol := &model.Policy{Name: "broken", MatchTarget: model.ScanTargetMAC, Enabled: true}
	port := &model.DiscoveredPort{Host: "192.168.1.5", Port: 22, Protocol: "tcp", TargetKind: model.ScanTargetCIDR}
	if policyTargetMatches(pol, port, "aa:bb:cc:dd:ee:ff") {
		t.Fatal("a mac-kind policy without patterns must match nothing")
	}
}
