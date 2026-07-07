package subscription

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// parseURL parses a single share-link URL into a Node. The whole URL is kept
// in Raw for later hand-off; we only extract what we need to identify, pool,
// and probe the node (protocol, server, port, name).
func parseURL(raw, source string) (Node, error) {
	raw = strings.TrimSpace(raw)
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return Node{}, fmt.Errorf("no scheme in %q", raw)
	}
	scheme = strings.ToLower(scheme)

	if scheme == "vmess" {
		return parseVMess(rest, raw, source)
	}

	proto := schemeProto(scheme)
	if proto == "" {
		return Node{}, fmt.Errorf("unsupported scheme %q", scheme)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return Node{}, fmt.Errorf("parse %q: %w", raw, err)
	}
	host, port := u.Hostname(), portInt(u.Port())

	// ss links may encode method:pass@host:port inside base64 with no explicit
	// host:port (ss://BASE64#name); url.Parse then mistakes the blob for the
	// host and leaves the port empty. Decode it in that case.
	if proto == ProtoShadowsocks && port == 0 {
		if h, p := decodeSSHost(rest); h != "" && p != 0 {
			host, port = h, p
		}
	}
	if host == "" || !validPort(port) {
		return Node{}, fmt.Errorf("missing/invalid host:port in %q", raw)
	}

	n := Node{
		Protocol:    proto,
		Server:      host,
		Port:        port,
		DisplayName: decodeFragment(u.Fragment),
		Raw:         raw,
	}
	n.finalize(source)
	return n, nil
}

func schemeProto(scheme string) string {
	switch scheme {
	case "hysteria2", "hy2":
		return ProtoHysteria2
	case "vless":
		return ProtoVLESS
	case "trojan":
		return ProtoTrojan
	case "ss":
		return ProtoShadowsocks
	case "anytls":
		return ProtoAnyTLS
	case "tuic":
		return ProtoTUIC
	case "shadowtls":
		return ProtoShadowTLS
	case "wireguard", "wg":
		return ProtoWireGuard
	default:
		return ""
	}
}

// parseVMess decodes vmess://<base64 json> ({add, port, ps, ...}).
func parseVMess(b64, raw, source string) (Node, error) {
	if i := strings.IndexAny(b64, "#?"); i >= 0 {
		b64 = b64[:i]
	}
	decoded, err := decodeBase64(b64)
	if err != nil {
		return Node{}, fmt.Errorf("vmess base64: %w", err)
	}
	var v struct {
		Add  string `json:"add"`
		Port any    `json:"port"`
		PS   string `json:"ps"`
	}
	if err := json.Unmarshal([]byte(decoded), &v); err != nil {
		return Node{}, fmt.Errorf("vmess json: %w", err)
	}
	port := asInt(v.Port)
	if v.Add == "" || !validPort(port) {
		return Node{}, fmt.Errorf("vmess missing/invalid add/port")
	}
	n := Node{
		Protocol:    ProtoVMess,
		Server:      v.Add,
		Port:        port,
		DisplayName: v.PS,
		Raw:         raw,
	}
	n.finalize(source)
	return n, nil
}

// decodeSSHost pulls host:port out of ss://base64(method:pass@host:port)#name.
func decodeSSHost(rest string) (string, int) {
	if i := strings.IndexAny(rest, "#?"); i >= 0 {
		rest = rest[:i]
	}
	decoded, err := decodeBase64(rest)
	if err != nil {
		return "", 0
	}
	at := strings.LastIndex(decoded, "@")
	if at < 0 {
		return "", 0
	}
	hostport := decoded[at+1:]
	host, port, ok := strings.Cut(hostport, ":")
	if !ok {
		return "", 0
	}
	return host, portInt(port)
}

func decodeFragment(f string) string {
	if d, err := url.QueryUnescape(f); err == nil {
		return d
	}
	return f
}

func portInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// validPort reports whether p is a usable TCP/UDP port (1..65535). Parsers use
// it to drop a node with a missing or out-of-range port rather than carry the
// bad value into a generated config, where an invalid server_port would fail
// sing-box validation for the whole node set.
func validPort(p int) bool { return p >= 1 && p <= 65535 }
