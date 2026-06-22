package singbox

import (
	"encoding/json"
	"fmt"

	"github.com/strace-me/lotsman/pkg/subscription"
)

// This file surgically merges subscription nodes into an ALREADY-EXISTING
// sing-box config (e.g. the hand-tuned one on the router) instead of generating
// a fresh config. It adds node outbounds and one url-test group per node group,
// then points an existing selector at those groups. Everything else in the
// config — inbounds, route rules, DNS, manually-added nodes, the clash-api
// block — is preserved byte-for-byte.
//
// The merge is idempotent: re-running with the same (or refreshed) subscriptions
// replaces the managed outbounds rather than accumulating duplicates. Managed
// outbounds are identified by tag: every node tag we generate, every pool group
// name, and the UDP group tag. A manually-added outbound (e.g. a hysteria2 node)
// keeps its own tag and is never touched.
//
// UDP handling (UDPMode): VLESS carries UDP only via xudp (UDP-over-TCP, higher
// latency), while hysteria2/tuic are UDP-native. To keep real-time UDP on the
// native path, non-"unified" modes add a `network:udp` route rule for whatever
// rule-sets currently target the selector, pointing at a dedicated UDP group:
//
//	unified          — no split; UDP rides the selector (VLESS via xudp). Simplest.
//	split            — UDP -> UDP-native nodes only; VLESS never carries UDP.
//	primary-fallback — UDP -> url-test over [UDP-native nodes + VLESS pools]; the
//	                   fastest-alive wins (native when healthy, VLESS when the
//	                   native nodes die). Automatic, but latency-based, not a
//	                   strict priority — strict "native-first" needs the runtime
//	                   balancer (vpnbalance) driving a selector.
const udpGroupTag = "vpn-udp"

// urlTestTolerance (ms) damps url-test flapping: only switch when a candidate is
// faster than the current pick by more than this. Matches the home-network spec.
const urlTestTolerance = 50

// UDP mode values.
const (
	UDPUnified         = "unified"
	UDPSplit           = "split"
	UDPPrimaryFallback = "primary-fallback"
)

// NodeGroup is a set of nodes that should become one url-test pool.
type NodeGroup struct {
	PoolName string // the url-test group's tag (e.g. "vpn-b-pool")
	Nodes    []subscription.Node
}

// MergeOptions tunes the merge. Zero values fall back to sane defaults.
type MergeOptions struct {
	SelectorTag string // selector whose outbounds get the pools appended (default "vpn")
	ProbeURL    string // url-test health-check URL
	Interval    string // url-test interval
	UDPMode     string // unified | split | primary-fallback (default primary-fallback)
}

func (o MergeOptions) withDefaults() MergeOptions {
	if o.SelectorTag == "" {
		o.SelectorTag = "vpn"
	}
	if o.ProbeURL == "" {
		o.ProbeURL = "https://www.gstatic.com/generate_204"
	}
	if o.Interval == "" {
		o.Interval = "5m"
	}
	if o.UDPMode == "" {
		o.UDPMode = UDPPrimaryFallback
	}
	return o
}

// MergeResult is the merged config plus what happened.
type MergeResult struct {
	JSON      []byte
	Skipped   []SkipError // nodes whose protocol could not be emitted
	Pools     []string    // pool group names actually added (non-empty groups)
	NodeCount int         // node outbounds added
	UDPGroup  []string    // UDP group members (empty if no UDP split was applied)
}

// Merge injects the node groups into existing (a marshaled sing-box config) and
// returns the updated config. The selector named opts.SelectorTag must exist; we
// append the pool tags to its outbounds (preserving its current members and
// default). Empty groups are skipped.
func Merge(existing []byte, groups []NodeGroup, opts MergeOptions) (MergeResult, error) {
	opts = opts.withDefaults()
	var res MergeResult

	var cfg map[string]any
	if err := json.Unmarshal(existing, &cfg); err != nil {
		return res, fmt.Errorf("parse existing config: %w", err)
	}
	rawOut, ok := cfg["outbounds"].([]any)
	if !ok {
		return res, fmt.Errorf("existing config has no outbounds array")
	}

	// Build managed outbounds (node outbounds + one url-test group per non-empty
	// group) and the set of tags they own.
	managed := map[string]bool{}
	if opts.UDPMode != UDPUnified {
		managed[udpGroupTag] = true // so a prior UDP group is replaced, not duplicated
	}
	var nodeObs []outbound
	var poolObs []outbound
	for _, g := range groups {
		var tags []string
		for _, n := range g.Nodes {
			ob, extras, err := nodeOutbound(n)
			if err != nil {
				if se, ok := err.(SkipError); ok {
					res.Skipped = append(res.Skipped, se)
					continue
				}
				return res, err
			}
			tag := ob["tag"].(string)
			if managed[tag] {
				continue // same node in two groups: emit once
			}
			managed[tag] = true
			nodeObs = append(nodeObs, ob)
			nodeObs = append(nodeObs, extras...)
			tags = append(tags, tag)
		}
		if len(tags) == 0 {
			continue // empty group: no url-test to make
		}
		managed[g.PoolName] = true
		poolObs = append(poolObs, urltestGroup(g.PoolName, tags, opts))
		res.Pools = append(res.Pools, g.PoolName)
	}
	res.NodeCount = len(nodeObs)

	// Drop any previously-managed outbounds (idempotent re-run), keep the rest,
	// and collect UDP-native node tags from what we keep.
	kept := make([]any, 0, len(rawOut))
	selectorFound := false
	var udpNative []string
	for _, item := range rawOut {
		m, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}
		tag, _ := m["tag"].(string)
		if managed[tag] {
			continue // a node/pool/UDP group we are about to re-add
		}
		if typ, _ := m["type"].(string); typ == "hysteria2" || typ == "tuic" {
			if tag != "" {
				udpNative = append(udpNative, tag)
			}
		}
		if tag == opts.SelectorTag {
			selectorFound = true
			m["outbounds"] = mergeSelectorMembers(m["outbounds"], res.Pools)
		}
		kept = append(kept, item)
	}
	if !selectorFound {
		return res, fmt.Errorf("selector %q not found in existing config", opts.SelectorTag)
	}

	// Append node outbounds first, then pool groups, so groups can reference them.
	for _, ob := range nodeObs {
		kept = append(kept, map[string]any(ob))
	}
	for _, ob := range poolObs {
		kept = append(kept, map[string]any(ob))
	}

	// UDP split: build the dedicated UDP group and a network:udp route rule.
	if opts.UDPMode != UDPUnified && len(res.Pools) > 0 {
		members := append([]string{}, udpNative...)
		if opts.UDPMode == UDPPrimaryFallback {
			members = append(members, res.Pools...) // native first, VLESS pools as fallback
		}
		if len(members) > 0 {
			kept = append(kept, map[string]any(urltestGroup(udpGroupTag, members, opts)))
			if err := injectUDPRule(cfg, opts.SelectorTag, udpGroupTag); err != nil {
				return res, err
			}
			res.UDPGroup = members
		}
	}

	cfg["outbounds"] = kept

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return res, err
	}
	res.JSON = out
	return res, nil
}

func urltestGroup(tag string, members []string, opts MergeOptions) outbound {
	return outbound{
		"type":      "urltest",
		"tag":       tag,
		"outbounds": members,
		"url":       opts.ProbeURL,
		"interval":  opts.Interval,
		"tolerance": urlTestTolerance,
	}
}

// injectUDPRule inserts (idempotently) a `network:udp` rule that sends whatever
// rule-sets currently target selectorTag to udpTag, placed just before the first
// such rule so first-match routing keeps UDP on the native/fallback group while
// TCP continues to the selector.
func injectUDPRule(cfg map[string]any, selectorTag, udpTag string) error {
	route, ok := cfg["route"].(map[string]any)
	if !ok {
		return fmt.Errorf("existing config has no route block")
	}
	rules, ok := route["rules"].([]any)
	if !ok {
		return fmt.Errorf("existing config route has no rules")
	}

	seen := map[string]bool{}
	var ruleSets []any
	insertAt := -1
	filtered := make([]any, 0, len(rules))
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if ok && m["outbound"] == udpTag {
			continue // drop a prior managed UDP rule (idempotent)
		}
		filtered = append(filtered, r)
		if ok && m["outbound"] == selectorTag {
			if insertAt < 0 {
				insertAt = len(filtered) - 1
			}
			if rs, ok := m["rule_set"].([]any); ok {
				for _, x := range rs {
					if s, _ := x.(string); s != "" && !seen[s] {
						seen[s] = true
						ruleSets = append(ruleSets, x)
					}
				}
			}
		}
	}
	if insertAt < 0 || len(ruleSets) == 0 {
		// Nothing rule-set-routed to the selector; leave routing untouched.
		route["rules"] = filtered
		return nil
	}
	udpRule := map[string]any{"rule_set": ruleSets, "network": "udp", "outbound": udpTag}
	withRule := make([]any, 0, len(filtered)+1)
	withRule = append(withRule, filtered[:insertAt]...)
	withRule = append(withRule, udpRule)
	withRule = append(withRule, filtered[insertAt:]...)
	route["rules"] = withRule
	return nil
}

// mergeSelectorMembers returns the selector's existing members with the pool
// names appended (deduplicated, order preserved). Managed pool names that were
// already present are not duplicated; non-managed members (e.g. the hysteria2
// pool, concrete fallback nodes) are kept.
func mergeSelectorMembers(existing any, pools []string) []any {
	seen := map[string]bool{}
	var out []any
	if arr, ok := existing.([]any); ok {
		for _, v := range arr {
			s, _ := v.(string)
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, p := range pools {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
