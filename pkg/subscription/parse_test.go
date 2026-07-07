package subscription

import (
	"encoding/base64"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestParseSingleURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		proto   string
		server  string
		port    int
		udp     bool
		display string
	}{
		{"vless-ws", "vless://uuid@1.2.3.4:443?type=ws&security=tls#node-a", ProtoVLESS, "1.2.3.4", 443, false, "node-a"},
		{"hysteria2", "hysteria2://pass@5.6.7.8:8443#hy-de", ProtoHysteria2, "5.6.7.8", 8443, true, "hy-de"},
		{"hy2-alias", "hy2://pass@5.6.7.8:8443#hy", ProtoHysteria2, "5.6.7.8", 8443, true, "hy"},
		{"trojan", "trojan://pw@9.9.9.9:443#tr", ProtoTrojan, "9.9.9.9", 443, false, "tr"},
		{"ss-host-explicit", "ss://" + b64("aes-256-gcm:secret") + "@1.1.1.1:8388#ss", ProtoShadowsocks, "1.1.1.1", 8388, true, "ss"},
		{"ss-base64-full", "ss://" + b64("aes-256-gcm:secret@2.2.2.2:8388") + "#ss2", ProtoShadowsocks, "2.2.2.2", 8388, true, "ss2"},
		{"vmess", "vmess://" + b64(`{"add":"3.3.3.3","port":"443","ps":"vm-1"}`), ProtoVMess, "3.3.3.3", 443, false, "vm-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nodes, err := Parse([]byte(c.url), FormatSingleURL, "test")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(nodes) != 1 {
				t.Fatalf("got %d nodes, want 1", len(nodes))
			}
			n := nodes[0]
			if n.Protocol != c.proto || n.Server != c.server || n.Port != c.port {
				t.Errorf("got %s %s:%d, want %s %s:%d", n.Protocol, n.Server, n.Port, c.proto, c.server, c.port)
			}
			if n.Caps.UDPNative != c.udp {
				t.Errorf("udp_native = %v, want %v", n.Caps.UDPNative, c.udp)
			}
			if !n.Caps.TCP {
				t.Error("tcp cap should always be true")
			}
			if c.display != "" && n.DisplayName != c.display {
				t.Errorf("display = %q, want %q", n.DisplayName, c.display)
			}
			if n.ID == "" {
				t.Error("node ID not set")
			}
			if n.Source != "test" {
				t.Errorf("source = %q, want test", n.Source)
			}
		})
	}
}

func TestParseV2rayPlaintextAndBase64(t *testing.T) {
	lines := "vless://uuid@1.2.3.4:443#a\ntrojan://pw@9.9.9.9:443#b\n# a comment with no scheme\nhysteria2://pass@5.6.7.8:443#c\n"

	plain, err := Parse([]byte(lines), FormatV2rayPlain, "plain")
	if err != nil {
		t.Fatalf("plaintext: %v", err)
	}
	if len(plain) != 3 {
		t.Fatalf("plaintext: got %d nodes, want 3", len(plain))
	}

	enc, err := Parse([]byte(b64(lines)), FormatV2rayBase64, "b64")
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	if len(enc) != 3 {
		t.Fatalf("base64: got %d nodes, want 3", len(enc))
	}
}

func TestParseClash(t *testing.T) {
	yaml := `
proxies:
  - { name: "DE-1", type: hysteria2, server: 5.6.7.8, port: 443, password: pw }
  - { name: "NL-1", type: vless, server: 1.2.3.4, port: 443, uuid: u }
  - { name: "bad", type: vless }
`
	nodes, err := Parse([]byte(yaml), FormatClash, "clash")
	if err != nil {
		t.Fatalf("clash: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2 (bad one dropped)", len(nodes))
	}
	if nodes[0].Protocol != ProtoHysteria2 || !nodes[0].Caps.UDPNative {
		t.Errorf("node0 = %s udp=%v, want hysteria2 udp=true", nodes[0].Protocol, nodes[0].Caps.UDPNative)
	}
	if nodes[0].Native {
		t.Error("clash node must not be Native (Raw uses clash field names)")
	}
}

func TestParsePortOutOfRange(t *testing.T) {
	// A single out-of-range URL yields no valid node.
	for _, u := range []string{
		"trojan://pw@9.9.9.9:65536#x",
		"hysteria2://pw@9.9.9.9:99999#x",
		"vless://u@9.9.9.9:0#x",
	} {
		if _, err := Parse([]byte(u), FormatSingleURL, "t"); err == nil {
			t.Errorf("%q: want error for out-of-range port", u)
		}
	}
	// A bad port must not sink the rest of the pull (parser contract).
	mixed := "trojan://pw@9.9.9.9:65536#bad\nvless://u@1.2.3.4:443#good\n"
	nodes, err := Parse([]byte(mixed), FormatV2rayPlain, "t")
	if err != nil {
		t.Fatalf("mixed: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Port != 443 {
		t.Fatalf("mixed: want 1 good node on :443, got %d nodes", len(nodes))
	}
	// Clash and sing-box out-of-range ports are dropped, the good node kept.
	clash := "proxies:\n  - { name: bad, type: hysteria2, server: 5.6.7.8, port: 70000, password: pw }\n  - { name: ok, type: hysteria2, server: 5.6.7.8, port: 443, password: pw }\n"
	if ns, _ := Parse([]byte(clash), FormatClash, "t"); len(ns) != 1 || ns[0].Port != 443 {
		t.Errorf("clash: want 1 node on :443, got %d", len(ns))
	}
	sb := `{"outbounds":[{"type":"hysteria2","tag":"bad","server":"5.6.7.8","server_port":70000},{"type":"hysteria2","tag":"ok","server":"5.6.7.8","server_port":443}]}`
	if ns, _ := Parse([]byte(sb), FormatSingbox, "t"); len(ns) != 1 || ns[0].Port != 443 {
		t.Errorf("singbox: want 1 node on :443, got %d", len(ns))
	}
	// vmess out-of-range port errors.
	if _, err := Parse([]byte("vmess://"+b64(`{"add":"3.3.3.3","port":"65536","ps":"x"}`)), FormatSingleURL, "t"); err == nil {
		t.Error("vmess: want error for out-of-range port")
	}
}

func TestParseSingbox(t *testing.T) {
	js := `{"outbounds":[
		{"type":"direct","tag":"direct"},
		{"type":"hysteria2","tag":"hy-1","server":"5.6.7.8","server_port":443},
		{"type":"selector","tag":"vpn","outbounds":["hy-1"]}
	]}`
	nodes, err := Parse([]byte(js), FormatSingbox, "sb")
	if err != nil {
		t.Fatalf("singbox: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1 (direct/selector skipped)", len(nodes))
	}
	if nodes[0].Server != "5.6.7.8" || nodes[0].Port != 443 {
		t.Errorf("got %s:%d, want 5.6.7.8:443", nodes[0].Server, nodes[0].Port)
	}
	if !nodes[0].Native {
		t.Error("sing-box node must be Native (Raw is a native outbound)")
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Format
	}{
		{"singbox", `{"outbounds":[]}`, FormatSingbox},
		{"clash", "proxies:\n  - {}\n", FormatClash},
		{"single", "vless://u@1.2.3.4:443#x", FormatSingleURL},
		{"plain", "vless://u@1.2.3.4:443#x\ntrojan://p@9.9.9.9:443#y", FormatV2rayPlain},
		{"base64", b64("vless://u@1.2.3.4:443#x\ntrojan://p@9.9.9.9:443#y"), FormatV2rayBase64},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Detect([]byte(c.in)); got != c.want {
				t.Errorf("Detect = %s, want %s", got, c.want)
			}
		})
	}
}

func TestParseAutoRoundTrip(t *testing.T) {
	// FormatAuto should route each payload to the right parser.
	for _, in := range []string{
		`{"outbounds":[{"type":"vless","tag":"a","server":"1.2.3.4","server_port":443}]}`,
		"proxies:\n  - { name: a, type: vless, server: 1.2.3.4, port: 443 }\n",
		"vless://u@1.2.3.4:443#a",
		b64("vless://u@1.2.3.4:443#a"),
	} {
		nodes, err := Parse([]byte(in), FormatAuto, "auto")
		if err != nil {
			t.Fatalf("auto parse %q: %v", in[:min(20, len(in))], err)
		}
		if len(nodes) == 0 {
			t.Errorf("auto parse produced no nodes for %q", in[:min(20, len(in))])
		}
	}
}

// Bug-hunt: a leading UTF-8 BOM (some CDNs prepend it) must not defeat format
// detection and silently yield zero nodes from a valid subscription.
func TestParseStripsBOM(t *testing.T) {
	body := "\xEF\xBB\xBF" + "hysteria2://pw@2.2.2.2:443#b"
	nodes, err := Parse([]byte(body), FormatAuto, "s")
	if err != nil {
		t.Fatalf("BOM-prefixed sub must parse, got %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1 (BOM stripped)", len(nodes))
	}
}
