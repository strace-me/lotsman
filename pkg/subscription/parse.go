package subscription

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Format identifies a subscription encoding.
type Format string

const (
	FormatAuto        Format = "auto"
	FormatClash       Format = "clash"
	FormatSingbox     Format = "singbox"
	FormatV2rayBase64 Format = "v2ray_base64"
	FormatV2rayPlain  Format = "v2ray_plaintext"
	FormatSingleURL   Format = "single_url"
)

// Parse turns raw subscription bytes into normalized nodes. With FormatAuto it
// sniffs the encoding. Unparseable individual entries are skipped (a bad line
// should not sink the whole pull); a wholly unrecognized payload errors.
func Parse(data []byte, format Format, source string) ([]Node, error) {
	// Strip a leading UTF-8 BOM: some providers/CDNs prepend it, which otherwise
	// defeats format detection (BOM bytes aren't base64/JSON/scheme chars) and
	// silently yields zero nodes from an otherwise-valid subscription.
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if format == FormatAuto {
		format = Detect(data)
	}
	nodes, err := parseFormat(data, format, source)
	if err != nil {
		return nil, err
	}
	// Identity is finalised over the WHOLE pull, because whether a connection
	// parameter distinguishes two exits or is merely a rotating credential can
	// only be told from how many nodes share an address at once. See assignIDs.
	assignIDs(nodes)
	return nodes, nil
}

func parseFormat(data []byte, format Format, source string) ([]Node, error) {
	switch format {
	case FormatClash:
		return parseClash(data, source)
	case FormatSingbox:
		return parseSingbox(data, source)
	case FormatV2rayBase64:
		return parseV2rayBase64(data, source)
	case FormatV2rayPlain:
		return parseURLLines(string(data), source)
	case FormatSingleURL:
		n, err := parseURL(strings.TrimSpace(string(data)), source)
		if err != nil {
			return nil, err
		}
		return []Node{n}, nil
	default:
		return nil, fmt.Errorf("subscription: unrecognized format")
	}
}

// Detect sniffs the format from content. Reliable for the common cases; a
// caller can always override with an explicit Format.
func Detect(data []byte) Format {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return FormatAuto
	}
	switch s[0] {
	case '{':
		if strings.Contains(s, "\"outbounds\"") {
			return FormatSingbox
		}
	case '[':
		// Liberty returns a JSON array of sing-box configs.
		if strings.Contains(s, "\"outbounds\"") {
			return FormatSingbox
		}
	}
	if strings.Contains(s, "proxies:") {
		return FormatClash
	}
	// Plaintext / single URL: lines carry a scheme.
	if strings.Contains(s, "://") {
		if strings.Contains(strings.TrimSpace(s), "\n") {
			return FormatV2rayPlain
		}
		return FormatSingleURL
	}
	// No scheme, no structure markers: assume base64-wrapped URL list.
	if looksBase64(s) {
		return FormatV2rayBase64
	}
	return FormatAuto
}

func looksBase64(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '+', r == '/', r == '=', r == '-', r == '_', r == '\n', r == '\r':
		default:
			return false
		}
	}
	return true
}

// --- structured formats ---

func parseClash(data []byte, source string) ([]Node, error) {
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("subscription: clash yaml: %w", err)
	}
	out := make([]Node, 0, len(doc.Proxies))
	for _, p := range doc.Proxies {
		n := Node{
			Protocol:    clashType(asString(p["type"])),
			Server:      asString(p["server"]),
			Port:        asInt(p["port"]),
			DisplayName: asString(p["name"]),
		}
		if n.Server == "" || !validPort(n.Port) || n.Protocol == "" {
			continue
		}
		raw, _ := json.Marshal(p)
		n.Raw = string(raw)
		n.finalize(source)
		out = append(out, n)
	}
	return out, nil
}

func parseSingbox(data []byte, source string) ([]Node, error) {
	// Liberty returns a JSON array of sing-box configs; a plain object is also valid.
	var docs []map[string]any
	if err := json.Unmarshal(data, &docs); err != nil {
		// Maybe it's a single config object, not an array.
		var single map[string]any
		if err2 := json.Unmarshal(data, &single); err2 != nil {
			return nil, fmt.Errorf("subscription: singbox json: %w", err)
		}
		docs = []map[string]any{single}
	}
	out := make([]Node, 0, len(docs))
	for _, doc := range docs {
		rawOutbounds, ok := doc["outbounds"]
		if !ok {
			continue
		}
		outbounds, ok := rawOutbounds.([]any)
		if !ok {
			continue
		}
		for _, raw := range outbounds {
			o, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			// Support both native sing-box ("type"/"server"/"server_port")
			// and v2ray-style ("protocol"/"settings.vnext"/"settings.servers")
			// outbounds, as used by liberty and similar providers.
			proto := singboxType(asString(o["type"]))
			if proto == "" {
				proto = singboxType(asString(o["protocol"]))
			}
			if proto == "" {
				continue // skip non-node outbounds (direct, block, selector, ...)
			}
			server, port := extractServerPort(o)
			if server == "" || !validPort(port) {
				continue
			}
			n := Node{
				Protocol:    proto,
				Server:      server,
				Port:        port,
				DisplayName: asString(o["tag"]),
			}
			raw, _ := json.Marshal(o)
			n.Raw = string(raw)
			n.Native = true // Raw is a native sing-box outbound: emit it verbatim
			n.finalize(source)
			out = append(out, n)
		}
	}
	return out, nil
}

// extractServerPort pulls the server address and port from an outbound,
// supporting both native sing-box (top-level server/server_port) and v2ray-style
// nested fields (settings.vnext[0] or settings.servers[0]).
func extractServerPort(o map[string]any) (string, int) {
	// Native sing-box format.
	if s := asString(o["server"]); s != "" {
		return s, asInt(o["server_port"])
	}
	// v2ray-style: settings.vnext[0] (vless/trojan/vmess)
	if settings, ok := o["settings"].(map[string]any); ok {
		if vnext, ok := settings["vnext"].([]any); ok && len(vnext) > 0 {
			if v, ok := vnext[0].(map[string]any); ok {
				return asString(v["address"]), asInt(v["port"])
			}
		}
		// shadowsocks style: settings.servers[0]
		if servers, ok := settings["servers"].([]any); ok && len(servers) > 0 {
			if srv, ok := servers[0].(map[string]any); ok {
				return asString(srv["address"]), asInt(srv["port"])
			}
		}
		// hysteria v2 native: settings.address / settings.port
		if addr := asString(settings["address"]); addr != "" {
			return addr, asInt(settings["port"])
		}
	}
	return "", 0
}

func parseV2rayBase64(data []byte, source string) ([]Node, error) {
	decoded, err := decodeBase64(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("subscription: base64: %w", err)
	}
	return parseURLLines(decoded, source)
}

// --- URL line formats ---

func parseURLLines(text, source string) ([]Node, error) {
	var out []Node
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "://") {
			continue
		}
		n, err := parseURL(line, source)
		if err != nil {
			continue // skip the bad entry, keep the rest
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("subscription: no valid node URLs")
	}
	return out, nil
}

func decodeBase64(s string) (string, error) {
	s = strings.TrimSpace(s)
	// Subscriptions use std or url-safe alphabet, often unpadded.
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("not valid base64")
}

func clashType(t string) string {
	switch strings.ToLower(t) {
	case "hysteria2", "hy2":
		return ProtoHysteria2
	case "vless":
		return ProtoVLESS
	case "trojan":
		return ProtoTrojan
	case "ss", "shadowsocks":
		return ProtoShadowsocks
	case "vmess":
		return ProtoVMess
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

func singboxType(t string) string {
	switch strings.ToLower(t) {
	case "hysteria", "hysteria2":
		return ProtoHysteria2
	case "vless":
		return ProtoVLESS
	case "trojan":
		return ProtoTrojan
	case "shadowsocks":
		return ProtoShadowsocks
	case "vmess":
		return ProtoVMess
	case "anytls":
		return ProtoAnyTLS
	case "tuic":
		return ProtoTUIC
	case "shadowtls":
		return ProtoShadowTLS
	case "wireguard":
		return ProtoWireGuard
	default:
		return "" // direct/block/dns/selector/urltest/...
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(x); err == nil {
			return n
		}
	}
	return 0
}
