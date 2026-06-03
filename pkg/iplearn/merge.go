package iplearn

import (
	"context"
	"net"
)

// ServiceConfig describes how one service's IP set is sourced.
type ServiceConfig struct {
	// Name is the service key (matches Learner service keys).
	Name string
	// Domain is the external lookup key (e.g. "googlevideo.com"). Empty disables
	// the external source for this service.
	Domain string
	// ExternalEnabled gates the external Source for this service even when a
	// Domain is set.
	ExternalEnabled bool
}

// Config is the library configuration: the per-service source map plus the
// shrink-guard ratio. No file I/O — callers build this from their own config.
type Config struct {
	Services []ServiceConfig
	// ShrinkMinRatio is the floor a merged set may shrink to relative to the
	// last-good size before the refresh is rejected (e.g. 0.7 = reject below
	// 70%). Non-positive disables the guard. Mirrors aggregate.ShrinkOK.
	ShrinkMinRatio float64
}

// ShrinkOK reports whether next is an acceptable size given prev under
// minRatio. A non-positive prev (first run) or minRatio (disabled) always
// passes; growth always passes. Mirrors aggregate.ShrinkOK so the self-heal
// pipeline behaves identically to the hostlist pipeline.
func ShrinkOK(prev, next int, minRatio float64) bool {
	if prev <= 0 || minRatio <= 0 {
		return true
	}
	return float64(next) >= float64(prev)*minRatio
}

// MergeResult is the outcome of a MergeSources call.
type MergeResult struct {
	// CIDRs is the accepted, deduplicated, sorted union (either the fresh merge
	// or the retained last-good set when the guard tripped).
	CIDRs []*net.IPNet
	// Rejected is true when the shrink guard rejected the fresh merge and
	// lastGood was retained instead.
	Rejected bool
	// FreshCount is the size of the fresh merge before the guard decision.
	FreshCount int
}

// MergeSources unions the external CIDRs (from a Source, may be empty/nil) with
// the learned CIDRs (from the Learner snapshot, may be empty/nil), deduplicates
// by containment, and applies the shrink guard against lastGood.
//
// If the fresh union drops below minRatio of len(lastGood), the merge is
// rejected and lastGood is returned unchanged (Rejected=true) — a half-broken
// upstream cannot collapse a service's coverage. Otherwise the fresh union is
// returned. lastGood may be nil (first run: the guard always passes).
func MergeSources(external, learned, lastGood []*net.IPNet, minRatio float64) MergeResult {
	set := map[string]*net.IPNet{}
	for _, n := range external {
		if n != nil {
			addToSet(set, normalizeNet(n))
		}
	}
	for _, n := range learned {
		if n != nil {
			addToSet(set, normalizeNet(n))
		}
	}
	fresh := make([]*net.IPNet, 0, len(set))
	for _, n := range set {
		fresh = append(fresh, n)
	}
	sortNets(fresh)

	res := MergeResult{FreshCount: len(fresh)}
	if !ShrinkOK(len(lastGood), len(fresh), minRatio) {
		res.Rejected = true
		res.CIDRs = cloneNets(lastGood)
		sortNets(res.CIDRs)
		return res
	}
	res.CIDRs = fresh
	return res
}

// BuildService fetches the external source for cfg (when enabled and a domain
// is set), unions it with the learner snapshot, and applies the shrink guard.
// A failed external fetch is surfaced in err but the learned set is still merged
// (best-effort: external is supplementary). When src is nil or external is
// disabled, only the learned set is used.
func BuildService(ctx context.Context, src Source, learner *Learner, cfg ServiceConfig, lastGood []*net.IPNet, minRatio float64) (MergeResult, error) {
	var external []*net.IPNet
	var fetchErr error
	if src != nil && cfg.ExternalEnabled && cfg.Domain != "" {
		external, fetchErr = src.Fetch(ctx, cfg.Domain)
	}
	var learned []*net.IPNet
	if learner != nil {
		learned = learner.Snapshot(cfg.Name)
	}
	return MergeSources(external, learned, lastGood, minRatio), fetchErr
}

// normalizeNet canonicalizes a net so v4 entries share the 4-byte form (so a
// learned /32 and an external /32 dedup against each other).
func normalizeNet(n *net.IPNet) *net.IPNet {
	if v4 := n.IP.To4(); v4 != nil {
		ones, _ := n.Mask.Size()
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(ones, 32)}
	}
	return dupNet(n)
}

func cloneNets(in []*net.IPNet) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(in))
	for _, n := range in {
		if n != nil {
			out = append(out, dupNet(n))
		}
	}
	return out
}
