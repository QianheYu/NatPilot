//go:build windows

package netutil

import (
	"fmt"
	"net"

	"golang.org/x/sys/windows"
)

// SetMulticastInterface pins the outgoing multicast interface of conn to ip.
//
// Windows chooses the egress interface for 239.255.255.250 from its multicast
// routing table, which follows the interface metric. On a desktop with a VPN or
// Hyper-V/VMware adapter that is often not the LAN card, so SSDP would leave
// through the tunnel. IP_MULTICAST_IF fixes that without any external command.
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
		serr = windows.SetsockoptInet4Addr(windows.Handle(fd), windows.IPPROTO_IP, windows.IP_MULTICAST_IF,
			[4]byte{v4[0], v4[1], v4[2], v4[3]})
	}); cerr != nil {
		return cerr
	}
	return serr
}
