// Package contract holds tests that guard the SHAPE of the system rather than the
// behaviour of one package.
//
// They exist because this project has paid twice for the same class of defect in
// ways no ordinary unit test could see: the client wired a SUBSET of the daemon
// for months, each root tested only what it had, both were green, and the only
// detector was a service misbehaving on the owner's device (LOT-65). A test that
// checks one function cannot notice that a second root never got the same
// feature. These can.
package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// repoRoot walks up from this file to the module root, so the guards read the
// real tree no matter where `go test` is invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the contract package")
		}
		dir = parent
	}
}

// goFiles lists .go files under root, skipping vendored and generated trees.
func goFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "dist", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// flagsIn parses the flag registrations of one file and returns their names.
func flagsIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	found := map[string]bool{}
	flagCall := regexp.MustCompile(`^flag\.([A-Za-z]+)$`)
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || !flagCall.MatchString("flag."+pkg.Name) {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		found[strings.Trim(lit.Value, `"`)] = true
		return true
	})
	return found
}
