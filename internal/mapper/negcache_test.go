package mapper

import (
	"testing"
	"time"
)

// TestShouldSkipDiscovery covers the negative cache: an unsuccessful probe is
// reused only while the network fingerprint is unchanged and the result is still
// fresh. A different fingerprint (router plugged in, VPN up, DHCP change) or an
// expired window must retry immediately.
func TestShouldSkipDiscovery(t *testing.T) {
	m := &Mapper{}
	now := time.Now()
	fp := "192.168.1.1|192.168.1.10|192.168.1.10,127.0.0.1"

	// Nothing recorded yet -> must probe.
	if m.shouldSkipDiscovery(fp, now) {
		t.Fatal("empty cache must not skip discovery")
	}

	// Record a successful probe (non-empty): nothing to suppress, providers exist.
	m.lastProbeAt = now
	m.lastProbeEmpty = false
	m.lastProbeFP = fp
	if m.shouldSkipDiscovery(fp, now.Add(time.Second)) {
		t.Fatal("a successful probe must not be suppressed")
	}

	// Empty probe, same network, inside the window -> skip.
	m.lastProbeEmpty = true
	if !m.shouldSkipDiscovery(fp, now.Add(5*time.Second)) {
		t.Fatal("recent empty probe on the same network should be reused")
	}

	// Empty probe, same network, outside the window -> probe again.
	if m.shouldSkipDiscovery(fp, now.Add(discoveryNegativeTTL+time.Second)) {
		t.Fatal("an expired negative result must be retried")
	}

	// Empty probe, network changed -> probe again immediately.
	other := "10.0.0.1|10.0.0.20|10.0.0.20"
	if m.shouldSkipDiscovery(other, now.Add(time.Second)) {
		t.Fatal("a changed network fingerprint must invalidate the cache")
	}
}

// TestRecordProbe stores the fingerprint at call time.
func TestRecordProbeStoresFingerprint(t *testing.T) {
	m := &Mapper{}
	m.recordProbe(true)

	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.lastProbeEmpty {
		t.Fatal("recordProbe(true) should mark the probe empty")
	}
	if m.lastProbeAt.IsZero() {
		t.Fatal("recordProbe should stamp the time")
	}
	if m.lastProbeFP != networkFingerprint() {
		t.Fatal("recordProbe should store the current network fingerprint")
	}
}

// TestNetworkFingerprintIsStable makes sure the fingerprint does not change while
// the host's network setup is untouched (otherwise the cache would never hit).
func TestNetworkFingerprintIsStable(t *testing.T) {
	a := networkFingerprint()
	b := networkFingerprint()
	if a != b {
		t.Fatalf("fingerprint changed between calls: %q vs %q", a, b)
	}
	if a == "" {
		t.Fatal("fingerprint should not be empty")
	}
}
