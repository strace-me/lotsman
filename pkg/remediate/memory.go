package remediate

import (
	"encoding/json"
	"os"
	"sync"
)

// Memory is the remediation knowledge base (LOT-19): it remembers, per
// (service, failure-class, action), how often that remediation RESOLVED the
// misroute vs how often it had to be rolled back. The controller records an
// outcome after each canary; the planner consults Best to jump straight to a
// known-working remediation instead of climbing the ladder from rung 1.
//
// failure-class is a misroute.Verdict.Kind ("leak" | "dead"); action is one of
// the Action* constants. The class/action are plain strings so this stays
// import-free of misroute and decoupled from the planner's rung order.
//
// This is the self-contained unit; wiring it into the live armed controller
// (record after stepCanary, consult before Decide) changes armed behavior and is
// done separately.
type Memory struct {
	mu sync.Mutex
	m  map[memKey]memOutcome
}

type memKey struct{ service, class, action string }

type memOutcome struct{ success, failure int }

// NewMemory builds an empty remediation memory.
func NewMemory() *Memory {
	return &Memory{m: map[memKey]memOutcome{}}
}

// Record folds one applied-remediation outcome into the memory: ok=true when the
// remediation resolved the misroute (canary recovered), ok=false when it was
// rolled back without recovery. A "none"/empty action or class is ignored — only
// real remediations carry signal.
func (m *Memory) Record(service, class, action string, ok bool) {
	if service == "" || class == "" || action == "" || action == ActionNone {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey{service, class, action}
	o := m.m[k]
	if ok {
		o.success++
	} else {
		o.failure++
	}
	m.m[k] = o
}

// Best returns the action most likely to fix a (service, class) misroute, and
// whether one is known with confidence. It first looks for a service-specific
// winner; failing that it GENERALIZES across services by failure-class (LOT-19:
// "a remediation that worked for one service is tried first on another with the
// same failure"). An action qualifies only when it has resolved at least once
// and resolved more often than it was rolled back. Ties break on net wins.
func (m *Memory) Best(service, class string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.bestAmongLocked(func(k memKey) bool { return k.service == service && k.class == class }); ok {
		return a, true
	}
	return m.bestAmongLocked(func(k memKey) bool { return k.class == class })
}

// bestAmongLocked aggregates outcomes per action across the keys matching pred
// and returns the confidently-best action. Caller holds m.mu.
func (m *Memory) bestAmongLocked(pred func(memKey) bool) (string, bool) {
	agg := map[string]memOutcome{}
	for k, o := range m.m {
		if !pred(k) {
			continue
		}
		cur := agg[k.action]
		cur.success += o.success
		cur.failure += o.failure
		agg[k.action] = cur
	}
	bestAction, bestNet, found := "", 0, false
	for a, o := range agg {
		if o.success == 0 || o.success <= o.failure {
			continue // not confidently good
		}
		net := o.success - o.failure
		if !found || net > bestNet {
			bestAction, bestNet, found = a, net, true
		}
	}
	return bestAction, found
}

// memRecord is the persisted form (a JSON object map can't key on a struct).
type memRecord struct {
	Service string `json:"service"`
	Class   string `json:"class"`
	Action  string `json:"action"`
	Success int    `json:"success"`
	Failure int    `json:"failure"`
}

// Save writes the memory to path atomically (temp + rename), mirroring kb.Save —
// so learned remediations survive a restart.
func (m *Memory) Save(path string) error {
	m.mu.Lock()
	recs := make([]memRecord, 0, len(m.m))
	for k, o := range m.m {
		recs = append(recs, memRecord{k.service, k.class, k.action, o.success, o.failure})
	}
	m.mu.Unlock()

	data, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load merges saved records into the memory. A missing file is a cold start (nil,
// not an error); a corrupt file returns an error so the operator notices.
func (m *Memory) Load(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var recs []memRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range recs {
		m.m[memKey{r.Service, r.Class, r.Action}] = memOutcome{r.Success, r.Failure}
	}
	return nil
}
