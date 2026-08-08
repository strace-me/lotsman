package presetimport

import (
	"strings"
	"testing"
)

// A trimmed but structurally faithful ALT12: the `^` continuations, the quoted
// %LISTS%/%BIN% paths, the WinDivert capture line, the empty user lists and a
// game profile whose ports only exist once service.bat has run.
const sample = `@echo off
set "BIN=%~dp0bin\"
set "LISTS=%~dp0lists\"
start "zapret: %~n0" /min "%BIN%winws.exe" --wf-tcp=80,443,%GameFilterTCP% --wf-udp=443,%GameFilterUDP% ^
--filter-tcp=443 --hostlist="%LISTS%list-google.txt" --dpi-desync=hostfakesplit --dpi-desync-hostfakesplit-mod=host=www.google.com --new ^
--filter-tcp=80,443 --hostlist="%LISTS%list-general.txt" --hostlist="%LISTS%list-general-user.txt" --hostlist-exclude="%LISTS%list-exclude.txt" --dpi-desync=fake,multisplit --dpi-desync-fake-tls="%BIN%tls_clienthello_max_ru.bin" --new ^
--filter-tcp=%GameFilterTCP% --ipset="%LISTS%ipset-all.txt" --dpi-desync=fake
`

func TestFromBatKeepsTheProfilesAndTheirOwnLists(t *testing.T) {
	p, err := FromBat("ALT12", sample, Options{ListsDir: "/lists"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Args, " ")

	// The point of the whole mode: each profile keeps ITS OWN list. Collapsing them
	// onto one is what makes our per-rule rendering refuse ALT12 as self-shadowing.
	if !strings.Contains(got, "--hostlist=/lists/list-google.txt") ||
		!strings.Contains(got, "--hostlist=/lists/list-general.txt") {
		t.Errorf("the per-profile hostlists did not survive:\n%s", got)
	}
	if strings.Count(got, "--new") != 1 {
		t.Errorf("profile boundaries wrong, want one --new between two kept profiles:\n%s", got)
	}
	// Payloads resolve against the zapret files dir, as they do for a composed
	// strategy, so they must arrive bare.
	if !strings.Contains(got, "--dpi-desync-fake-tls=tls_clienthello_max_ru.bin") {
		t.Errorf("payload path not reduced to a basename:\n%s", got)
	}
	// And the desync arguments themselves are never interpreted.
	if !strings.Contains(got, "--dpi-desync-hostfakesplit-mod=host=www.google.com") {
		t.Errorf("a desync argument was altered:\n%s", got)
	}
}

// Three things cannot mean anything on this side, and each must be dropped
// LOUDLY: a silent drop is a preset that is not the preset, which is the entire
// failure this package exists to stop.
func TestFromBatRecordsEverythingItChanged(t *testing.T) {
	p, err := FromBat("ALT12", sample, Options{ListsDir: "/lists"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Args, " ")

	if strings.Contains(got, "--wf-tcp") || strings.Contains(got, "--wf-udp") {
		t.Error("the WinDivert capture survived; ours is derived from --filter-* into nft")
	}
	if strings.Contains(got, "-user.txt") {
		t.Error("an empty upstream user list survived — nfqws exits when a hostlist is missing")
	}
	if strings.Contains(got, "%") {
		t.Errorf("an unresolved Windows variable reached the argv:\n%s", got)
	}
	// The game profile is filtered only by a variable service.bat fills in, so it
	// cannot be rendered — and it must not be quietly turned into an unfiltered
	// profile, which would claim every packet the queue hands it.
	if strings.Contains(got, "--ipset=/lists/ipset-all.txt") {
		t.Error("the game profile was kept without its filter")
	}
	if len(p.Dropped) != 3 {
		t.Errorf("every change must be recorded, got %v", p.Dropped)
	}
	for _, want := range []string{"--wf-*", "-user.txt", "%GameFilter"} {
		if !strings.Contains(strings.Join(p.Dropped, " "), want) {
			t.Errorf("%q missing from the record: %v", want, p.Dropped)
		}
	}
}

func TestNameFromFile(t *testing.T) {
	for in, want := range map[string]string{
		"general (ALT12).bat":         "ALT12",
		"general (FAKE TLS AUTO).bat": "FAKE TLS AUTO",
		"general.bat":                 "general",
		"/tmp/x/general (SIMPLE).bat": "SIMPLE",
	} {
		if got := NameFromFile(in); got != want {
			t.Errorf("NameFromFile(%q) = %q, want %q", in, got, want)
		}
	}
}
