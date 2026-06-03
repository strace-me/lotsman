// Package subscription parses VPN subscriptions in several formats into a
// normalized node list. It is pure logic (no network, no disk): given bytes
// and a format, it returns nodes. Health checks, quarantine, snapshots, and
// expiry tracking (spec 4.2.4-4.2.7) are separate concerns added later.
package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
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
	Caps        Caps
	Tags        []string
	Country     string // ISO-3166 alpha-2, lower-case, parsed from the flag emoji in DisplayName ("" = unknown)
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

// nodeID derives a stable ID from the essential connection fields, so the same
// node appearing in successive pulls keeps its identity (for health history /
// dedup). Cosmetic fields (display name, tags) are excluded.
func nodeID(proto, server string, port int) string {
	h := sha256.Sum256([]byte(proto + "|" + server + "|" + strconv.Itoa(port)))
	return hex.EncodeToString(h[:8])
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
