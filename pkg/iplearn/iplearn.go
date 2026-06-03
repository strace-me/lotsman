// Package iplearn maintains, per service, a learned set of destination CIDRs —
// the IP-learning core for self-heal misroute remediation (LOT-17). It has two
// sources of truth:
//
//  1. A self-learned accumulator (Learner): destination IPs observed for a
//     service are recorded as host routes (/32, /128), deduplicated, and
//     optionally coalesced into covering CIDRs. This is the primary source.
//
//  2. An external source (Source / RekrytSource): a maintained per-domain CIDR
//     list (e.g. rekryt/iplist at iplist.opencck.org). Opt-in per service.
//
// MergeSources unions external + learned under a shrink guard so a half-broken
// upstream cannot silently collapse a service's coverage.
//
// The package is pure and dependency-light: no file I/O, no network except the
// injectable HTTP client in RekrytSource, and inputs are plain net types so it
// builds standalone (it does not import pkg/observe or pkg/aggregate).
package iplearn

import (
	"net"
	"sort"
	"sync"
)

// Learner accumulates observed destination IPs per service and exposes a
// deduplicated CIDR set. Safe for concurrent use.
type Learner struct {
	mu       sync.Mutex
	widener  Widener
	coalesce bool
	// per service: set of canonical CIDR strings -> parsed net.
	sets map[string]map[string]*net.IPNet
}

// LearnerOption configures a Learner.
type LearnerOption func(*Learner)

// WithWidener installs a Widener used to widen each observed host route to its
// owning block (e.g. a whois prefix). The default is a no-op (host routes only).
func WithWidener(w Widener) LearnerOption {
	return func(l *Learner) { l.widener = w }
}

// WithCoalesce enables coalescing of adjacent/contained CIDRs in Snapshot. When
// disabled (default) the snapshot still drops CIDRs contained in a larger one,
// but does not merge sibling pairs into a covering prefix.
func WithCoalesce(on bool) LearnerOption {
	return func(l *Learner) { l.coalesce = on }
}

// NewLearner builds a Learner. By default the Widener is a no-op and coalescing
// is off (host routes are kept, only redundant containment is removed).
func NewLearner(opts ...LearnerOption) *Learner {
	l := &Learner{
		widener: noopWidener{},
		sets:    make(map[string]map[string]*net.IPNet),
	}
	for _, o := range opts {
		o(l)
	}
	if l.widener == nil {
		l.widener = noopWidener{}
	}
	return l
}

// Observe records a single destination IP seen for service. A nil/unspecified
// IP is ignored. The IP is recorded as a host route (/32 or /128), widened by
// the configured Widener, then folded into the service's set (a route already
// covered by an existing CIDR is dropped; a route that covers existing entries
// evicts them).
func (l *Learner) Observe(service string, ip net.IP) {
	if hostCIDR(ip) == nil {
		return
	}
	l.ObserveCIDR(service, hostCIDR(ip))
}

// ObserveCIDR records an already-formed CIDR for service. A nil CIDR is ignored.
func (l *Learner) ObserveCIDR(service string, n *net.IPNet) {
	if n == nil {
		return
	}
	widened := l.widener.Widen(n)
	if widened == nil {
		widened = n
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	set := l.sets[service]
	if set == nil {
		set = make(map[string]*net.IPNet)
		l.sets[service] = set
	}
	addToSet(set, widened)
}

// Snapshot returns the current deduplicated CIDR set for service, sorted
// canonically. When coalescing is enabled, adjacent/contained CIDRs are merged
// into covering prefixes. The returned slice is a fresh copy.
func (l *Learner) Snapshot(service string) []*net.IPNet {
	l.mu.Lock()
	set := l.sets[service]
	out := make([]*net.IPNet, 0, len(set))
	for _, n := range set {
		out = append(out, dupNet(n))
	}
	l.mu.Unlock()

	if l.coalesce {
		out = Coalesce(out)
	}
	sortNets(out)
	return out
}

// Services returns the service names that have at least one learned CIDR.
func (l *Learner) Services() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.sets))
	for s, set := range l.sets {
		if len(set) > 0 {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// --- Widener (whois-widen seam) ---------------------------------------------

// Widener widens a host route to the owning network block. Implementations may
// consult whois/BGP data. Returning nil means "no wider block known" and the
// input route is kept as-is. This is the seam for the whois-widen step from
// research; the package ships only a no-op default.
type Widener interface {
	Widen(host *net.IPNet) *net.IPNet
}

// noopWidener keeps host routes unchanged — the dependency-free default.
type noopWidener struct{}

func (noopWidener) Widen(host *net.IPNet) *net.IPNet { return host }

// --- set helpers ------------------------------------------------------------

// addToSet folds n into set with containment dedup: if an existing entry
// already covers n, n is dropped; if n covers existing entries, they are
// evicted. Entries are keyed by canonical CIDR string.
func addToSet(set map[string]*net.IPNet, n *net.IPNet) {
	key := n.String()
	if _, ok := set[key]; ok {
		return
	}
	for k, ex := range set {
		if covers(ex, n) {
			return // already covered by a broader (or equal) block
		}
		if covers(n, ex) {
			delete(set, k) // n is broader; evict the narrower entry
		}
	}
	set[key] = n
}

// covers reports whether a fully contains b (same family, a's prefix no longer
// than b's, and b's network address inside a).
func covers(a, b *net.IPNet) bool {
	aOnes, aBits := a.Mask.Size()
	bOnes, bBits := b.Mask.Size()
	if aBits != bBits || aBits == 0 {
		return false // different family or non-canonical mask
	}
	if aOnes > bOnes {
		return false // a is narrower than b
	}
	return a.Contains(b.IP)
}

// hostCIDR turns an IP into a host route (/32 for v4, /128 for v6). Returns nil
// for nil/unspecified addresses.
func hostCIDR(ip net.IP) *net.IPNet {
	if ip == nil || ip.IsUnspecified() {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
	}
	if v6 := ip.To16(); v6 != nil {
		return &net.IPNet{IP: v6, Mask: net.CIDRMask(128, 128)}
	}
	return nil
}

func dupNet(n *net.IPNet) *net.IPNet {
	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)
	mask := make(net.IPMask, len(n.Mask))
	copy(mask, n.Mask)
	return &net.IPNet{IP: ip, Mask: mask}
}

// sortNets orders nets by family, then network address, then prefix length —
// stable canonical ordering for snapshots and merge output.
func sortNets(nets []*net.IPNet) {
	sort.Slice(nets, func(i, j int) bool {
		a, b := nets[i], nets[j]
		if len(a.IP) != len(b.IP) {
			return len(a.IP) < len(b.IP)
		}
		if c := compareBytes(a.IP, b.IP); c != 0 {
			return c < 0
		}
		ao, _ := a.Mask.Size()
		bo, _ := b.Mask.Size()
		return ao < bo
	})
}

func compareBytes(a, b net.IP) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}
