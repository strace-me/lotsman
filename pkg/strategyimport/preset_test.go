package strategyimport

import (
	"strings"
	"testing"
)

// Structurally faithful ALT12: `^` continuations, `set` assignments whose values
// keep %~dp0, quoted %LISTS%/%BIN% paths inside the argument, the WinDivert
// capture, an empty user list, and a game profile whose ports only exist once
// service.bat has run.
const batSample = `@echo off
set "BIN=%~dp0bin\"
set "LISTS=%~dp0lists\"
start "zapret: %~n0" /min "%BIN%winws.exe" --wf-tcp=80,443,%GameFilterTCP% --wf-udp=443,%GameFilterUDP% ^
--filter-tcp=443 --hostlist="%LISTS%list-google.txt" --dpi-desync=hostfakesplit --dpi-desync-hostfakesplit-mod=host=www.google.com --new ^
--filter-tcp=80,443 --hostlist="%LISTS%list-general.txt" --hostlist="%LISTS%list-general-user.txt" --hostlist-exclude="%LISTS%list-exclude.txt" --dpi-desync=fake,multisplit --dpi-desync-fake-tls="%BIN%tls_clienthello_max_ru.bin" --new ^
--filter-tcp=%GameFilterTCP% --ipset="%LISTS%ipset-all.txt" --dpi-desync=fake
`

func presetSource() Source {
	return Source{Name: "Flowseal 1.10.0, general (ALT12).bat", Kind: KindBat, Body: []byte(batSample)}
}

// The whole reason preset mode exists: each profile keeps ITS OWN list. Collapsed
// onto one — which is what a per-rule rendering does — the second profile can
// never match and the recipe is refused as self-shadowing.
func TestAsPresetKeepsThePerProfileLists(t *testing.T) {
	p, err := AsPreset("ALT12", presetSource(), PresetOptions{ListsDir: "/lists"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Args, " ")
	for _, want := range []string{
		"--hostlist=/lists/list-google.txt",
		"--hostlist=/lists/list-general.txt",
		"--hostlist-exclude=/lists/list-exclude.txt",
		// Payloads resolve against the zapret files dir, as they do for a composed
		// strategy, so they arrive bare.
		"--dpi-desync-fake-tls=tls_clienthello_max_ru.bin",
		// And no desync argument is ever interpreted.
		"--dpi-desync-hostfakesplit-mod=host=www.google.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Three profiles survive: the two hostlist ones and the game one, pinned to the
	// port upstream assigns when its filter is off.
	if n := strings.Count(got, "--new"); n != 2 {
		t.Errorf("want two separators between three kept profiles, got %d:\n%s", n, got)
	}
	if p.Lists == nil || len(p.Payloads) == 0 {
		t.Errorf("the file must declare what it needs on disk: lists=%v payloads=%v", p.Lists, p.Payloads)
	}
}

// A .bat escapes `!` as `^!` (delayed expansion). nfqws reads `^!` as a filename
// and refuses to start ("could not read ^!"), so a preset that kept the caret lost
// its only real-ClientHello profile whole (LOT-82). The caret escape must be stripped.
func TestAsPresetStripsBatchCaretEscapes(t *testing.T) {
	const bat = `@echo off
start "zapret: %~n0" /min "%BIN%winws.exe" --filter-tcp=443 --dpi-desync=fake,multidisorder --dpi-desync-fake-tls=0x00000000 --dpi-desync-fake-tls=^! --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com
`
	p, err := AsPreset("FAKE TLS AUTO", Source{Name: "x.bat", Kind: KindBat, Body: []byte(bat)}, PresetOptions{ListsDir: "/lists"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Args, " ")
	if strings.Contains(got, "^!") {
		t.Errorf("the batch caret survived into argv (nfqws would read ^! as a file):\n%s", got)
	}
	if !strings.Contains(got, "--dpi-desync-fake-tls=!") {
		t.Errorf("expected a bare `!` (real-ClientHello marker):\n%s", got)
	}
}

// Three things cannot mean anything here, and each must be dropped LOUDLY — a
// silent drop yields a preset that is not the preset, which is the failure this
// mode exists to end.
func TestAsPresetRecordsEverythingItChanged(t *testing.T) {
	p, err := AsPreset("ALT12", presetSource(), PresetOptions{ListsDir: "/lists"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Args, " ")
	if strings.Contains(got, "--wf-") {
		t.Error("the WinDivert capture survived; ours is derived from --filter-* into nft")
	}
	if strings.Contains(got, "-user.txt") {
		t.Error("an empty upstream user list survived — nfqws exits on a missing hostlist")
	}
	if strings.ContainsAny(got, `%"`) {
		t.Errorf("an unresolved variable or stray quote reached the argv:\n%s", got)
	}
	joined := strings.Join(p.Dropped, " ")
	for _, want := range []string{"--wf-*", "-user.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q missing from the record: %v", want, p.Dropped)
		}
	}
	// And the record reaches the file, where the person reading the argv will see
	// it, rather than staying in a commit message.
	if !strings.Contains(RenderPreset(p, "test"), "CHANGED FROM UPSTREAM") {
		t.Error("the rendered file does not say what was changed")
	}
}

// The game profile is KEPT, with the port service.bat assigns when the filter is
// off — which is how every release ships. Dropping it would have made our file
// quietly differ from the thing preset mode promises to run verbatim, and the
// difference would have been invisible: nothing listens on port 12 either way.
func TestTheGameProfileIsPinnedToTheShippedDefaultNotDropped(t *testing.T) {
	p, err := AsPreset("ALT12", presetSource(), PresetOptions{ListsDir: "/lists"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Args, " ")
	if !strings.Contains(got, "--filter-tcp=12") {
		t.Errorf("the game profile was lost instead of pinned to the default:\n%s", got)
	}
	if !strings.Contains(got, "ipset-all.txt") {
		t.Error("the game profile's own ipset went with it")
	}
	if n := strings.Count(got, "--new"); n != 2 {
		t.Errorf("want three profiles kept, got %d separators:\n%s", n, got)
	}
	// A substitution is not a removal and must not be reported as one.
	if strings.Contains(strings.Join(p.Dropped, " "), "GameFilter") {
		t.Errorf("a pinned default was filed as a drop: %v", p.Dropped)
	}
	if !strings.Contains(strings.Join(p.Substituted, " "), "GameFilter") {
		t.Errorf("the substitution went unrecorded: %v", p.Substituted)
	}
	if !strings.Contains(RenderPreset(p, "t"), "substituted %GameFilter") {
		t.Error("the rendered file does not say the value was pinned")
	}
}

func TestPresetName(t *testing.T) {
	for in, want := range map[string]string{
		"general (ALT12).bat":         "ALT12",
		"general (FAKE TLS AUTO).bat": "FAKE TLS AUTO",
		"general.bat":                 "general",
		"/tmp/x/general (SIMPLE).bat": "SIMPLE",
	} {
		if got := PresetName(in); got != want {
			t.Errorf("PresetName(%q) = %q, want %q", in, got, want)
		}
	}
}
