//go:build unix

package externalbox

import "syscall"

// syscallKill0 probes whether a pid still exists without signalling it.
func syscallKill0(pid int) error { return syscall.Kill(pid, 0) }
