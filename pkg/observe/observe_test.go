package observe

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/strategy"
)

// fixture mirrors a sing-box GET /connections payload (subset). It contains:
//   - a youtube QUIC flow that went to "direct" while alive  -> leak
//   - a youtube QUIC flow tunnelled via hysteria2/sel-youtube -> ok
//   - a youtube QUIC flow tunnelled but with 0 download       -> dead (not leak)
//   - a youtube flow matched by destination IP (sniff failed) -> ok, tunnelled
//   - a ru_direct flow that went direct                       -> NOT a leak
//   - an unrelated flow (github) that went direct             -> unmatched
const fixture = `{"connections":[
  {"chains":["direct"],"upload":1200,"download":0,
   "metadata":{"host":"rr1---sn-x.googlevideo.com","destinationIP":"142.251.1.1","destinationPort":"443","network":"udp"}},
  {"chains":["hysteria2-node","sel-youtube"],"upload":1200,"download":98765,
   "metadata":{"host":"rr2---sn-x.googlevideo.com","destinationIP":"142.251.1.2","destinationPort":"443","network":"udp"}},
  {"chains":["hysteria2-node","sel-youtube"],"upload":1200,"download":0,
   "metadata":{"host":"rr3---sn-x.googlevideo.com","destinationIP":"142.251.1.3","destinationPort":"443","network":"udp"}},
  {"chains":["hysteria2-node","sel-youtube"],"upload":500,"download":40000,
   "metadata":{"host":"","destinationIP":"142.251.99.4","destinationPort":"443","network":"tcp"}},
  {"chains":["direct"],"upload":300,"download":4000,
   "metadata":{"host":"mail.ru","destinationIP":"94.100.180.200","destinationPort":"443","network":"tcp"}},
  {"chains":["direct"],"upload":300,"download":4000,
   "metadata":{"host":"github.com","destinationIP":"140.82.112.3","destinationPort":"443","network":"tcp"}}
]}`

// wireConn matches the dataplane.Connection JSON shape so the test fixture can
// be decoded here without importing dataplane (avoids a test-only cycle).
type wireConn struct {
	Chains   []string `json:"chains"`
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
	Metadata struct {
		Host          string `json:"host"`
		DestinationIP string `json:"destinationIP"`
		Network       string `json:"network"`
	} `json:"metadata"`
}

type fakeSource struct{ conns []Conn }

func (f fakeSource) Connections(context.Context) ([]Conn, error) { return f.conns, nil }

func loadFixture(t *testing.T) []Conn {
	t.Helper()
	var body struct {
		Connections []wireConn `json:"connections"`
	}
	if err := json.Unmarshal([]byte(fixture), &body); err != nil {
		t.Fatalf("fixture decode: %v", err)
	}
	out := make([]Conn, len(body.Connections))
	for i, c := range body.Connections {
		out[i] = Conn{
			Chains: c.Chains, Upload: c.Upload, Download: c.Download,
			Host: c.Metadata.Host, DestIP: c.Metadata.DestinationIP, Network: c.Metadata.Network,
		}
	}
	return out
}

// testRegistry: youtube (streaming, has VPN chain) matched by googlevideo.com
// suffix and a 142.251.99.0/24 IP block; ru_mail (ru_direct, direct-only)
// matched by mail.ru.
func testRegistry() *registry.Registry {
	return &registry.Registry{Services: map[string]registry.Service{
		"youtube": {
			Name:    "youtube",
			Domains: []string{"googlevideo.com"},
			IPs:     []string{"142.251.99.0/24"},
			Chain: []registry.ChainStep{
				{State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test"},
			},
		},
		"ru_mail": {
			Name:    "ru_mail",
			Domains: []string{"mail.ru"},
			Chain: []registry.ChainStep{
				{State: registry.StateLocked, StrategyClass: strategy.ClassDirect, StrategyID: "direct"},
			},
		},
	}}
}

func TestObserveLeakAndDeadRatios(t *testing.T) {
	eye := New(fakeSource{loadFixture(t)}, testRegistry())
	snap, err := eye.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	// 5 matched (4 youtube + 1 ru_mail), 1 unmatched (github).
	if snap.Matched != 5 || snap.Unmatched != 1 {
		t.Fatalf("matched=%d unmatched=%d, want 5/1", snap.Matched, snap.Unmatched)
	}

	yt, ok := snap.Services["youtube"]
	if !ok {
		t.Fatal("youtube missing from snapshot")
	}
	// youtube flows: 4 (3 by domain + 1 by IP).
	if yt.Flows != 4 {
		t.Errorf("youtube flows=%d, want 4", yt.Flows)
	}
	// leak: only the first flow (direct + alive). The dead-tunnelled flow is
	// NOT a leak (it rode the selector). want LeakFlows=1, ratio=0.25.
	if yt.LeakFlows != 1 || yt.LeakRatio != 0.25 {
		t.Errorf("youtube leak=%d ratio=%v, want 1/0.25", yt.LeakFlows, yt.LeakRatio)
	}
	// UDP flows: 3 (the 3 udp googlevideo flows). Dead among them: the leaked
	// one (download 0) and the tunnelled-0 one => 2. ratio = 2/3.
	if yt.UDPFlows != 3 || yt.DeadUDPFlows != 2 {
		t.Errorf("youtube udp=%d dead=%d, want 3/2", yt.UDPFlows, yt.DeadUDPFlows)
	}
	if got := yt.DeadFlowRatio; got < 0.666 || got > 0.667 {
		t.Errorf("youtube dead_ratio=%v, want ~0.6667", got)
	}
	// one-way UDP: both dead flows uploaded (1200) with 0 download => 2 of 3 UDP.
	if yt.OneWayUDPFlows != 2 {
		t.Errorf("youtube oneway_udp=%d, want 2", yt.OneWayUDPFlows)
	}
	if got := yt.OneWayUDPRatio; got < 0.666 || got > 0.667 {
		t.Errorf("youtube oneway_udp_ratio=%v, want ~0.6667", got)
	}
	// only 3 UDP flows here (< rtcMinUDPFlows) => not enough to flag wedged RTC.
	if yt.WedgedOneWayRTC() {
		t.Error("3 UDP flows is below rtcMinUDPFlows; must not flag wedged RTC")
	}
	// bytes = sum upload+download over the 4 matched youtube flows.
	wantBytes := int64((1200 + 0) + (1200 + 98765) + (1200 + 0) + (500 + 40000))
	if yt.Bytes != wantBytes {
		t.Errorf("youtube bytes=%d, want %d", yt.Bytes, wantBytes)
	}

	// DestIPs: the 4 matched youtube flows carry 4 distinct parseable IPs; the
	// unmatched github flow (140.82.112.3) must NOT appear.
	gotIPs := map[string]bool{}
	for _, ip := range yt.DestIPs {
		gotIPs[ip.String()] = true
	}
	wantIPs := []string{"142.251.1.1", "142.251.1.2", "142.251.1.3", "142.251.99.4"}
	if len(yt.DestIPs) != len(wantIPs) {
		t.Errorf("youtube DestIPs=%v, want %v", yt.DestIPs, wantIPs)
	}
	for _, w := range wantIPs {
		if !gotIPs[w] {
			t.Errorf("youtube DestIPs missing %s (got %v)", w, yt.DestIPs)
		}
	}
	if gotIPs["140.82.112.3"] {
		t.Error("youtube DestIPs must not include the unmatched github flow IP")
	}

	// ru_mail is direct-only: its direct flow is NOT a leak.
	rm := snap.Services["ru_mail"]
	if rm.Flows != 1 || rm.LeakFlows != 0 || rm.LeakRatio != 0 {
		t.Errorf("ru_mail flows=%d leak=%d ratio=%v, want 1/0/0", rm.Flows, rm.LeakFlows, rm.LeakRatio)
	}
	// ru_mail's single matched flow contributes its dest IP.
	if len(rm.DestIPs) != 1 || rm.DestIPs[0].String() != "94.100.180.200" {
		t.Errorf("ru_mail DestIPs=%v, want [94.100.180.200]", rm.DestIPs)
	}
}

// TestLeakOnlyForTunnelIntended covers LOT-22: a zapret-PREFERRED service whose
// flows all go "direct" is NOT leaking (direct is its by-design path), while a
// VPN-PREFERRED service with the same direct flows IS leaking.
func TestLeakOnlyForTunnelIntended(t *testing.T) {
	// Two flows to "direct", matched by distinct domains.
	conns := []Conn{
		{Chains: []string{"direct"}, Upload: 100, Download: 200, Host: "play.epicgames.com", Network: "tcp"},
		{Chains: []string{"direct"}, Upload: 100, Download: 200, Host: "play.epicgames.com", Network: "tcp"},
	}

	// zapret-PREFERRED (with a VPN fallback further down the chain): direct is
	// by design, so it must NOT be flagged.
	zapretReg := &registry.Registry{Services: map[string]registry.Service{
		"gaming-epic": {
			Name:    "gaming-epic",
			Domains: []string{"epicgames.com"},
			Chain: []registry.ChainStep{
				{Position: 0, State: registry.StatePreferred, StrategyClass: strategy.ClassZapret},
				{Position: 1, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test_udp"},
			},
		},
	}}
	snap, err := New(fakeSource{conns}, zapretReg).Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe (zapret): %v", err)
	}
	if g := snap.Services["gaming-epic"]; g.Flows != 2 || g.LeakFlows != 0 || g.LeakRatio != 0 {
		t.Errorf("zapret-preferred gaming-epic flows=%d leak=%d ratio=%v, want 2/0/0 (direct is by design)",
			g.Flows, g.LeakFlows, g.LeakRatio)
	}

	// VPN-PREFERRED: the same direct flows ARE a leak.
	vpnReg := &registry.Registry{Services: map[string]registry.Service{
		"gaming-epic": {
			Name:    "gaming-epic",
			Domains: []string{"epicgames.com"},
			Chain: []registry.ChainStep{
				{Position: 0, State: registry.StateVPN, StrategyClass: strategy.ClassVPN, StrategyID: "vpn_url_test_udp"},
			},
		},
	}}
	snap, err = New(fakeSource{conns}, vpnReg).Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe (vpn): %v", err)
	}
	if g := snap.Services["gaming-epic"]; g.Flows != 2 || g.LeakFlows != 2 || g.LeakRatio != 1 {
		t.Errorf("vpn-preferred gaming-epic flows=%d leak=%d ratio=%v, want 2/2/1",
			g.Flows, g.LeakFlows, g.LeakRatio)
	}
}

// TestDestIPsDedup: many flows to the same edge collapse to one DestIPs entry.
func TestDestIPsDedup(t *testing.T) {
	conns := []Conn{
		{Chains: []string{"sel-youtube"}, Host: "a.googlevideo.com", DestIP: "142.251.1.1", Network: "udp"},
		{Chains: []string{"sel-youtube"}, Host: "b.googlevideo.com", DestIP: "142.251.1.1", Network: "udp"},
		{Chains: []string{"sel-youtube"}, Host: "c.googlevideo.com", DestIP: "142.251.1.2", Network: "udp"},
	}
	snap, err := New(fakeSource{conns}, testRegistry()).Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got := snap.Services["youtube"].DestIPs; len(got) != 2 {
		t.Errorf("DestIPs=%v, want 2 distinct (dedup of 142.251.1.1)", got)
	}
}

func TestMatchByDomainSuffix(t *testing.T) {
	m := matcher{suffixes: []string{"googlevideo.com"}}
	cases := []struct {
		host string
		want bool
	}{
		{"rr1---sn-x.googlevideo.com", true},
		{"googlevideo.com", true},
		{"GoogleVideo.com", true},     // case-insensitive
		{"googlevideo.com.", true},    // trailing dot
		{"notgooglevideo.com", false}, // not a label boundary
		{"googlevideo.com.evil.net", false},
		{"github.com", false},
	}
	for _, tc := range cases {
		if got := m.matchesHeuristic(Conn{Host: tc.host}); got != tc.want {
			t.Errorf("matches(host=%q)=%v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestMatchByIPCIDR(t *testing.T) {
	eye := New(fakeSource{}, testRegistry()) // builds matchers
	var ytm *matcher
	for i := range eye.matchers {
		if eye.matchers[i].svc.Name == "youtube" {
			ytm = &eye.matchers[i]
		}
	}
	if ytm == nil {
		t.Fatal("no youtube matcher")
	}
	if !ytm.matchesHeuristic(Conn{DestIP: "142.251.99.4"}) {
		t.Error("142.251.99.4 should match 142.251.99.0/24")
	}
	if ytm.matchesHeuristic(Conn{DestIP: "142.251.100.4"}) {
		t.Error("142.251.100.4 should NOT match 142.251.99.0/24")
	}
}

// LOT-20: a flow routed by a rule_set (no inline-domain/IP match) is attributed
// via sing-box's own route(sel-<svc>) decision in the rule string.
func TestMatchBySelectorRoute(t *testing.T) {
	eye := New(fakeSource{}, testRegistry())
	var ytm *matcher
	for i := range eye.matchers {
		if eye.matchers[i].svc.Name == "youtube" {
			ytm = &eye.matchers[i]
		}
	}
	if ytm == nil {
		t.Fatal("no youtube matcher")
	}
	// www.youtube.com via geosite-youtube: host NOT in inline suffixes, no DestIP —
	// the heuristic misses it, but the route target attributes it.
	c := Conn{Host: "www.youtube.com", Rule: "rule_set=geosite-youtube => route(sel-youtube)"}
	if ytm.matchesHeuristic(c) {
		t.Error("heuristic should NOT match www.youtube.com (not an inline suffix)")
	}
	if !ytm.matchesSelector(c) {
		t.Error("selector matcher must attribute the geosite-youtube flow to youtube")
	}
	// a flow routed elsewhere must not match youtube's selector.
	if ytm.matchesSelector(Conn{Rule: "rule_set=geosite-discord => route(sel-discord)"}) {
		t.Error("youtube matcher must not claim a sel-discord flow")
	}
	// empty rule (direct/final) -> no selector match.
	if ytm.matchesSelector(Conn{Rule: ""}) {
		t.Error("empty rule must not selector-match")
	}
}

// LOT-35: HasLiveRealtimeUDP is the voice/RTC signal the reconciler uses to defer a
// sing-box restart. It must report a service only when a LIVE (non-dead) UDP flow
// exists — a wedged service whose UDP flows are all dead must NOT block a recovery
// restart, and a TCP-only service must never count.
func TestHasLiveRealtimeUDP(t *testing.T) {
	tests := []struct {
		name    string
		svcs    map[string]ServiceMetrics
		wantOK  bool
		wantSvc string
	}{
		{"no services", nil, false, ""},
		{"live udp flow", map[string]ServiceMetrics{"discord": {Service: "discord", UDPFlows: 2, DeadUDPFlows: 1}}, true, "discord"},
		{"all udp dead (wedged) does not block", map[string]ServiceMetrics{"youtube": {Service: "youtube", UDPFlows: 3, DeadUDPFlows: 3}}, false, ""},
		{"tcp-only never counts", map[string]ServiceMetrics{"web": {Service: "web", Flows: 5, UDPFlows: 0}}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, ok := Snapshot{Services: tt.svcs}.HasLiveRealtimeUDP()
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tt.wantOK)
			}
			if tt.wantSvc != "" && svc != tt.wantSvc {
				t.Errorf("svc=%q, want %q", svc, tt.wantSvc)
			}
		})
	}
}

// LOT-35 #2: WedgedOneWayRTC surfaces a voice/RTC session that is sending with no
// return (torn conntrack after a restart). It requires enough UDP flows AND most of
// them one-way — a healthy or merely-idle service must not trip it. SURFACE-ONLY.
func TestWedgedOneWayRTC(t *testing.T) {
	tests := []struct {
		name string
		m    ServiceMetrics
		want bool
	}{
		{"wedged: 5 udp, 4 one-way", ServiceMetrics{UDPFlows: 5, OneWayUDPFlows: 4, OneWayUDPRatio: 0.8}, true},
		{"too few udp flows", ServiceMetrics{UDPFlows: 3, OneWayUDPFlows: 3, OneWayUDPRatio: 1.0}, false},
		{"enough flows but mostly two-way", ServiceMetrics{UDPFlows: 6, OneWayUDPFlows: 2, OneWayUDPRatio: 0.33}, false},
		{"exactly at ratio threshold (not over)", ServiceMetrics{UDPFlows: 6, OneWayUDPFlows: 3, OneWayUDPRatio: 0.5}, false},
		{"no udp at all", ServiceMetrics{Flows: 10, UDPFlows: 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.WedgedOneWayRTC(); got != tt.want {
				t.Errorf("WedgedOneWayRTC()=%v, want %v", got, tt.want)
			}
		})
	}
}
