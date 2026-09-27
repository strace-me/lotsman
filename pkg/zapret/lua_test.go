package zapret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/strategystore"
)

const realLuaDir = "/nix/store/zfhygqlbghphg4q0sn9jbbrc6yvxkz0k-zapret2-0.9.5.2/share/zapret2/lua"

func fakeLuaDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func nfqws2Bundle(args ...string) strategystore.Bundle {
	return strategystore.Bundle{
		Root:     "/tmp/bundle",
		Manifest: strategystore.Manifest{ID: "alt12", Engine: "nfqws2", Args: "command.args"},
		Args:     strategystore.Args{Values: args},
	}
}

func TestLuaFunctionsReadsInstalledScripts(t *testing.T) {
	dir := fakeLuaDir(t, map[string]string{
		"a.lua":     "function fake_syndata(ctx)\nend\n\nlocal function hidden()\nend\n",
		"b.lua":     "-- function commented_out()\nfunction fakedsplit(ctx)\nend\n",
		"notes.txt": "function not_lua()\n",
	})
	names, err := LuaFunctions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !names["fake_syndata"] || !names["fakedsplit"] {
		t.Errorf("top-level functions were missed: %v", names)
	}
	// A nested or commented definition is not something --lua-desync can call, and
	// accepting it would let a name through that the engine would never resolve.
	if names["hidden"] || names["commented_out"] || names["not_lua"] {
		t.Errorf("indexed a name the engine cannot call: %v", names)
	}
}

func TestLuaFunctionsRejectsAnEmptyOrWrongDir(t *testing.T) {
	if _, err := LuaFunctions(""); err == nil {
		t.Error("an empty lua dir was accepted")
	}
	if _, err := LuaFunctions(fakeLuaDir(t, map[string]string{"readme.md": "nothing"})); err == nil {
		t.Error("a dir with no functions was accepted — it is probably not a zapret2 install")
	}
}

func TestUnknownLuaFunctionsReportsRatherThanRefuses(t *testing.T) {
	// The engine checks NEITHER kind of name: on v0.9.5.2 both
	// `--lua-desync=fake_syndata` (real, native) and `--lua-desync=nosuchfunction`
	// (a typo) pass --dry-run. So a name absent from the lua index is ambiguous,
	// and refusing it would kill a working strategy — the exact loss this guard
	// exists to prevent.
	dir := fakeLuaDir(t, map[string]string{"z.lua": "function fakedsplit(ctx)\nend\n"})
	engine := Nfqws2Engine{}

	if unknown, err := engine.UnknownLuaFunctions(nfqws2Bundle("--lua-desync=fakedsplit"), dir); err != nil || len(unknown) != 0 {
		t.Fatalf("a lua-defined function was reported unknown: %v %v", unknown, err)
	}

	unknown, err := engine.UnknownLuaFunctions(nfqws2Bundle("--lua-desync=fake_syndata"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 1 || unknown[0] != "fake_syndata" {
		t.Fatalf("a native function should be REPORTED, not refused: %v", unknown)
	}
}

func TestUnknownLuaFunctionsHandlesParamsAndEmptyName(t *testing.T) {
	dir := fakeLuaDir(t, map[string]string{"z.lua": "function fakedsplit(ctx)\nend\n"})
	if unknown, err := (Nfqws2Engine{}).UnknownLuaFunctions(nfqws2Bundle("--lua-desync=fakedsplit:seqovl=3:ttl=6"), dir); err != nil || len(unknown) != 0 {
		t.Fatalf("a parametrised lua call was mishandled: %v %v", unknown, err)
	}
	if _, err := (Nfqws2Engine{}).UnknownLuaFunctions(nfqws2Bundle("--lua-desync="), dir); err == nil {
		t.Error("an empty --lua-desync was accepted")
	}
}

func TestPreparePassesTheWriteableDir(t *testing.T) {
	args, err := (Nfqws2Engine{}).Prepare(nfqws2Bundle("--filter-tcp=443", "--lua-desync=fake_syndata"),
		strategystore.PrepareOptions{QNum: 200, Fwmark: 0x4554, WorkDir: "/tmp/b", WriteableDir: "/run/lotsman/lua"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--writeable=/run/lotsman/lua") {
		t.Errorf("writeable dir missing: %s", joined)
	}
}

// If this box carries zapret2, the index must find the real thing — a parser that
// only works on the fixture is a parser that quietly finds nothing in production.
func TestLuaFunctionsOnTheInstalledPackage(t *testing.T) {
	if _, err := os.Stat(realLuaDir); err != nil {
		t.Skip("zapret2 lua dir not on this box")
	}
	names, err := LuaFunctionNames(realLuaDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 10 {
		t.Fatalf("only %d functions indexed from the installed package: %v", len(names), names)
	}
	// fake_syndata is deliberately NOT expected here: it is compiled into the
	// binary, and its absence is exactly why UnknownLuaFunctions reports instead
	// of refusing. The lua scripts do define the classic techniques.
	for _, want := range []string{"fake", "fakedsplit", "hostfakesplit", "multisplit", "syndata"} {
		if !containsName(names, want) {
			t.Errorf("the lua scripts should define %q: %d indexed", want, len(names))
		}
	}
	if containsName(names, "fake_syndata") {
		t.Error("fake_syndata is native to the binary; finding it in lua would mean the index is lying")
	}
	t.Logf("indexed %d lua functions; fake_syndata is absent because it is native", len(names))
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
