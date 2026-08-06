// Package nfqwsgen composes an nfqws strategy config from the registry — one
// `--new` block per zapret-class service, each scoped to that service's own
// domains with its chosen recipe. It is the nfqws-side projection of the single
// source of truth (the service config), symmetric to singbox.Generate for the
// routing side: the SAME service declaration drives both where traffic goes
// (sing-box) and how the local DPI-desync treats it (nfqws). Pure — no I/O.
//
// This closes the coherence loop by construction: a service's domains are inlined
// into its own `--hostlist-domains` inside its `--new` block (the zms/Zapret-
// Manager model), so a service routed to zapret is desynced for exactly the
// domains it owns — no separate, drifting hostlist file, no global single
// strategy. The recipe per block is chosen by the tester (the recipe that
// measurably beats the no-desync baseline); this package only assembles them.
package nfqwsgen

import (
	"strings"

	"github.com/strace-me/lotsman/pkg/aggregate"
	"github.com/strace-me/lotsman/pkg/strategycat"
)

// domainsPlaceholder is the template the catalog uses for the service's domains.
const domainsPlaceholder = "{{DOMAINS}}"

// Block is one zapret-class service paired with the recipe chosen for it.
type Block struct {
	Service string
	Domains []string // the service's plaintext domains (inlined into --hostlist-domains)
	Recipe  strategycat.Recipe
	// HostlistPath, when set, makes this block reference a hostlist FILE instead of
	// inlining its domains into --hostlist-domains. That matters for more than
	// tidiness: nfqws re-reads hostlist files whenever their mtime or size changes
	// (and on SIGHUP), so membership can then change WITHOUT restarting the engine
	// — and a restart drops the desync mid-flow on every live connection. With the
	// domains inlined they are part of argv, so any change forces a restart.
	// The caller owns writing the file (Compose stays pure).
	HostlistPath string
	// Exclude are domains to pass RAW within this block — added as
	// --hostlist-exclude-domains so the desync is cancelled for them. nfqws checks
	// the exclude list first, so a domain here is never fooled even if it also
	// matches the block's hostlist. Use for CDNs/endpoints that work raw but break
	// under the desync (e.g. Epic download/EasyAntiCheat — LOT-36).
	Exclude []string
}

// Compose assembles the ordered nfqws argument list: each block becomes a
// `--new`-prefixed segment whose `{{DOMAINS}}` is filled with the block's
// comma-joined domains, optionally followed by a --hostlist-exclude-domains for
// its raw-pass set. Blocks with no domains or an empty recipe are skipped
// (nothing to desync, or nothing to apply). The result is the body Lotsman would
// write into the nfqws config (e.g. OpenWrt's NFQWS_OPT).
func Compose(blocks []Block) []string {
	var out []string
	for _, b := range blocks {
		// Validate domains before they are joined into a /bin/sh exec arg list
		// (RenderComposed): rule_set-resolved domains come from a remote .srs and a
		// stray space/;/backtick would break the args or inject a shell token. Drop
		// anything that isn't a clean domain (defense-in-depth on the shell sink).
		doms := validDomains(b.Domains)
		profiles := b.Recipe.AllBlocks()
		if len(doms) == 0 || len(profiles) == 0 {
			continue
		}
		joined := strings.Join(doms, ",")
		// A recipe always carries --hostlist-domains={{DOMAINS}}; pointing it at a
		// file is a rewrite of that one argument.
		rewrite := func(a string) string {
			if b.HostlistPath != "" && strings.HasPrefix(a, "--hostlist-domains=") {
				return "--hostlist=" + b.HostlistPath
			}
			return strings.ReplaceAll(a, domainsPlaceholder, joined)
		}
		// --new DELIMITS profiles; nfqws creates the first one itself. Leading with
		// it therefore prepends an auto-created profile that has no filter — and a
		// profile with an empty filter matches every packet. Since profiles are
		// matched first to last until the first match, that catch-all swallows all
		// traffic and applies no desync, so every block behind it is dead. Verified
		// live: the strategy loaded cleanly and changed nothing.
		// A recipe may carry SEVERAL profiles (Flowseal's ALT12 needs three to cover
		// one service: a Google-specific one on tcp/443, a general one on tcp/80,443
		// and QUIC on udp/443). Each becomes its own --new profile, in the recipe's
		// order, because nfqws matches first-to-last and stops.
		excl := validDomains(b.Exclude)
		for _, profile := range profiles {
			if len(profile) == 0 {
				continue
			}
			if len(out) > 0 {
				out = append(out, "--new")
			}
			for _, a := range profile {
				out = append(out, rewrite(a))
			}
			// The exclusion belongs to every profile of the block, not just the first:
			// nfqws checks it per profile, so attaching it once would leave the other
			// profiles free to desync exactly the endpoints the operator excluded.
			if len(excl) > 0 {
				out = append(out, "--hostlist-exclude-domains="+strings.Join(excl, ","))
			}
		}
	}
	return out
}

// Conflict is a domain claimed by MORE THAN ONE block. nfqws applies the first
// matching `--new` profile and ignores the rest, so a domain in two blocks means
// the later block's recipe is silently DEAD for it (the bol-van "one fake per
// filter / declaration-order" hazard — zapret #1543/#1146). When per-domain
// splitting emits several blocks for one service, the composer must not let two of
// them fight over a domain.
type Conflict struct {
	Domain   string
	Services []string // the blocks claiming it, in declaration (first-match) order
}

// Validate reports cross-block domain conflicts: any domain that appears in more
// than one composed block (only blocks that actually emit — non-empty recipe — are
// considered). Deterministic, order-preserving. An empty result means the block set
// composes cleanly; a non-empty result is a coherence bug the caller should log /
// reject before applying, because nfqws would silently drop the later recipes.
func Validate(blocks []Block) []Conflict {
	claims := map[string][]string{}
	var order []string
	for _, b := range blocks {
		if len(b.Recipe.AllBlocks()) == 0 {
			continue
		}
		for _, d := range validDomains(b.Domains) {
			if _, seen := claims[d]; !seen {
				order = append(order, d)
			}
			claims[d] = append(claims[d], b.Service)
		}
	}
	var out []Conflict
	for _, d := range order {
		if svcs := claims[d]; len(svcs) > 1 {
			out = append(out, Conflict{Domain: d, Services: svcs})
		}
	}
	return out
}

// validDomains keeps only well-formed domains (aggregate.ValidDomain), preserving
// order. The shell-sink guard for Compose.
func validDomains(in []string) []string {
	out := make([]string, 0, len(in))
	for _, d := range in {
		if aggregate.ValidDomain(d) {
			out = append(out, d)
		}
	}
	return out
}
