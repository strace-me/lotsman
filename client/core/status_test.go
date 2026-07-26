package core

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/strace-me/lotsman/client/platform/netid"
	"github.com/strace-me/lotsman/pkg/subscription"
)

func TestVerdict(t *testing.T) {
	tests := []struct {
		name     string
		running  bool
		services []NodeStatus
		want     string
		working  int
		failing  int
		broken   int
	}{
		{"down when the data plane is not live", false, []NodeStatus{{}}, "down", 1, 0, 0},
		{"working when nothing is failing or broken", true, []NodeStatus{{}, {}}, "working", 2, 0, 0},
		{"working with no services", true, nil, "working", 0, 0, 0},
		{"a service failing at its rung is not counted working", true, []NodeStatus{{}, {Fails: 2}}, "partial", 1, 1, 0},
		{"partial when some are broken", true, []NodeStatus{{}, {Broken: true}, {}}, "partial", 2, 0, 1},
		{"not-working when all are failing or broken", true, []NodeStatus{{Fails: 1}, {Broken: true}}, "not-working", 0, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := verdict(tt.running, tt.services)
			if v.State != tt.want {
				t.Errorf("state = %q, want %q", v.State, tt.want)
			}
			if v.Working != tt.working || v.Failing != tt.failing || v.Broken != tt.broken {
				t.Errorf("working/failing/broken = %d/%d/%d, want %d/%d/%d",
					v.Working, v.Failing, v.Broken, tt.working, tt.failing, tt.broken)
			}
		})
	}
}

func TestNetworkInfoReportsRoamingWhileASwapIsPending(t *testing.T) {
	c := &Core{currentNet: netid.Network{Id: "abc", Kind: "wifi", IFace: "wlan0"}}
	if got := c.networkInfo(); got.ID != "abc" || got.Kind != "wifi" || got.Roaming {
		t.Fatalf("steady state: %+v", got)
	}
	c.pendingCount = 1
	if got := c.networkInfo(); !got.Roaming {
		t.Error("a pending network change must read as roaming")
	}
}

func TestSubStatusesMapsQuotaAndSortsByName(t *testing.T) {
	exp := time.Now().Add(48 * time.Hour)
	c := &Core{subInfo: map[string]subscription.Userinfo{
		"b-sub": {Upload: 1, Download: 2, Total: 100, Expire: exp},
		"a-sub": {}, // unknown quota, no expiry
	}}
	got := c.subStatuses()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "a-sub" || got[1].Name != "b-sub" {
		t.Errorf("not sorted by name: %q, %q", got[0].Name, got[1].Name)
	}
	if got[0].FractionUsed != -1 {
		t.Errorf("unknown total must give fractionUsed -1, got %v", got[0].FractionUsed)
	}
	if got[1].UsedBytes != 3 || got[1].TotalBytes != 100 {
		t.Errorf("used/total = %d/%d, want 3/100", got[1].UsedBytes, got[1].TotalBytes)
	}
	if got[1].DaysUntilExpire <= 0 {
		t.Errorf("expiry in 48h must give positive days, got %v", got[1].DaysUntilExpire)
	}
}

func TestReportJSONIsABackwardCompatibleSuperset(t *testing.T) {
	// The original {running, services} paths must survive so an old tray still reads.
	b, err := json.Marshal(Report{Running: true, Services: []NodeStatus{{Service: "youtube", State: "VPN"}}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"running", "services", "verdict", "network", "engines", "fleet"} {
		if _, ok := m[k]; !ok {
			t.Errorf("rich /status is missing %q", k)
		}
	}
	// And the old subset still decodes cleanly from the rich payload.
	var legacy Status
	if err := json.Unmarshal(b, &legacy); err != nil {
		t.Fatalf("legacy decode: %v", err)
	}
	if !legacy.Running || len(legacy.Services) != 1 {
		t.Errorf("legacy view lost data: %+v", legacy)
	}
}
