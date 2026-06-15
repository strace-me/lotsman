// Package singbox generates a sing-box config from the node set and pool
// memberships, grounded in the real R5S deployment (tproxy:7893,
// default_mark 255, route private->direct, rule-sets->selector, final direct).
// It enables the Clash API so Lotsman can switch the selector at runtime.
//
// Scope of this first cut: hysteria2 outbounds (the proven protocol on the
// router) are built from any source shape; nodes that arrived as native
// sing-box JSON are passed through. Other protocols are reported as skipped
// rather than emitted wrong — generating an invalid vless/trojan block blind,
// with no sing-box to validate against, would be worse than omitting it.
package singbox

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/strace-me/lotsman/pkg/subscription"
)

// outbound is a sing-box outbound as a generic map (sing-box's native shape).
type outbound = map[string]any

// SkipError marks a node whose protocol this generator does not yet emit.
type SkipError struct {
	NodeID   string
	Protocol string
}

func (e SkipError) Error() string {
	return fmt.Sprintf("node %s: protocol %q not yet generated", e.NodeID, e.Protocol)
}

// nodeOutbound builds a sing-box outbound for a node, deriving its tag from the
// node ID so pool groups can reference it stably. Returns SkipError for
// protocols not yet supported.
// nodeOutbound builds the sing-box outbound(s) for a node. It returns the
// PRIMARY outbound (the pinnable url-test target, tagged with the node's tag)
// plus any sibling outbounds it depends on — e.g. ShadowTLS, where the
// shadowsocks primary detours through a separate shadowtls outbound. Siblings
// have derived tags and are never pool/selector targets themselves.
func nodeOutbound(n subscription.Node) (outbound, []outbound, error) {
	tag := outboundTag(n)

	// Native sing-box source: Raw already is an outbound object; reuse it and
	// force a stable tag.
	if looksJSON(n.Raw) {
		var ob outbound
		if err := json.Unmarshal([]byte(n.Raw), &ob); err == nil {
			if t, _ := ob["type"].(string); t != "" {
				ob["tag"] = tag
				return ob, nil, nil
			}
		}
	}

	switch n.Protocol {
	case subscription.ProtoShadowTLS:
		return shadowtlsOutbounds(n, tag)
	case subscription.ProtoHysteria2:
		ob, err := hysteria2Outbound(n, tag)
		return ob, nil, err
	case subscription.ProtoVLESS:
		ob, err := vlessOutbound(n, tag)
		return ob, nil, err
	case subscription.ProtoTrojan:
		ob, err := trojanOutbound(n, tag)
		return ob, nil, err
	case subscription.ProtoShadowsocks:
		ob, err := shadowsocksOutbound(n, tag)
		return ob, nil, err
	case subscription.ProtoVMess:
		ob, err := vmessOutbound(n, tag)
		return ob, nil, err
	case subscription.ProtoAnyTLS:
		ob, err := anytlsOutbound(n, tag)
		return ob, nil, err
	case subscription.ProtoTUIC:
		ob, err := tuicOutbound(n, tag)
		return ob, nil, err
	default:
		return nil, nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
}

// shadowtlsOutbounds builds the ShadowTLS v3 detour pair from a share-link:
//
//	shadowtls://base64(ss_method:ss_password)@server:port?version=3&password=ST_PASSWORD&sni=decoy.com[&insecure=1][&fp=chrome]#name
//
// ShadowTLS camouflages the connection as a real TLS handshake to `sni` (a decoy
// host); the actual shadowsocks cipher detours through it. Returns the
// shadowsocks outbound (tagged `tag`, the pinnable target) plus the shadowtls
// outbound it detours through. Verified accepted by sing-box 1.12.17.
func shadowtlsOutbounds(n subscription.Node, tag string) (outbound, []outbound, error) {
	user, q, ok := parseShareURL(n.Raw)
	if !ok || user == "" {
		return nil, nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	method, password, ok := splitSSUserinfo(user)
	if !ok {
		return nil, nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	version := 3
	if v := q.Get("version"); v != "" {
		if vi, err := strconv.Atoi(v); err == nil && vi > 0 {
			version = vi
		}
	}
	sni := first(q, "sni", "host", "server_name")
	tls := outbound{"enabled": true, "server_name": sni}
	if isTruthy(q.Get("insecure")) || isTruthy(q.Get("allow_insecure")) {
		tls["insecure"] = true
	}
	if fp := first(q, "fp", "fingerprint"); fp != "" {
		tls["utls"] = outbound{"enabled": true, "fingerprint": fp}
	}
	stTag := tag + "-stls"
	st := outbound{
		"type": "shadowtls", "tag": stTag,
		"server": n.Server, "server_port": n.Port,
		"version": version, "password": q.Get("password"),
		"tls": tls,
	}
	ss := outbound{
		"type": "shadowsocks", "tag": tag,
		"method": method, "password": password,
		"detour": stTag,
	}
	return ss, []outbound{st}, nil
}

// splitSSUserinfo decodes a shadowsocks userinfo field — base64(method:password)
// or the bare "method:password" — into its parts.
func splitSSUserinfo(user string) (method, password string, ok bool) {
	if dec, err := decodeAnyBase64(user); err == nil {
		user = dec
	}
	m, p, found := strings.Cut(user, ":")
	if !found || m == "" {
		return "", "", false
	}
	return m, p, true
}

func isTruthy(s string) bool { return s == "1" || strings.EqualFold(s, "true") }

// wireguardEndpoint builds a sing-box WireGuard `endpoints` entry from a
// share-link:
//
//	wireguard://PRIVATE_KEY@server:port?peer=PEER_PUBKEY&address=10.0.0.2/32&mtu=1408&psk=PSK&reserved=0,0,0&allowed=0.0.0.0/0
//
// PRIVATE_KEY and PEER_PUBKEY are base64 WireGuard keys. The deprecated
// wireguard OUTBOUND form is intentionally not produced — this is the 1.11+
// endpoint form (the only one sing-box 1.12.17 accepts without a deprecation
// env var). Verified accepted on sing-box 1.12.17.
func wireguardEndpoint(n subscription.Node, tag string) (outbound, error) {
	u, err := url.Parse(strings.TrimSpace(n.Raw))
	if err != nil || u.User == nil {
		return nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	priv := u.User.Username()
	q := u.Query()
	peer := first(q, "peer", "public_key", "peer_public_key")
	if priv == "" || peer == "" {
		return nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	addrs := csvFields(first(q, "address", "local_address"))
	if len(addrs) == 0 {
		addrs = []string{"10.0.0.2/32"}
	}
	allowed := csvFields(q.Get("allowed"))
	if len(allowed) == 0 {
		allowed = []string{"0.0.0.0/0", "::/0"}
	}
	peerObj := outbound{
		"address":     n.Server,
		"port":        n.Port,
		"public_key":  peer,
		"allowed_ips": allowed,
	}
	if psk := first(q, "psk", "pre_shared_key"); psk != "" {
		peerObj["pre_shared_key"] = psk
	}
	if r := csvInts(q.Get("reserved")); len(r) == 3 {
		peerObj["reserved"] = r
	}
	ep := outbound{
		"type": "wireguard", "tag": tag,
		"address":     addrs,
		"private_key": priv,
		"peers":       []outbound{peerObj},
	}
	if mtu, err := strconv.Atoi(q.Get("mtu")); err == nil && mtu > 0 {
		ep["mtu"] = mtu
	}
	return ep, nil
}

func csvFields(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func csvInts(s string) []int {
	var out []int
	for _, p := range csvFields(s) {
		if v, err := strconv.Atoi(p); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// tuicOutbound builds a tuic outbound from a share-link
// (tuic://uuid:password@host:port?congestion_control=bbr&alpn=h3&sni=…&allow_insecure=1&udp_relay_mode=native).
// QUIC/UDP; verified accepted by sing-box 1.12.17.
func tuicOutbound(n subscription.Node, tag string) (outbound, error) {
	u, err := url.Parse(strings.TrimSpace(n.Raw))
	if err != nil || u.User == nil {
		return nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	uuid := u.User.Username()
	if uuid == "" {
		return nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	q := u.Query()
	ob := outbound{
		"type": "tuic", "tag": tag,
		"server": n.Server, "server_port": n.Port, "uuid": uuid,
	}
	if pw, ok := u.User.Password(); ok && pw != "" {
		ob["password"] = pw
	}
	if cc := q.Get("congestion_control"); cc != "" {
		ob["congestion_control"] = cc
	}
	if urm := q.Get("udp_relay_mode"); urm != "" {
		ob["udp_relay_mode"] = urm
	}
	tls := tlsBlock(q, n.Server)
	if tls == nil {
		tls = outbound{"enabled": true, "server_name": n.Server}
	}
	ob["tls"] = tls
	return ob, nil
}

// anytlsOutbound builds an anytls outbound from a share-link
// (anytls://password@host:port?sni=…&insecure=1). AnyTLS is TCP-TLS; insecure
// (self-signed, no domain) is honored from the query. Verified accepted by
// sing-box 1.12.17.
func anytlsOutbound(n subscription.Node, tag string) (outbound, error) {
	password, q, ok := parseShareURL(n.Raw)
	if !ok || password == "" {
		return nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	ob := outbound{
		"type": "anytls", "tag": tag,
		"server": n.Server, "server_port": n.Port, "password": password,
	}
	tls := tlsBlock(q, n.Server)
	if tls == nil {
		tls = outbound{"enabled": true, "server_name": n.Server}
	}
	ob["tls"] = tls
	return ob, nil
}

// hysteria2Outbound builds a hysteria2 outbound from whatever shape Raw is
// (share-link URL, clash map, or sing-box map).
func hysteria2Outbound(n subscription.Node, tag string) (outbound, error) {
	ob := outbound{
		"type":        "hysteria2",
		"tag":         tag,
		"server":      n.Server,
		"server_port": n.Port,
	}
	password, sni := hy2Secrets(n)
	if password == "" {
		return nil, fmt.Errorf("hysteria2 node %s: no password found", n.ID)
	}
	ob["password"] = password
	tls := outbound{"enabled": true}
	if sni != "" {
		tls["server_name"] = sni
	}
	if hy2Insecure(n) {
		tls["insecure"] = true // self-signed cert (no domain) — client must skip verify
	}
	ob["tls"] = tls
	if otype, opass := hy2Obfs(n); otype != "" {
		ob["obfs"] = outbound{"type": otype, "password": opass}
	}
	// Port hopping + Brutal CC, pass-through from the node if it declares them
	// (1.12.17-valid: server_ports since 1.11, up/down_mbps base). A node without
	// them (e.g. the single fastvpn key) is unaffected — these are opt-in per node.
	ports, hop, up, down := hy2Extras(n)
	if len(ports) > 0 {
		ob["server_ports"] = ports
		if hop != "" {
			ob["hop_interval"] = hop
		}
	}
	if up > 0 {
		ob["up_mbps"] = up
	}
	if down > 0 {
		ob["down_mbps"] = down
	}
	return ob, nil
}

// hy2Extras extracts hysteria2 port-hopping (server_ports + hop_interval) and
// Brutal bandwidth (up/down_mbps) from a node's Raw across source shapes.
// server_ports is normalized to sing-box form: a comma list of "start:end"
// tokens ("20000-30000" or "20000:30000" -> "20000:30000", a bare port kept).
func hy2Extras(n subscription.Node) (serverPorts []string, hopInterval string, up, down int) {
	raw := strings.TrimSpace(n.Raw)
	switch {
	case strings.HasPrefix(raw, "hysteria2://"), strings.HasPrefix(raw, "hy2://"):
		if u, err := url.Parse(raw); err == nil {
			q := u.Query()
			serverPorts = normalizePorts(first(q, "mport", "ports", "server_ports"))
			hopInterval = first(q, "hop_interval", "hop")
			up = toInt(first(q, "up_mbps", "upmbps", "up"))
			down = toInt(first(q, "down_mbps", "downmbps", "down"))
		}
	case looksJSON(raw):
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil {
			switch v := m["server_ports"].(type) {
			case []any:
				for _, p := range v {
					serverPorts = append(serverPorts, normalizePorts(toStr(p))...)
				}
			case string:
				serverPorts = normalizePorts(v)
			}
			hopInterval = firstString(m, "hop_interval")
			up = toInt(m["up_mbps"])
			down = toInt(m["down_mbps"])
		}
	}
	return serverPorts, hopInterval, up, down
}

// toStr renders a JSON scalar (string or number) as a string for port parsing.
func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.Itoa(int(x))
	}
	return ""
}

// normalizePorts splits a comma list and rewrites "a-b" ranges to sing-box "a:b".
func normalizePorts(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(strings.ReplaceAll(tok, "-", ":"))
		if tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// hy2Obfs extracts hysteria2 obfuscation (e.g. salamander) from a node's Raw.
// Without it a salamander-obfs server silently drops the handshake, so it must
// survive into the generated outbound.
func hy2Obfs(n subscription.Node) (otype, opass string) {
	raw := strings.TrimSpace(n.Raw)
	switch {
	case strings.HasPrefix(raw, "hysteria2://"), strings.HasPrefix(raw, "hy2://"):
		if u, err := url.Parse(raw); err == nil {
			otype = u.Query().Get("obfs")
			opass = first(u.Query(), "obfs-password", "obfs_password")
		}
	case looksJSON(raw):
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil {
			otype = firstString(m, "obfs")
			opass = firstString(m, "obfs-password")
			if ob, ok := m["obfs"].(map[string]any); ok { // sing-box nested shape
				otype = firstString(ob, "type")
				opass = firstString(ob, "password")
			}
		}
	}
	return otype, opass
}

// hy2Insecure reports whether a hysteria2 node wants TLS cert verification
// skipped (self-signed server, no domain) — from the URL query (insecure /
// allowInsecure / allow_insecure) or the JSON (tls.insecure / skip-cert-verify).
func hy2Insecure(n subscription.Node) bool {
	raw := strings.TrimSpace(n.Raw)
	switch {
	case strings.HasPrefix(raw, "hysteria2://"), strings.HasPrefix(raw, "hy2://"):
		if u, err := url.Parse(raw); err == nil {
			q := u.Query()
			return q.Get("insecure") == "1" || q.Get("allowInsecure") == "1" || q.Get("allow_insecure") == "1"
		}
	case looksJSON(raw):
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil {
			if tls, ok := m["tls"].(map[string]any); ok {
				if b, ok := tls["insecure"].(bool); ok {
					return b
				}
			}
			if b, ok := m["skip-cert-verify"].(bool); ok { // clash shape
				return b
			}
		}
	}
	return false
}

// hy2Secrets extracts (password, sni) from a node's Raw across source shapes.
func hy2Secrets(n subscription.Node) (password, sni string) {
	raw := strings.TrimSpace(n.Raw)
	switch {
	case strings.HasPrefix(raw, "hysteria2://"), strings.HasPrefix(raw, "hy2://"):
		if u, err := url.Parse(raw); err == nil {
			if u.User != nil {
				password = u.User.Username()
			}
			sni = u.Query().Get("sni")
		}
	case looksJSON(raw):
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil {
			password = firstString(m, "password")
			sni = firstString(m, "sni") // clash shape
			if sni == "" {
				if tls, ok := m["tls"].(map[string]any); ok { // sing-box shape
					sni = firstString(tls, "server_name")
				}
			}
		}
	}
	return password, sni
}

// injectUTLS sets a default tls.utls fingerprint on a TCP TLS outbound that has
// a tls block but no utls and no reality (REALITY already pins its own
// fingerprint, and a node that declared `fp=` keeps it). uTLS is client-side
// ClientHello mimicry — no server coordination — so it is safe to default. It is
// skipped for QUIC protocols (hysteria2/tuic), whose handshake is not standard
// TLS. A no-op when fingerprint is "" or the outbound has no eligible tls block.
func injectUTLS(ob outbound, fingerprint string) {
	if fingerprint == "" {
		return
	}
	switch ob["type"] {
	case "vless", "trojan", "vmess", "anytls", "shadowtls":
	default:
		return // hysteria2/tuic (QUIC) and non-TLS protocols: skip
	}
	tls, ok := ob["tls"].(outbound)
	if !ok {
		return
	}
	if _, has := tls["utls"]; has {
		return
	}
	if _, reality := tls["reality"]; reality {
		return
	}
	tls["utls"] = outbound{"enabled": true, "fingerprint": fingerprint}
}

// injectMultiplex sets a default `multiplex` block on a TCP proxy outbound
// (vless/vmess/trojan/shadowsocks) that doesn't already declare one. Mux carries
// many streams over fewer connections (breaking per-flow correlation); padding
// pads the framing and brutal pins a congestion target. Server-cooperative, so
// it is opt-in. Skipped for QUIC (hysteria2/tuic) and anytls (already a muxing
// protocol). A no-op when m is nil or the outbound already has a multiplex block.
func injectMultiplex(ob outbound, m *MultiplexOptions) {
	if m == nil {
		return
	}
	switch ob["type"] {
	case "vless", "trojan", "vmess", "shadowsocks":
	default:
		return // hysteria2/tuic (QUIC), anytls (self-muxing): skip
	}
	if _, has := ob["multiplex"]; has {
		return // node declared its own mux: keep it
	}
	if _, chained := ob["detour"]; chained {
		return // chained outbound (e.g. ss over shadowtls): leave the chain alone
	}
	mb := outbound{"enabled": true}
	if m.Protocol != "" {
		mb["protocol"] = m.Protocol
	}
	if m.MaxConnections > 0 {
		mb["max_connections"] = m.MaxConnections
	}
	if m.MinStreams > 0 {
		mb["min_streams"] = m.MinStreams
	}
	if m.Padding {
		mb["padding"] = true
	}
	if m.BrutalUp > 0 || m.BrutalDown > 0 {
		mb["brutal"] = outbound{"enabled": true, "up_mbps": m.BrutalUp, "down_mbps": m.BrutalDown}
	}
	ob["multiplex"] = mb
}

// stripGeckoObfs removes a hysteria2 obfs block of type "gecko" (used when the
// target sing-box version is < 1.14.0, which rejects it as "unknown obfs type"
// and would fail the whole config). Salamander/other obfs are left intact.
// Returns whether it removed one. The node then connects without obfs — broken
// against a gecko-only server, but the config stays loadable (reported once).
func stripGeckoObfs(ob outbound) bool {
	o, ok := ob["obfs"].(outbound)
	if !ok {
		return false
	}
	if o["type"] == "gecko" {
		delete(ob, "obfs")
		return true
	}
	return false
}

// stripECH removes a tls.ech block from an outbound (used when the target
// sing-box version doesn't support ECH). Returns whether it removed one.
func stripECH(ob outbound) bool {
	tls, ok := ob["tls"].(outbound)
	if !ok {
		return false
	}
	if _, has := tls["ech"]; has {
		delete(tls, "ech")
		return true
	}
	return false
}

// NodeTag returns the sing-box outbound tag Generate assigns to n, and whether
// n is a supported protocol. ok=false means Generate skips n (it is not a
// pinnable outbound), so callers like pkg/noderank must drop it. Sharing this
// with Generate keeps the runtime's pin targets identical to the generated tags.
func NodeTag(n subscription.Node) (tag string, ok bool) {
	// WireGuard is an endpoint, not an outbound, but its tag is a valid pool/pin
	// target — resolve it directly (nodeOutbound only handles outbounds).
	if n.Protocol == subscription.ProtoWireGuard {
		return outboundTag(n), true
	}
	ob, _, err := nodeOutbound(n)
	if err != nil {
		return "", false
	}
	return ob["tag"].(string), true
}

func outboundTag(n subscription.Node) string {
	slug := sanitizeTag(n.DisplayName)
	if len(slug) < 2 {
		slug = n.Protocol // name was all emoji/non-latin (e.g. "🇭🇷 Загреб")
	}
	// finalize() gives every subscription node a 16-hex ID, but NodeTag/Generate
	// take arbitrary nodes (native-JSON passthrough, manual, tests, future sources);
	// a short/empty ID must not panic the whole generation on n.ID[:6] (LOT-38).
	id := n.ID
	if len(id) > 6 {
		id = id[:6]
	}
	return slug + "-" + id
}

// sanitizeTag produces a clash-api-safe outbound tag: ASCII letters/digits and
// single dashes only. Emoji flags, Cyrillic, commas and quotes are dropped (they
// break URL paths like /proxies/{tag}/delay that the runtime balancer calls, and
// byte-truncating multibyte names yields invalid UTF-8). Node identity is carried
// by the "-<id>" suffix the caller appends, so a lossy slug is fine.
func sanitizeTag(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == ' ' || r == '-' || r == '_' || r == '.':
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 24 {
		out = strings.Trim(out[:24], "-")
	}
	return out
}

func looksJSON(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "{") }

func firstString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
