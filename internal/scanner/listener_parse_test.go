package scanner

import (
	"fmt"
	"testing"
)

// The darwin and windows listener enumeration shells out to OS utilities that do
// not exist here, so the parsing is verified against captured output. Keeping
// the parsers pure (listener_parse.go) is what makes this testable.

func TestParseLsofListeners(t *testing.T) {
	out := `COMMAND     PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
node      12345   user   23u  IPv6 0x1234567890      0t0  TCP *:8080 (LISTEN)
nginx       999   root    6u  IPv4 0xabcdef1234      0t0  TCP 127.0.0.1:80 (LISTEN)
node      12345   user   24u  IPv6 0x1234567891      0t0  UDP *:5353
sshd        500   root    3u  IPv4 0xabcdef9999      0t0  TCP *:22 (LISTEN)
`
	ls := parseLsofListeners(out)
	if len(ls) != 4 {
		t.Fatalf("got %d listeners, want 4: %+v", len(ls), ls)
	}
	// Two wildcard TCP rows exist (*:8080 and *:22), so the port is part of the key.
	byKey := map[string]LocalListener{}
	for _, l := range ls {
		byKey[fmt.Sprintf("%s/%s:%d", l.Proto, l.Host, l.Port)] = l
	}
	if l := byKey["tcp/*:8080"]; l.Process != "node" || l.PID != 12345 {
		t.Errorf("wildcard TCP listener = %+v", l)
	}
	if l := byKey["tcp/*:22"]; l.Process != "sshd" || l.PID != 500 {
		t.Errorf("second wildcard TCP listener = %+v", l)
	}
	if l := byKey["tcp/127.0.0.1:80"]; l.Process != "nginx" {
		t.Errorf("loopback listener = %+v", l)
	}
	// UDP rows have no (LISTEN) suffix and must still parse.
	if l := byKey["udp/*:5353"]; l.Proto != "udp" {
		t.Errorf("udp listener = %+v", l)
	}
}

func TestParseMacNetstat(t *testing.T) {
	out := `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp4       0      0  127.0.0.1.8080         *.*                    LISTEN
tcp6       0      0  ::1.80                 *.*                    LISTEN
tcp4       0      0  *.5000                 *.*                    LISTEN
tcp4       0      0  192.168.1.100.52344    93.184.216.34.443      ESTABLISHED
tcp4       0      0  192.168.1.100.52345    93.184.216.34.80       TIME_WAIT
udp4       0      0  *.5353                 *.*
udp6       0      0  *.5354                 *.*
`
	ls := parseMacNetstat(out)
	if len(ls) != 5 {
		t.Fatalf("got %d listeners, want 5 (non-LISTEN TCP excluded): %+v", len(ls), ls)
	}
	for _, l := range ls {
		if l.Port == 52344 || l.Port == 52345 {
			t.Errorf("established/time_wait socket leaked into listeners: %+v", l)
		}
	}
	byKey := map[string]LocalListener{}
	for _, l := range ls {
		byKey[l.Proto+"/"+l.Host] = l
	}
	// macOS joins address and port with a dot, so the port is the last component.
	if l := byKey["tcp/127.0.0.1"]; l.Port != 8080 {
		t.Errorf("dotted IPv4 parse = %+v", l)
	}
	if l := byKey["tcp/::1"]; l.Port != 80 {
		t.Errorf("dotted IPv6 parse = %+v", l)
	}
	if l := byKey["tcp/*"]; l.Port != 5000 {
		t.Errorf("wildcard parse = %+v", l)
	}
	if _, ok := byKey["udp/*"]; !ok {
		t.Errorf("udp rows missing: %+v", ls)
	}
}

func TestParseWinNetstat(t *testing.T) {
	out := `Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       960
  TCP    0.0.0.0:8080           0.0.0.0:0              LISTENING       1234
  TCP    [::]:8080              [::]:0                 LISTENING       1234
  TCP    192.168.1.100:52344    93.184.216.34:443      ESTABLISHED     4321
  TCP    127.0.0.1:49670        127.0.0.1:49671        TIME_WAIT       0
  UDP    0.0.0.0:5353           *:*                                    1234
  UDP    [::1]:1900             *:*                                    5678
`
	ls := parseWinNetstat(out)
	if len(ls) != 5 {
		t.Fatalf("got %d listeners, want 5 (non-LISTENING TCP excluded): %+v", len(ls), ls)
	}
	for _, l := range ls {
		if l.Port == 52344 || l.Port == 49670 {
			t.Errorf("non-listening socket leaked: %+v", l)
		}
	}
	var have135, have8080, haveUDP1900 bool
	for _, l := range ls {
		switch {
		case l.Proto == "tcp" && l.Port == 135 && l.PID == 960:
			have135 = true
		case l.Proto == "tcp" && l.Port == 8080 && l.PID == 1234:
			have8080 = true
		case l.Proto == "udp" && l.Port == 1900 && l.Host == "::1":
			haveUDP1900 = true
		}
	}
	if !have135 || !have8080 || !haveUDP1900 {
		t.Errorf("missing expected entries (135=%v 8080=%v udp1900=%v): %+v", have135, have8080, haveUDP1900, ls)
	}
}

func TestParseTasklist(t *testing.T) {
	out := `"System Idle Process","0","Services","0","8 K"
"chrome.exe","1234","Console","1","120,000 K"
"svchost.exe","960","Services","0","12,345 K"
`
	m := parseTasklist(out)
	if m[1234] != "chrome.exe" {
		t.Errorf("pid 1234 = %q, want chrome.exe", m[1234])
	}
	// The quoted comma inside the memory column must not shift the fields.
	if m[960] != "svchost.exe" {
		t.Errorf("pid 960 = %q, want svchost.exe", m[960])
	}
	if m[0] != "System Idle Process" {
		t.Errorf("pid 0 = %q", m[0])
	}
	if len(m) != 3 {
		t.Errorf("parsed %d entries, want 3", len(m))
	}
}

func TestDedupeListeners(t *testing.T) {
	in := []LocalListener{
		{Proto: "tcp", Host: "127.0.0.1", Port: 80},
		{Proto: "tcp", Host: "127.0.0.1", Port: 80, Process: "nginx", PID: 999},
		{Proto: "tcp", Host: "0.0.0.0", Port: 80},
		{Proto: "udp", Host: "127.0.0.1", Port: 80},
	}
	out := dedupeListeners(in)
	if len(out) != 3 {
		t.Fatalf("got %d entries, want 3 (tcp/udp on the same port are distinct): %+v", len(out), out)
	}
	// The duplicate must be replaced by the attributed copy.
	if out[0].Process != "nginx" || out[0].PID != 999 {
		t.Errorf("dedupe kept the unattributed entry: %+v", out[0])
	}
}

func TestSplitHostPortLoose(t *testing.T) {
	cases := []struct{ in, host, port string }{
		{"*:8080", "*", "8080"},
		{"127.0.0.1:80", "127.0.0.1", "80"},
		{"[::1]:80", "::1", "80"},
		{"[::]:8080", "::", "8080"},
		{"0.0.0.0:135", "0.0.0.0", "135"},
	}
	for _, c := range cases {
		h, p := splitHostPortLoose(c.in)
		if h != c.host || p != c.port {
			t.Errorf("splitHostPortLoose(%q) = (%q,%q), want (%q,%q)", c.in, h, p, c.host, c.port)
		}
	}
}
