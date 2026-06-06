//go:build !linux

package dataplane

import "syscall"

// markControl is a no-op off Linux (SO_MARK is Linux-only). Lets the package build
// and unit-test on dev machines; the real marking happens on the router (Linux).
func markControl(mark int) func(network, address string, c syscall.RawConn) error {
	return nil
}
