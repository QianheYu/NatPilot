package netutil

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"
)

// TCPCheck attempts a TCP connection and returns (open, latency, error).
func TCPCheck(ctx context.Context, host string, port int, timeout time.Duration) (bool, time.Duration, error) {
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	lat := time.Since(start)
	if err != nil {
		return false, lat, err
	}
	_ = conn.Close()
	return true, lat, nil
}

// UDPProbe sends a datagram and reports whether an ICMP port-unreachable comes
// back. A closed UDP port on a reachable host answers with ECONNREFUSED on the
// second read, which is the only portable signal; silence is reported as
// "unknown/open|filtered".
func UDPProbe(ctx context.Context, host string, port int, timeout time.Duration) (string, time.Duration, error) {
	raddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "unknown", 0, err
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return "unknown", 0, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)
	start := time.Now()
	// A DNS-ish payload gives application services a chance to answer.
	payload := []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if _, err := conn.Write(payload); err != nil {
		return "unknown", time.Since(start), err
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "connection refused") {
			return "closed", time.Since(start), nil
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return "open|filtered", time.Since(start), nil
		}
		return "unknown", time.Since(start), err
	}
	_ = n
	return "open", time.Since(start), nil
}

// GrabBanner reads a short greeting from a TCP service (best effort).
func GrabBanner(ctx context.Context, host string, port int, timeout time.Duration) string {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return ""
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	// Ports that speak first (SSH, SMTP, FTP, ...) need no probe.
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err == nil && n > 0 {
		return sanitizeBanner(buf[:n])
	}
	// HTTP-ish ports: send a HEAD request and read the status line.
	if isHTTPPort(port) {
		_, _ = conn.Write([]byte("HEAD / HTTP/1.0\r\nHost: " + host + "\r\nUser-Agent: NatPilot\r\n\r\n"))
		n, err = conn.Read(buf)
		if err == nil && n > 0 {
			return sanitizeBanner(buf[:n])
		}
	}
	return ""
}

func isHTTPPort(port int) bool {
	switch port {
	case 80, 81, 88, 443, 8000, 8008, 8080, 8081, 8088, 8090, 8443, 8888, 9000, 9443, 5000:
		return true
	}
	return false
}

func sanitizeBanner(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// GuessService maps a port number to a well known service name.
func GuessService(port int) string {
	if s, ok := wellKnownPorts[port]; ok {
		return s
	}
	return ""
}

var wellKnownPorts = map[int]string{
	20: "ftp-data", 21: "ftp", 22: "ssh", 23: "telnet", 25: "smtp", 53: "dns",
	67: "dhcp", 68: "dhcp", 69: "tftp", 80: "http", 88: "kerberos", 110: "pop3",
	111: "rpcbind", 119: "nntp", 123: "ntp", 135: "msrpc", 137: "netbios-ns",
	138: "netbios-dgm", 139: "netbios-ssn", 143: "imap", 161: "snmp", 162: "snmptrap",
	179: "bgp", 389: "ldap", 443: "https", 445: "smb", 465: "smtps", 514: "syslog",
	515: "lpd", 548: "afp", 554: "rtsp", 587: "submission", 631: "ipp", 636: "ldaps",
	873: "rsync", 993: "imaps", 995: "pop3s", 1080: "socks", 1194: "openvpn",
	1433: "mssql", 1521: "oracle", 1701: "l2tp", 1723: "pptp", 1883: "mqtt",
	1900: "ssdp", 2049: "nfs", 2082: "cpanel", 2083: "cpanel-ssl", 2181: "zookeeper",
	2375: "docker", 2376: "docker-tls", 3000: "http-alt", 3128: "squid", 3306: "mysql",
	3389: "rdp", 3478: "stun", 4000: "icmp-echo", 4369: "epmd", 4848: "glassfish",
	5000: "http-alt", 5060: "sip", 5061: "sips", 5222: "xmpp", 5351: "nat-pmp",
	5353: "mdns", 5357: "wsdapi", 5432: "postgres", 5555: "adb", 5601: "kibana",
	5672: "amqp", 5900: "vnc", 5984: "couchdb", 6000: "x11", 6379: "redis",
	6443: "kube-apiserver", 6881: "bittorrent", 7001: "weblogic", 7077: "spark",
	8000: "http-alt", 8008: "http-alt", 8080: "http-proxy", 8081: "http-alt",
	8086: "influxdb", 8088: "http-alt", 8090: "http-alt", 8123: "clickhouse",
	8161: "activemq", 8200: "vault", 8333: "bitcoin", 8443: "https-alt",
	8500: "consul", 8686: "jmx", 8888: "http-alt", 9000: "http-alt", 9042: "cassandra",
	9090: "prometheus", 9092: "kafka", 9100: "node-exporter", 9200: "elasticsearch",
	9300: "elasticsearch", 9418: "git", 9999: "http-alt", 10000: "webmin",
	11211: "memcached", 15672: "rabbitmq-mgmt", 16379: "redis-cluster",
	27017: "mongodb", 27018: "mongodb", 32400: "plex", 50000: "sap", 50070: "hadoop-nn",
	61616: "activemq",
}
