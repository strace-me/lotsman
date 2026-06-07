package singbox

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/strace-me/lotsman/pkg/subscription"
)

// This file builds sing-box outbounds for share-link protocols (vless, trojan,
// shadowsocks, vmess) by re-parsing the node's original URL from Node.Raw.
//
// Clean-room: field names and nesting come from the sing-box outbound schema
// (sing-box.sagernet.org) and the de-facto VLESS/Reality share-link query
// conventions (security/sni/fp/pbk/sid/flow/type/path/host/serviceName). No
// code was taken from s-ui (GPL); only the public interface facts.

// parseShareURL splits a share link into userinfo, query params, and fragment.
// Server/port come from the already-parsed Node, so only the parts the parser
// dropped are recovered here.
func parseShareURL(raw string) (user string, q url.Values, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", nil, false
	}
	if u.User != nil {
		user = u.User.Username()
	}
	return user, u.Query(), true
}

// tlsBlock builds the tls object from share-link params, or nil when TLS is off.
func tlsBlock(q url.Values, serverName string) outbound {
	sec := q.Get("security")
	// trojan/vless-reality always use TLS; treat empty security as TLS when an
	// sni is present, otherwise off.
	if sec == "none" {
		return nil
	}
	// For Reality the SNI must be the decoy server_name (sni/peer), never the WS/
	// HTTP transport `host` header — using host as SNI breaks the Reality handshake
	// (LOT-38). Non-Reality TLS may still fall back to host (common in ws links).
	isReality := sec == "reality" || q.Get("pbk") != ""
	var sni string
	if isReality {
		sni = first(q, "sni", "peer")
	} else {
		sni = first(q, "sni", "peer", "host")
	}
	if sec == "" && sni == "" && q.Get("pbk") == "" {
		return nil
	}
	tls := outbound{"enabled": true}
	if sni == "" {
		sni = serverName
	}
	if sni != "" {
		tls["server_name"] = sni
	}
	if alpn := q.Get("alpn"); alpn != "" {
		tls["alpn"] = strings.Split(alpn, ",")
	}
	if q.Get("allowInsecure") == "1" || q.Get("insecure") == "1" || q.Get("allow_insecure") == "1" {
		tls["insecure"] = true
	}
	if fp := q.Get("fp"); fp != "" {
		tls["utls"] = outbound{"enabled": true, "fingerprint": fp}
	}
	if ech := q.Get("ech"); ech != "" {
		// ech=1/true -> enabled (config resolved via DNS at runtime); otherwise
		// treat the value as a base64-wrapped ECHConfigList PEM and inline it.
		e := outbound{"enabled": true}
		if ech != "1" && !strings.EqualFold(ech, "true") {
			if dec, derr := decodeAnyBase64(ech); derr == nil {
				e["config"] = strings.Split(strings.TrimSpace(dec), "\n")
			}
		}
		tls["ech"] = e
	}
	if sec == "reality" || q.Get("pbk") != "" {
		reality := outbound{"enabled": true}
		if pbk := q.Get("pbk"); pbk != "" {
			reality["public_key"] = pbk
		}
		if sid := q.Get("sid"); sid != "" {
			reality["short_id"] = sid
		}
		tls["reality"] = reality
	}
	return tls
}

// transportBlock builds the V2Ray transport object, or nil for plain TCP.
func transportBlock(q url.Values) outbound {
	switch q.Get("type") {
	case "ws":
		tr := outbound{"type": "ws"}
		if p := q.Get("path"); p != "" {
			tr["path"] = p
		}
		if h := q.Get("host"); h != "" {
			tr["headers"] = outbound{"Host": h}
		}
		return tr
	case "grpc":
		tr := outbound{"type": "grpc"}
		if s := first(q, "serviceName", "servicename"); s != "" {
			tr["service_name"] = s
		}
		return tr
	case "http", "h2", "xhttp", "splithttp":
		// sing-box names this transport "http"; xhttp/splithttp are mapped
		// best-effort (newer Xray names) since sing-box has no distinct type.
		tr := outbound{"type": "http"}
		if h := q.Get("host"); h != "" {
			tr["host"] = []string{h}
		}
		if p := q.Get("path"); p != "" {
			tr["path"] = p
		}
		return tr
	case "httpupgrade":
		tr := outbound{"type": "httpupgrade"}
		if h := q.Get("host"); h != "" {
			tr["host"] = h
		}
		if p := q.Get("path"); p != "" {
			tr["path"] = p
		}
		return tr
	default:
		return nil // tcp or empty: no transport block
	}
}

func vlessOutbound(n subscription.Node, tag string) (outbound, error) {
	uuid, q, ok := parseShareURL(n.Raw)
	if !ok || uuid == "" {
		return nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	ob := outbound{
		"type": "vless", "tag": tag,
		"server": n.Server, "server_port": n.Port, "uuid": uuid,
		"packet_encoding": "xudp",
	}
	if flow := q.Get("flow"); flow != "" {
		ob["flow"] = flow
	}
	if tls := tlsBlock(q, n.Server); tls != nil {
		ob["tls"] = tls
	}
	if tr := transportBlock(q); tr != nil {
		ob["transport"] = tr
	}
	return ob, nil
}

func trojanOutbound(n subscription.Node, tag string) (outbound, error) {
	password, q, ok := parseShareURL(n.Raw)
	if !ok || password == "" {
		return nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	ob := outbound{
		"type": "trojan", "tag": tag,
		"server": n.Server, "server_port": n.Port, "password": password,
	}
	tls := tlsBlock(q, n.Server)
	if tls == nil {
		tls = outbound{"enabled": true, "server_name": n.Server} // trojan implies TLS
	}
	ob["tls"] = tls
	if tr := transportBlock(q); tr != nil {
		ob["transport"] = tr
	}
	return ob, nil
}

func shadowsocksOutbound(n subscription.Node, tag string) (outbound, error) {
	method, password, ok := ssCreds(n.Raw)
	if !ok {
		return nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	ob := outbound{
		"type": "shadowsocks", "tag": tag,
		"server": n.Server, "server_port": n.Port,
		"method": method, "password": password,
	}
	if _, q, ok := parseShareURL(n.Raw); ok {
		if plugin := q.Get("plugin"); plugin != "" {
			// plugin spec is "name;opts"; sing-box wants them split.
			name, opts, _ := strings.Cut(plugin, ";")
			ob["plugin"] = name
			if opts != "" {
				ob["plugin_opts"] = opts
			}
		}
	}
	return ob, nil
}

// ssCreds extracts (method, password) from an ss:// link in either encoding:
//
//	ss://base64(method:password)@host:port
//	ss://base64(method:password@host:port)
func ssCreds(raw string) (method, password string, ok bool) {
	rest := strings.TrimPrefix(strings.TrimPrefix(raw, "ss://"), "//")
	if i := strings.IndexAny(rest, "#?"); i >= 0 {
		rest = rest[:i]
	}
	creds := rest
	userinfoForm := false
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		creds = rest[:at] // userinfo form: base64(method:pass)@host:port
		userinfoForm = true
	}
	dec, err := decodeAnyBase64(creds)
	if err == nil {
		creds = dec
	}
	// full-base64 form decodes to method:pass@host:port; keep only userinfo. Skip
	// this when the @host:port was ALREADY stripped above (userinfo form) — else an
	// '@' inside the (now decoded) password would be mis-cut and truncate it.
	if !userinfoForm {
		if at := strings.LastIndex(creds, "@"); at >= 0 {
			creds = creds[:at]
		}
	}
	m, p, found := strings.Cut(creds, ":")
	if !found || m == "" {
		return "", "", false
	}
	return m, p, true
}

func vmessOutbound(n subscription.Node, tag string) (outbound, error) {
	b64 := strings.TrimPrefix(n.Raw, "vmess://")
	if i := strings.IndexAny(b64, "#?"); i >= 0 {
		b64 = b64[:i]
	}
	dec, err := decodeAnyBase64(b64)
	if err != nil {
		return nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	var v struct {
		ID   string `json:"id"`
		Aid  any    `json:"aid"`
		Net  string `json:"net"`
		Type string `json:"type"`
		Host string `json:"host"`
		Path string `json:"path"`
		TLS  string `json:"tls"`
		SNI  string `json:"sni"`
		Scy  string `json:"scy"`
	}
	if json.Unmarshal([]byte(dec), &v) != nil || v.ID == "" {
		return nil, SkipError{NodeID: n.ID, Protocol: n.Protocol}
	}
	security := v.Scy
	if security == "" {
		security = "auto"
	}
	ob := outbound{
		"type": "vmess", "tag": tag,
		"server": n.Server, "server_port": n.Port,
		"uuid": v.ID, "alter_id": toInt(v.Aid), "security": security,
	}
	if v.TLS == "tls" {
		tls := outbound{"enabled": true}
		if v.SNI != "" {
			tls["server_name"] = v.SNI
		} else if v.Host != "" {
			tls["server_name"] = v.Host
		}
		ob["tls"] = tls
	}
	// vmess transport comes from the "net" field rather than query params.
	q := url.Values{}
	switch v.Net {
	case "ws", "grpc", "http", "h2", "httpupgrade":
		q.Set("type", v.Net)
		if v.Path != "" {
			q.Set("path", v.Path)
		}
		if v.Host != "" {
			q.Set("host", v.Host)
			q.Set("serviceName", v.Host)
		}
		if tr := transportBlock(q); tr != nil {
			ob["transport"] = tr
		}
	}
	return ob, nil
}

func first(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func toInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

func decodeAnyBase64(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}
	return "", errInvalidBase64
}

var errInvalidBase64 = &base64Error{}

type base64Error struct{}

func (*base64Error) Error() string { return "invalid base64" }
