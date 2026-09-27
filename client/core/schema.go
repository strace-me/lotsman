package core

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/strace-me/lotsman/pkg/configv2"
	"github.com/strace-me/lotsman/pkg/strategycat"
)

// handleSchema serves the v2 schema/metadata document.
//
// The window and the tray are unprivileged and read NOTHING from disk — they talk
// to this socket and render /status. So a metadata document living in a library is
// a document the UI cannot reach, and "one source of truth for the enums" stops
// being true the moment a UI needs it: the alternative is a hand-maintained copy in
// the frontend, which is the drift this exists to prevent.
//
// It is a GET, it touches no file and mutates nothing, so it is available whether or
// not a v2 config is loaded. A box on the v1 config still gets the enums, the chain
// actions and the strategy catalogue, which is exactly what a UI needs to render
// an editor before the config is migrated.
func (s *ControlServer) handleSchema(w http.ResponseWriter, r *http.Request) {
	// The pools and desync instances come from whatever config is loaded, so a UI
	// shows the names THIS box actually has. With no config at all the document is
	// still complete: enums, chain actions and the catalogue are static.
	metadata := configv2.BuildMetadata(nil, nil, s.poolNames(), strategyInfo())
	metadata.Desync = s.desyncInstances()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(metadata); err != nil {
		s.log.Warn("control: could not encode schema metadata", "err", err)
	}
}

// strategyInfo describes every catalogued strategy, tagged with the engine it is
// written for. The engine is the part a UI most needs and most easily guesses wrong:
// nfqws1 and nfqws2 share a catalogue entry name space, and offering an nfqws2
// recipe on a box running nfqws1 yields a strategy that desyncs nothing.
// poolNames are the pools THIS box declares, so a UI offers the real ones instead
// of a hand-typed list that drifts from the config.
func (s *ControlServer) poolNames() []string {
	conf := s.core.conf
	if conf == nil {
		return nil
	}
	if conf.Pools == nil {
		return nil
	}
	out := make([]string, 0, len(conf.Pools.Pools))
	for name := range conf.Pools.Pools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// desyncInstances reports the running instances, which is what a UI needs to show
// a strategy picker: the engine decides which strategies are valid for a rule, and
// a picker that ignores it offers recipes the box cannot run.
func (s *ControlServer) desyncInstances() map[string]configv2.Instance {
	conf := s.core.conf
	out := map[string]configv2.Instance{}
	if conf == nil {
		return out
	}
	engine := s.core.opts.DesyncEngine
	if engine == "" {
		engine = "nfqws1"
	}
	first := true
	for _, in := range conf.Zapret {
		name := in.Name
		if first {
			name = engine
			first = false
		}
		out[name] = configv2.Instance{Engine: engine, Enabled: true}
	}
	return out
}

func strategyInfo() []configv2.StrategyInfo {
	var out []configv2.StrategyInfo
	for _, r := range strategycat.Load() {
		out = append(out, configv2.StrategyInfo{
			Instance: r.EngineOf(),
			Engine:   r.EngineOf(),
			Strategy: r.ID,
			Revision: r.MinEngine,
		})
	}
	return out
}
