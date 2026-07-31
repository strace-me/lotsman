//go:build unix

package core

import (
	"io/fs"
	"strconv"
	"syscall"
	"testing"
)

// assertSocketGID checks the socket really carries the requested group. Split into a
// unix-only file because syscall.Stat_t does not exist on Windows, and a test that
// fails to COMPILE there means the whole package is untestable on that platform —
// which is how Windows regressions in exactly this package would go unnoticed.
func assertSocketGID(t *testing.T, fi fs.FileInfo, gid string) {
	t.Helper()
	want, _ := strconv.Atoi(gid)
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Gid) != want {
		t.Errorf("socket gid = %d, want %d", st.Gid, want)
	}
}
