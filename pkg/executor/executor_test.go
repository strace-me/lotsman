package executor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/strace-me/lotsman/pkg/dataplane"
)

// clashRec is a minimal Clash-API stand-in recording selector PUTs; a selector in
// notFound 404s (a DirectOnly service with no per-service selector).
type clashRec struct {
	mu       sync.Mutex
	puts     map[string]string
	notFound map[string]bool
}

func newClashServer(t *testing.T, rec *clashRec) *dataplane.ClashClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/proxies/")
		if r.Method == http.MethodPut {
			if rec.notFound[name] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var body struct {
				Name string `json:"name"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			rec.mu.Lock()
			rec.puts[name] = body.Name
			rec.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK) // GET = EnsurePool
	}))
	t.Cleanup(srv.Close)
	return dataplane.NewClashClient(srv.URL, "")
}

func TestVPNEnableSetsSelectorToPool(t *testing.T) {
	rec := &clashRec{puts: map[string]string{}}
	v := NewVPN(newClashServer(t, rec), false, discardLog())
	if err := v.Enable(context.Background(), "youtube", "vpn_url_test"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := rec.puts["sel-youtube"]; got != "vpn_url_test" {
		t.Errorf("selector sel-youtube = %q, want vpn_url_test", got)
	}
	if v.Class() != "vpn" {
		t.Errorf("class=%q, want vpn", v.Class())
	}
}

func TestVPNEnablePinsBestNode(t *testing.T) {
	rec := &clashRec{puts: map[string]string{}}
	v := NewVPN(newClashServer(t, rec), false, discardLog()).WithBestNode(func(string) string { return "de-node" })
	if err := v.Enable(context.Background(), "youtube", "vpn_url_test"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := rec.puts["sel-youtube"]; got != "de-node" {
		t.Errorf("selector = %q, want de-node (pinned)", got)
	}
}

func TestVPNEnableDryRunNoCall(t *testing.T) {
	rec := &clashRec{puts: map[string]string{}}
	v := NewVPN(newClashServer(t, rec), true, discardLog())
	if err := v.Enable(context.Background(), "youtube", "vpn_url_test"); err != nil {
		t.Fatal(err)
	}
	if len(rec.puts) != 0 {
		t.Errorf("dry-run must not PUT, got %v", rec.puts)
	}
}

func TestDirectEnableSetsSelectorDirect(t *testing.T) {
	rec := &clashRec{puts: map[string]string{}}
	d := NewDirect(newClashServer(t, rec), false, discardLog())
	if err := d.Enable(context.Background(), "ru_mail", ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := rec.puts["sel-ru_mail"]; got != "direct" {
		t.Errorf("selector = %q, want direct", got)
	}
	if d.Class() != "direct" {
		t.Errorf("class=%q, want direct", d.Class())
	}
}

func TestDirectEnableSwallowsMissingSelector(t *testing.T) {
	rec := &clashRec{puts: map[string]string{}, notFound: map[string]bool{"sel-ru_direct": true}}
	d := NewDirect(newClashServer(t, rec), false, discardLog())
	// DirectOnly service: a 404 selector is success (route rule already sends direct).
	if err := d.Enable(context.Background(), "ru_direct", ""); err != nil {
		t.Errorf("missing selector (DirectOnly) must be success, got %v", err)
	}
}

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

// recordRunner remembers every command, so a test can see which script the
// symlink ended up pointing at.
type recordRunner struct{ cmds [][]string }

func (r *recordRunner) Run(_ context.Context, name string, args ...string) error {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	return nil
}

func (r *recordRunner) lastLink() string {
	for i := len(r.cmds) - 1; i >= 0; i-- {
		if r.cmds[i][0] == "ln" {
			return r.cmds[i][2] // ln -sfn <script> <link>: the script is arg 2
		}
	}
	return ""
}

func (r *recordRunner) linkedScripts() []string {
	var out []string
	for _, c := range r.cmds {
		if c[0] == "ln" {
			out = append(out, c[2])
		}
	}
	return out
}

func switcherFor(t *testing.T, alive func(context.Context) bool) (*ScriptSwitcher, *recordRunner) {
	t.Helper()
	r := &recordRunner{}
	s := NewZapret(r, "/opt/z", "/opt/z/active.sh", "/etc/init.d/nfqws", false,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.VerifyWith(alive)
	return s, r
}

// A strategy the engine will not run must cost one rung, not the whole desync
// layer. nft's `flags bypass` means a dead engine is invisible — traffic keeps
// flowing, undesynced — so the switch has to be confirmed, not assumed.
func TestFailedSwitchRollsBackToTheLastLiveStrategy(t *testing.T) {
	engineStarts := true
	s, r := switcherFor(t, func(context.Context) bool { return engineStarts })

	if err := s.Enable(context.Background(), "youtube", "alt12"); err != nil {
		t.Fatalf("healthy switch failed: %v", err)
	}
	engineStarts = false
	err := s.Enable(context.Background(), "youtube", "alt99")

	if err == nil {
		t.Fatal("a switch the engine did not survive was reported as success")
	}
	if !strings.Contains(err.Error(), "kept \"alt12\"") {
		t.Errorf("the error does not say what is still in service: %v", err)
	}
	if got := r.lastLink(); got != "/opt/z/alt12.sh" {
		t.Errorf("active link left at %q, want the last strategy the engine ran", got)
	}
	// And the switcher must not believe it is on the strategy it rejected, or the
	// next Enable would treat alt99 as already active and skip the switch.
	if err := s.Enable(context.Background(), "youtube", "alt99"); err == nil {
		t.Error("the rejected strategy was recorded as current; a retry became a no-op")
	}
}

// With nothing known-good behind it there is nothing to keep, and the switcher
// must say that rather than silently pretend the strategy took.
func TestFailedFirstSwitchHasNothingToKeep(t *testing.T) {
	s, r := switcherFor(t, func(context.Context) bool { return false })
	err := s.Enable(context.Background(), "youtube", "alt12")
	if err == nil || !strings.Contains(err.Error(), "no known-good strategy") {
		t.Fatalf("err = %v, want a refusal naming the absent fallback", err)
	}
	if n := len(r.linkedScripts()); n != 1 {
		t.Errorf("attempted %d symlink changes, want 1 (no rollback target existed)", n)
	}
}

// Without a liveness check the switcher must behave exactly as it did before:
// this runs on a router where the check may not be wired.
func TestUnverifiedSwitchKeepsTheOldBehaviour(t *testing.T) {
	r := &recordRunner{}
	s := NewZapret(r, "/opt/z", "/opt/z/active.sh", "/etc/init.d/nfqws", false,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.Enable(context.Background(), "youtube", "alt12"); err != nil {
		t.Fatalf("unverified switch should succeed: %v", err)
	}
	if got := r.lastLink(); got != "/opt/z/alt12.sh" {
		t.Errorf("active link = %q", got)
	}
}
