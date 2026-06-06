//go:build linux

package dataplane

import "syscall"

// markControl returns a net.Dialer.Control hook that stamps SO_MARK on the socket
// before connect, so the kernel fwmark routes/matches the connection. The v7 tuner
// uses it to mark its probe traffic (0x4554) so a temp nft chain diverts ONLY the
// probe to the isolated test qnum — leaving production traffic (unmarked) alone.
func markControl(mark int) func(network, address string, c syscall.RawConn) error {
	if mark == 0 {
		return nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
		}); err != nil {
			return err
		}
		return serr
	}
}
