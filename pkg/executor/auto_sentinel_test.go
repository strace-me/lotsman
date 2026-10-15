package executor

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// This is the failure that took nfqws down on the router. A rung migrated from v1
// carries the "auto" sentinel (v1 spelled it as an EMPTY strategy_id), Enable got
// it verbatim, and `switchTo` ran:
//
//	ln -sfn /opt/zapret-lotsman/auto.sh /opt/zapret-lotsman/active.sh
//	/etc/init.d/nfqws restart
//
// active.sh then pointed at a file that does not exist, nfqws came back dead, and
// the last-known-good rollback could not save it because nothing alive had ever
// been observed to seed it. The startup guard in cmd/lotsmand catches this before
// boot; this is the same bug arriving at runtime, after boot.
func TestAutoSentinelIsNeverSymlinkedAsActive(t *testing.T) {
	s, r := switcherFor(t, func(context.Context) bool { return true })

	if err := s.Enable(context.Background(), "youtube", "auto"); err != nil {
		t.Fatalf("auto sentinel returned an error instead of deferring: %v", err)
	}
	if got := r.linkedScripts(); len(got) != 0 {
		t.Fatalf("active symlink written for the auto sentinel: %v", got)
	}
	if slices.Contains(r.cmdsNames(), "/etc/init.d/nfqws") {
		t.Fatalf("engine restarted for a sentinel strategy; it would come back on a missing launcher")
	}
}

// The guard above is worthless if it also swallows a real request: a pinned
// strategy must still switch, symlink and restart exactly as before.
func TestConcreteStrategyStillSwitches(t *testing.T) {
	s, r := switcherFor(t, func(context.Context) bool { return true })

	if err := s.Enable(context.Background(), "youtube", "alt12"); err != nil {
		t.Fatalf("concrete strategy failed to apply: %v", err)
	}
	if got, want := filepath.Base(r.lastLink()), "alt12.sh"; got != want {
		t.Fatalf("active.sh points at %q, want %q", got, want)
	}
	if !slices.Contains(r.cmdsNames(), "/etc/init.d/nfqws") {
		t.Fatalf("engine was not restarted after a real switch: %v", r.cmds)
	}
}
