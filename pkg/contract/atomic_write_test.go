package contract

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// handRolledAtomicWrites are the files that build a temporary file and rename it
// over the target themselves, with a REASON each. Every one of these has a reason
// pkg/atomicfile cannot serve — but a reason is not the same as a licence, and the
// reason that keeps the list honest is that it is a fixed, reviewed set. A sixth
// file is not a judgement call, it is a copy of the code this project already
// replaced once: a FIXED "path.tmp" name let two writers of one file interleave
// into the same temporary, and the result was a config that reconciled differently
// on every pass (LOT-1).
var handRolledAtomicWrites = map[string]string{
	"pkg/atomicfile/atomicfile.go": "the implementation itself",
	"pkg/reconcile/reconcile.go": "swaps a config its CALLER streamed in: it holds a backup " +
		"and a rollback, which is a two-phase protocol rather than a write",
	"pkg/aggregate/guard.go": "chmods the result and returns a shrink-guard verdict, so the " +
		"caller decides on the value the write produced",
	"client/platform/rulesets/rulesets.go": "streams a rule-set out of a zip, so the bytes are " +
		"produced by a reader rather than held in memory",
	"pkg/flowseal/installer.go": "renames a SYMLINK to repoint a versioned bundle directory",
	"pkg/config/materialize.go": "swaps a whole deployment TREE — it renames the document and the " +
		"rules directory as a unit and restores both halves if the second move fails, which " +
		"atomicfile's single-file write cannot express. Its per-FILE writes use atomicfile; " +
		"only the tree swap is hand-rolled",
}

var writeRe = []string{"os.CreateTemp", "os.Rename", "ioutil.TempFile"}

// TestNobodyReimplementsAtomicWrite catches a NEW hand-rolled temp-and-rename
// sequence. Direct writes are fine — many are, for sidecars and scripts — the thing
// being policed is re-implementing the dance the project already centralised.
func TestNobodyReimplementsAtomicWrite(t *testing.T) {
	root := repoRoot(t)
	var found []string
	for _, path := range goFiles(t, root) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		// The dance is a temporary file AND a rename. Either alone is ordinary.
		creates, renames := false, false
		for _, re := range writeRe {
			if strings.Contains(body, re) {
				creates = true
			}
		}
		if strings.Contains(body, "os.Rename") {
			renames = true
		}
		if creates && renames {
			found = append(found, rel)
		}
	}

	var unexpected []string
	for _, rel := range found {
		if _, ok := handRolledAtomicWrites[rel]; !ok {
			unexpected = append(unexpected, rel)
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		t.Errorf("these files hand-roll a temp-file-and-rename write:\n  %s\n"+
			"either use pkg/atomicfile, or add the file to handRolledAtomicWrites in "+
			"pkg/contract WITH a reason — an unreviewed fifth copy is how two writers "+
			"of one file ended up sharing a temp name (LOT-1)",
			strings.Join(unexpected, "\n  "))
	}
	// The reverse direction matters too: a listed exception that stopped doing the
	// dance means the comment above is claiming a reason for code that is gone.
	var stale []string
	for rel := range handRolledAtomicWrites {
		if rel == "pkg/atomicfile/atomicfile.go" {
			continue
		}
		kept := false
		for _, f := range found {
			if f == rel {
				kept = true
			}
		}
		if !kept {
			stale = append(stale, rel)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("these are listed as hand-rolled but no longer are — drop them:\n  %s",
			strings.Join(stale, "\n  "))
	}
}
