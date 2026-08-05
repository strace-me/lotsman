package core

import (
	"context"
	"github.com/strace-me/lotsman/pkg/version"
	"sort"
	"time"

	"github.com/strace-me/lotsman/pkg/observe"
	"github.com/strace-me/lotsman/pkg/subscription"
)

// Report is the rich /status payload a UI renders. It is a backward-compatible
// SUPERSET of the original {running, services}: those two keep their exact JSON
// paths, and every richer section is additive, so a tray decoding only the old
// Status struct is unaffected.
type Report struct {
	// Version identifies the build answering. A status with no build identity is
	// unanswerable in a bug report and useless in a rollback.
	Version       string         `json:"version"`
	Running       bool           `json:"running"`
	Verdict       Verdict        `json:"verdict"`
	Network       NetworkInfo    `json:"network"`
	Engines       []EngineStatus `json:"engines"`
	Fleet         FleetStatus    `json:"fleet"`
	Subscriptions []SubStatus    `json:"subscriptions,omitempty"`
	Services      []NodeStatus   `json:"services"`
	// DomainListsDrifted reports that a background refresh changed a declared domain
	// pack, so the running config routes a slightly older set. Deliberately NOT applied
	// on a timer — re-routing under a live tunnel unasked would drop connections the
	// operator did not ask to lose — so the UI offers to apply it instead.
	DomainListsDrifted bool `json:"domain_lists_drifted,omitempty"`
}

// Verdict is the top-line rollup — is coverage working, partial, or down. It is
// honest: State is gated by real data-plane liveness (Healthy), and the counts
// come from the brain's own per-service snapshot, never from configuration. A
// service counts as Working only when its last probe passed (Fails==0) and it is
// not chain-exhausted; one actively failing at its current rung (Fails>0, not yet
// Broken) is Failing, not Working — so a collapsing fleet is never painted green
// just because nothing has reached the end of its chain yet.
type Verdict struct {
	State   string `json:"state"` // working | partial | not-working | down
	Working int    `json:"working"`
	Failing int    `json:"failing"` // actively failing at the current rung, not yet chain-exhausted
	Broken  int    `json:"broken"`  // escalated past the last rung and still failing
	Total   int    `json:"total"`
}

// NetworkInfo identifies the network the live KB belongs to. Roaming is true
// while a network change is mid-debounce (a swap is pending).
type NetworkInfo struct {
	ID      string `json:"id,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Carrier string `json:"carrier,omitempty"`
	IFace   string `json:"iface,omitempty"`
	Roaming bool   `json:"roaming"`
}

// EngineStatus is one process the client drives. Only engines this host actually
// runs are listed — nfqws appears only where a desync rung is wired.
type EngineStatus struct {
	Name  string `json:"name"`  // lotsman | sing-box | nfqws
	State string `json:"state"` // running | stopped
}

// FleetStatus reports the exit pool. Total is the node count the last config
// generate loaded. Per-node alive/frozen/dead health is not tracked client-side
// yet (no Ranker is wired here), so it is omitted rather than faked — a follow-up.
type FleetStatus struct {
	Total int `json:"total"`
}

// SubStatus is one subscription's quota/expiry, captured from the provider's
// Subscription-Userinfo header at the last fetch.
type SubStatus struct {
	Name            string  `json:"name"`
	UsedBytes       int64   `json:"usedBytes"`
	TotalBytes      int64   `json:"totalBytes"`      // 0 = unknown/unlimited
	FractionUsed    float64 `json:"fractionUsed"`    // -1 when total is unknown
	DaysUntilExpire float64 `json:"daysUntilExpire"` // -1 when no expiry is set
	Expired         bool    `json:"expired"`
}

// Report assembles the rich status a UI renders. Every section reads a signal the
// client already holds; nothing here probes or blocks beyond the liveness check
// Healthy already does.
func (c *Core) Report(ctx context.Context) Report {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	services := c.statusLocked(ctx)
	running := c.Healthy(ctx)
	return Report{
		Version:       version.String(),
		Running:       running,
		Verdict:       verdict(running, services),
		Network:       c.networkInfo(),
		Engines:       c.engineStatuses(ctx),
		Fleet:         FleetStatus{Total: c.lastNodes},
		Subscriptions: c.subStatuses(),
		Services:      services,

		DomainListsDrifted: c.listsDrifted.Load(),
	}
}

// verdict rolls the per-service states into one honest headline. A service is
// Working only if its last probe passed (Fails==0) and it is not chain-exhausted;
// Fails>0 (still escalating) is Failing, chain-exhausted is Broken. State never
// reads "working" while any service is failing, so the headline cannot go green
// over a fleet that is quietly falling apart.
func verdict(running bool, services []NodeStatus) Verdict {
	v := Verdict{Total: len(services)}
	for _, s := range services {
		switch {
		case s.Broken:
			v.Broken++
		case s.Fails > 0:
			v.Failing++
		}
	}
	v.Working = v.Total - v.Broken - v.Failing
	switch {
	case !running:
		v.State = "down"
	case v.Total == 0 || v.Working == v.Total:
		v.State = "working"
	case v.Working == 0:
		v.State = "not-working"
	default:
		v.State = "partial"
	}
	return v
}

// networkInfo returns the live network fingerprint under roamMu, since roamLoop
// rewrites these fields from another goroutine.
func (c *Core) networkInfo() NetworkInfo {
	c.roamMu.Lock()
	defer c.roamMu.Unlock()
	return NetworkInfo{
		ID:      c.currentNet.Id,
		Kind:    c.currentNet.Kind,
		Carrier: c.currentNet.Carrier,
		IFace:   c.currentNet.IFace,
		Roaming: c.pendingCount > 0,
	}
}

// engineStatuses lists only the engines this host runs, each by MEASURED process
// liveness. sing-box uses box.Alive; nfqws is present only where a desync rung was
// wired and reports its real child state via Engine.Alive — never "running"
// inferred from rung assignment, which would show green over an uncovered plan or
// a crashed engine (LOT-49).
func (c *Core) engineStatuses(ctx context.Context) []EngineStatus {
	out := []EngineStatus{{Name: "lotsman", State: "running"}}
	sb := "stopped"
	if c.box != nil && c.box.Alive(ctx) {
		sb = "running"
	}
	out = append(out, EngineStatus{Name: "sing-box", State: sb})
	if c.zap != nil {
		st := "stopped"
		if c.zap.Alive() {
			st = "running"
		}
		out = append(out, EngineStatus{Name: "nfqws", State: st})
	}
	return out
}

// subStatuses lists every configured (enabled) subscription by name, overlaying the
// quota/expiry captured from the Subscription-Userinfo header at the last fetch. A
// subscription that yielded nodes but whose provider sent no such header still shows,
// with quota/expiry left unknown — so the UI never claims "нет подписок" while the
// fleet is running off one. Sorted by name so the output is stable. (c.conf is nil
// only in unit tests, hence the guard.)
func (c *Core) subStatuses() []SubStatus {
	c.subMu.Lock()
	info := c.subInfo
	c.subMu.Unlock()

	now := time.Now()
	var out []SubStatus
	seen := make(map[string]bool)
	if c.conf != nil {
		for _, d := range c.conf.Subscriptions {
			if !d.Enabled || seen[d.Name] {
				continue
			}
			seen[d.Name] = true
			out = append(out, subStatus(d.Name, info[d.Name], now))
		}
	}
	// Surface any captured userinfo whose name is no longer in the config.
	for name, ui := range info {
		if !seen[name] {
			out = append(out, subStatus(name, ui, now))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// subStatus maps one subscription's captured userinfo to a SubStatus. A zero-value
// Userinfo (no header captured) yields the documented "unknown" fields: TotalBytes 0,
// FractionUsed/DaysUntilExpire -1, not expired.
func subStatus(name string, ui subscription.Userinfo, now time.Time) SubStatus {
	return SubStatus{
		Name:            name,
		UsedBytes:       ui.Used(),
		TotalBytes:      ui.Total,
		FractionUsed:    ui.FractionUsed(),
		DaysUntilExpire: ui.DaysUntilExpire(now),
		Expired:         ui.Expired(now),
	}
}

// observeSnapshot returns the last passive-eye snapshot under obsMu. The eye
// replaces the whole snapshot each pass rather than mutating it, so the returned
// value is a stable past observation safe to read after the lock is dropped.
func (c *Core) observeSnapshot() observe.Snapshot {
	c.obsMu.Lock()
	defer c.obsMu.Unlock()
	return c.obsSnap
}
