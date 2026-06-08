package misroute

import (
	"sort"
	"testing"

	"github.com/strace-me/lotsman/pkg/observe"
)

// snap builds an observe.Snapshot from the given per-service metrics, deriving
// the ratios the way observe.Eye.Observe does so fixtures stay self-consistent.
func snap(metrics ...observe.ServiceMetrics) observe.Snapshot {
	s := observe.Snapshot{Services: map[string]observe.ServiceMetrics{}}
	for _, m := range metrics {
		if m.Flows > 0 {
			m.LeakRatio = float64(m.LeakFlows) / float64(m.Flows)
		}
		if m.UDPFlows > 0 {
			m.DeadFlowRatio = float64(m.DeadUDPFlows) / float64(m.UDPFlows)
		}
		s.Services[m.Service] = m
	}
	return s
}

// verdictFor finds the verdict for a service in an (unordered) Detect result.
func verdictFor(t *testing.T, vs []Verdict, service string) Verdict {
	t.Helper()
	for _, v := range vs {
		if v.Service == service {
			return v
		}
	}
	t.Fatalf("no verdict for service %q", service)
	return Verdict{}
}

func TestDetect_HighLeak_IsMisroutedLeak(t *testing.T) {
	// 4 flows, 3 leaked → ratio 0.75 > 0.3, Flows 4 >= MinFlows 4.
	s := snap(observe.ServiceMetrics{Service: "youtube", Flows: 4, LeakFlows: 3})
	v := verdictFor(t, Detect(s, DefaultConfig()), "youtube")

	if !v.Misrouted || v.Kind != KindLeak {
		t.Fatalf("want misrouted leak, got misrouted=%v kind=%q", v.Misrouted, v.Kind)
	}
	if v.LeakRatio != 0.75 {
		t.Fatalf("LeakRatio = %v, want 0.75", v.LeakRatio)
	}
	if v.Reason == "" {
		t.Fatal("expected a non-empty Reason")
	}
}

func TestDetect_HighDead_IsMisroutedDead(t *testing.T) {
	// No leaks; 4 UDP flows, 3 dead (all QUIC/udp443) → ratio 0.75 > 0.5, UDPFlows 4 >= MinUDPFlows 4.
	s := snap(observe.ServiceMetrics{Service: "meet", Flows: 4, UDPFlows: 4, DeadUDPFlows: 3, DeadQUICFlows: 3})
	v := verdictFor(t, Detect(s, DefaultConfig()), "meet")

	if !v.Misrouted || v.Kind != KindDead {
		t.Fatalf("want misrouted dead, got misrouted=%v kind=%q", v.Misrouted, v.Kind)
	}
	if v.DeadFlowRatio != 0.75 {
		t.Fatalf("DeadFlowRatio = %v, want 0.75", v.DeadFlowRatio)
	}
}

// LOT-35: a dead-ratio made up ENTIRELY of one-way voice/RTC flows (no dead QUIC
// on udp/443) must NOT fire a dead verdict — reject-quic can't fix voice one-way,
// and the condition self-heals on the client's renegotiation. This is the discord
// reject-quic flap fix.
func TestDetect_OneWayVoice_NotDead(t *testing.T) {
	// 4 UDP voice flows, 3 dead one-way, but DeadQUICFlows==0 (none on udp/443).
	s := snap(observe.ServiceMetrics{Service: "discord", Flows: 4, UDPFlows: 4, DeadUDPFlows: 3, DeadQUICFlows: 0})
	v := verdictFor(t, Detect(s, DefaultConfig()), "discord")
	if v.Misrouted || v.Kind != KindNone {
		t.Fatalf("voice one-way (no dead QUIC) should NOT be dead-misrouted: misrouted=%v kind=%q", v.Misrouted, v.Kind)
	}
}

func TestDetect_Healthy_NotMisrouted(t *testing.T) {
	// Plenty of flows, no leaks, all UDP flows alive.
	s := snap(observe.ServiceMetrics{Service: "netflix", Flows: 10, UDPFlows: 6, DeadUDPFlows: 0})
	v := verdictFor(t, Detect(s, DefaultConfig()), "netflix")

	if v.Misrouted || v.Kind != KindNone {
		t.Fatalf("healthy service flagged: misrouted=%v kind=%q", v.Misrouted, v.Kind)
	}
}

func TestDetect_BelowMinFlows_NotMisrouted(t *testing.T) {
	// 2 flows, both leaked → ratio 1.0 but Flows 2 < MinFlows 4: too small a
	// sample to trust. Same for a single dead UDP flow.
	s := snap(
		observe.ServiceMetrics{Service: "youtube", Flows: 2, LeakFlows: 2},
		observe.ServiceMetrics{Service: "meet", Flows: 1, UDPFlows: 1, DeadUDPFlows: 1},
	)
	vs := Detect(s, DefaultConfig())

	if v := verdictFor(t, vs, "youtube"); v.Misrouted {
		t.Fatalf("youtube below MinFlows flagged: %+v", v)
	}
	if v := verdictFor(t, vs, "meet"); v.Misrouted {
		t.Fatalf("meet below MinUDPFlows flagged: %+v", v)
	}
}

func TestDetect_DirectOnlyStyle_ZeroLeak_NotMisrouted(t *testing.T) {
	// A direct-only service never accrues LeakFlows in observe, so it presents
	// as many flows with LeakRatio 0 — must not be flagged.
	s := snap(observe.ServiceMetrics{Service: "ru_direct", Flows: 12, LeakFlows: 0, UDPFlows: 5, DeadUDPFlows: 0})
	v := verdictFor(t, Detect(s, DefaultConfig()), "ru_direct")

	if v.Misrouted {
		t.Fatalf("direct-only-style service flagged: %+v", v)
	}
}

func TestDetect_LeakWinsOverDead(t *testing.T) {
	// Both signals fire; leak is the more actionable verdict and should win.
	s := snap(observe.ServiceMetrics{
		Service: "youtube", Flows: 6, LeakFlows: 4, UDPFlows: 6, DeadUDPFlows: 5,
	})
	v := verdictFor(t, Detect(s, DefaultConfig()), "youtube")

	if !v.Misrouted || v.Kind != KindLeak {
		t.Fatalf("want leak to win, got misrouted=%v kind=%q", v.Misrouted, v.Kind)
	}
}

func TestDetect_OnePerService(t *testing.T) {
	s := snap(
		observe.ServiceMetrics{Service: "a", Flows: 4, LeakFlows: 4},
		observe.ServiceMetrics{Service: "b", Flows: 10},
		observe.ServiceMetrics{Service: "c", Flows: 4, UDPFlows: 4, DeadUDPFlows: 4},
	)
	vs := Detect(s, DefaultConfig())
	if len(vs) != 3 {
		t.Fatalf("got %d verdicts, want 3", len(vs))
	}
	got := []string{}
	for _, v := range vs {
		got = append(got, v.Service)
	}
	sort.Strings(got)
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("services = %v, want %v", got, want)
		}
	}
}

func TestDetect_EmptySnapshot(t *testing.T) {
	if vs := Detect(observe.Snapshot{}, DefaultConfig()); len(vs) != 0 {
		t.Fatalf("empty snapshot → %d verdicts, want 0", len(vs))
	}
}
