// Package prospect accumulates desync strategies this box has proven for itself.
//
// The tuner can find a working strategy in a sandbox, but a single win is thin
// evidence: DPI behaviour drifts through the day, and one measurement taken in
// one minute can be luck. So a find is not a recipe yet — it is a candidate that
// has won once. Only a SECOND win, in a separate pass, promotes it to something
// the composer may use.
//
// The point is independence. Today every recipe comes from a bundle somebody else
// maintains; if that bundle stops being updated, or its author's assumptions stop
// matching this line, the box has nothing of its own. A store that grows a few
// self-proven strategies a week is worth more than a large catalog that was true
// somewhere else.
package prospect

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/strace-me/lotsman/pkg/strategycat"
)

// Finding is one strategy this box has measured for itself.
type Finding struct {
	ID    string   `json:"id"`
	Args  []string `json:"args"`
	Wins  int      `json:"wins"`
	Fails int      `json:"fails"`
	// Services the wins were measured against. A strategy proven twice on the
	// same service is weaker evidence than one proven on two, and a later reader
	// should be able to tell them apart.
	Services  []string  `json:"services,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastWin   time.Time `json:"last_win,omitempty"`
	// Seed is the axis map the strategy came from, kept so a later search can
	// start NEXT TO a proven point instead of from a cold grid. Rendered args
	// cannot be turned back into axes, so if this is not stored when the
	// strategy is found it is gone.
	Seed map[string]string `json:"seed,omitempty"`
	// Demoted is when the finding fell out of the pool. Kept because several
	// findings falling out TOGETHER says something none of them says alone.
	Demoted time.Time `json:"demoted,omitempty"`
}

// Confirmed reports whether the finding has earned a place in the pool.
func (f Finding) Confirmed() bool { return f.Wins >= confirmAt && f.Fails < demoteAt }

const (
	// A single win can be luck: DPI behaviour drifts, and one measurement in one
	// minute proves little. Two, in separate passes, is the cheapest evidence
	// that is not a coincidence.
	confirmAt = 2
	// Losing this many times after confirmation drops it back out of the pool —
	// what worked in June is routinely dead by August.
	demoteAt = 3
)

// Store is the persisted set of findings.
type Store struct {
	path string
	mu   sync.Mutex
	all  map[string]*Finding
	now  func() time.Time
}

// Open loads the store, or starts an empty one when the file does not exist yet.
// A corrupt file is an error rather than a silent reset: quietly discarding weeks
// of accumulated evidence is worse than refusing to start.
func Open(path string, now func() time.Time) (*Store, error) {
	s := &Store{path: path, all: map[string]*Finding{}, now: now}
	if s.now == nil {
		s.now = time.Now
	}
	if path == "" {
		return s, nil
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Finding
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("prospect: %s is corrupt (%w); move it aside to start fresh", path, err)
	}
	for _, f := range list {
		s.all[f.ID] = f
	}
	return s, nil
}

// Won records that a strategy carried the service in a measured pass.
func (s *Store) Won(id string, args []string, service string, seed map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.all[id]
	if f == nil {
		f = &Finding{ID: id, Args: append([]string(nil), args...), FirstSeen: s.now()}
		s.all[id] = f
	}
	if f.Seed == nil && len(seed) > 0 {
		f.Seed = maps.Clone(seed)
	}
	f.Demoted = time.Time{} // it is carrying again
	f.Wins++
	f.LastWin = s.now()
	if !contains(f.Services, service) {
		f.Services = append(f.Services, service)
		sort.Strings(f.Services)
	}
}

// Lost records that a previously found strategy did not carry the service.
func (s *Store) Lost(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.all[id]
	if f == nil {
		return
	}
	was := f.Confirmed()
	f.Fails++
	if was && !f.Confirmed() {
		f.Demoted = s.now()
	}
}

// StableSeed returns the axis map of the strategy with the strongest record:
// most wins, oldest tenure breaking ties. It is what a search should start next
// to, because the neighbourhood of something that has held for weeks is a better
// place to look than a cold grid — and it costs no probes to consult.
func (s *Store) StableSeed() (map[string]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Finding
	for _, f := range s.all {
		if !f.Confirmed() || len(f.Seed) == 0 {
			continue
		}
		if best == nil || f.Wins > best.Wins ||
			(f.Wins == best.Wins && f.FirstSeen.Before(best.FirstSeen)) {
			best = f
		}
	}
	if best == nil {
		return nil, false
	}
	return maps.Clone(best.Seed), true
}

// Collapsed reports whether at least min confirmed findings fell out of the pool
// within window.
//
// Independent strategies do not die together by chance. When several do, the
// thing that changed is upstream — the DPI moved — and the accumulated negative
// evidence describes a network that no longer exists. That is worth more than a
// calendar threshold, which is always wrong in one direction: too short and it
// re-tests what is still dead, spending the one resource that actually costs
// (handshakes to a watched SNI); too long and the box stays blind after a change.
//
// The CALLER must gate this on the link being otherwise healthy. A dead uplink,
// a changed exit IP or a lost WAN also fails everything at once, and a detector
// that cannot tell those apart would erase months of evidence during a
// five-minute outage — quietly, while reporting that it worked as designed.
func (s *Store) Collapsed(now time.Time, window time.Duration, min int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, f := range s.all {
		if !f.Demoted.IsZero() && now.Sub(f.Demoted) <= window {
			n++
		}
	}
	return n >= min
}

// Rearm forgets the failures. Called after a collapse: the losses were recorded
// against a network that has since changed, so keeping them would rule out
// strategies for reasons that no longer apply. Wins are kept — a strategy that
// once carried traffic is still a better lead than one that never did.
func (s *Store) Rearm() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, f := range s.all {
		if f.Fails > 0 {
			f.Fails = 0
			f.Demoted = time.Time{}
			n++
		}
	}
	return n
}

// Recipes returns the CONFIRMED findings as catalog recipes, ready to merge into
// the composer's pool. Unconfirmed ones are deliberately withheld: a strategy
// that has won once is a lead, and putting leads into production is how an
// unverified strategy reaches the household.
func (s *Store) Recipes() []strategycat.Recipe {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.all))
	for id, f := range s.all {
		if f.Confirmed() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids) // deterministic: the composer diffs its argv
	out := make([]strategycat.Recipe, 0, len(ids))
	for _, id := range ids {
		f := s.all[id]
		out = append(out, strategycat.Recipe{
			ID:         "self-" + id,
			Provenance: fmt.Sprintf("proven on this box (%d wins: %s)", f.Wins, strings.Join(f.Services, ", ")),
			NfqwsArgs:  append([]string(nil), f.Args...),
			Notes:      "discovered by the tuner, confirmed by a second pass",
		})
	}
	return out
}

// Leads are findings that have won but are not confirmed yet. They must be
// re-tested DELIBERATELY: a discovery pass generates its candidates fresh each
// time, so a lead that is never offered again never earns its second win and the
// store fills with strategies that are permanently one measurement short.
func (s *Store) Leads() []Finding {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Finding
	for _, f := range s.all {
		if !f.Confirmed() && f.Fails < demoteAt {
			out = append(out, *f)
		}
	}
	// Oldest first: a lead that has waited longest is the one whose evidence is
	// going stalest.
	sort.Slice(out, func(i, j int) bool { return out[i].LastWin.Before(out[j].LastWin) })
	return out
}

// Pending is the leads: found once, not yet confirmed. For logs and the UI.
func (s *Store) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, f := range s.all {
		if !f.Confirmed() && f.Fails < demoteAt {
			n++
		}
	}
	return n
}

// Save writes the store atomically. Callers save after each pass rather than at
// shutdown: the value here accumulates over weeks, and a box that loses power
// must not lose the month.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	list := make([]*Finding, 0, len(s.all))
	for _, f := range s.all {
		list = append(list, f)
	}
	s.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })

	body, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func contains(hay []string, s string) bool {
	for _, h := range hay {
		if h == s {
			return true
		}
	}
	return false
}
