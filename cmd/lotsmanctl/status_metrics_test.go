package main

import "testing"

// sampleMetrics mirrors the daemon's /metrics output (pkg/metrics): labels are
// %q-quoted, gauges interleaved with HELP/TYPE comments, ratios as f-formatted
// floats. Covers three services with different shapes: discord (broken +
// misrouted), youtube (dead flows, no misroute), and telegram (clean).
const sampleMetrics = `# HELP lotsman_service_position Current chain position per service.
# TYPE lotsman_service_position gauge
lotsman_service_position{service="discord",state="degraded"} 2
lotsman_service_position{service="telegram",state="healthy"} 0
lotsman_service_position{service="youtube",state="probing"} 1
# HELP lotsman_service_broken Whether a service has exhausted its chain (1) or not (0).
# TYPE lotsman_service_broken gauge
lotsman_service_broken{service="discord"} 1
lotsman_service_broken{service="telegram"} 0
lotsman_service_broken{service="youtube"} 0
# HELP lotsman_active_fails Consecutive active-probe failures for the current strategy.
# TYPE lotsman_active_fails gauge
lotsman_active_fails{service="discord"} 5
lotsman_active_fails{service="telegram"} 0
lotsman_active_fails{service="youtube"} 2
# HELP lotsman_service_leak_ratio leak
# TYPE lotsman_service_leak_ratio gauge
lotsman_service_leak_ratio{service="discord"} 0.2500
lotsman_service_leak_ratio{service="telegram"} 0.0000
lotsman_service_leak_ratio{service="youtube"} 0.0000
# HELP lotsman_service_dead_flow_ratio dead
# TYPE lotsman_service_dead_flow_ratio gauge
lotsman_service_dead_flow_ratio{service="discord"} 0.0000
lotsman_service_dead_flow_ratio{service="telegram"} 0.0000
lotsman_service_dead_flow_ratio{service="youtube"} 0.7500
# HELP lotsman_service_flows flows
# TYPE lotsman_service_flows gauge
lotsman_service_flows{service="discord"} 12
lotsman_service_flows{service="telegram"} 3
lotsman_service_flows{service="youtube"} 8
# HELP lotsman_service_misrouted misrouted
# TYPE lotsman_service_misrouted gauge
lotsman_service_misrouted{service="discord",kind="leak"} 1
lotsman_service_misrouted{service="telegram",kind="leak"} 0
lotsman_service_misrouted{service="youtube",kind="dead_flow"} 0
# HELP lotsman_strategy_ewma ewma
# TYPE lotsman_strategy_ewma gauge
lotsman_strategy_ewma{service="discord",strategy="fake-tls"} 0.3200
`

func TestParseMetrics(t *testing.T) {
	got := parseMetrics(sampleMetrics)

	if len(got) != 3 {
		t.Fatalf("expected 3 services, got %d: %v", len(got), keysOf(got))
	}

	discord := got["discord"]
	if discord == nil {
		t.Fatal("discord missing")
	}
	if discord.State != "degraded" || discord.Position != 2 {
		t.Errorf("discord state/pos = %q/%d, want degraded/2", discord.State, discord.Position)
	}
	if !discord.Broken {
		t.Error("discord should be broken")
	}
	if discord.Fails != 5 {
		t.Errorf("discord fails = %d, want 5", discord.Fails)
	}
	if discord.LeakRatio != 0.25 {
		t.Errorf("discord leak = %v, want 0.25", discord.LeakRatio)
	}
	if discord.Flows != 12 {
		t.Errorf("discord flows = %d, want 12", discord.Flows)
	}
	if !discord.Misrouted || discord.MisrouteKind != "leak" {
		t.Errorf("discord misrouted=%v kind=%q, want true/leak", discord.Misrouted, discord.MisrouteKind)
	}

	youtube := got["youtube"]
	if youtube == nil {
		t.Fatal("youtube missing")
	}
	if youtube.State != "probing" || youtube.Position != 1 {
		t.Errorf("youtube state/pos = %q/%d, want probing/1", youtube.State, youtube.Position)
	}
	if youtube.Broken {
		t.Error("youtube should not be broken")
	}
	if youtube.DeadFlowRatio != 0.75 {
		t.Errorf("youtube dead = %v, want 0.75", youtube.DeadFlowRatio)
	}
	if youtube.Misrouted {
		t.Error("youtube misrouted should be false (gauge was 0)")
	}

	telegram := got["telegram"]
	if telegram == nil {
		t.Fatal("telegram missing")
	}
	if telegram.State != "healthy" || telegram.Broken || telegram.Misrouted || telegram.Fails != 0 {
		t.Errorf("telegram not clean: %+v", telegram)
	}
}

func TestParseMetricsEmpty(t *testing.T) {
	if got := parseMetrics(""); len(got) != 0 {
		t.Errorf("empty input should yield no services, got %v", keysOf(got))
	}
	// Comment-only / malformed lines must not produce phantom services.
	junk := "# HELP foo bar\nnot_a_sample_line\nlotsman_service_flows{nolabel=\"x\"} 1\n"
	if got := parseMetrics(junk); len(got) != 0 {
		t.Errorf("junk input should yield no services, got %v", keysOf(got))
	}
}

func keysOf(m map[string]*serviceStatus) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
