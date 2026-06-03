package vpnbalance

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/strace-me/lotsman/pkg/balancer"
	"github.com/strace-me/lotsman/pkg/dataplane"
)

type fakeAPI struct {
	info   map[string]dataplane.ProxyInfo
	delays map[string]int // node -> ms (present = alive)
	now    string         // current selector pick (mutated by SetSelector)
	sets   []string       // record of SetSelector targets
}

func (f *fakeAPI) Proxy(_ context.Context, name string) (dataplane.ProxyInfo, error) {
	pi, ok := f.info[name]
	if !ok {
		return dataplane.ProxyInfo{}, fmt.Errorf("no such proxy %q", name)
	}
	if name == "vpn" {
		pi.Now = f.now
	}
	return pi, nil
}

func (f *fakeAPI) NodeDelay(_ context.Context, name, _ string, _ time.Duration) (int, error) {
	if d, ok := f.delays[name]; ok {
		return d, nil
	}
	return 0, fmt.Errorf("node %q dead", name)
}

func (f *fakeAPI) SetSelector(_ context.Context, selector, target string) error {
	f.now = target
	f.sets = append(f.sets, target)
	return nil
}

func newFake(now string, delays map[string]int) *fakeAPI {
	return &fakeAPI{
		info: map[string]dataplane.ProxyInfo{
			"vpn":         {Type: "Selector", All: []string{"vpn-pool", "fastvpn-nl", "stealthsurf"}},
			"vpn-pool":    {Type: "URLTest"},
			"fastvpn-nl":   {Type: "Hysteria2"},
			"stealthsurf": {Type: "Hysteria2"},
		},
		delays: delays,
		now:    now,
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRebalanceSwitchesOffDeadNode(t *testing.T) {
	// selector stuck on the dead stealthsurf; fastvpn-nl is alive.
	f := newFake("stealthsurf", map[string]int{"fastvpn-nl": 148})
	b := New(f, "vpn", "https://x/204", 3, balancer.ProfileFor("general"), false, quietLog())

	if err := b.Rebalance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.now != "fastvpn-nl" {
		t.Fatalf("now=%q, want fastvpn-nl", f.now)
	}
	if len(f.sets) != 1 || f.sets[0] != "fastvpn-nl" {
		t.Fatalf("sets=%v, want [fastvpn-nl]", f.sets)
	}
}

func TestRebalanceNoSwitchWhenBestAlreadyActive(t *testing.T) {
	// both alive, fastvpn-nl faster and already selected -> no PUT.
	f := newFake("fastvpn-nl", map[string]int{"fastvpn-nl": 50, "stealthsurf": 200})
	b := New(f, "vpn", "https://x/204", 3, balancer.ProfileFor("general"), false, quietLog())

	if err := b.Rebalance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.sets) != 0 {
		t.Fatalf("expected no selector change, got sets=%v", f.sets)
	}
}

func TestRebalanceAllDownNoSwitch(t *testing.T) {
	// every node dead -> hold, don't thrash the selector.
	f := newFake("stealthsurf", map[string]int{})
	b := New(f, "vpn", "https://x/204", 2, balancer.ProfileFor("general"), false, quietLog())

	if err := b.Rebalance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.sets) != 0 {
		t.Fatalf("expected no switch when all down, got sets=%v", f.sets)
	}
}

func TestRebalanceSkipsNestedGroups(t *testing.T) {
	// vpn-pool (URLTest) must never be probed or pinned.
	f := newFake("fastvpn-nl", map[string]int{"fastvpn-nl": 50, "stealthsurf": 60})
	if _, ok := f.delays["vpn-pool"]; ok {
		t.Fatal("test setup error")
	}
	b := New(f, "vpn", "https://x/204", 1, balancer.ProfileFor("general"), false, quietLog())
	if err := b.Rebalance(context.Background()); err != nil {
		t.Fatal(err)
	}
	// best concrete node is fastvpn-nl (already active) -> no switch, and it never
	// tried to pin vpn-pool.
	for _, s := range f.sets {
		if s == "vpn-pool" {
			t.Fatal("pinned a nested group")
		}
	}
}
