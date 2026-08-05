//go:build !unix

package externalbox

import "errors"

// syscallKill0 has no meaning off unix: there is no signal 0 to probe a pid with.
// It exists so lifecycle_test.go COMPILES on Windows — without it the whole
// package is untestable there, which is the silent breakage the CI cross-build
// was added to catch, and which it caught.
func syscallKill0(int) error { return errors.New("kill(0) is unix-only") }
