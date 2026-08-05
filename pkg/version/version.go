// Package version carries the build identity every Lotsman binary reports.
//
// One tag, one string, every target — the daemon, the control CLI, the desktop
// service, the window, the tray and the Android facade. The submodules under
// client/ have their own go.mod but `replace ../..` back to the root, so they
// import this package like anything else and cannot drift into a second scheme.
//
// Until now `cmd/lotsmand` held a private `version = "dev"` that nothing set,
// and the client reported nothing at all. That makes a bug report unanswerable
// ("which build?"), a changelog boundaryless, and a rollback a hunt through
// .bak files by date — all of which matter more, not less, once a release is
// being stabilised.
package version

import (
	"fmt"
	"runtime/debug"
)

// Set by the build script through -ldflags -X. Defaults describe an untagged
// developer build honestly rather than claiming a version nobody released.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// String is the one-line identity: "v6.13 (a3791b0, 2026-08-05)". Missing parts
// are omitted rather than printed empty, so a `go build` with no ldflags still
// produces something truthful.
func String() string {
	out := Version
	if c := commit(); c != "" {
		if Date != "" {
			return fmt.Sprintf("%s (%s, %s)", out, c, Date)
		}
		return fmt.Sprintf("%s (%s)", out, c)
	}
	return out
}

// commit falls back to the VCS stamp the Go toolchain embeds, so even a plain
// `go build` identifies its source revision.
func commit() string {
	if Commit != "" {
		return Commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			return s.Value[:7]
		}
	}
	return ""
}
