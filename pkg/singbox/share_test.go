package singbox

import (
	"encoding/base64"
	"testing"

	"github.com/strace-me/lotsman/pkg/subscription"
)

func build(t *testing.T, raw string, f subscription.Format) outbound {
	t.Helper()
	n := mustParse(t, raw, f)
	ob, _, err := nodeOutbound(n)
	if err != nil {
		t.Fatalf("nodeOutbound(%s): %v", raw, err)
	}
	return ob
}

func TestHysteria2Obfs(t *testing.T) {
	ob := build(t, "hysteria2://pass123@1.2.3.4:443?sni=nl3.example&obfs=salamander&obfs-password=obpw", subscription.FormatSingleURL)
	if ob["type"] != "hysteria2" || ob["password"] != "pass123" {
		t.Fatalf("hy2 base wrong: %v", ob)
	}
	obfs, ok := ob["obfs"].(outbound)
	if !ok || obfs["type"] != "salamander" || obfs["password"] != "obpw" {
		t.Errorf("obfs = %v, want salamander/obpw", ob["obfs"])
	}
}

func TestVLESSRealityVision(t *testing.T) {
	ob := build(t, "vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?security=reality&flow=xtls-rprx-vision&pbk=PUBKEY&sid=ab12&sni=www.example.com&fp=chrome#x", subscription.FormatSingleURL)
	if ob["type"] != "vless" || ob["uuid"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("vless base wrong: %v", ob)
	}
	if ob["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow = %v", ob["flow"])
	}
	tls := ob["tls"].(outbound)
	if tls["server_name"] != "www.example.com" {
		t.Errorf("sni = %v", tls["server_name"])
	}
	if tls["utls"].(outbound)["fingerprint"] != "chrome" {
		t.Errorf("fp = %v", tls["utls"])
	}
	r := tls["reality"].(outbound)
	if r["enabled"] != true || r["public_key"] != "PUBKEY" || r["short_id"] != "ab12" {
		t.Errorf("reality = %v", r)
	}
}

func TestVLESSWebSocketTLS(t *testing.T) {
	ob := build(t, "vless://uuid@1.2.3.4:443?security=tls&type=ws&path=%2Fvideo&host=cdn.example.com&sni=cdn.example.com#x", subscription.FormatSingleURL)
	tr := ob["transport"].(outbound)
	if tr["type"] != "ws" || tr["path"] != "/video" {
		t.Errorf("ws transport = %v", tr)
	}
	if tr["headers"].(outbound)["Host"] != "cdn.example.com" {
		t.Errorf("ws host header = %v", tr["headers"])
	}
	if ob["tls"] == nil {
		t.Error("tls block missing for security=tls")
	}
}

func TestTrojanGRPC(t *testing.T) {
	ob := build(t, "trojan://secretpw@9.9.9.9:443?type=grpc&serviceName=gun&sni=trojan.example#x", subscription.FormatSingleURL)
	if ob["type"] != "trojan" || ob["password"] != "secretpw" {
		t.Fatalf("trojan base wrong: %v", ob)
	}
	if ob["tls"].(outbound)["server_name"] != "trojan.example" {
		t.Errorf("trojan sni = %v", ob["tls"])
	}
	if ob["transport"].(outbound)["service_name"] != "gun" {
		t.Errorf("grpc service_name = %v", ob["transport"])
	}
}

func TestTrojanImpliesTLS(t *testing.T) {
	// No security param: trojan still gets a TLS block (server_name = server).
	ob := build(t, "trojan://pw@9.9.9.9:443#x", subscription.FormatSingleURL)
	if ob["tls"].(outbound)["enabled"] != true {
		t.Errorf("trojan should imply TLS: %v", ob["tls"])
	}
}

func TestShadowsocksBothForms(t *testing.T) {
	// userinfo form: base64(method:pass)@host:port
	ob := build(t, "ss://"+b64ss("aes-256-gcm:hunter2")+"@1.1.1.1:8388#x", subscription.FormatSingleURL)
	if ob["type"] != "shadowsocks" || ob["method"] != "aes-256-gcm" || ob["password"] != "hunter2" {
		t.Fatalf("ss userinfo form: %v", ob)
	}
	// full-base64 form: base64(method:pass@host:port)
	ob = build(t, "ss://"+b64ss("chacha20-ietf-poly1305:pw@2.2.2.2:8388")+"#x", subscription.FormatSingleURL)
	if ob["method"] != "chacha20-ietf-poly1305" || ob["password"] != "pw" {
		t.Fatalf("ss full-base64 form: %v", ob)
	}
}

func TestVMessWS(t *testing.T) {
	js := `{"v":"2","add":"3.3.3.3","port":"443","id":"vmess-uuid","aid":"0","net":"ws","host":"vm.example","path":"/ray","tls":"tls","scy":"auto"}`
	ob := build(t, "vmess://"+b64ss(js), subscription.FormatSingleURL)
	if ob["type"] != "vmess" || ob["uuid"] != "vmess-uuid" {
		t.Fatalf("vmess base: %v", ob)
	}
	if ob["alter_id"] != 0 {
		t.Errorf("alter_id = %v", ob["alter_id"])
	}
	if ob["transport"].(outbound)["type"] != "ws" {
		t.Errorf("vmess transport = %v", ob["transport"])
	}
	if ob["tls"].(outbound)["server_name"] != "vm.example" {
		t.Errorf("vmess tls sni = %v", ob["tls"])
	}
}

func b64ss(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
