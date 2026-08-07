//go:build !linux

package dataplane

import "syscall"

// SO_BINDTODEVICE is Linux-only. Elsewhere there is no way to make this dial
// escape the tun, so the hook is absent and BoundProber refuses to be built —
// a probe that silently measured the tunnel instead is the failure this exists
// to prevent.
func bindToDeviceControl(func() string) func(network, address string, c syscall.RawConn) error {
	return nil
}
