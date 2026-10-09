package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/configv2"
	"gopkg.in/yaml.v3"
)

// A deployment is configuration.yaml plus one file per rule, and the editor's job is
// to make that a single thing to work with. It does NOT pretend it is one file: a
// save carries the document AND every rule, each with the filename it belongs under,
// so the UI never has to invent paths and the service never has to guess which rule a
// structural edit was meant to touch.
//
// The flow is stage-then-swap. A candidate is materialised into a scratch directory
// and loaded by the ONE reader (`config.LoadV2`), so a config is validated exactly as
// it would be at start — same schema, same reference checks, same projections — and a
// candidate that fails never touches a live file. The alternative, validating in
// memory, is how a config comes to validate in the editor and be refused by the
// daemon: two readers, each green in its own tests.

// configDoc is a whole deployment as the GUI works with it.
type configDoc struct {
	Path     string `json:"path"`
	RulesDir string `json:"rules_dir,omitempty"`
	// YAML is the document's raw text. It is the ESCAPE HATCH: present alone it means
	// "save exactly this", which is the way a hand-kept file survives a UI that does
	// not know about it.
	YAML   string                  `json:"yaml,omitempty"`
	Config *configv2.Configuration `json:"config,omitempty"`
	Rules  []configRule            `json:"rules,omitempty"`
}

type configRule struct {
	File string         `json:"file"`
	YAML string         `json:"yaml,omitempty"`
	Rule *configv2.Rule `json:"rule,omitempty"`
}

// filenameForRule is the rule's file name, derived from its id so the two cannot
// disagree: a document is readable by listing the directory and matching id to file.
func filenameForRule(r configv2.Rule) string {
	name := config.Slug(r.ID)
	if name == "" {
		name = "rule"
	}
	return name + ".yaml"
}

func readConfigDoc(r *http.Request) (configDoc, error) {
	var in configDoc
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&in); err != nil {
		return configDoc{}, fmt.Errorf("bad request: %w", err)
	}
	return in, nil
}

// handleGetConfig returns the document and every rule, both raw and structured. The
// structured view is best-effort per file: a rule the UI cannot parse still comes back
// as raw YAML so it can be repaired by hand instead of becoming invisible.
func (s *ControlServer) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	if s.configPath == "" {
		http.Error(w, "in-app config editing is not enabled on this instance", http.StatusNotImplemented)
		return
	}
	rulesDir := config.RulesDirFor(s.configPath, s.rulesDir)
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		http.Error(w, "read config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := configDoc{Path: s.configPath, RulesDir: rulesDir, YAML: string(data)}
	if cfg, derr := configv2.Parse(data); derr == nil {
		out.Config = cfg
	}
	out.Rules = readRulesDir(rulesDir)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func readRulesDir(dir string) []configRule {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // no rules yet is a normal state, not an error
	}
	var out []configRule
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		item := configRule{File: e.Name(), YAML: string(data)}
		if rule, rerr := configv2.ParseRule(data); rerr == nil {
			item.Rule = rule
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// staged is a candidate deployment on disk, plus the runtime projection of it.
type staged struct {
	dir  string
	cfg  *config.Config
	load *config.V2Load
}

// stage materialises a candidate into a scratch directory and loads it with the one
// reader. Nothing live is touched; on failure the scratch tree is removed and the live
// deployment is exactly as it was.
func stage(in configDoc, storeRoot, liveRulesDir string) (*staged, error) {
	dir, err := os.MkdirTemp("", "lotsman-config-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }

	carryLiveRules(&in, liveRulesDir)
	if err := writeCandidate(dir, in); err != nil {
		cleanup()
		return nil, err
	}
	// The bundle store is read from its LIVE location: a strategy bundle is not part
	// of the document, and staging a copy of it would mean validating against a
	// catalog the deployment does not actually have.
	load, err := config.LoadV2(dir, filepath.Join(dir, "rules"), storeRoot)
	if err != nil {
		cleanup()
		return nil, err
	}
	cfg, gaps, err := config.FromV2(load.Config, load.Rules)
	if err != nil {
		cleanup()
		return nil, err
	}
	_ = gaps
	return &staged{dir: dir, cfg: cfg, load: load}, nil
}

func (st *staged) close() {
	if st != nil && st.dir != "" {
		os.RemoveAll(st.dir)
	}
}

// writeCandidate lays the document and every rule into dir.
func writeCandidate(dir string, in configDoc) error {
	doc := in.YAML
	if in.Config != nil {
		out, err := yaml.Marshal(in.Config)
		if err != nil {
			return fmt.Errorf("re-encode configuration: %w", err)
		}
		doc = string(out)
	}
	if strings.TrimSpace(doc) == "" {
		return errors.New("the candidate carries neither `yaml` nor `config`")
	}
	if err := os.WriteFile(filepath.Join(dir, "configuration.yaml"), []byte(doc), 0o600); err != nil {
		return fmt.Errorf("write candidate document: %w", err)
	}
	rulesDir := filepath.Join(dir, "rules")
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		return err
	}
	for _, item := range in.Rules {
		text := item.YAML
		if item.Rule != nil {
			out, err := yaml.Marshal(item.Rule)
			if err != nil {
				return fmt.Errorf("re-encode rule %s: %w", item.File, err)
			}
			text = string(out)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		name := filepath.Base(item.File)
		if name != item.File || !strings.HasSuffix(name, ".yaml") {
			return fmt.Errorf("rule file %q is not a plain .yaml name inside the rules directory", item.File)
		}
		if err := os.WriteFile(filepath.Join(rulesDir, name), []byte(text), 0o600); err != nil {
			return fmt.Errorf("write candidate rule %s: %w", name, err)
		}
	}
	return nil
}

// carryLiveRules fills in the rules a partial candidate left out.
//
// ABSENT means "I am not touching the rules", and an absent list that emptied the
// directory would delete every rule the operator has — from a request that only meant
// to change the document. `[]` (present and empty) is how you ask for that
// deliberately, which is the difference the UI has to be able to express.
func carryLiveRules(in *configDoc, rulesDir string) {
	if in.Rules != nil {
		return
	}
	in.Rules = readRulesDir(rulesDir)
}

// commit copies a staged deployment over the live one. Rules are written first and
// the document LAST: a crash between the two leaves a document that describes fewer
// rules than the directory holds, which is a valid state (a rule nobody referenced),
// whereas the reverse order would leave a document naming rules that do not exist.
func (st *staged) commit(docPath, rulesDir string) error {
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		return err
	}
	// Which files the candidate owns. Built from the rules it actually loaded rather
	// than from the request, so a file the loader skipped cannot be mistaken for one
	// the operator asked to keep.
	wanted := make(map[string]bool, len(st.load.Rules))
	for _, item := range st.load.Rules {
		data, err := yaml.Marshal(&item)
		if err != nil {
			return fmt.Errorf("re-encode rule %s: %w", item.ID, err)
		}
		name := filenameForRule(item)
		if err := writeFileAtomic(filepath.Join(rulesDir, name), data, 0o600); err != nil {
			return fmt.Errorf("write rule %s: %w", item.ID, err)
		}
		wanted[name] = true
	}

	// Remove the rules the candidate dropped. Without this a rule deleted in the GUI
	// is deleted in the DOCUMENT but not on disk, and the loader — which reads a
	// directory — keeps steering the traffic the operator asked it to stop steering.
	// The request and the commit agree about what the deployment contains only if
	// something here takes the two apart.
	//
	// Done BEFORE the document lands, so a failure leaves a directory holding a
	// superset (an unreferenced rule) rather than a document naming rules that are
	// gone. The reverse order would break the loading deployment, not just add to it.
	kept, err := ruleFilesIn(rulesDir)
	if err != nil {
		return err
	}
	for _, name := range kept {
		if wanted[name] {
			continue
		}
		if err := os.Remove(filepath.Join(rulesDir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove rule %s the candidate dropped: %w", name, err)
		}
	}

	if err := writeFileAtomic(docPath, mustMarshal(st.load.Config), 0o600); err != nil {
		return err
	}
	return nil
}

// ruleFilesIn lists the rule files in a directory — plain .yaml names, which is what
// the deployment is defined to hold. Anything else is left alone rather than deleted:
// a stray file is a thing to notice, not to remove on the operator's behalf.
func ruleFilesIn(rulesDir string) ([]string, error) {
	entries, err := os.ReadDir(rulesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

func mustMarshal(v any) []byte {
	out, err := yaml.Marshal(v)
	if err != nil {
		// Marshal failed on a value the loader just built from these bytes, so this
		// is unreachable rather than merely unlikely. Returning the document's own
		// text keeps a marshal quirk from taking the service down mid-write.
		return []byte("# lotsman could not re-encode this configuration; the original text is in the journal\n")
	}
	return out
}

// handleValidateConfig parses a candidate WITHOUT writing it, so the editor can offer a
// check that never risks the running config.
func (s *ControlServer) handleValidateConfig(w http.ResponseWriter, r *http.Request) {
	in, err := readConfigDoc(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	st, err := stage(in, s.storeRoot(), config.RulesDirFor(s.configPath, s.rulesDir))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	gaps := config.GapsFor(st.load.Config)
	st.close()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"valid": true, "gaps": gaps})
}

// handleSetConfig validates a candidate in a scratch tree, commits it, and applies it.
// It validates BEFORE touching a live file — a configuration that fails to load is
// never written, so the service can always start into a good tree. That is the whole
// safety of editing config from a UI.
func (s *ControlServer) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" || s.restart == nil {
		http.Error(w, "in-app config editing is not enabled on this instance", http.StatusNotImplemented)
		return
	}
	in, err := readConfigDoc(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	st, err := stage(in, s.storeRoot(), config.RulesDirFor(s.configPath, s.rulesDir))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer st.close()
	rulesDir := config.RulesDirFor(s.configPath, s.rulesDir)
	if err := st.commit(s.configPath, rulesDir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	gaps := config.GapsFor(st.load.Config)
	// Content-Type is set BEFORE WriteHeader on purpose: after WriteHeader the
	// header is already flushed and a later Set is silently dropped, which is why
	// these two responses were arriving as application/octet-stream.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"applied": true, "gaps": gaps})
	// Apply AFTER the ack, so the caller sees success before the data plane blips.
	s.applyAfterAck(st.cfg)
}

// handleSetEnabled switches one rule on or off — the tile toggle. It is a CONFIG edit,
// not a runtime override, on purpose: the desired state is re-asserted every few
// seconds from the config-built registry, so anything held only in memory is undone
// within a tick. Writing the bit to the file and reloading makes the switch mean the
// same thing to every consumer, survive a restart, and read the same way on the router.
func (s *ControlServer) handleSetEnabled(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" || s.restart == nil {
		http.Error(w, "in-app config editing is not enabled on this instance", http.StatusNotImplemented)
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Enabled == nil {
		http.Error(w, `body must set "enabled" to true or false`, http.StatusBadRequest)
		return
	}
	id := r.PathValue("name")
	rulesDir := config.RulesDirFor(s.configPath, s.rulesDir)
	// Locate the rule's own file by parsing the ones present, so the toggle follows a
	// rule the operator renamed the file of rather than assuming id + ".yaml".
	target := ""
	for _, item := range readRulesDir(rulesDir) {
		if item.Rule != nil && item.Rule.ID == id {
			target = item.File
			break
		}
	}
	if target == "" {
		http.Error(w, "no rule with id "+id+" is declared in the config", http.StatusBadRequest)
		return
	}
	path := filepath.Join(rulesDir, target)
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "read rule: "+err.Error(), http.StatusInternalServerError)
		return
	}
	y, found, err := configv2.SetRuleEnabled(data, id, *in.Enabled)
	if err != nil {
		http.Error(w, "the rule file does not parse — repair it in the YAML editor first: "+err.Error(), http.StatusConflict)
		return
	}
	if !found {
		http.Error(w, "no rule with id "+id+" is declared in "+target, http.StatusBadRequest)
		return
	}
	// Validate the WHOLE deployment, not just the edit: switching the last rule off
	// leaves nothing to steer, and that must fail before a file is touched.
	doc, derr := os.ReadFile(s.configPath)
	if derr != nil {
		http.Error(w, "read config: "+derr.Error(), http.StatusInternalServerError)
		return
	}
	rules := readRulesDir(rulesDir)
	for i := range rules {
		if rules[i].File == target {
			rules[i].YAML = string(y)
			rules[i].Rule = nil
		}
	}
	st, err := stage(configDoc{Path: s.configPath, RulesDir: rulesDir, YAML: string(doc), Rules: rules}, s.storeRoot(), rulesDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer st.close()
	if err := writeFileAtomic(path, y, 0o600); err != nil {
		http.Error(w, "write rule: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.log.Info("rule switched", "rule", id, "enabled", *in.Enabled)
	// Content-Type is set BEFORE WriteHeader on purpose: after WriteHeader the
	// header is already flushed and a later Set is silently dropped, which is why
	// these two responses were arriving as application/octet-stream.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"rule": id, "enabled": *in.Enabled})
	s.applyAfterAck(st.cfg)
}

// storeRoot is where the strategy bundles live, for validation against the catalog the
// deployment really has rather than one the scratch tree invented.
func (s *ControlServer) storeRoot() string {
	if s.storeDir != "" {
		return s.storeDir
	}
	return "/var/lib/lotsman/strategies"
}
