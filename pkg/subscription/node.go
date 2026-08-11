// Package subscription parses VPN subscriptions in several formats into a
// normalized node list. It is pure logic (no network, no disk): given bytes
// and a format, it returns nodes. Health checks, quarantine, snapshots, and
// expiry tracking (spec 4.2.4-4.2.7) are separate concerns added later.
package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Protocols recognized across formats.
const (
	ProtoHysteria2   = "hysteria2"
	ProtoVLESS       = "vless"
	ProtoTrojan      = "trojan"
	ProtoShadowsocks = "shadowsocks"
	ProtoVMess       = "vmess"
	ProtoMTProto     = "mtproto"
	ProtoAnyTLS      = "anytls"
	ProtoTUIC        = "tuic"
	ProtoShadowTLS   = "shadowtls" // shadowsocks wrapped in a ShadowTLS v3 handshake to a decoy SNI
	ProtoWireGuard   = "wireguard" // wireguard tunnel (emitted as a sing-box `endpoints` entry)
)

// Caps is what a node can carry. Derived statically from the protocol here; an
// active probe refines it later (e.g. VLESS-vision xudp, which needs both ends
// to agree, or a shadowsocks node with UDP actually disabled).
type Caps struct {
	TCP       bool
	UDPNative bool
}

// Node is the normalized form a parser produces. Raw holds the original,
// opaque per-protocol config (URL or marshaled map) that is later handed to
// sing-box unchanged — we do not try to fully model every protocol here.
type Node struct {
	ID          string
	Source      string
	DisplayName string
	Protocol    string
	Server      string
	Port        int
	Raw         string
	// Native marks a node whose Raw is already a valid sing-box outbound object
	// (from FormatSingbox), so the generator can emit it verbatim. Nodes from
	// other formats (e.g. Clash, whose Raw uses different field names) must go
	// through the protocol-specific builders instead.
	Native  bool
	Caps    Caps
	Tags    []string
	Country string // ISO-3166 alpha-2, lower-case, parsed from the flag emoji in DisplayName ("" = unknown)
}

// capsForProtocol returns the static capability guess for a protocol. UDP
// transport over TCP-only protocols (trojan, vmess via ws, etc.) is treated as
// no native UDP. shadowsocks is assumed UDP-capable until a probe says
// otherwise; the alternative (assuming no UDP) would wrongly exclude usable
// nodes from messaging/gaming pools.
func capsForProtocol(proto string) Caps {
	switch proto {
	case ProtoHysteria2, ProtoShadowsocks, ProtoTUIC, ProtoWireGuard:
		return Caps{TCP: true, UDPNative: true}
	case ProtoVLESS, ProtoTrojan, ProtoVMess, ProtoMTProto, ProtoShadowTLS:
		return Caps{TCP: true, UDPNative: false}
	default:
		return Caps{TCP: true}
	}
}

// nodeID is a node's identity across pulls: address alone. Two providers force
// this to be more subtle than it looks, and they pull in opposite directions.
//
//   - vpn-a ROTATES the REALITY short_id on the same node every few minutes
//     (LOT-1). Folding that into the identity renames every node every few
//     minutes, which rewrites the config and restarts sing-box on pure churn.
//   - AcmeVPN SELECTS THE EXIT by short_id: it advertises 50 entries across 6
//     addresses, and three sharing one address came out at 198.51.100.10,
//     198.51.100.11 and 198.51.100.12 — three different countries. Address
//     identity collapsed 50 real exits into 6 and threw away 44 of them.
//
// What tells them apart is not the field, it is the SHAPE OF ONE PULL: a rotating
// credential appears once per address, while a selector appears many times at
// once. So the base identity stays the address, and assignIDs (below) extends it
// only for addresses that carry more than one node in the same pull.
func nodeID(proto, server string, port int) string {
	h := sha256.Sum256([]byte(proto + "|" + server + "|" + strconv.Itoa(port)))
	return hex.EncodeToString(h[:8])
}

// assignIDs finalises identity across a whole pull, which is the only place the
// distinction above can be made. An address carrying one node keeps the plain
// address identity, so a rotated credential is invisible; an address carrying
// several gets each of them extended by what actually differs between them, so
// they survive as separate exits instead of collapsing into one.
//
// The cost of being wrong each way is asymmetric and decides the default:
// over-distinguishing splits a node's health history, which heals in minutes,
// while under-distinguishing silently deletes exits you are paying for.
//
// The premise above — "a rotating credential appears once per address, a selector
// appears many times at once" — turned out to be false for AcmeVPN, and that is
// what this function now guards against. Measured on the live subscription
// 2026-08-11: 50 nodes over 6 addresses, up to 15 on one, and a fresh `sid` on
// EVERY fetch — 48 of 50 changed between two pulls three seconds apart. So the
// provider both multiplexes by address and rotates, the extension below baked the
// rotating value into every ID, and sing-box was rebuilt and restarted every five
// minutes on churn (LOT-1 again, from the other side).
//
// The rule is therefore self-checking rather than provider-specific: prefer the
// identity that ignores rotating credentials, but only where it still tells the
// pull's own nodes apart, and fall back to the full one for any address group it
// would merge. A wrong guess in volatileParams then cannot cost an exit — the
// worst it can do is leave the churn as it is today, which is the recoverable
// direction.
//
// It returns the addresses it fell back on. NOTHING SURFACES THAT YET — Parse's
// signature has thirteen call sites and widening it would bury this fix in
// mechanical churn — so today the list exists for the tests and the fallback is
// visible only as the restarts continuing. That gap is LOT-60: a provider whose
// only discriminator rotates is a fact worth a log line.
func assignIDs(nodes []Node) (fellBack []string) {
	byAddr := make(map[string][]int, len(nodes))
	for i := range nodes {
		byAddr[nodes[i].ID] = append(byAddr[nodes[i].ID], i)
	}
	for addr, idx := range byAddr {
		if len(idx) < 2 {
			// One node behind this address: the plain address identity already tells it
			// apart from everything else, so a rotated credential stays invisible.
			continue
		}
		stable := make(map[string]bool, len(idx))
		for _, i := range idx {
			stable[connectionIdentityStable(nodes[i].Raw)] = true
		}
		useStable := len(stable) == len(idx)
		if !useStable {
			fellBack = append(fellBack, nodes[idx[0]].Server)
		}
		for _, i := range idx {
			ident := connectionIdentityStable(nodes[i].Raw)
			if !useStable {
				ident = connectionIdentity(nodes[i].Raw)
			}
			h := sha256.Sum256([]byte(addr + "|" + ident))
			nodes[i].ID = hex.EncodeToString(h[:8])
		}
	}
	sort.Strings(fellBack)
	return fellBack
}

// cosmeticKeys are the fields that name a node rather than describe how to reach
// it, across the shapes Raw takes (a Clash proxy map, a sing-box outbound).
var cosmeticKeys = []string{"name", "tag", "remarks"}

// volatileParams are credentials a provider may re-issue for a node that has not
// otherwise changed, so they describe the CONNECTION but not the NODE. The two
// spellings are not a duplicate: `sid` is the vless URL query parameter and
// `short_id` is the sing-box/Clash field for the same REALITY value, and Raw
// arrives in both shapes. pkg/reconcile has its own list (`volatileKeys`) that
// blanks the same value in the rendered config; this is that fact applied one
// level earlier, because blanking the value there could not help once the value
// had already been hashed into the outbound's TAG.
//
// Nothing here is trusted blindly — assignIDs verifies on every pull that
// ignoring these still tells the nodes apart, and keeps them when it does not.
var volatileParams = map[string]bool{
	"sid":      true,
	"short_id": true,
}

// connectionIdentityStable is connectionIdentity with the rotating credentials
// removed, so re-fetching an unchanged fleet yields unchanged identities.
func connectionIdentityStable(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			// Unparseable: fall back to the full identity rather than guess. Merging two
			// exits is the unrecoverable direction.
			return connectionIdentity(raw)
		}
		q := u.Query()
		for k := range volatileParams {
			q.Del(k)
		}
		u.RawQuery = q.Encode() // sorted by key, so it is stable across pulls
		u.Fragment = ""
		return u.String()
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	for _, k := range cosmeticKeys {
		delete(m, k)
	}
	dropVolatile(m)
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return string(out)
}

// dropVolatile walks a decoded node and deletes the rotating credentials wherever
// they sit — REALITY's short_id is nested under tls.reality, not at the top.
func dropVolatile(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			if volatileParams[k] {
				delete(t, k)
			} else {
				dropVolatile(t[k])
			}
		}
	case []any:
		for _, e := range t {
			dropVolatile(e)
		}
	}
}

// connectionIdentity reduces a node's raw config to the part that determines the
// connection, dropping only what a provider is free to re-label. A URL's fragment
// is its display name; a marshalled map carries the name as a field. Anything we
// cannot recognise is hashed whole — over-distinguishing splits a node's health
// history on a rename, which is recoverable, while under-distinguishing merges
// two different exits, which is not.
func connectionIdentity(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if i := strings.IndexByte(raw, '#'); i >= 0 {
			return raw[:i]
		}
		return raw
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	for _, k := range cosmeticKeys {
		delete(m, k)
	}
	// encoding/json sorts map keys, so this is stable across pulls.
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return string(out)
}

// finalize fills derived fields (ID, Caps) on a node whose connection fields
// are already set, and stamps the source.
func (n *Node) finalize(source string) {
	n.Source = source
	n.Caps = capsForProtocol(n.Protocol)
	n.ID = nodeID(n.Protocol, n.Server, n.Port)
	n.Country = countryFromName(n.DisplayName)
	if n.DisplayName == "" {
		n.DisplayName = n.Server
	}
}

// countryFromName extracts a 2-letter ISO country code from the first flag emoji
// in a node name (e.g. "🇳🇱 Amsterdam" -> "nl"). Providers encode the exit
// location in the name; this is a cheap static hint (no geoip lookup) used to
// keep RF-blocked services off RU-exit nodes. A "smart location" node with no
// flag yields "" and is never excluded by country. Returns "" when unknown.
func countryFromName(name string) string {
	const base = 0x1F1E6 // regional indicator 'A'
	var code []rune
	for _, r := range name {
		if r >= base && r <= 0x1F1FF {
			code = append(code, 'a'+(r-base))
			if len(code) == 2 {
				return string(code)
			}
		} else if len(code) > 0 {
			code = code[:0] // a lone indicator isn't a flag; reset
		}
	}
	return ""
}
