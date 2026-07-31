//go:build !unix

package core

import (
	"io/fs"
	"testing"
)

// assertSocketGID is a no-op off unix: there is no Stat_t to read a gid from, and the
// group mechanism itself is a POSIX-permissions feature. The rest of the socket test
// still runs, which is the point of the split — the package must stay testable here.
func assertSocketGID(t *testing.T, fi fs.FileInfo, gid string) { t.Helper() }
