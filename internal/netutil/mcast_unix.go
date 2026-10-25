//go:build linux || darwin

package netutil

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// SetMulticastInterface pins the outgoing multicast interface of conn to ip.
//
// Binding a socket to 192.168.1.8 is not enough for multicast: on a multi-homed
// host the kernel picks the egress interface from the multicast routing table,
// which usually points at the interface that owns the default route (a VPN/TUN
// adapter on many desktops). IP_MULTICAST_IF is the only portable way to say
// "send this M-SEARCH out of the LAN card".
func SetMulticastInterface(conn *net.UDPConn, ip net.IP) error {
	v4 := ip.To4()
	if v4 == nil {
		return fmt.Errorf("multicast interface requires an IPv4 address, got %v", ip)
	}
	rc, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if cerr := rc.Control(func(fd uintptr) {
		serr = unix.SetsockoptInet4Addr(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_IF,
			[4]byte{v4[0], v4[1], v4[2], v4[3]})
	}); cerr != nil {
		return cerr
	}
	return serr
}
