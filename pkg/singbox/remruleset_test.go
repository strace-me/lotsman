package singbox

import (
	"os"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/registry"
)

func TestRemRuleSetSourceArmed(t *testing.T) {
	got := string(RemRuleSetSource([]string{"YouTube.com", "googlevideo.com", "googlevideo.com"}, []string{"142.250.0.0/15", "8.8.8.8/32"}))
	for _, want := range []string{
		`"version": 2`,
		`"domain_suffix"`, `"googlevideo.com"`, `"youtube.com"`, // lowercased + deduped
		`"ip_cidr"`, `"8.8.8.8/32"`, `"142.250.0.0/15"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("armed rule_set missing %q in:\n%s", want, got)
		}
	}
}

func TestRemRuleSetSourceDisarmedEmpty(t *testing.T) {
	got := string(RemRuleSetSource(nil, nil))
	if !strings.Contains(got, `"rules": []`) {
		t.Errorf("disarmed rule_set must have empty rules (matches nothing):\n%s", got)
	}
}

func TestRemRuleSetSourceDeterministic(t *testing.T) {
	a := string(RemRuleSetSource([]string{"b.com", "a.com"}, []string{"2.2.2.2/32", "1.1.1.1/32"}))
	b := string(RemRuleSetSource([]string{"a.com", "b.com"}, []string{"1.1.1.1/32", "2.2.2.2/32"}))
	if a != b {
		t.Error("output must be deterministic (sorted) regardless of input order")
	}
}

func TestRemTagsAndPath(t *testing.T) {
	if RemRejectTag("youtube") != "rem-rq-youtube" || RemFallbackTag("youtube") != "rem-fb-youtube" {
		t.Error("rem tag naming wrong")
	}
	if RemFilePath("/etc/sing-box", "rem-rq-youtube") != "/etc/sing-box/rem-rq-youtube.json" {
		t.Error("rem file path wrong")
	}
}

func TestRemLocalDefs(t *testing.T) {
	if remLocalDefs(Options{}) != nil {
		t.Error("gate off => no local defs")
	}
	defs := remLocalDefs(Options{RemHotReload: true, RemServices: []string{"youtube"}, RemDir: "/etc/sing-box"})
	if len(defs) != 2 {
		t.Fatalf("want 2 local defs (rq+fb), got %d", len(defs))
	}
	m := defs[0].(map[string]any)
	if m["type"] != "local" || m["format"] != "source" || m["tag"] != "rem-rq-youtube" || m["path"] != "/etc/sing-box/rem-rq-youtube.json" {
		t.Errorf("local def wrong: %v", m)
	}
}

func TestEnsureRemFiles(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureRemFiles(dir, []string{"youtube"}); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"rem-rq-youtube", "rem-fb-youtube"} {
		b, err := os.ReadFile(RemFilePath(dir, tag))
		if err != nil {
			t.Fatalf("file %s not created: %v", tag, err)
		}
		if !strings.Contains(string(b), `"rules": []`) {
			t.Errorf("%s should be disarmed (empty rules): %s", tag, b)
		}
	}
	// idempotent + preserves an existing (armed) file
	armed := RemRuleSetSource([]string{"x.com"}, nil)
	os.WriteFile(RemFilePath(dir, "rem-rq-youtube"), armed, 0o644)
	EnsureRemFiles(dir, []string{"youtube"})
	b, _ := os.ReadFile(RemFilePath(dir, "rem-rq-youtube"))
	if !strings.Contains(string(b), "x.com") {
		t.Error("EnsureRemFiles must not clobber an existing armed file")
	}
}

func TestGenerateRemScaffoldGated(t *testing.T) {
	svc := registry.Service{Name: "youtube", Domains: []string{"youtube.com"}}
	off, _ := Generate([]registry.Service{svc}, nil, nil, nil, DefaultOptions())
	if strings.Contains(string(off.JSON), "rem-rq-youtube") {
		t.Error("gate off must NOT emit the rem scaffold")
	}
	o := DefaultOptions()
	o.RemHotReload = true
	o.RemServices = []string{"youtube"}
	on, err := Generate([]registry.Service{svc}, nil, nil, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	js := string(on.JSON)
	for _, want := range []string{"rem-rq-youtube", "rem-fb-youtube", `"action": "reject"`, `"type": "local"`} {
		if !strings.Contains(js, want) {
			t.Errorf("gate on must emit %q", want)
		}
	}
}
