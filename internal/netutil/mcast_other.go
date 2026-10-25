//go:build !linux && !darwin && !windows

package netutil

import "net"

// SetMulticastInterface is a no-op on platforms without a known socket option
// API; the caller still binds the socket to the interface address, which is
// enough on single-homed systems.
func SetMulticastInterface(conn *net.UDPConn, ip net.IP) error { return nil }
