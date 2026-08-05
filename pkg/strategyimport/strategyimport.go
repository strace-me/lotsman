// Package strategyimport reads third-party zapret strategy bundles and turns
// them into catalog recipes.
//
// The bundles disagree about containers but not about content: Flowseal ships
// Windows .bat files driving winws, its Linux port and the router's own scripts
// ship shell driving nfqws, and the upstream manager documents strategies in
// fenced markdown blocks. winws and nfqws take the same desync flags, so one
// normalizer serves all of them — and the .bat reader is also most of what a
// Windows engine will need.
//
// This is the automation of what pkg/strategycat/gen.py describes in its header
// and does by hand: gen.py's strategies were transcribed by a person reading the
// upstreams, so the catalog froze at the versions that person read. Parsing
// instead of transcribing is what lets a bundle update ADD recipes rather than
// only break things.
//
// Nothing here executes a bundle. These are third-party scripts; they are read
// as text, variables are resolved by simple textual substitution, and anything
// that cannot be resolved is left alone for the normalizer to reject.
package strategyimport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategycat"
)

// Kind names the container a bundle writes its strategies in.
type Kind int

const (
	// KindBat is a Flowseal-style Windows batch file: winws arguments, lines
	// continued with a trailing caret.
	KindBat Kind = iota
	// KindShell is a POSIX shell script or a zapret `config` file: nfqws
	// arguments, lines continued with a trailing backslash, values often held
	// in variables.
	KindShell
	// KindMarkdown is documentation with the strategies in fenced code blocks.
	KindMarkdown
)

// Source is one file to read, with the label its recipes will be attributed to.
type Source struct {
	Name string // provenance, e.g. "Flowseal/zapret-discord-youtube 1.10.0"
	Kind Kind
	Body []byte
}

// Skip records a block that was read but deliberately not turned into a recipe.
// Dropping a recipe whole is the point: silently removing an argument we cannot
// express would leave a recipe that no longer does what its source said, and the
// canary would then record a verdict against a strategy that never ran.
type Skip struct {
	Source string
	Args   string
	Reason string
}

// Result is what an import produced.
type Result struct {
	Recipes []strategycat.Recipe
	Skipped []Skip
}

var (
	// A block's arguments begin at the first flag; everything before it is the
	// invocation (start "..." /min "%~dp0winws.exe", exec nfqws, NFQWS_OPT=...).
	firstFlag = regexp.MustCompile(`(^|\s)--[a-z]`)
	// VAR=value / VAR="value" / VAR='value' at the start of a line.
	assignment = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_][A-Za-z0-9_]*)=("([^"]*)"|'([^']*)'|([^\s#]*))[ \t]*$`)
	// Batch spells the same thing `set NAME=value` or `set "NAME=value"`, the
	// quote wrapping the whole assignment rather than the value.
	batchAssignment = regexp.MustCompile(`(?mi)^[ \t]*set[ \t]+"?([A-Za-z_][A-Za-z0-9_]*)=([^"\r\n]*)"?[ \t]*$`)
	fence           = regexp.MustCompile("(?s)```[a-zA-Z0-9]*\\n(.*?)```")
	varRef          = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
	batchVarRef     = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)
)

// Import reads every source and returns the deduplicated recipes.
//
// Recipe IDs are derived from the normalized arguments, never from the source
// or its order, so the same strategy keeps one identity across bundles and
// across re-imports. That is load-bearing rather than tidy: the knowledge base
// keys outcomes on the recipe ID, so an ID that moved when a bundle updated
// would silently discard everything learned about that strategy.
func Import(sources []Source) Result {
	var res Result
	byArgs := map[string]int{} // normalized args -> index into res.Recipes
	for _, src := range sources {
		for _, block := range Blocks(src) {
			args, err := Normalize(block)
			if err != nil {
				res.Skipped = append(res.Skipped, Skip{Source: src.Name, Args: strings.Join(block, " "), Reason: err.Error()})
				continue
			}
			key := strings.Join(args, " ")
			if i, seen := byArgs[key]; seen {
				// The same strategy from another bundle is evidence, not noise:
				// independent authors converging on it is the one prior an
				// importer can offer without measuring anything itself.
				if !strings.Contains(res.Recipes[i].Provenance, src.Name) {
					res.Recipes[i].Provenance += " | also: " + src.Name
					res.Recipes[i].Consensus++
				}
				continue
			}
			proto, class := classify(args)
			byArgs[key] = len(res.Recipes)
			res.Recipes = append(res.Recipes, strategycat.Recipe{
				ID:          recipeID(args),
				Provenance:  src.Name,
				TargetClass: class,
				Protocol:    proto,
				Techniques:  Techniques(args),
				NfqwsArgs:   args,
				Consensus:   1,
			})
		}
	}
	return res
}

// ImportDir reads every strategy file in a bundle directory, recursively. It is
// the seam a bundle updater calls after installing a new release: today an
// update replaces the files and nothing re-reads them, so a bundle can only ever
// cost us (a lost file stops the engine) and never pay (new strategies go
// unnoticed). Files whose extension is not recognised are ignored.
func ImportDir(root string) (Result, error) {
	// The deployed layout points a stable symlink at the versioned bundle, and
	// that symlink is what callers have. WalkDir does not follow one: it lstats
	// the root, sees a non-directory, hands it to the callback once and finishes
	// — no error, no files, just an empty result that looks like a bundle with no
	// strategies in it. Resolve first.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	var srcs []Source
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		var kind Kind
		switch strings.ToLower(filepath.Ext(path)) {
		case ".bat", ".cmd":
			kind = KindBat
		case ".sh":
			kind = KindShell
		case ".md":
			kind = KindMarkdown
		default:
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		srcs = append(srcs, Source{Name: rel, Kind: kind, Body: body})
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("strategyimport: walk %s: %w", root, err)
	}
	// Deterministic order: the first source to carry a recipe owns its
	// provenance, so a directory walk that reordered itself would rewrite
	// attributions for no reason.
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].Name < srcs[j].Name })
	return Import(srcs), nil
}

// Merge returns base followed by the entries of extra that base does not already
// contain, comparing by normalized arguments rather than by ID.
//
// It is deliberately additive and base-preferring. Additive, so a bundle we
// cannot read costs nothing: the curated catalog is still there in full, and the
// engine keeps composing exactly what it composed before. Base-preferring,
// because a recipe present in both must keep the CURATED id — the knowledge base
// keys outcomes on the id, and admitting a second id for the same argv would
// split one strategy's learned history in two and make both halves look colder
// than the truth.
func Merge(base, extra []strategycat.Recipe) []strategycat.Recipe {
	known := make(map[string]bool, len(base))
	for _, r := range base {
		known[strings.Join(r.NfqwsArgs, " ")] = true
	}
	out := append([]strategycat.Recipe(nil), base...)
	for _, r := range extra {
		key := strings.Join(r.NfqwsArgs, " ")
		if known[key] {
			continue
		}
		known[key] = true
		out = append(out, r)
	}
	return out
}

// Blocks extracts each "--new" block from one source as its own argument list.
func Blocks(src Source) [][]string {
	var out [][]string
	for _, chunk := range chunks(src) {
		for _, seg := range splitOnNew(strings.Fields(chunk)) {
			if len(seg) > 0 {
				out = append(out, seg)
			}
		}
	}
	return out
}

// chunks returns the argument text of a source, one string per invocation found.
func chunks(src Source) []string {
	body := string(src.Body)
	switch src.Kind {
	case KindBat:
		joined := joinContinuations(body, "^")
		return []string{argsAfterInvocation(expandBatchVars(joined, batchAssignments(joined)))}
	case KindMarkdown:
		var out []string
		for _, m := range fence.FindAllStringSubmatch(body, -1) {
			if s := argsAfterInvocation(joinContinuations(m[1], `\`)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default: // KindShell
		joined := joinContinuations(body, `\`)
		return []string{argsAfterInvocation(expandVars(joined, assignments(joined)))}
	}
}

// joinContinuations folds lines ending in the continuation character into one.
func joinContinuations(body, cont string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "#") || strings.HasPrefix(s, "rem ") {
			continue
		}
		if strings.HasSuffix(line, cont) {
			b.WriteString(strings.TrimSuffix(line, cont))
			b.WriteString(" ")
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// assignments collects simple top-level VAR=value pairs. Textual only: nothing
// is executed, and command substitution or anything else non-literal is skipped.
func assignments(body string) map[string]string {
	vars := map[string]string{}
	for _, m := range assignment.FindAllStringSubmatch(body, -1) {
		val := m[3] + m[4] + m[5] // exactly one of the alternatives matched
		if strings.ContainsAny(val, "$`(") {
			continue // not a literal; leave the reference unresolved
		}
		vars[m[1]] = val
	}
	return vars
}

// batchAssignments collects `set NAME=value` pairs. %~dp0 is left in the value:
// it means "the directory holding me", and reducing the reference to a basename
// later throws it away anyway.
func batchAssignments(body string) map[string]string {
	vars := map[string]string{}
	for _, m := range batchAssignment.FindAllStringSubmatch(body, -1) {
		vars[m[1]] = strings.TrimSpace(m[2])
	}
	return vars
}

// expandBatchVars resolves %NAME% until it stops changing, since one assignment
// routinely refers to another. An unknown name is left for the normalizer to
// reject.
func expandBatchVars(body string, vars map[string]string) string {
	if len(vars) == 0 {
		return body
	}
	for range 4 { // assignments nest a level or two; the bound stops a cycle
		next := batchVarRef.ReplaceAllStringFunc(body, func(ref string) string {
			if v, ok := vars[strings.Trim(ref, "%")]; ok {
				return v
			}
			return ref
		})
		if next == body {
			break
		}
		body = next
	}
	return body
}

// expandVars substitutes known variables. An unknown reference is left as-is so
// the normalizer sees it and can reject the block rather than guess.
func expandVars(body string, vars map[string]string) string {
	if len(vars) == 0 {
		return body
	}
	return varRef.ReplaceAllStringFunc(body, func(ref string) string {
		name := strings.Trim(ref, "${}")
		if v, ok := vars[name]; ok {
			return v
		}
		return ref
	})
}

// Program returns the engine binary a script invokes — the last token before its
// first flag, with the launcher noise (exec, quotes) removed. Blocks throws this
// away on purpose, since a recipe is not tied to a binary; a canary that means to
// run the very command production runs needs it back.
func Program(src Source) string {
	body := string(src.Body)
	switch src.Kind {
	case KindBat:
		body = expandBatchVars(joinContinuations(body, "^"), batchAssignments(joinContinuations(body, "^")))
	default:
		joined := joinContinuations(body, `\`)
		body = expandVars(joined, assignments(joined))
	}
	loc := firstFlag.FindStringIndex(body)
	if loc == nil {
		return ""
	}
	fields := strings.Fields(body[:loc[0]])
	for i := len(fields) - 1; i >= 0; i-- {
		tok := strings.Trim(fields[i], `"'`)
		if tok == "" || tok == "exec" || strings.HasPrefix(tok, "$") {
			continue
		}
		return tok
	}
	return ""
}

// argsAfterInvocation drops everything before the first flag, which is the
// program and how it was launched, and keeps the rest.
func argsAfterInvocation(s string) string {
	loc := firstFlag.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	start := loc[0]
	if s[start] != '-' {
		start++ // the match began on the preceding space
	}
	return s[start:]
}

// splitOnNew divides one invocation's arguments into per-block segments.
func splitOnNew(args []string) [][]string {
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

// recipeID is a stable name for a strategy: a technique hint for a human plus a
// digest of the normalized arguments so two spellings of the same recipe cannot
// collide and one recipe cannot drift into two identities.
func recipeID(args []string) string {
	sum := sha256.Sum256([]byte(strings.Join(args, " ")))
	digest := hex.EncodeToString(sum[:3])
	hint := "desync"
	if t := Techniques(args); len(t) > 0 {
		hint = strings.ReplaceAll(t[0], ":", "-")
	}
	return fmt.Sprintf("imp-%s-%s", hint, digest)
}
