package netutil

import "testing"

// The darwin and windows code paths obtain their data from OS utilities rather
// than from /proc. Those utilities are not available here, so the parsing logic
// is verified against captured real-world output. The parsing is deliberately
// kept in pure functions (netparse.go) so this is possible.

func TestParseRouteGetDarwin(t *testing.T) {
	out := `   route to: default
destination: default
       mask: default
    gateway: 192.168.1.1
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
 recvpipe  sendpipe  ssthresh  rtt,msec    rttvar  hopcount      mtu     expire
       0         0         0         0         0         0      1500         0
`
	if got := parseRouteGetDarwin(out); got != "192.168.1.1" {
		t.Errorf("got %q, want 192.168.1.1", got)
	}
	if got := parseRouteGetDarwin("no gateway here\n"); got != "" {
		t.Errorf("expected empty for output without a gateway, got %q", got)
	}
}

func TestParseNetstatDarwin(t *testing.T) {
	out := `Routing tables

Internet:
Destination        Gateway            Flags        Netif Expire
default            192.168.1.1        UGScg          en0
default            fe80::1%en0        UGScg          en0
127                127.0.0.1          UCS            lo0
127.0.0.1          127.0.0.1          UH             lo0
192.168.1          link#4             UCS            en0
`
	// The first `default` row is the IPv4 gateway; the IPv6 default must not win
	// because parseNetstatDarwin is used for IPv4 lookups.
	if got := parseNetstatDarwin(out); got != "192.168.1.1" {
		t.Errorf("got %q, want 192.168.1.1", got)
	}
}

func TestParseRoutePrintWindows(t *testing.T) {
	out := `===========================================================================
Interface List
 12...00 11 22 33 44 55 ......Intel(R) Ethernet Connection I219-V
===========================================================================
IPv4 Route Table
===========================================================================
Active Routes:
Network Destination        Netmask          Gateway       Interface  Metric
          0.0.0.0          0.0.0.0      192.168.1.1     192.168.1.100     25
        127.0.0.0        255.0.0.0         On-link         127.0.0.1    331
        127.0.0.1  255.255.255.255         On-link         127.0.0.1    331
===========================================================================
Persistent Routes:
  Network Address          Netmask  Gateway Address  Metric
          0.0.0.0          0.0.0.0           10.0.0.1       1
===========================================================================
`
	// The active default route must win over the persistent one.
	if got := parseRoutePrintWindows(out); got != "192.168.1.1" {
		t.Errorf("got %q, want 192.168.1.1", got)
	}
	// `netstat -rn` on Windows prints the same table, so the same parser applies.
	netstat := `===========================================================================
Interface List
 12...00 11 22 33 44 55 ......Intel(R) Ethernet Connection I219-V
===========================================================================
IPv4 Route Table
===========================================================================
Active Routes:
Network Destination        Netmask          Gateway       Interface  Metric
          0.0.0.0          0.0.0.0       10.20.30.1      10.20.30.100     35
`
	if got := parseRoutePrintWindows(netstat); got != "10.20.30.1" {
		t.Errorf("netstat shape: got %q, want 10.20.30.1", got)
	}
	// "On-link" is not an IP and must never be returned.
	onlink := `          0.0.0.0          0.0.0.0         On-link         127.0.0.1    331
`
	if got := parseRoutePrintWindows(onlink); got != "" {
		t.Errorf("On-link row should not yield a gateway, got %q", got)
	}
}

func TestNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"b8:27:eb:11:22:33": "b8:27:eb:11:22:33",
		"B8-27-EB-11-22-33": "b8:27:eb:11:22:33",
		"0:11:22:33:44:55":  "00:11:22:33:44:55", // macOS drops leading zeros
		"b8:27:eb:11:22":    "",                  // too short
		"not-a-mac":         "",
		"zz:27:eb:11:22:33": "",
	}
	for in, want := range cases {
		if got := normalizeMAC(in); got != want {
			t.Errorf("normalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsUsableMAC(t *testing.T) {
	cases := map[string]bool{
		"b8:27:eb:11:22:33": true,
		"00:11:22:33:44:55": true,
		"00:00:00:00:00:00": false, // all zero placeholder
		"ff:ff:ff:ff:ff:ff": false, // broadcast
		"01:00:5e:00:00:16": false, // multicast
		"33:33:00:00:00:01": false, // IPv6 multicast
		"":                  false,
	}
	for in, want := range cases {
		if got := isUsableMAC(in); got != want {
			t.Errorf("isUsableMAC(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseARPOutputDarwin(t *testing.T) {
	out := `? (192.168.1.1) at 0:11:22:33:44:55 on en0 ifscope [ethernet]
? (192.168.1.20) at b8:27:eb:11:22:33 on en0 ifscope [ethernet]
? (192.168.1.99) at (incomplete) on en0 ifscope [ethernet]
? (192.168.1.255) at ff:ff:ff:ff:ff:ff on en0 ifscope [ethernet]
? (192.168.1.30) at 8:0:27:aa:bb:cc on en1 ifscope [ethernet]
`
	ns := parseARPOutputDarwin(out)
	if len(ns) != 3 {
		t.Fatalf("got %d neighbours, want 3: %+v", len(ns), ns)
	}
	if ns[0].IP != "192.168.1.1" || ns[0].MAC != "00:11:22:33:44:55" || ns[0].Iface != "en0" {
		t.Errorf("first neighbour = %+v", ns[0])
	}
	if ns[1].MAC != "b8:27:eb:11:22:33" {
		t.Errorf("second MAC = %q", ns[1].MAC)
	}
	// Single digit octets are zero padded, and a different interface is kept.
	if ns[2].MAC != "08:00:27:aa:bb:cc" || ns[2].Iface != "en1" {
		t.Errorf("third neighbour = %+v", ns[2])
	}
}

func TestParseARPOutputWindows(t *testing.T) {
	out := `Interface: 192.168.1.100 --- 0xc
  Internet Address      Physical Address      Type
  192.168.1.1           00-11-22-33-44-55     dynamic
  192.168.1.20          b8-27-eb-11-22-33     dynamic
  192.168.1.255         ff-ff-ff-ff-ff-ff     static
  224.0.0.22            01-00-5e-00-00-16     static
  239.255.255.250       01-00-5e-7f-ff-fa     static
Interface: 10.0.0.5 --- 0x5
  Internet Address      Physical Address      Type
  10.0.0.1              52-54-00-aa-bb-cc     dynamic
`
	ns := parseARPOutputWindows(out)
	if len(ns) != 3 {
		t.Fatalf("got %d neighbours, want 3 (multicast/broadcast excluded): %+v", len(ns), ns)
	}
	if ns[0].IP != "192.168.1.1" || ns[0].MAC != "00:11:22:33:44:55" || ns[0].Iface != "192.168.1.100" {
		t.Errorf("first neighbour = %+v", ns[0])
	}
	// The interface switches mid-output and must be tracked per row.
	if ns[2].IP != "10.0.0.1" || ns[2].Iface != "10.0.0.5" {
		t.Errorf("third neighbour = %+v", ns[2])
	}
}
