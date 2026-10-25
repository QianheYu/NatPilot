package scanner

// Regression tests for the cross-platform listener handling:
//
//   - windows/macOS used to report every listener as 127.0.0.1, which produced
//     rules whose mapping target was loopback;
//   - the windows listener state column is localised, so keying on "LISTENING"
//     silently dropped every TCP row on a German/French system.

import "testing"

// TestNormalizeListenerHost covers wildcard binds, concrete addresses and the
// IPv4 folding of IPv6 endpoints.
func TestNormalizeListenerHost(t *testing.T) {
	const lan = "192.168.1.8"
	cases := []struct{ bind, want string }{
		// wildcard binds mean "every address" -> the LAN address
		{"0.0.0.0", lan},
		{"", lan},
		{"*", lan},
		{"::", lan},
		{"[::]", lan},
		{"0:0:0:0:0:0:0:0", lan},
		// concrete IPv4 binds are kept as-is
		{"127.0.0.1", "127.0.0.1"},
		{"192.168.1.8", "192.168.1.8"},
		{"10.121.20.206", "10.121.20.206"},
		// IPv6: loopback folds to the IPv4 loopback, everything else to the LAN
		// address, because IGD/PCP internal clients are IPv4 addresses.
		{"[::1]", "127.0.0.1"},
		{"[fe80::1]", lan},
		{"[fe80::150:8af9:623c:b011%7]", lan}, // zone id stripped
		{"240e:810:40:709:7646:b1cb:dc0e:6f35", lan},
		{"[fdfe:dcba:9876::1]", lan},
		// a v4-mapped IPv6 address is a v4 address
		{"[::ffff:192.168.1.9]", "192.168.1.9"},
	}
	for _, c := range cases {
		if got := normalizeListenerHost(c.bind, lan); got != c.want {
			t.Errorf("normalizeListenerHost(%q) = %q, want %q", c.bind, got, c.want)
		}
	}
	// Without a LAN address the loopback address is the only safe fallback.
	if got := normalizeListenerHost("0.0.0.0", ""); got != "127.0.0.1" {
		t.Errorf("empty fallback = %q, want 127.0.0.1", got)
	}
}

// TestBannerTarget checks the address used for the banner probe: a concrete
// bind is reachable directly, a wildcard bind only through loopback.
func TestBannerTarget(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0":                    "127.0.0.1",
		"::":                         "127.0.0.1",
		"":                           "127.0.0.1",
		"127.0.0.1":                  "127.0.0.1",
		"192.168.1.8":                "192.168.1.8",
		"fe80::150:8af9:623c:b011%7": "fe80::150:8af9:623c:b011",
	}
	for bind, want := range cases {
		if got := bannerTarget(bind); got != want {
			t.Errorf("bannerTarget(%q) = %q, want %q", bind, got, want)
		}
	}
}

// TestParseWinNetstatLocalisedStates reproduces the failure mode of a localised
// Windows: the state column is translated, but the rows are still listeners and
// must be reported.
func TestParseWinNetstatLocalisedStates(t *testing.T) {
	out := `Aktive Verbindungen

  Proto  Lokale Adresse         Remoteadresse          Status           PID
  TCP    0.0.0.0:135            0.0.0.0:0              ABHÖREN          1088
  TCP    0.0.0.0:445            0.0.0.0:0              ABHÖREN          4
  TCP    192.168.1.8:139        0.0.0.0:0              ABHÖREN          4
  TCP    192.168.1.8:5357       192.168.1.20:50123     HERGESTELLT      4120
  UDP    0.0.0.0:5353           *:*                                    8120
`
	listeners := parseWinNetstat(out)
	if len(listeners) != 4 {
		t.Fatalf("got %d listeners, want 4 (3 TCP listeners + 1 UDP): %+v", len(listeners), listeners)
	}
	if listeners[0].Port != 135 || listeners[0].PID != 1088 || listeners[0].Proto != "tcp" {
		t.Errorf("first listener = %+v", listeners[0])
	}
	if listeners[2].Host != "192.168.1.8" || listeners[2].Port != 139 {
		t.Errorf("third listener = %+v", listeners[2])
	}
	if listeners[3].Proto != "udp" || listeners[3].Port != 5353 {
		t.Errorf("udp listener = %+v", listeners[3])
	}
}

// TestParseWinNetstatKeepsEnglishOutput makes sure the ordinary output still
// parses (the English state token remains a valid signal).
func TestParseWinNetstatKeepsEnglishOutput(t *testing.T) {
	out := `  TCP    0.0.0.0:8080           0.0.0.0:0              LISTENING       1234
  TCP    10.0.0.5:5900          10.0.0.9:51000         ESTABLISHED     4321
  TCP    [::]:8080              [::]:0                 LISTENING       1234
  UDP    0.0.0.0:1900           *:*                                    8120
`
	listeners := parseWinNetstat(out)
	if len(listeners) != 3 {
		t.Fatalf("got %d listeners, want 3 (established rows excluded): %+v", len(listeners), listeners)
	}
	if listeners[1].Host != "::" || listeners[1].Port != 8080 {
		t.Errorf("ipv6 listener = %+v", listeners[1])
	}
}

// TestIsWildcardEndpoint covers the shapes `netstat -ano` prints.
func TestIsWildcardEndpoint(t *testing.T) {
	yes := []string{"0.0.0.0:0", "[::]:0", "*:*", "*:0"}
	no := []string{"0.0.0.0:8080", "192.168.1.1:0", "[::1]:0", "*:5353", ""}
	for _, s := range yes {
		if !isWildcardEndpoint(s) {
			t.Errorf("isWildcardEndpoint(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isWildcardEndpoint(s) {
			t.Errorf("isWildcardEndpoint(%q) = true, want false", s)
		}
	}
}
