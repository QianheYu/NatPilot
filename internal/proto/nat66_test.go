package proto

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// NAT66 is decided from two addresses only; this table pins every branch of the
// pure decision, including the "unknown" cases that must never be reported as
// "no NAT66".
func TestDecideNAT66(t *testing.T) {
	cases := []struct {
		name     string
		external string
		local    string
		want     NAT66State
	}{
		{
			name:     "same address means no NAT66",
			external: "2001:db8:1::5",
			local:    "2001:db8:1::5",
			want:     NAT66State{Available: true, ExternalIPv6: "2001:db8:1::5", LocalIPv6: "2001:db8:1::5", Nat66: false},
		},
		{
			name:     "ULA local with global external means NAT66",
			external: "2001:db8:1::99",
			local:    "fd00:1234::5",
			want:     NAT66State{Available: true, ExternalIPv6: "2001:db8:1::99", LocalIPv6: "fd00:1234::5", Nat66: true},
		},
		{
			name:     "two different globals mean NAT66",
			external: "2001:db8:2::1",
			local:    "2001:db8:1::5",
			want:     NAT66State{Available: true, ExternalIPv6: "2001:db8:2::1", LocalIPv6: "2001:db8:1::5", Nat66: true},
		},
		{
			name:     "non-canonical spelling of the same address is not a mismatch",
			external: "2001:0db8:0001:0000:0000:0000:0000:0005",
			local:    "[2001:db8:1::5]",
			want:     NAT66State{Available: true, ExternalIPv6: "2001:db8:1::5", LocalIPv6: "2001:db8:1::5", Nat66: false},
		},
		{
			name:     "zone id is ignored",
			external: "2001:db8:1::5",
			local:    "2001:db8:1::5%eth0",
			want:     NAT66State{Available: true, ExternalIPv6: "2001:db8:1::5", LocalIPv6: "2001:db8:1::5", Nat66: false},
		},
		{
			name:  "unavailable external is unknown, not no-NAT",
			local: "2001:db8:1::5",
			want:  NAT66State{LocalIPv6: "2001:db8:1::5", Err: "未获得外部 IPv6 地址（IPv6 探测不可用），无法判断是否使用 NAT66"},
		},
		{
			name:     "unavailable local is unknown",
			external: "2001:db8:1::5",
			want:     NAT66State{ExternalIPv6: "2001:db8:1::5", Err: "未获得本机的全局/ULA IPv6 地址，无法判断是否使用 NAT66"},
		},
		{
			name:     "IPv4 is never a NAT66 comparison address",
			external: "203.0.113.9",
			local:    "2001:db8:1::5",
			want:     NAT66State{LocalIPv6: "2001:db8:1::5", Err: `外部地址 "203.0.113.9" 不是可用的 IPv6 地址`},
		},
		{
			name:     "link-local local address is not usable",
			external: "2001:db8:1::5",
			local:    "fe80::1",
			want:     NAT66State{ExternalIPv6: "2001:db8:1::5", Err: "未获得本机的全局/ULA IPv6 地址，无法判断是否使用 NAT66"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecideNAT66(c.external, c.local, "gw1")
			got.Gateway = ""
			if got != c.want {
				t.Errorf("DecideNAT66(%q, %q) =\n  %+v\nwant\n  %+v", c.external, c.local, got, c.want)
			}
		})
	}
}

// A host with several prefixes is not NATed when the Internet sees any of its
// own addresses, even if the first candidate is not the one used on the wire.
func TestDecideNAT66AddressesUsesAnyLocalAddress(t *testing.T) {
	locals := []string{"2001:db8:1::5", "fd00:1234::5"}

	noNat := DecideNAT66Addresses("fd00:1234::5", locals, "")
	if !noNat.Available || noNat.Nat66 {
		t.Errorf("matching the ULA should mean no NAT66: %+v", noNat)
	}
	if noNat.LocalIPv6 != "fd00:1234::5" {
		t.Errorf("reported local address = %q, want the matching one", noNat.LocalIPv6)
	}

	nat := DecideNAT66Addresses("2001:db8:9::7", locals, "")
	if !nat.Available || !nat.Nat66 {
		t.Errorf("an unknown external address should mean NAT66: %+v", nat)
	}
	if nat.LocalIPv6 != "2001:db8:1::5" {
		t.Errorf("reported local address = %q, want the first global candidate", nat.LocalIPv6)
	}
}

func TestIsNAT66Address(t *testing.T) {
	valid := []string{"2001:db8::1", "[2001:db8::1]", "fd00::1", "2001:db8::1%eth0"}
	for _, s := range valid {
		if !IsNAT66Address(s) {
			t.Errorf("IsNAT66Address(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "192.168.1.1", "::ffff:192.168.1.1", "::1", "fe80::1", "ff02::1", "::", "router.lan"}
	for _, s := range invalid {
		if IsNAT66Address(s) {
			t.Errorf("IsNAT66Address(%q) = true, want false", s)
		}
	}
}

func TestDefaultExternalIPv6EndpointsIsCopy(t *testing.T) {
	got := DefaultExternalIPv6Endpoints()
	want := []string{"https://api6.ipify.org", "https://v6.ident.me", "https://ipv6.icanhazip.com"}
	if len(got) != len(want) {
		t.Fatalf("endpoints = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("endpoints = %v, want %v", got, want)
		}
	}
	got[0] = "https://mutated.invalid"
	if again := DefaultExternalIPv6Endpoints(); again[0] != want[0] {
		t.Errorf("caller mutation leaked into the package defaults: %v", again)
	}
}

// probeExternalIPv6 is the probe with an explicit dial family; the test server
// is IPv4, so passing "tcp4" exercises the response filtering that the real
// "tcp6" probe uses.
func TestProbeExternalIPv6BodyHandling(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v6", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); !strings.Contains(got, "NatPilot") {
			t.Errorf("probe User-Agent = %q", got)
		}
		_, _ = w.Write([]byte("2001:0db8:0001:0000:0000:0000:0000:0007\n"))
	})
	mux.HandleFunc("/v4", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("203.0.113.9\n"))
	})
	mux.HandleFunc("/junk", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not an address"))
	})
	mux.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	got, err := probeExternalIPv6(ctx, []string{srv.URL + "/v6"}, 2*time.Second, "tcp4")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if got != "2001:db8:1::7" {
		t.Errorf("probe = %q, want the canonical v6 address", got)
	}

	// A v4-only answer must not be accepted as the IPv6 external address; the
	// next endpoint is tried instead.
	got, err = probeExternalIPv6(ctx, []string{srv.URL + "/v4", srv.URL + "/junk", srv.URL + "/v6"}, 2*time.Second, "tcp4")
	if err != nil {
		t.Fatalf("probe with fallback: %v", err)
	}
	if got != "2001:db8:1::7" {
		t.Errorf("probe with fallback = %q", got)
	}

	if _, err := probeExternalIPv6(ctx, []string{srv.URL + "/v4"}, 2*time.Second, "tcp4"); err == nil {
		t.Errorf("a v4 answer must be rejected")
	}
	if _, err := probeExternalIPv6(ctx, []string{srv.URL + "/fail"}, 2*time.Second, "tcp4"); err == nil {
		t.Errorf("an HTTP error must be reported")
	}
	if _, err := probeExternalIPv6(ctx, []string{"", "  "}, 2*time.Second, "tcp4"); err == nil {
		t.Errorf("an empty endpoint list must be reported, not silently accepted")
	}
}

// DetectNAT66 with injected inputs never touches the network, which is what the
// mapper relies on for its cached probe and tests.
func TestDetectNAT66InjectedInputs(t *testing.T) {
	state := DetectNAT66(context.Background(), NAT66Options{
		ExternalIPv6: "2001:db8:9::1",
		LocalIPv6:    "fd00::5",
		Gateway:      "router",
	})
	if !state.Available || !state.Nat66 || state.Gateway != "router" {
		t.Errorf("DetectNAT66 = %+v, want NAT66 on fd00::5", state)
	}

	state = DetectNAT66(context.Background(), NAT66Options{
		ExternalIPv6: "2001:db8:9::1",
		LocalIPv6:    "2001:db8:9::1",
	})
	if !state.Available || state.Nat66 {
		t.Errorf("DetectNAT66 = %+v, want no NAT66", state)
	}

	state = DetectNAT66(context.Background(), NAT66Options{
		LocalIPv6: "fd00::5",
		SkipHTTP:  true,
	})
	if state.Available || state.Nat66 || state.Err == "" {
		t.Errorf("DetectNAT66 without an external address = %+v, want unavailable with an error", state)
	}

	state = DetectNAT66(context.Background(), NAT66Options{
		LocalIPv6:    "192.168.1.5",
		ExternalIPv6: "2001:db8:9::1",
	})
	if state.Available || state.Err == "" {
		t.Errorf("a v4 local address cannot be used for the comparison: %+v", state)
	}
}

// LocalIPv6Addresses must not panic or invent addresses on a host without IPv6,
// and everything it returns must be usable for the comparison.
func TestLocalIPv6AddressesAreUsable(t *testing.T) {
	list, err := LocalIPv6Addresses()
	if err != nil {
		t.Fatalf("LocalIPv6Addresses: %v", err)
	}
	for _, addr := range list {
		ip := net.ParseIP(addr)
		if ip == nil || ip.To4() != nil {
			t.Errorf("returned a non-IPv6 address %q", addr)
			continue
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
			t.Errorf("returned an unusable comparison address %q", addr)
		}
	}
	if _, err := LocalNAT66IPv6(); err != nil && len(list) > 0 {
		t.Errorf("LocalNAT66IPv6 failed with %d candidates: %v", len(list), err)
	}
}
