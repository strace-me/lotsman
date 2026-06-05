package zapretgen

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategycat"
	"github.com/strace-me/lotsman/pkg/zaptune"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func zapretStep() registry.ChainStep {
	return registry.ChainStep{Position: 0, State: registry.StatePreferred, StrategyClass: strategy.ClassZapret}
}
func vpnStep() registry.ChainStep {
	return registry.ChainStep{Position: 1, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"}
}

func discordSvc() registry.Service {
	return registry.Service{Name: "discord", Profile: "voice", Domains: []string{"discord.media"}, Chain: []registry.ChainStep{zapretStep(), vpnStep()}}
}

func recipes() []strategycat.Recipe {
	return []strategycat.Recipe{
		{ID: "disc-1", TargetClass: strategycat.ClassDiscordTCP, NfqwsArgs: []string{"--filter-tcp=443", "--hostlist-domains={{DOMAINS}}", "--dpi-desync=fake"}},
	}
}

func TestZapretActiveFiltersByBrainPosition(t *testing.T) {
	svcs := []registry.Service{discordSvc()}
	// position 0 = zapret rung -> active.
	r := New(svcs, func(string) int { return 0 }, recipes(), zaptune.FirstPicker, "", quietLog())
	if got := r.zapretActive(); len(got) != 1 || got[0].Name != "discord" {
		t.Fatalf("pos0 should be zapret-active, got %v", got)
	}
	// position 1 = VPN rung -> NOT active (escalated past zapret).
	r2 := New(svcs, func(string) int { return 1 }, recipes(), zaptune.FirstPicker, "", quietLog())
	if got := r2.zapretActive(); len(got) != 0 {
		t.Errorf("pos1 (VPN) must not be zapret-active, got %v", got)
	}
}

func TestReconcileProposeOnlyDiffNoop(t *testing.T) {
	r := New([]registry.Service{discordSvc()}, func(string) int { return 0 }, recipes(), zaptune.FirstPicker, "", quietLog())
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r.last == "" {
		t.Fatal("covered config should set last (composed launcher)")
	}
	first := r.last
	// Second identical pass: still composes the same launcher (churn-guard keeps last).
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile2: %v", err)
	}
	if r.last != first {
		t.Error("unchanged compose must not change last (no churn)")
	}
}

func TestReconcileUncoveredKeepsExisting(t *testing.T) {
	// gaming has no matching recipe in the catalog -> not covered -> last stays "".
	gaming := registry.Service{Name: "gaming-epic", Profile: "gaming", Domains: []string{"epicgames.com"}, Chain: []registry.ChainStep{zapretStep()}}
	r := New([]registry.Service{gaming}, func(string) int { return 0 }, recipes(), zaptune.FirstPicker, "", quietLog())
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r.last != "" {
		t.Errorf("uncovered must not set a composed config, last=%q", r.last)
	}
}
