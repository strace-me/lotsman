package strategyimport

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// This file is the second rendering of the SAME read. Import turns a bundle into
// catalog recipes — one profile per rule, host selectors replaced by
// {{DOMAINS}}, `--ipset` refused — which is what the per-rule model needs and
// which necessarily takes the bundle apart. Preset keeps it whole.
//
// Both are needed and they are not interchangeable. Flowseal's ALT12 is one
// launch whose profiles carry DIFFERENT hostlists: the narrow tcp/443 Google
// treatment against list-google, the broad one against list-general. Rendered per
// rule, both profiles receive the same rule's domains, the second can never match
// and the recipe is refused as self-shadowing — a correct statement about our
// rendering and a false one about ALT12. Preset mode runs the author's argv
// instead, so the bundle is measured as the thing that actually works out there.

// gameFilterOff is the port service.bat assigns when the game filter is off,
// which is how every release ships. Nothing listens on 12; the profile is inert
// by design rather than by our editing.
const gameFilterOff = "12"

// Preset is one upstream launcher converted to nfqws argv, verbatim.
type Preset struct {
	Name     string   // upstream's own name ("ALT12", "FAKE TLS AUTO")
	Args     []string // nfqws argv; no desync argument is interpreted
	Lists    []string // hostlist/ipset basenames it needs on disk
	Payloads []string // .bin basenames it needs in the zapret files dir
	Dropped  []string // what was removed, and why — never silent
	// Substituted records values we resolved to an upstream DEFAULT rather than
	// removed. It is a different claim from Dropped and deserves a different word:
	// nothing is missing, one variable was pinned to the value the bundle ships.
	Substituted []string
}

// PresetOptions carries the one path that differs between the upstream layout
// and ours.
type PresetOptions struct {
	// ListsDir is where the bundle's own lists live on the target box. Their
	// upstream basenames are KEPT, because which list a profile carries is what
	// tells the profiles apart — the very thing a per-rule rendering destroys.
	ListsDir string
}

var (
	// A path inside the bundle: <anything>lists\name.ext or <anything>bin\name.bin,
	// in either slash direction. The prefix is deliberately loose because
	// batchAssignments leaves `%~dp0` in place — "the directory holding me" — so the
	// text is `%~dp0lists\list-general.txt`, with no separator before `lists`.
	rePresetList = regexp.MustCompile(`(?i)[^\s=]*lists[\\/]([A-Za-z0-9._-]+)`)
	rePresetBin  = regexp.MustCompile(`(?i)[^\s=]*bin[\\/]([A-Za-z0-9._-]+\.bin)`)
	reWinVar     = regexp.MustCompile(`%[^%\s]+%`)
	reGameFilter = regexp.MustCompile(`%GameFilter(TCP|UDP)?%`)
	reFilterFlag = regexp.MustCompile(`^--(filter-tcp|filter-udp|filter-l7)=`)
)

// AsPreset converts one source into a verbatim preset, reusing the same reader
// Import uses: joined continuations, `set NAME=value` expansion, argv taken after
// the launcher, split on --new.
func AsPreset(name string, src Source, opts PresetOptions) (*Preset, error) {
	p := &Preset{Name: name}
	lists, payloads := map[string]bool{}, map[string]bool{}
	dropped, substituted := map[string]bool{}, map[string]bool{}

	for _, block := range Blocks(src) {
		out, ok := presetProfile(block, opts, lists, payloads, dropped, substituted)
		if !ok {
			continue
		}
		if len(p.Args) > 0 {
			p.Args = append(p.Args, "--new")
		}
		p.Args = append(p.Args, out...)
	}
	if len(p.Args) == 0 {
		return nil, fmt.Errorf("strategyimport: %s produced no usable profile", name)
	}
	p.Lists, p.Payloads = sortedKeys(lists), sortedKeys(payloads)
	p.Dropped, p.Substituted = sortedKeys(dropped), sortedKeys(substituted)
	return p, nil
}

// presetProfile rewrites one profile, or reports that it cannot be rendered here.
//
// Three things cannot mean anything on this side. Each is dropped and RECORDED:
// a silent drop produces a preset that is not the preset, which is the whole
// failure this mode exists to end.
func presetProfile(args []string, opts PresetOptions, lists, payloads, dropped, substituted map[string]bool) ([]string, bool) {
	var out []string
	filtered := false
	for _, a := range args {
		// Quotes sit INSIDE the token (`--hostlist="..."`), so trimming the ends is
		// not enough.
		a = strings.ReplaceAll(a, `"`, "")
		switch {
		case a == "":
			continue
		case strings.HasPrefix(a, "--wf-tcp=") || strings.HasPrefix(a, "--wf-udp="):
			// The WinDivert capture. Ours is derived from the --filter-* arguments into
			// nft, and two independent statements about which traffic is captured is
			// exactly how Discord voice ended up filtering ports the queue never carried.
			dropped["--wf-* (WinDivert capture; ours is derived from --filter-* into nft)"] = true
			continue
		case strings.Contains(a, "-user.txt"):
			// The bundle's per-user override lists ship EMPTY, and nfqws exits when a
			// hostlist file is missing — so a reference nobody will create turns the
			// whole preset into an engine that refuses to start.
			dropped["*-user.txt references (empty upstream; a missing hostlist makes nfqws exit)"] = true
			continue
		}
		if reFilterFlag.MatchString(a) {
			if reGameFilter.MatchString(a) {
				// service.bat fills these in, and its SHIPPED state is off: with no
				// `utils/game_filter.enabled` in the release — and there is none — it sets
				// GameFilterTCP=GameFilterUDP=12. Port 12 carries nothing, so upstream's
				// default renders these profiles deliberately inert; the 1024-65535 form
				// only appears once a user turns the filter on by hand.
				//
				// So substituting the default keeps the bundle at its full profile count
				// and byte-faithful to how it ships, where dropping the profile would make
				// our file quietly differ from the thing we promise to run verbatim.
				a = reGameFilter.ReplaceAllString(a, gameFilterOff)
				substituted["%GameFilter*% → "+gameFilterOff+" (upstream's shipped default: the game filter is OFF, and port "+gameFilterOff+" carries nothing)"] = true
			}
			if reWinVar.MatchString(a) {
				// Some other runtime variable we cannot resolve. Dropped WHOLE rather than
				// kept unfiltered: a profile with no filter claims every packet the queue
				// hands it, which here is the household's uplink, not one Windows adapter.
				dropped["profiles filtered by an unresolvable %VAR%"] = true
				return nil, false
			}
			filtered = true
		}
		a = rePresetBin.ReplaceAllString(a, "$1")
		a = rePresetList.ReplaceAllStringFunc(a, func(m string) string {
			sub := rePresetList.FindStringSubmatch(m)
			lists[sub[1]] = true
			return path.Join(opts.ListsDir, sub[1])
		})
		if reWinVar.MatchString(a) {
			dropped["arguments carrying an unresolved %VAR%"] = true
			continue
		}
		if i := strings.IndexByte(a, '='); i > 0 && strings.HasSuffix(a, ".bin") {
			payloads[path.Base(a[i+1:])] = true
		}
		out = append(out, a)
	}
	if !filtered {
		dropped["profiles with no --filter-* (they would claim all captured traffic)"] = true
		return nil, false
	}
	return out, len(out) > 0
}

// PresetName turns "general (ALT12).bat" into "ALT12" and "general.bat" into
// "general" — the names the upstream's users say out loud.
func PresetName(filename string) string {
	base := strings.TrimSuffix(path.Base(filename), path.Ext(filename))
	if i := strings.IndexByte(base, '('); i >= 0 {
		if j := strings.IndexByte(base[i:], ')'); j > 0 {
			return strings.TrimSpace(base[i+1 : i+j])
		}
	}
	return strings.TrimSpace(base)
}

// RenderPreset writes the .args file: where it came from, what it needs, what
// was changed, then one argument per line.
func RenderPreset(p *Preset, source string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — converted from %s\n#\n", p.Name, source)
	b.WriteString("# Generated by pkg/strategyimport. Run VERBATIM: in preset mode Lotsman\n")
	b.WriteString("# composes nothing, so these are the author's profiles, in the author's order,\n")
	b.WriteString("# against the author's lists.\n#\n")
	if len(p.Lists) > 0 {
		fmt.Fprintf(&b, "# Needs these lists on disk: %s\n", strings.Join(p.Lists, ", "))
	}
	if len(p.Payloads) > 0 {
		fmt.Fprintf(&b, "# Needs these payloads in the zapret files dir: %s\n", strings.Join(p.Payloads, ", "))
	}
	if len(p.Dropped) > 0 || len(p.Substituted) > 0 {
		b.WriteString("#\n# CHANGED FROM UPSTREAM — everything else passed through untouched:\n")
		for _, d := range p.Dropped {
			fmt.Fprintf(&b, "#   - dropped %s\n", d)
		}
		for _, x := range p.Substituted {
			fmt.Fprintf(&b, "#   - substituted %s\n", x)
		}
	}
	b.WriteString("\n")
	for _, a := range p.Args {
		b.WriteString(a)
		b.WriteString("\n")
	}
	return b.String()
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
