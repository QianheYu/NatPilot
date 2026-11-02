package proto

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// NAT66 (IPv6 NAT) breaks the assumption every "IPv6 needs no NAT" code path
// makes: the gateway rewrites the source prefix of the internal ULA/global
// address, so an inbound WANIPv6FirewallControl pinhole for the *internal*
// address cannot work. The only mechanism that still works is a real port
// mapping (PCP MAP, RFC 6887 §11.2), whose external port really is translated.
//
// Detection has no protocol-level answer: IGDv2 has no action that reports the
// gateway's external IPv6, and NPTv6/NAT66 devices do not advertise the
// translation at all. The practical test is to compare an HTTP echo of the
// address the Internet sees with the host's own global/ULA address:
//
//	same address      -> the host address is passed through, no NAT66
//	different address -> something translated/hid the source address, NAT66 in use
//
// The comparison is a heuristic. Known caveats are documented on DetectNAT66.

// defaultExternalIPv6Endpoints are v6-only or dual-stack HTTP address echoes.
// They are queried over IPv6 only (see ProbeExternalIPv6), so a dual-stack
// service answers with the address of the IPv6 socket, not with the v4 one.
var defaultExternalIPv6Endpoints = []string{
	"https://api6.ipify.org",
	"https://v6.ident.me",
	"https://ipv6.icanhazip.com",
}

// DefaultExternalIPv6Endpoints returns a copy of the built-in endpoint list, so
// a caller (or a settings UI) can seed its own configuration without aliasing
// the package defaults.
func DefaultExternalIPv6Endpoints() []string {
	return append([]string(nil), defaultExternalIPv6Endpoints...)
}

// nat66HTTPTimeout bounds a single endpoint probe.
const nat66HTTPTimeout = 4 * time.Second

// NAT66State is the outcome of the NAT66 probe.
//
// Available reports whether a definitive comparison was possible: both an
// external and at least one local IPv6 address were obtained. Only then does
// Nat66 carry a verdict. When the probe fails (no IPv6 uplink, HTTP endpoints
// unreachable, no local global/ULA address) Available is false, Err explains
// why and Nat66 stays false — "unknown", deliberately not "no NAT".
type NAT66State struct {
	Available    bool   `json:"available"`
	ExternalIPv6 string `json:"external_ipv6,omitempty"`
	LocalIPv6    string `json:"local_ipv6,omitempty"`
	Nat66        bool   `json:"nat66"`
	Gateway      string `json:"gateway,omitempty"`
	Err          string `json:"error,omitempty"`
}

// String renders the state for logs and error messages.
func (s NAT66State) String() string {
	if s.Available {
		if s.Nat66 {
			return fmt.Sprintf("NAT66 detected (external %s, local %s)", s.ExternalIPv6, s.LocalIPv6)
		}
		return fmt.Sprintf("no NAT66 (external %s, local %s)", s.ExternalIPv6, s.LocalIPv6)
	}
	return "NAT66 unknown: " + s.Err
}

// NAT66Options tunes DetectNAT66. The zero value is usable on a dual-stack
// host: it reads the local addresses and probes the built-in endpoints.
type NAT66Options struct {
	// Endpoints overrides DefaultExternalIPv6Endpoints.
	Endpoints []string
	// Timeout bounds one endpoint probe (default 4s).
	Timeout time.Duration
	// LocalIPv6 overrides the local address, mainly for tests. When empty the
	// host interfaces are inspected.
	LocalIPv6 string
	// ExternalIPv6 is a pre-known external address (e.g. reported by the
	// gateway, or a canned value in tests). When set, no HTTP probe is made.
	ExternalIPv6 string
	// Gateway labels the gateway in the state; purely informational.
	Gateway string
	// SkipHTTP prevents the HTTP probe when ExternalIPv6 is not set, so a
	// caller can use the probe as a pure local-address query.
	SkipHTTP bool
}

// DetectNAT66 decides whether the network in use translates IPv6 (NAT66).
//
// The decision logic is the pure DecideNAT66Addresses function; this wrapper
// only gathers the two inputs:
//
//   - local addresses: every global unicast / ULA address of an up, non-loopback
//     interface (see LocalIPv6Addresses). Link-local, loopback, multicast and
//     IPv4-mapped addresses are never compared: they cannot be a WAN source.
//   - external address: opts.ExternalIPv6 when the caller already knows it
//     (e.g. a gateway that exposes its external IPv6, which is optional because
//     IGDv2 has no such action), otherwise the HTTP echo probe.
//
// Known false positives:
//
//  1. A privacy/temporary address (RFC 4941) or a second prefix on the host that
//     is not the one the kernel picks for the echo service produces a mismatch
//     even on a native, un-NATed network.
//  2. A prefix change between the probe and the address read (ISP renumbering,
//     DHCPv6-PD renewal) looks exactly like NAT66.
//  3. A CGNAT/downstream router that translates only some prefixes.
//
// Known false negatives:
//
//  1. The host also owns the external address (e.g. the NAT66 prefix is also
//     assigned on the LAN) — the comparison then says "no NAT66".
//  2. The echo service is unreachable or answers with the v4 address; the probe
//     reports Available=false rather than guessing.
//  3. The gateway translates but the host's source address is passed through
//     unchanged (no NAT66 for that flow); that flow is indeed not translated.
func DetectNAT66(ctx context.Context, opts NAT66Options) NAT66State {
	if ctx == nil {
		ctx = context.Background()
	}

	var locals []string
	localErr := ""
	if opts.LocalIPv6 != "" {
		if addr, ok := canonicalNAT66Address(opts.LocalIPv6); ok {
			locals = []string{addr}
		} else {
			localErr = fmt.Sprintf("本机 IPv6 地址 %q 不是可用的全局/ULA 地址", opts.LocalIPv6)
		}
	} else if found, err := LocalIPv6Addresses(); err != nil {
		localErr = err.Error()
	} else {
		locals = found
	}

	external := strings.TrimSpace(opts.ExternalIPv6)
	probeErr := ""
	if external == "" && !opts.SkipHTTP {
		addr, err := ProbeExternalIPv6(ctx, opts.Endpoints, opts.Timeout)
		if err != nil {
			probeErr = err.Error()
		} else {
			external = addr
		}
	}

	state := DecideNAT66Addresses(external, locals, opts.Gateway)
	if !state.Available {
		// Preserve the concrete reason (probe failure, interface enumeration
		// failure) instead of reducing it to the generic "unknown" message.
		details := []string{}
		if localErr != "" {
			details = append(details, localErr)
		}
		if probeErr != "" {
			details = append(details, probeErr)
		}
		if len(details) == 0 {
			details = append(details, state.Err)
		}
		state.Err = strings.Join(details, "；")
	}
	return state
}

// DecideNAT66 is the pure single-address decision used by DetectNAT66 and by
// callers that already picked the local address. Empty/invalid inputs yield
// Available=false, never a guess.
func DecideNAT66(externalIPv6, localIPv6, gateway string) NAT66State {
	var locals []string
	if localIPv6 != "" {
		locals = []string{localIPv6}
	}
	return DecideNAT66Addresses(externalIPv6, locals, gateway)
}

// DecideNAT66Addresses is the pure NAT66 decision: with the external address
// the Internet sees and every candidate local global/ULA address, an external
// address that equals one of them means the host is reachable under its own
// address (no translation); a different address means the path translated it.
//
// It performs no I/O, which is what makes the NAT66 rule testable without real
// NAT66 hardware.
func DecideNAT66Addresses(externalIPv6 string, localIPv6 []string, gateway string) NAT66State {
	state := NAT66State{Gateway: gateway}

	seen := map[string]bool{}
	var normalized []string
	for _, raw := range localIPv6 {
		addr, ok := canonicalNAT66Address(raw)
		if !ok || seen[addr] {
			continue
		}
		seen[addr] = true
		normalized = append(normalized, addr)
	}
	localErr := ""
	if len(normalized) == 0 {
		localErr = "未获得本机的全局/ULA IPv6 地址，无法判断是否使用 NAT66"
	} else {
		state.LocalIPv6 = normalized[0]
	}

	external, extOK := canonicalNAT66Address(externalIPv6)
	extErr := ""
	if !extOK {
		if strings.TrimSpace(externalIPv6) == "" {
			extErr = "未获得外部 IPv6 地址（IPv6 探测不可用），无法判断是否使用 NAT66"
		} else {
			extErr = fmt.Sprintf("外部地址 %q 不是可用的 IPv6 地址", externalIPv6)
		}
	} else {
		state.ExternalIPv6 = external
	}

	switch {
	case extErr != "" && localErr != "":
		state.Err = extErr + "；" + localErr
		return state
	case extErr != "":
		state.Err = extErr
		return state
	case localErr != "":
		state.Err = localErr
		return state
	}

	state.Available = true
	for _, addr := range normalized {
		if addr == external {
			state.LocalIPv6 = addr
			state.Nat66 = false
			return state
		}
	}
	state.Nat66 = true
	return state
}

// LocalIPv6Addresses returns every usable IPv6 address of this host: global
// unicast and unique-local (fc00::/7) addresses on up, non-loopback interfaces.
// Link-local, loopback, multicast, unspecified and IPv4-mapped addresses are
// excluded, as are addresses that are still tentative (unspecified).
//
// Global addresses are listed before ULA addresses: a host that is not behind
// NAT66 keeps its global source address on the wire, so that is the address the
// HTTP echo must match.
func LocalIPv6Addresses() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("枚举本机网络接口失败: %w", err)
	}
	global := []string{}
	ula := []string{}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, aerr := ifc.Addrs()
		if aerr != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsUnspecified() || ipnet.IP.IsLoopback() || ipnet.IP.IsMulticast() {
				continue
			}
			addr, ok := canonicalNAT66Address(ipnet.IP.String())
			if !ok {
				continue
			}
			if isULAIPv6(net.ParseIP(addr)) {
				ula = append(ula, addr)
			} else {
				global = append(global, addr)
			}
		}
	}
	return append(global, ula...), nil
}

// LocalNAT66IPv6 returns the single address DetectNAT66 would compare first.
func LocalNAT66IPv6() (string, error) {
	list, err := LocalIPv6Addresses()
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", fmt.Errorf("本机没有全局/ULA IPv6 地址")
	}
	return list[0], nil
}

// IsNAT66Address reports whether addr is an IPv6 address that can plausibly
// appear as a WAN or LAN source address for the NAT66 comparison: global
// unicast or ULA, not IPv4/IPv4-mapped, not loopback/link-local/multicast.
func IsNAT66Address(addr string) bool {
	_, ok := canonicalNAT66Address(addr)
	return ok
}

// canonicalNAT66Address normalizes an IPv6 literal for comparison: no
// brackets, no zone id, canonical form.
func canonicalNAT66Address(addr string) (string, bool) {
	s := strings.TrimSpace(addr)
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	if i := strings.IndexByte(s, '%'); i >= 0 {
		s = s[:i]
	}
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() != nil {
		return "", false
	}
	v6 := ip.To16()
	if v6 == nil || v6.IsUnspecified() || v6.IsLoopback() || v6.IsMulticast() || v6.IsLinkLocalUnicast() {
		return "", false
	}
	return v6.String(), true
}

// isULAIPv6 reports whether ip is in fc00::/7 (unique local addresses).
func isULAIPv6(ip net.IP) bool {
	v6 := ip.To16()
	if v6 == nil || ip.To4() != nil {
		return false
	}
	return v6[0]&0xfe == 0xfc
}

// ProbeExternalIPv6 asks an HTTP address echo which address the Internet sees.
// The request is forced over IPv6 ("tcp6"), so a dual-stack endpoint cannot
// answer with a v4 address, and a response that is not a usable global/ULA IPv6
// address is rejected instead of being returned.
//
// It mirrors netutil.ExternalIPHTTP (which stays unchanged and is family
// agnostic); the family pinning is what v4 checking cannot express.
func ProbeExternalIPv6(ctx context.Context, endpoints []string, timeout time.Duration) (string, error) {
	return probeExternalIPv6(ctx, endpoints, timeout, "tcp6")
}

// probeExternalIPv6 is the probe with an explicit dial network, so tests can
// exercise the parsing/filtering over an IPv4 test server.
func probeExternalIPv6(ctx context.Context, endpoints []string, timeout time.Duration, network string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = nat66HTTPTimeout
	}
	if len(endpoints) == 0 {
		endpoints = defaultExternalIPv6Endpoints
	}

	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		DialContext: func(dctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(dctx, network, addr)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout}

	var lastErr error
	tried := 0
	for _, raw := range endpoints {
		endpoint := strings.TrimSpace(raw)
		if endpoint == "" {
			continue
		}
		tried++
		body, err := nat66HTTPGet(ctx, client, endpoint)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", endpoint, err)
			continue
		}
		addr, ok := canonicalNAT66Address(strings.TrimSpace(body))
		if !ok {
			lastErr = fmt.Errorf("%s 返回的不是可用的 IPv6 地址", endpoint)
			continue
		}
		return addr, nil
	}
	if lastErr == nil {
		if tried == 0 {
			return "", fmt.Errorf("没有配置 IPv6 探测端点")
		}
		lastErr = fmt.Errorf("没有可用的 IPv6 探测端点")
	}
	return "", fmt.Errorf("IPv6 外部地址探测失败: %w", lastErr)
}

// nat66HTTPGet fetches an address echo with a small body cap.
func nat66HTTPGet(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "NatPilot/0.1 (IPv6 NAT66 probe)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	return string(body), nil
}
