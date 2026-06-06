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

	"github.com/strace-me/lotsman/pkg/strategycat"
)

// domainsPlaceholder is the template the catalog uses for the service's domains.
const domainsPlaceholder = "{{DOMAINS}}"

// Block is one zapret-class service paired with the recipe chosen for it.
type Block struct {
	Service string
	Domains []string // the service's plaintext domains (inlined into --hostlist-domains)
	Recipe  strategycat.Recipe
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
		if len(b.Domains) == 0 || len(b.Recipe.NfqwsArgs) == 0 {
			continue
		}
		doms := strings.Join(b.Domains, ",")
		out = append(out, "--new")
		for _, a := range b.Recipe.NfqwsArgs {
			out = append(out, strings.ReplaceAll(a, domainsPlaceholder, doms))
		}
		if len(b.Exclude) > 0 {
			out = append(out, "--hostlist-exclude-domains="+strings.Join(b.Exclude, ","))
		}
	}
	return out
}
