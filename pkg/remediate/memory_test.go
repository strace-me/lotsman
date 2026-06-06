package remediate

import (
	"path/filepath"
	"testing"

	"github.com/strace-me/lotsman/pkg/misroute"
)

// A confidently-good action (resolved more than rolled back) is returned for the
// same (service, class); a noisy one (more failures than successes) is not.
func TestMemoryBestServiceSpecific(t *testing.T) {
	m := NewMemory()
	// ip-fallback resolved youtube|leak twice, rolled back once -> net +1, good.
	m.Record("youtube", misroute.KindLeak, ActionIPFallback, true)
	m.Record("youtube", misroute.KindLeak, ActionIPFallback, true)
	m.Record("youtube", misroute.KindLeak, ActionIPFallback, false)
	// reject-quic was net-negative for youtube|leak -> not a winner.
	m.Record("youtube", misroute.KindLeak, ActionRejectQUIC, false)

	if a, ok := m.Best("youtube", misroute.KindLeak); !ok || a != ActionIPFallback {
		t.Errorf("Best(youtube,leak) = (%q,%v), want (ip-fallback,true)", a, ok)
	}
	// No record for the dead class -> nothing known.
	if a, ok := m.Best("youtube", misroute.KindDead); ok {
		t.Errorf("Best(youtube,dead) = (%q,%v), want no known action", a, ok)
	}
}

// With no service-specific record, Best generalizes across services by class.
func TestMemoryBestGeneralizesAcrossServices(t *testing.T) {
	m := NewMemory()
	// reject-quic resolved the "dead" class on discord; youtube has no dead record.
	m.Record("discord", misroute.KindDead, ActionRejectQUIC, true)
	m.Record("discord", misroute.KindDead, ActionRejectQUIC, true)

	a, ok := m.Best("youtube", misroute.KindDead)
	if !ok || a != ActionRejectQUIC {
		t.Errorf("cross-service Best(youtube,dead) = (%q,%v), want (reject-quic,true)", a, ok)
	}
}

// A purely-failing remediation is never recommended.
func TestMemoryRejectsNetNegative(t *testing.T) {
	m := NewMemory()
	m.Record("youtube", misroute.KindLeak, ActionRejectQUIC, false)
	m.Record("youtube", misroute.KindLeak, ActionRejectQUIC, false)
	if a, ok := m.Best("youtube", misroute.KindLeak); ok {
		t.Errorf("net-negative action must not be recommended, got (%q,%v)", a, ok)
	}
}

// Empty/none inputs carry no signal and are ignored.
func TestMemoryIgnoresEmptyAndNone(t *testing.T) {
	m := NewMemory()
	m.Record("", misroute.KindLeak, ActionIPFallback, true)
	m.Record("youtube", "", ActionIPFallback, true)
	m.Record("youtube", misroute.KindLeak, ActionNone, true)
	if _, ok := m.Best("youtube", misroute.KindLeak); ok {
		t.Error("empty/none records must not produce a recommendation")
	}
}

// Save/Load round-trips the learned outcomes.
func TestMemorySaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remmem.json")
	m := NewMemory()
	m.Record("youtube", misroute.KindLeak, ActionIPFallback, true)
	m.Record("youtube", misroute.KindLeak, ActionIPFallback, true)
	if err := m.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	m2 := NewMemory()
	if err := m2.Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if a, ok := m2.Best("youtube", misroute.KindLeak); !ok || a != ActionIPFallback {
		t.Errorf("after load Best = (%q,%v), want (ip-fallback,true)", a, ok)
	}

	// Loading a missing file is a cold start, not an error.
	if err := NewMemory().Load(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Errorf("missing file must be a cold start, got %v", err)
	}
}
