package executor

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// TestVPNTargetAdvisory locks the single-writer invariant: the VPN executor uses
// the ranker's advised node when there is one, and falls back to the pool when
// the advice is empty or absent. (The ranker never writes the selector itself.)
func TestVPNTargetAdvisory(t *testing.T) {
	v := NewVPN(nil, true, discardLog())
	if got := v.target("youtube", "vpn_url_test"); got != "vpn_url_test" {
		t.Errorf("no advisor: target = %q, want the pool", got)
	}

	advice := map[string]string{"youtube": "de-node", "ai": ""}
	v.WithBestNode(func(s string) string { return advice[s] })
	if got := v.target("youtube", "vpn_url_test"); got != "de-node" {
		t.Errorf("with advice: target = %q, want de-node", got)
	}
	if got := v.target("ai", "vpn_url_test"); got != "vpn_url_test" {
		t.Errorf("empty advice: target = %q, want pool fallback", got)
	}
	if got := v.target("unknown", "vpn_url_test"); got != "vpn_url_test" {
		t.Errorf("no advice for service: target = %q, want pool fallback", got)
	}
}

// recRunner records the commands it is asked to run.
type recRunner struct{ cmds []string }

func (r *recRunner) Run(_ context.Context, name string, args ...string) error {
	r.cmds = append(r.cmds, name+" "+strings.Join(args, " "))
	return nil
}

func TestZapretSwitchAndIdempotency(t *testing.T) {
	r := &recRunner{}
	z := NewZapret(r, "/opt/z", "/opt/z/active.sh", "/etc/init.d/nfqws", false, discardLog())
	ctx := context.Background()

	// First enable: symlink swap + restart.
	if err := z.Enable(ctx, "discord", "alt12"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ln -sfn /opt/z/alt12.sh /opt/z/active.sh",
		"/etc/init.d/nfqws restart",
	}
	if !eq(r.cmds, want) {
		t.Fatalf("first enable cmds = %v, want %v", r.cmds, want)
	}

	// Re-enabling the same strategy is a no-op (no restart churn).
	r.cmds = nil
	if err := z.Enable(ctx, "discord", "alt12"); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("idempotent enable ran commands: %v", r.cmds)
	}

	// Switching to a different strategy swaps + restarts again.
	r.cmds = nil
	if err := z.Enable(ctx, "discord", "alt11"); err != nil {
		t.Fatal(err)
	}
	if !eq(r.cmds, []string{"ln -sfn /opt/z/alt11.sh /opt/z/active.sh", "/etc/init.d/nfqws restart"}) {
		t.Errorf("switch cmds = %v", r.cmds)
	}
}

func TestZapretDryRunRunsNothing(t *testing.T) {
	r := &recRunner{}
	z := NewZapret(r, "/opt/z", "/opt/z/active.sh", "/etc/init.d/nfqws", true, discardLog())
	if err := z.Enable(context.Background(), "discord", "alt12"); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("dry-run ran commands: %v", r.cmds)
	}
	// dry-run still tracks current so a later real switch is detected.
	if z.current != "alt12" {
		t.Errorf("dry-run did not track current: %q", z.current)
	}
}

func TestZapretClass(t *testing.T) {
	z := NewZapret(&recRunner{}, "/d", "/d/a.sh", "/etc/init.d/nfqws", true, discardLog())
	if z.Class() != "zapret" {
		t.Errorf("class = %q, want zapret", z.Class())
	}
}

func TestByeDPIClassAndSwitch(t *testing.T) {
	r := &recRunner{}
	b := NewByeDPI(r, "/opt/b", "/opt/b/active.sh", "/etc/init.d/byedpi", false, discardLog())
	if b.Class() != "byedpi" {
		t.Errorf("class = %q, want byedpi", b.Class())
	}
	if err := b.Enable(context.Background(), "youtube", "fake_split"); err != nil {
		t.Fatal(err)
	}
	if !eq(r.cmds, []string{"ln -sfn /opt/b/fake_split.sh /opt/b/active.sh", "/etc/init.d/byedpi restart"}) {
		t.Errorf("byedpi switch cmds = %v", r.cmds)
	}
}

// fakeSelector records SetSelector calls.
type fakeSelector struct{ calls []string }

func (f *fakeSelector) SetSelector(_ context.Context, selector, target string) error {
	f.calls = append(f.calls, selector+"->"+target)
	return nil
}

func TestZapretRouteToDirect(t *testing.T) {
	r := &recRunner{}
	sel := &fakeSelector{}
	z := NewZapret(r, "/opt/z", "/opt/z/active.sh", "/etc/init.d/nfqws", false, discardLog())
	z.RouteToDirect(sel)
	ctx := context.Background()

	// Enable on a zapret step: route the service direct AND swap nfqws.
	if err := z.Enable(ctx, "youtube", "alt12"); err != nil {
		t.Fatal(err)
	}
	if !eq(sel.calls, []string{"sel-youtube->direct"}) {
		t.Errorf("selector calls = %v, want [sel-youtube->direct]", sel.calls)
	}
	if !eq(r.cmds, []string{"ln -sfn /opt/z/alt12.sh /opt/z/active.sh", "/etc/init.d/nfqws restart"}) {
		t.Errorf("cmds = %v", r.cmds)
	}

	// Re-entering the same zapret step still re-routes to direct (the selector may
	// have drifted to a VPN pool), but does not churn nfqws.
	r.cmds, sel.calls = nil, nil
	if err := z.Enable(ctx, "youtube", "alt12"); err != nil {
		t.Fatal(err)
	}
	if !eq(sel.calls, []string{"sel-youtube->direct"}) {
		t.Errorf("re-enable must re-route, got %v", sel.calls)
	}
	if len(r.cmds) != 0 {
		t.Errorf("re-enable must not restart nfqws, got %v", r.cmds)
	}
}

func TestZapretRouteDryRunNoCall(t *testing.T) {
	sel := &fakeSelector{}
	z := NewZapret(&recRunner{}, "/d", "/d/a.sh", "/etc/init.d/nfqws", true, discardLog())
	z.RouteToDirect(sel)
	if err := z.Enable(context.Background(), "youtube", "alt12"); err != nil {
		t.Fatal(err)
	}
	if len(sel.calls) != 0 {
		t.Errorf("dry-run must not call SetSelector, got %v", sel.calls)
	}
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
