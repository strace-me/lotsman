// Package config projects the one configuration schema (pkg/configv2) onto the
// runtime shape the daemon and the client consume.
//
// The division is deliberate: configv2 owns the DOCUMENT — what a file says — and
// this package owns the RUNTIME — what the brain, the applier, the nft renderer and
// the sing-box generator read. One schema, one reader, one projection. Two schemas
// with two readers is the shape this project keeps meeting: a config that validates
// in one place and is rejected in another, each green in its own tests.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/strace-me/lotsman/pkg/aggregate"
	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/pools"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/subscription"
	"github.com/strace-me/lotsman/pkg/zapret"
)

// Config is the parsed, validated configuration.
//
// It is the RUNTIME shape, not a document: one schema exists (configv2) and this is
// what a document projects onto. Keeping them separate is what lets a single reader
// answer "what is a valid config" — the failure this project keeps meeting is two
// readers that disagree, each green in its own tests.
type Config struct {
	Registry        *registry.Registry
	Subscriptions   []subscription.Declaration
	Pools           *pools.Set
	Zapret          []zapret.Instance
	Devices         []registry.Device
	Hostlists       []configv2.ResolvedHostlist
	Strategies      []strategy.Definition // declared strategies, added on top of the builtin catalog
	UTLSFingerprint string                // default tls.utls fingerprint injected into TCP TLS outbounds lacking one ("" = off)
	SingboxVersion  string                // target sing-box version (e.g. "1.12.17"); gates version-specific knobs ("" = generator baseline)
	DNS             *configv2.DNS         // split-DNS: multi-server, DoT/DoH/DoQ, VPN-detoured (nil = client default)
	Multiplex       *configv2.Multiplex   // default outbound multiplex for TCP proxies (nil = off)
	SubViaPool      string                // route subscription endpoint hosts through this VPN pool (LOT-28); needs -probe-proxy ("" = off, direct fetch)
	// DesyncOverride runs an upstream bundle verbatim instead of composing a recipe per
	// rule. More than one means the lane TRIES them, whole, and only one that carried
	// moves live traffic. See configv2.Override for what the mode costs.
	DesyncOverride []configv2.ResolvedOverride
	// DisabledServices names the rules the operator switched OFF. They are absent from
	// Registry entirely — nothing probes, routes or desyncs them — and are carried here
	// only so a UI can list them and offer to switch them back on.
	DisabledServices []string
	// Detours composes egress: pool -> the pool its nodes dial out THROUGH. Composition,
	// not fallback: a chain step is "try until one works", a detour is "use this pool,
	// but reach it through that one", and it nests.
	Detours map[string]string
	// RemoteRuleSets is tag -> URL for lists declared with `srs_url`: sing-box fetches
	// them itself, so we neither download nor compile them.
	RemoteRuleSets map[string]string
	// BypassedServices names the rules that declared `bypass: true`. They are absent from
	// Registry for a different reason than DisabledServices — one is a switch the operator
	// turned off, the other is a standing statement that these domains are not Lotsman's
	// business — and a UI offering to switch one back on must not offer it for the other.
	BypassedServices []string
}

// baseQNum is the router's historical production queue. An instance that names no
// queue takes the next number from here, so the first one keeps the base and an
// existing single-instance config is unaffected.
const baseQNum = 200

// DeriveQNum is the ONE derivation of a production queue number from a position.
//
// A queue number is DERIVED, not chosen. An operator naming several instances almost
// cannot pick disjoint numbers, and the collision it produces is the worst kind: two
// engines on one nfqueue steal each other's packets, so everything downstream still
// reads healthy while a measured result belongs to neither.
func DeriveQNum(index int) int { return baseQNum + index }

// portList stringifies a YAML port list whose items may be ints (443) or range
// strings ("50000-50100").
func portList(items []any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		switch v := it.(type) {
		case int:
			out = append(out, strconv.Itoa(v))
		case string:
			out = append(out, v)
		}
	}
	return out
}

// loadIPsFile reads extra CIDRs from a file (one per line; blank lines and "#"
// comments ignored), normalizing each. A missing file is not an error — it is the
// expected initial state before runtime learning populates it — but a malformed CIDR
// is, so typos are caught. Used for the Discord voice set, and by the v1->v2
// migration to inline what a v1 document kept on disk.
func loadIPsFile(service, path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: %q: ips_file %q: %w", service, path, err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		c, ok := aggregate.NormalizeCIDR(line)
		if !ok {
			return nil, fmt.Errorf("config: %q: ips_file %q: invalid ip/cidr %q", service, path, line)
		}
		out = append(out, c)
	}
	return out, nil
}

// injectBuiltinPools adds the standard pools (vpn_url_test, vpn_url_test_udp,
// emergency_pool) that a VPN/emergency chain step, subscription-via-pool, or device
// policy REFERENCES but the document never declared. The builtin categories name
// exactly these three, so without this a config that relies on category defaults
// would generate empty url-test groups unless it also hand-copied the three pool
// blocks that pools.Builtin already provides. A pool the document declares itself is
// authoritative and never overwritten; a referenced name that is not one of the
// builtins is left alone (it may be a user pool declared elsewhere, or a genuine typo
// the generator surfaces).
func injectBuiltinPools(set *pools.Set, reg *registry.Registry, subViaPool string, devices []registry.Device) {
	referenced := map[string]bool{}
	for _, svc := range reg.Services {
		for _, step := range svc.Chain {
			if step.StrategyClass != strategy.ClassVPN && step.StrategyClass != strategy.ClassEmergency {
				continue
			}
			if step.StrategyID != "" {
				referenced[step.StrategyID] = true
			}
		}
	}
	if subViaPool != "" {
		referenced[subViaPool] = true
	}
	for _, d := range devices {
		if d.Policy != "" {
			referenced[d.Policy] = true
		}
	}
	builtin := pools.Builtin()
	for name := range referenced {
		if _, have := set.Pools[name]; have {
			continue
		}
		if p, ok := builtin.Pools[name]; ok {
			set.Pools[name] = p
		}
	}
}

func lowerAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}
