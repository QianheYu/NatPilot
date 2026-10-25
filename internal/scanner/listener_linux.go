//go:build linux

package scanner

import (
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// procStates maps /proc/net/tcp state codes to names.
var procStates = map[string]string{
	"01": "established", "02": "syn_sent", "03": "syn_recv", "04": "fin_wait1",
	"05": "fin_wait2", "06": "time_wait", "07": "close", "08": "close_wait",
	"09": "last_ack", "0A": "listen", "0B": "closing",
}

// listLocalListeners parses /proc/net/{tcp,tcp6,udp,udp6} for listening (and
// bound) sockets and maps socket inodes back to owning processes.
func listLocalListeners() ([]LocalListener, error) {
	inodeMap := map[string]procRef{}
	buildInodeMap(inodeMap)

	var out []LocalListener
	files := []struct {
		path  string
		proto string
		udp   bool
	}{
		{"/proc/net/tcp", "tcp", false},
		{"/proc/net/tcp6", "tcp", false},
		{"/proc/net/udp", "udp", true},
		{"/proc/net/udp6", "udp", true},
	}
	for _, f := range files {
		b, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if i == 0 || strings.TrimSpace(line) == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			// /proc/net/<proto> columns:
			//   0 sl | 1 local_address | 2 rem_address | 3 st | 4 tx:rx |
			//   5 tr:tm | 6 retrnsmt | 7 uid | 8 timeout | 9 inode
			addr, port, err := parseHexAddr(fields[1])
			if err != nil {
				continue
			}
			state := fields[3]
			if !f.udp && procStates[state] != "listen" {
				continue
			}
			if f.udp {
				// For UDP, an unconnected socket in state 07 (CLOSE) is bound.
				if state != "07" {
					continue
				}
			}
			inode := fields[9]
			ref := inodeMap[inode]
			out = append(out, LocalListener{
				Host:    addr,
				Port:    port,
				Proto:   f.proto,
				Inode:   inode,
				Process: ref.Process,
				PID:     ref.PID,
				State:   procStates[state],
			})
		}
	}
	// De-duplicate by proto+port+host, keeping the entry with a process name.
	seen := map[string]int{}
	dedup := out[:0]
	for _, l := range out {
		key := fmt.Sprintf("%s|%s|%d", l.Proto, l.Host, l.Port)
		if idx, ok := seen[key]; ok {
			if dedup[idx].Process == "" && l.Process != "" {
				dedup[idx] = l
			}
			continue
		}
		seen[key] = len(dedup)
		dedup = append(dedup, l)
	}
	return dedup, nil
}

// parseHexAddr converts a /proc hex socket address to a host:port pair.
func parseHexAddr(s string) (string, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid address %q", s)
	}
	port, err := strconv.ParseInt(parts[1], 16, 32)
	if err != nil {
		return "", 0, err
	}
	raw, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", 0, err
	}
	switch len(raw) {
	case 4:
		// /proc stores IPv4 in little-endian order.
		ip := net.IPv4(raw[3], raw[2], raw[1], raw[0])
		return ip.String(), int(port), nil
	case 16:
		ip := make(net.IP, 16)
		// 32-bit words are stored little-endian.
		for w := 0; w < 4; w++ {
			for b := 0; b < 4; b++ {
				ip[w*4+b] = raw[w*4+(3-b)]
			}
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), int(port), nil
		}
		if ip.Equal(net.IPv6zero) {
			return "::", int(port), nil
		}
		return ip.String(), int(port), nil
	default:
		return "", 0, fmt.Errorf("unsupported address length %d", len(raw))
	}
}

type procRef struct {
	Process string
	PID     int
}

var (
	inodeCacheMu sync.Mutex
	inodeCache   = map[string]procRef{}
)

// buildInodeMap walks /proc/<pid>/fd symlinks to map socket inodes to processes.
func buildInodeMap(dst map[string]procRef) {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		name := readProcName(pid)
		fdDir := filepath.Join("/proc", p.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if !strings.HasPrefix(target, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			dst[inode] = procRef{Process: name, PID: pid}
		}
	}
	inodeCacheMu.Lock()
	inodeCache = dst
	inodeCacheMu.Unlock()
}

func readProcName(pid int) string {
	// cmdline is most descriptive; fall back to comm.
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil && len(b) > 0 {
		parts := strings.Split(string(b), "\x00")
		out := strings.TrimSpace(parts[0])
		for _, extra := range parts[1:] {
			extra = strings.TrimSpace(extra)
			if extra == "" || strings.HasPrefix(extra, "-") {
				continue
			}
			out += " " + extra
			if len(out) > 80 {
				break
			}
		}
		if base := filepath.Base(out); base != "" && base != "." && base != "/" {
			return out
		}
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}
