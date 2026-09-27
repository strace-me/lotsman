package zapret

import (
	"fmt"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategystore"
)

type Nfqws2Engine struct{}

func (Nfqws2Engine) Engine() string { return "nfqws2" }

func (Nfqws2Engine) Validate(b strategystore.Bundle) error {
	if b.Manifest.Engine != "nfqws2" {
		return fmt.Errorf("strategy %q is for engine %q, not nfqws2", b.Manifest.ID, b.Manifest.Engine)
	}
	hasAction := false
	for _, arg := range b.Args.Values {
		if strings.HasPrefix(arg, "--lua-desync=") {
			hasAction = true
		}
		for _, prefix := range []string{"--dpi-desync=", "--dpi-desync-fake-", "--dpi-desync-fooling=", "--dpi-desync-split-"} {
			if strings.HasPrefix(arg, prefix) {
				return fmt.Errorf("nfqws2 strategy %q uses nfqws1 argument %q", b.Manifest.ID, arg)
			}
		}
	}
	if !hasAction {
		return fmt.Errorf("nfqws2 strategy %q has no --lua-desync action", b.Manifest.ID)
	}
	return nil
}

func (Nfqws2Engine) Prepare(b strategystore.Bundle, opts strategystore.PrepareOptions) ([]string, error) {
	if err := (Nfqws2Engine{}).Validate(b); err != nil {
		return nil, err
	}
	if opts.QNum <= 0 {
		return nil, fmt.Errorf("nfqws2 strategy %q has no queue number", b.Manifest.ID)
	}
	args := []string{"--qnum=" + fmt.Sprint(opts.QNum)}
	if opts.Fwmark != 0 {
		args = append(args, fmt.Sprintf("--fwmark=0x%x", opts.Fwmark))
	}
	// The writeable dir is where zapret2 puts anything a LUA script writes
	// (pcap output, generated state). Without it a script that writes falls back
	// to a relative path resolved against the process CWD, which is wherever the
	// daemon happened to start. One dir is allowed, so it is a process setting
	// rather than a per-profile one.
	if opts.WriteableDir != "" {
		args = append(args, "--writeable="+opts.WriteableDir)
	}
	return append(args, b.Args.Values...), nil
}

// UnknownLuaFunctions reports the --lua-desync names a bundle calls that are NOT
// defined by any installed lua script.
//
// It reports rather than refuses, and that is a correction rather than a
// preference. zapret2 has desync functions compiled INTO THE BINARY as well as
// ones in its lua scripts: on v0.9.5.2 the lua dir defines 228 functions
// (fake, fakedsplit, hostfakesplit, multisplit, syndata, fakeddisorder…) and
// `fake_syndata` is not among them, yet `--lua-desync=fake_syndata` passes
// `--dry-run` and works. A guard that rejected everything outside the lua index
// would therefore refuse a working strategy — the same silent loss of desync it
// was written to prevent, just with a different trigger.
//
// The engine checks NEITHER: `--lua-desync=nosuchfunction` also passes
// `--dry-run`. So a typo is not catchable from configuration alone at all, and
// the only gate that actually proves a recipe is running it. This function exists
// to make the ambiguity visible ("this name is native, or it is a typo — run it
// and see"), not to decide.
func (Nfqws2Engine) UnknownLuaFunctions(b strategystore.Bundle, luaDir string) ([]string, error) {
	if err := (Nfqws2Engine{}).Validate(b); err != nil {
		return nil, err
	}
	known, err := LuaFunctions(luaDir)
	if err != nil {
		return nil, err
	}
	var unknown []string
	for _, arg := range b.Args.Values {
		raw, ok := strings.CutPrefix(arg, "--lua-desync=")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(raw, ":")
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("nfqws2 strategy %q has an empty --lua-desync", b.Manifest.ID)
		}
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	return unknown, nil
}
