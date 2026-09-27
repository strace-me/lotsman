package zapret

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// luaFunctionDef matches a top-level `function name(` in a zapret2 lua script.
// Anchored at the start of a line so a nested or commented-out definition does
// not register a name the engine will never call.
var luaFunctionDef = regexp.MustCompile(`(?m)^function ([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// LuaFunctions indexes the desync functions the INSTALLED zapret2 actually
// defines, by reading its lua scripts.
//
// The alternative — a hardcoded list — is a trap with a long fuse. `--lua-desync`
// is NOT validated by the engine at startup: `--lua-desync=nosuchfunction` passes
// `--dry-run` on v0.9.5.2 and only fails to do anything useful in production, so
// a typo is a silent loss of desync. A table would catch today's typos and then
// quietly start DROPPING working strategies the next time the package is upgraded
// and adds a function. Reading the files is version-proof and costs one parse.
func LuaFunctions(luaDir string) (map[string]bool, error) {
	if luaDir == "" {
		return nil, fmt.Errorf("zapret: no lua dir given; cannot check desync function names")
	}
	entries, err := os.ReadDir(luaDir)
	if err != nil {
		return nil, fmt.Errorf("zapret: read lua dir: %w", err)
	}
	names := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lua") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(luaDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("zapret: read %s: %w", entry.Name(), err)
		}
		for _, match := range luaFunctionDef.FindAllStringSubmatch(string(data), -1) {
			names[match[1]] = true
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("zapret: no desync functions found in %s; is this a zapret2 install?", luaDir)
	}
	return names, nil
}

// LuaFunctionNames returns the indexed names, sorted, for a warning that says
// what the engine offers rather than only what was rejected.
func LuaFunctionNames(luaDir string) ([]string, error) {
	names, err := LuaFunctions(luaDir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}
