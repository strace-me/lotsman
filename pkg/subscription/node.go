// Package subscription parses VPN subscriptions in several formats into a
// normalized node list. It is pure logic (no network, no disk): given bytes
// and a format, it returns nodes. Health checks, quarantine, snapshots, and
// expiry tracking (spec 4.2.4-4.2.7) are separate concerns added later.
package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
func assignIDs(nodes []Node) {
	byAddr := make(map[string]int, len(nodes))
	for i := range nodes {
		byAddr[nodes[i].ID]++
	}
	for i := range nodes {
		if byAddr[nodes[i].ID] < 2 {
			continue
		}
		h := sha256.Sum256([]byte(nodes[i].ID + "|" + connectionIdentity(nodes[i].Raw)))
		nodes[i].ID = hex.EncodeToString(h[:8])
	}
}

// cosmeticKeys are the fields that name a node rather than describe how to reach
// it, across the shapes Raw takes (a Clash proxy map, a sing-box outbound).
var cosmeticKeys = []string{"name", "tag", "remarks"}

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
