// Package presetimport turns an upstream Windows launcher into nfqws argv that
// Lotsman can run VERBATIM.
//
// It exists because the alternative is transcription by hand, and this project
// has already paid for that twice: the catalogue holds exactly one of Flowseal's
// twenty-one strategies, split into per-profile recipes, and the owner's question
// — why are we taking apart a bundle that works — is answered by not taking it
// apart. A converter also keeps the provenance honest: the argv in the file is
// derived from the release, and regenerating it against a newer one is a diff
// rather than an act of memory.
//
// What it does NOT do is interpret. Every desync argument passes through
// untouched; only the things that cannot mean anything here are rewritten or
// dropped, and each of those is recorded in the output so the difference from
// upstream is visible to whoever reads the file rather than buried in a commit.
package presetimport

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Preset is one converted launcher.
type Preset struct {
	Name     string   // upstream's own name, from the filename ("ALT12", "SIMPLE FAKE")
	Args     []string // nfqws argv, verbatim except for the rewrites below
	Lists    []string // hostlist/ipset basenames it needs on disk
	Payloads []string // .bin basenames it needs in the zapret files dir
	Dropped  []string // what was removed, and why — never silent
}

// Options carries the two paths that differ between the upstream layout and ours.
type Options struct {
	// ListsDir is where the bundle's own lists live on the target box. Its files
	// keep their upstream basenames, because a preset's profiles are told apart by
	// WHICH list each one carries — that is the whole reason a bundle cannot be
	// rendered per rule.
	ListsDir string
}

var (
	// The launcher line: everything after the winws executable is argv.
	reExec = regexp.MustCompile(`(?i)winws\.exe"?\s+`)
	// %LISTS%name.ext and %BIN%name.bin, quoted or not.
	reList    = regexp.MustCompile(`%LISTS%([A-Za-z0-9._-]+)`)
	reBin     = regexp.MustCompile(`%BIN%([A-Za-z0-9._-]+)`)
	reWinVar  = regexp.MustCompile(`%[A-Za-z0-9_]+%`)
	reProfile = regexp.MustCompile(`^--(filter-tcp|filter-udp|filter-l7)=`)
)

// FromBat converts one .bat launcher. name is the preset's display name.
func FromBat(name, content string, opts Options) (*Preset, error) {
	line := launcherLine(content)
	if line == "" {
		return nil, fmt.Errorf("presetimport: %s has no winws launcher line", name)
	}
	p := &Preset{Name: name}
	lists := map[string]bool{}
	payloads := map[string]bool{}
	dropped := map[string]bool{}

	for _, block := range splitProfiles(tokenize(line)) {
		out, ok := convertProfile(block, opts, lists, payloads, dropped)
		if !ok {
			continue
		}
		if len(p.Args) > 0 {
			p.Args = append(p.Args, "--new")
		}
		p.Args = append(p.Args, out...)
	}
	if len(p.Args) == 0 {
		return nil, fmt.Errorf("presetimport: %s produced no usable profile", name)
	}
	p.Lists, p.Payloads, p.Dropped = keys(lists), keys(payloads), keys(dropped)
	return p, nil
}

// launcherLine joins the `^` continuations and returns the argv text.
func launcherLine(content string) string {
	joined := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "^\n", " ")
	for _, line := range strings.Split(joined, "\n") {
		if m := reExec.FindStringIndex(line); m != nil {
			return strings.TrimSpace(line[m[1]:])
		}
	}
	return ""
}

// tokenize splits on whitespace, keeping quoted paths whole.
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// splitProfiles cuts the argv on --new, which is how nfqws itself separates one
// profile from the next.
func splitProfiles(args []string) [][]string {
	var out [][]string
	cur := []string{}
	for _, a := range args {
		if a == "--new" {
			out = append(out, cur)
			cur = []string{}
			continue
		}
		cur = append(cur, a)
	}
	return append(out, cur)
}

// convertProfile rewrites one profile's arguments, or reports that the profile
// cannot be rendered here at all.
func convertProfile(args []string, opts Options, lists, payloads, dropped map[string]bool) ([]string, bool) {
	var out []string
	filtered := false
	for _, a := range args {
		switch {
		case a == "":
			continue
		case strings.HasPrefix(a, "--wf-tcp=") || strings.HasPrefix(a, "--wf-udp="):
			// The WinDivert capture. Ours comes from nft and is derived from the
			// --filter-* arguments, so carrying this across would be a second, silently
			// disagreeing source of truth for the same thing.
			dropped["--wf-* (WinDivert capture; ours is derived from --filter-* into nft)"] = true
			continue
		case strings.Contains(a, "-user.txt"):
			// The bundle's per-user override lists. They ship EMPTY, and nfqws exits when
			// a hostlist file is missing — so keeping a reference to a file nobody will
			// create turns the whole preset into an engine that refuses to start.
			dropped["*-user.txt references (empty upstream; a missing hostlist makes nfqws exit)"] = true
			continue
		}
		if reProfile.MatchString(a) {
			// A filter whose ports come from a Windows variable the launcher script fills
			// in at runtime. We have neither the script nor the game-filter state.
			if reWinVar.MatchString(a) {
				dropped["profiles filtered only by %GameFilter*% (set by service.bat at runtime)"] = true
				return nil, false
			}
			filtered = true
		}
		a = reList.ReplaceAllString(a, path.Join(opts.ListsDir, "$1"))
		a = reBin.ReplaceAllString(a, "$1")
		if reWinVar.MatchString(a) {
			dropped["arguments carrying an unresolved %VAR%"] = true
			continue
		}
		for _, m := range reList.FindAllStringSubmatch(a, -1) {
			lists[m[1]] = true
		}
		out = append(out, a)
	}
	// Collect what the rewritten argument now points at.
	for _, a := range out {
		if i := strings.IndexByte(a, '='); i > 0 {
			v := a[i+1:]
			if strings.HasSuffix(v, ".bin") {
				payloads[path.Base(v)] = true
			}
			if opts.ListsDir != "" && strings.HasPrefix(v, opts.ListsDir+"/") {
				lists[path.Base(v)] = true
			}
		}
	}
	if !filtered {
		// A profile with no --filter-* claims EVERYTHING the queue hands it, which on
		// this side is the household's whole uplink rather than one Windows adapter.
		dropped["profiles with no --filter-* (they would claim all captured traffic)"] = true
		return nil, false
	}
	return out, len(out) > 0
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NameFromFile turns "general (ALT12).bat" into "ALT12", and "general.bat" into
// "general" — the names the upstream's users actually say out loud.
func NameFromFile(filename string) string {
	base := strings.TrimSuffix(path.Base(filename), ".bat")
	if i := strings.IndexByte(base, '('); i >= 0 {
		if j := strings.IndexByte(base[i:], ')'); j > 0 {
			return strings.TrimSpace(base[i+1 : i+j])
		}
	}
	return strings.TrimSpace(base)
}

// Render writes the .args file: a header saying where it came from and what was
// changed, then one argument per line.
func Render(p *Preset, source string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — converted from %s\n#\n", p.Name, source)
	b.WriteString("# Generated by pkg/presetimport. Run VERBATIM: Lotsman composes nothing in\n")
	b.WriteString("# preset mode, so these are the profiles the upstream author wrote, in order.\n#\n")
	if len(p.Lists) > 0 {
		fmt.Fprintf(&b, "# Needs these lists on disk: %s\n", strings.Join(p.Lists, ", "))
	}
	if len(p.Payloads) > 0 {
		fmt.Fprintf(&b, "# Needs these payloads in the zapret files dir: %s\n", strings.Join(p.Payloads, ", "))
	}
	if len(p.Dropped) > 0 {
		b.WriteString("#\n# CHANGED FROM UPSTREAM — everything else passed through untouched:\n")
		for _, d := range p.Dropped {
			fmt.Fprintf(&b, "#   - dropped %s\n", d)
		}
	}
	b.WriteString("\n")
	for _, a := range p.Args {
		b.WriteString(a)
		b.WriteString("\n")
	}
	return b.String()
}
