package scanner

import (
	"encoding/csv"
	"net"
	"strconv"
	"strings"
)

// LocalListener is a socket this machine is listening on. It is produced by the
// per-platform enumeration (see listener_<os>.go) and consumed by the scanner.
type LocalListener struct {
	Host    string
	Port    int
	Proto   string
	Inode   string
	Process string
	PID     int
	State   string
}

// This file holds the pure parsing helpers for the darwin and windows listener
// enumeration. They carry no build tags so they can be unit tested on any host,
// which is how those code paths are verified without access to those systems.

// splitHostPortLoose splits "host:port" and also accepts the "*:8080" and
// "*:*" forms these tools print for wildcard binds.
func splitHostPortLoose(s string) (string, string) {
	if h, p, err := net.SplitHostPort(s); err == nil {
		return h, p
	}
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", ""
	}
	return s[:i], s[i+1:]
}

// parseLsofListeners parses `lsof -nP -iTCP -sTCP:LISTEN` and `-iUDP` output:
//
//	COMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME
//	node    12345 user   23u  IPv6 0x1234      0t0  TCP *:8080 (LISTEN)
//	nginx     999 root    6u  IPv4 0xabcd      0t0  TCP 127.0.0.1:80 (LISTEN)
//	node    12345 user   24u  IPv6 0x1235      0t0  UDP *:5353
//
// The columns between FD and NAME vary, so the protocol token is located by
// value rather than by a fixed index.
func parseLsofListeners(out string) []LocalListener {
	res := []LocalListener{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "COMMAND") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 9 {
			continue
		}
		proto := ""
		name := ""
		for i := 4; i < len(f); i++ {
			if f[i] == "TCP" || f[i] == "UDP" {
				proto = strings.ToLower(f[i])
				if i+1 < len(f) {
					name = f[i+1]
				}
				break
			}
		}
		if proto == "" || name == "" {
			continue
		}
		name = strings.TrimSpace(strings.TrimSuffix(name, "(LISTEN)"))
		host, portStr := splitHostPortLoose(name)
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		pid, _ := strconv.Atoi(f[1])
		res = append(res, LocalListener{
			Host: host, Port: port, Proto: proto, Process: f[0], PID: pid, State: "listen",
		})
	}
	return res
}

// parseMacNetstat parses BSD `netstat -an -p tcp` / `-an -p udp` output. macOS
// joins the address and port with a dot rather than a colon:
//
//	tcp4       0      0  127.0.0.1.8080         *.*                    LISTEN
//	tcp6       0      0  ::1.80                 *.*                    LISTEN
//	udp4       0      0  *.5353                 *.*
//
// TCP rows are kept only when their state is LISTEN, because `netstat -an`
// lists every socket. UDP rows have no state column and every bound UDP socket
// is effectively a listener.
func parseMacNetstat(out string) []LocalListener {
	res := []LocalListener{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		proto := ""
		switch {
		case strings.HasPrefix(f[0], "tcp"):
			proto = "tcp"
		case strings.HasPrefix(f[0], "udp"):
			proto = "udp"
		default:
			continue
		}
		if proto == "tcp" {
			if len(f) < 6 || !strings.EqualFold(f[len(f)-1], "LISTEN") {
				continue
			}
		}
		local := f[3]
		i := strings.LastIndex(local, ".")
		if i < 0 {
			continue
		}
		host := local[:i]
		port, err := strconv.Atoi(local[i+1:])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		res = append(res, LocalListener{Host: host, Port: port, Proto: proto, State: "listen"})
	}
	return res
}

// parseWinNetstat parses Windows `netstat -ano`:
//
//	TCP    0.0.0.0:8080        0.0.0.0:0     LISTENING       1234
//	TCP    [::]:8080           [::]:0        LISTENING       1234
//	UDP    0.0.0.0:5353        *:*                            1234
//
// The state column is localised on non-English Windows ("ABHÖREN" on German,
// "À L'ÉCOUTE" on French, "侦听" in some builds), so listening sockets are
// recognised by their wildcard remote endpoint instead, which every locale
// prints identically.
func parseWinNetstat(out string) []LocalListener {
	res := []LocalListener{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		proto := ""
		remote := ""
		state := ""
		pidStr := ""
		switch strings.ToUpper(f[0]) {
		case "TCP":
			if len(f) < 5 {
				continue
			}
			proto, remote, state, pidStr = "tcp", f[2], f[3], f[4]
		case "UDP":
			proto, pidStr = "udp", f[3]
		default:
			continue
		}
		if proto == "tcp" && !isListeningRow(remote, state) {
			continue
		}
		host, portStr := splitHostPortLoose(f[1])
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		pid, _ := strconv.Atoi(pidStr)
		res = append(res, LocalListener{Host: host, Port: port, Proto: proto, PID: pid, State: "listen"})
	}
	return res
}

// isListeningRow reports whether a `netstat -ano` TCP row is a listening socket.
//
// A listener always has a wildcard remote endpoint (`0.0.0.0:0`, `[::]:0` or
// `*:*`), which is locale independent; the English state token is only used as
// a fallback for rows that carry a concrete peer.
func isListeningRow(remote, state string) bool {
	if isWildcardEndpoint(remote) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(state), "LISTENING")
}

// isWildcardEndpoint reports whether "host:port" is the wildcard "no peer" form.
func isWildcardEndpoint(addr string) bool {
	host, port := splitHostPortLoose(addr)
	if port != "0" && port != "*" {
		return false
	}
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "", "*", "0.0.0.0", "::":
		return true
	}
	return false
}

// parseTasklist maps PIDs to image names from `tasklist /FO CSV /NH`:
//
//	"chrome.exe","1234","Console","1","120,000 K"
func parseTasklist(out string) map[int]string {
	m := map[int]string{}
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return m
	}
	for _, rec := range records {
		if len(rec) < 2 {
			continue
		}
		pid, perr := strconv.Atoi(strings.TrimSpace(rec[1]))
		if perr != nil {
			continue
		}
		m[pid] = strings.TrimSpace(rec[0])
	}
	return m
}

// dedupeListeners collapses duplicate proto/port/host observations, keeping the
// entry that carries a process name.
func dedupeListeners(in []LocalListener) []LocalListener {
	out := in[:0]
	seen := map[string]int{}
	for _, l := range in {
		key := l.Proto + "|" + strings.ToLower(l.Host) + "|" + strconv.Itoa(l.Port)
		if idx, ok := seen[key]; ok {
			if out[idx].Process == "" && l.Process != "" {
				out[idx] = l
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, l)
	}
	return out
}
