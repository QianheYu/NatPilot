//go:build !linux && !darwin && !windows

package scanner

import "errors"

// listLocalListeners is implemented for linux (via /proc), darwin (netstat +
// lsof) and windows (netstat -ano + tasklist). On any other platform the
// scanner falls back to probing the loopback interface.
func listLocalListeners() ([]LocalListener, error) {
	return nil, errors.New("local listener enumeration is not implemented for this platform")
}
