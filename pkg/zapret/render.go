package zapret

import (
	"fmt"
	"strings"

	"github.com/strace-me/lotsman/pkg/strategy"
	"github.com/strace-me/lotsman/pkg/strategystore"
)

// DefaultNfqwsPath is the nfqws binary on the R5S (Flowseal layout).
const DefaultNfqwsPath = "/opt/zapret/binaries/linux-arm64/nfqws"

// LauncherOpts scopes a discovered strategy to a service when rendering its
// launcher. blockcheck finds a bare nfqws profile (the desync args); the scope
// — which ports and domains it applies to — is an apply-time decision the
// caller makes (the failing service's ports/domains), not part of the strategy.
type LauncherOpts struct {
	NfqwsPath       string   // nfqws binary; empty = DefaultNfqwsPath
	FilterTCP       string   // e.g. "80,443"; empty = no TCP filter
	FilterUDP       string   // e.g. "443"; empty = no UDP filter
	HostlistDomains []string // scope to these domains (joined into one --hostlist-domains)
	// Engine names the desync engine this launcher runs, so the flags are spelled
	// the way THAT engine spells them. Empty = nfqws1, unchanged.
	Engine string
	// Fwmark pins the mark the engine stamps on injected packets. On nfqws1 that
	// is --dpi-desync-fwmark, on nfqws2 it is --fwmark, and the difference is not
	// cosmetic: the bypass rule that keeps those packets out of the tunnel matches
	// on the mark, so a launcher that omits it falls back to a build default that
	// the rule was never written for.
	Fwmark int
	// WriteableDir is zapret2's --writeable, if the engine wants one.
	WriteableDir string
	// Adapters resolves Engine; nil uses the default registry.
	Adapters map[string]strategystore.EngineAdapter
}

// engineArgs renders a strategy's argv through the same adapter the desktop path
// uses, so the router and the client cannot drift on spelling.
//
// The queue is dropped from the result because the launcher takes it as $1 at run
// time: baking it in would freeze a value the executor passes per invocation.
// Everything else the adapter adds — nfqws1's --dpi-desync-fwmark, nfqws2's
// --fwmark and --writeable — is kept, because those are exactly what the router
// was missing.
func (o LauncherOpts) engineArgs(args []string) ([]string, error) {
	engine := o.Engine
	if engine == "" {
		engine = NfqwsEngine{}.Engine()
	}
	adapters := o.Adapters
	if adapters == nil {
		adapters = StrategyEngineAdapters()
	}
	adapter, ok := adapters[engine]
	if !ok {
		return nil, fmt.Errorf("render: unknown desync engine %q", engine)
	}
	bundle := strategystore.Bundle{
		Manifest: strategystore.Manifest{ID: "launcher", Engine: engine, Args: "command.args"},
		Args:     strategystore.Args{Values: args},
	}
	full, err := adapter.Prepare(bundle, strategystore.PrepareOptions{QNum: 1, Fwmark: o.Fwmark, WriteableDir: o.WriteableDir})
	if err != nil {
		return nil, err
	}
	// Drop the leading --qnum: the script supplies its own at run time.
	var out []string
	for _, arg := range full {
		if strings.HasPrefix(arg, "--qnum=") {
			continue
		}
		out = append(out, arg)
	}
	return out, nil
}

// RenderLauncher renders a discovered single-profile strategy (def.NFQWSArgs)
// into an nfqws launcher script in the same shape as Flowseal's <id>.sh: takes
// the queue number as $1, execs nfqws with the scoped filter, the target
// domains, and the discovered desync args. The executor symlinks this as active
// and restarts nfqws, exactly as for a pre-provisioned strategy.
//
// It errors if the definition has no NFQWSArgs (a script-backed strategy needs
// no rendering) or if no filter scope is given (an unscoped profile would
// desync all traffic).
func RenderLauncher(def strategy.Definition, opts LauncherOpts) (string, error) {
	if len(def.NFQWSArgs) == 0 {
		return "", fmt.Errorf("render %q: no NFQWSArgs (script-backed strategy)", def.ID)
	}
	if opts.FilterTCP == "" && opts.FilterUDP == "" {
		return "", fmt.Errorf("render %q: no filter scope (FilterTCP/FilterUDP both empty)", def.ID)
	}
	nfqws := opts.NfqwsPath
	if nfqws == "" {
		nfqws = DefaultNfqwsPath
	}

	var profile []string
	if opts.FilterTCP != "" {
		profile = append(profile, "--filter-tcp="+opts.FilterTCP)
	}
	if opts.FilterUDP != "" {
		profile = append(profile, "--filter-udp="+opts.FilterUDP)
	}
	if len(opts.HostlistDomains) > 0 {
		profile = append(profile, "--hostlist-domains="+strings.Join(opts.HostlistDomains, ","))
	}
	profile = append(profile, def.NFQWSArgs...)

	argv, err := opts.engineArgs(profile)
	if err != nil {
		return "", err
	}
	return renderShell(nfqws, argv, fmt.Sprintf("# Lotsman-discovered strategy %q. %s", def.ID, def.Notes))
}

// renderShell writes the launcher every generated strategy has the shape of: the
// queue is a RUN-TIME parameter ($1), and the engine-specific flags come from the
// adapter rather than from a hardcoded spelling. The desktop's Engine renders the
// same flags through the same adapter, so the two deployments cannot drift.
func renderShell(nfqwsPath string, argv []string, comment string) (string, error) {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "# %s\n", comment)
	b.WriteString("# Generated by Lotsman — do not edit by hand. Usage: <id>.sh [qnum].\n")
	b.WriteString("QNUM=\"${1:-200}\"\n")
	fmt.Fprintf(&b, "N=%s\n", nfqwsPath)
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, shellQuote(arg))
	}
	fmt.Fprintf(&b, "exec \"$N\" --qnum=\"$QNUM\" %s\n", strings.Join(quoted, " "))
	return b.String(), nil
}

// shellQuote single-quotes one argv element for /bin/sh.
//
// This is not a style choice. nfqws2 ranges carry `<` and `>` literally —
// `--out-range=s1<d1` is a valid argument to the engine and a REDIRECT to a shell —
// and the router runs a generated .sh, so an unquoted join turns a working
// strategy into a launcher that dies on the redirect (or, given a domain list, into
// something that expands). Quoting is what lets the composed argv reach the engine
// exactly as the adapter produced it.
func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	// Everything outside this set is shell-safe unquoted; quoting anyway would be
	// correct but needlessly noisy in a file operators are expected to read.
	if !strings.ContainsAny(arg, " \t\n'\"\\$`;&|<>()*?!#~^[]{},") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// RenderComposed renders the COMPOSED multi-block launcher (LOT-10b): the nfqws
// args produced by nfqwsgen.Compose (one --new block per zapret-active service,
// each already carrying its own --filter/--hostlist-domains) wrapped in the same
// <id>.sh shape the executor symlinks and runs. nfqwsPath empty = DefaultNfqwsPath.
// It errors on empty args (nothing to run — the caller should keep the existing
// config instead). Unlike RenderLauncher this takes pre-composed args, so the
// filter scope lives in each recipe, not in opts.
func RenderComposed(args []string, nfqwsPath string, opts ...LauncherOpts) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("render composed: no args (nothing to desync)")
	}
	nfqws := nfqwsPath
	if nfqws == "" {
		nfqws = DefaultNfqwsPath
	}
	var options LauncherOpts
	if len(opts) > 0 {
		options = opts[0]
		if options.NfqwsPath != "" {
			nfqws = options.NfqwsPath
		}
	}
	argv, err := options.engineArgs(args)
	if err != nil {
		return "", err
	}
	return renderShell(nfqws, argv, "Lotsman composed strategy (per-service --new blocks).")
}
