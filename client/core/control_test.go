package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/audit"
	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/configv2"
)

// shortTempDir keeps the socket path under the sockaddr_un limit: the default
// temp dir on macOS is long enough on its own to blow past it.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// unixClient dials the control socket the way a tray would.
func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
}

func startControl(t *testing.T, stop func()) (string, *ControlServer) {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "control.sock")
	s := NewControlServer(&Core{}, stop, slog.New(slog.DiscardHandler))
	if err := s.Serve(path); err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return path, s
}

func TestControlStatusIsServedOverTheSocket(t *testing.T) {
	path, _ := startControl(t, func() {})

	resp, err := unixClient(path).Get("http://unix/status")
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", resp.StatusCode)
	}
	var got Status
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A Core that never Started reports not-running rather than erroring, so a UI
	// attaching early gets an answer instead of a broken pipe.
	if got.Running {
		t.Error("a core that has not started must not report running")
	}
}

func TestControlStopIsAcknowledgedBeforeTeardown(t *testing.T) {
	stopped := make(chan struct{})
	path, _ := startControl(t, func() { close(stopped) })

	resp, err := unixClient(path).Post("http://unix/stop", "", nil)
	if err != nil {
		t.Fatalf("post stop: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status code = %d, want 202", resp.StatusCode)
	}
	// The acknowledgement must arrive intact — the caller should not see a dropped
	// connection because teardown raced the response.
	if body, _ := io.ReadAll(resp.Body); len(body) == 0 {
		t.Error("stop must acknowledge before tearing down")
	}
	<-stopped // the callback still fires
}

func TestControlSocketIsPrivateAndReplacesAStaleOne(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "control.sock")
	// A process killed with -9 leaves the socket file behind; binding must not
	// fail because of it.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed stale socket: %v", err)
	}

	s := NewControlServer(&Core{}, func() {}, slog.New(slog.DiscardHandler))
	if err := s.Serve(path); err != nil {
		t.Fatalf("serve over a stale socket: %v", err)
	}
	defer s.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perms = %o, want 0600 — a writable socket hands over the tunnel", perm)
	}
}

func TestControlSocketGroupOpensItTo0660(t *testing.T) {
	// Chowning to a group the caller already belongs to is allowed unprivileged, so
	// drive the test with the caller's own primary group — no root, no CI setup.
	u, err := user.Current()
	if err != nil {
		t.Skipf("user.Current: %v", err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Skipf("own group name: %v", err)
	}

	dir := shortTempDir(t)
	path := filepath.Join(dir, "control.sock")
	s := NewControlServer(&Core{}, func() {}, slog.New(slog.DiscardHandler)).WithSocketGroup(g.Name)
	if err := s.Serve(path); err != nil {
		t.Fatalf("serve with group %q: %v", g.Name, err)
	}
	defer s.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o660 {
		t.Errorf("socket perms = %o, want 0660 so a group member can drive it", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o750 {
		t.Errorf("socket dir perms = %o, want 0750 so the group can traverse to the socket", perm)
	}
	assertSocketGID(t, fi, u.Gid)
}

func TestControlSocketGroupThatDoesNotExistFailsLoudly(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "control.sock")
	s := NewControlServer(&Core{}, func() {}, slog.New(slog.DiscardHandler)).
		WithSocketGroup("lotsman-no-such-group-xyzzy")
	if err := s.Serve(path); err == nil {
		s.Close()
		t.Fatal("serve with an unknown group succeeded; want a loud failure, not a silent fallback")
	}
}

func TestControlCloseRemovesTheSocket(t *testing.T) {
	path, s := startControl(t, func() {})
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Close must remove the socket so the next start binds cleanly")
	}
}

func TestControlEventsIsServedOverTheSocket(t *testing.T) {
	path, _ := startControl(t, func() {})

	resp, err := unixClient(path).Get("http://unix/events")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", resp.StatusCode)
	}
	// A core that never started has no history, but the endpoint still answers
	// cleanly rather than erroring — a UI attaching early gets an empty list.
	var got []audit.Transition
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestControlRecheckRejectsUnknownService(t *testing.T) {
	path, _ := startControl(t, func() {})

	resp, err := unixClient(path).Post("http://unix/service/nope/recheck", "", nil)
	if err != nil {
		t.Fatalf("post recheck: %v", err)
	}
	defer resp.Body.Close()
	// A never-started core has no probing engine; a recheck must be refused with a
	// 400, not panic on the nil registry.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status code = %d, want 400", resp.StatusCode)
	}
}

// A minimal deployment that LOADS. Two of these things are load-bearing rather than
// decoration: the document declares `version: 2`, because a file without it is a
// version-1 document and is refused with a conversion hint; and the rule's chain names
// pools it does not declare, because that is what the builtins are for and a starter
// that had to hand-write them would be a worse first impression.
const validStarterDocument = `version: 2
target_version: "1.14.1"
subscriptions:
  - { name: s1, url: "https://example.com/sub", format: auto, tags: [normal], enabled: true }
desync:
  instances:
    nfqws1-main: { engine: nfqws1, enabled: true, capture: { tcp: [443] } }
pools: {}
`

func youtubeRule() string {
	return `id: youtube
name: YouTube
enabled: true
match:
  domains: [youtube.com]
probe:
  type: http
  target: https://www.youtube.com/generate_204
chain:
  - {tier: preferred, desync: {instance: nfqws1-main, strategy: auto}}
  - {tier: fallback, vpn: {pool: vpn_url_test}}
`
}

func discordRule() string {
	return `id: discord
name: Discord
enabled: true
match:
  domains: [discord.com]
probe:
  type: http
  target: https://discord.com/api/v9/gateway
chain:
  - {tier: fallback, vpn: {pool: vpn_url_test_udp}}
`
}

func TestControlConfigEditingDisabledByDefault(t *testing.T) {
	path, _ := startControl(t, func() {})
	resp, err := unixClient(path).Get("http://unix/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("GET /config without WithConfig = %d, want 501", resp.StatusCode)
	}
}

func TestControlConfigValidate(t *testing.T) {
	dir := seedDeployment(t, validStarterDocument, map[string]string{"youtube.yaml": youtubeRule()})
	post := func(doc string) int {
		body, _ := json.Marshal(map[string]string{"yaml": doc})
		resp, err := unixClient(dir.sock).Post("http://unix/config/validate", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post validate: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("version: 2\nthis: [is not: valid"); code != http.StatusBadRequest {
		t.Errorf("an invalid document validate = %d, want 400", code)
	}
	// ABSENT rules means "I am not touching the rules", so a document-only candidate is
	// validated against the LIVE ones. It must pass — and it must not have deleted
	// anything, because a request that only meant to change the document is not a
	// request to remove every rule the operator has.
	if code := post(validStarterDocument); code != http.StatusOK {
		t.Errorf("a document-only candidate over a deployment with rules = %d, want 200", code)
	}
	if _, err := os.Stat(filepath.Join(dir.rulesDir, "youtube.yaml")); err != nil {
		t.Errorf("validate deleted a rule file: %v", err)
	}
	body, _ := json.Marshal(map[string]any{"yaml": validStarterDocument, "rules": []any{
		map[string]string{"file": "youtube.yaml", "yaml": youtubeRule()},
	}})
	resp, err := unixClient(dir.sock).Post("http://unix/config/validate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post validate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a complete candidate validate = %d, want 200", resp.StatusCode)
	}
}

func TestControlConfigGetReturnsTheDocumentAndEveryRule(t *testing.T) {
	dir := seedDeploymentNoServer(t, validStarterDocument, map[string]string{"youtube.yaml": youtubeRule()})
	dir.sock, dir.server = startControl(t, func() {})
	dir.server.WithConfig(dir.configPath, dir.rulesDir, "", func() {})

	resp, err := unixClient(dir.sock).Get("http://unix/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /config = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Path     string
		RulesDir string
		YAML     string
		Config   *configv2.Configuration
		Rules    []struct {
			File string
			Rule *configv2.Rule
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Path != dir.configPath || got.YAML != validStarterDocument {
		t.Errorf("config mismatch: path=%q rulesDir=%q yaml-len=%d", got.Path, got.RulesDir, len(got.YAML))
	}
	if got.Config == nil || got.Config.Version != 2 {
		t.Errorf("the structured document did not decode: %#v", got.Config)
	}
	// The rules come back as their own entries, each with the file it belongs under —
	// the UI never has to invent a path, and never has to guess which rule a
	// structural edit was meant to touch.
	if len(got.Rules) != 1 || got.Rules[0].File != "youtube.yaml" || got.Rules[0].Rule == nil {
		t.Fatalf("rules = %#v", got.Rules)
	}
	if got.Rules[0].Rule.ID != "youtube" {
		t.Errorf("rule id = %q", got.Rules[0].Rule.ID)
	}
}

// twoRuleDeployment has a spare rule, so switching one off still leaves something to
// steer — the all-off case is exercised separately.
const twoRuleDocument = validStarterDocument

type deployment struct {
	configPath string
	rulesDir   string
	sock       string
	server     *ControlServer
}

// seedDeploymentNoServer writes a deployment and returns where it is, WITHOUT
// starting a control server: some tests attach one afterwards with their own apply
// signal, and some need the server up before they can post.
func seedDeploymentNoServer(t *testing.T, document string, rules map[string]string) deployment {
	t.Helper()
	dir := shortTempDir(t)
	d := deployment{
		configPath: filepath.Join(dir, "configuration.yaml"),
		rulesDir:   filepath.Join(dir, "rules"),
	}
	if err := os.WriteFile(d.configPath, []byte(document), 0o600); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := os.MkdirAll(d.rulesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range rules {
		if err := os.WriteFile(filepath.Join(d.rulesDir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("seed rule: %v", err)
		}
	}
	return d
}

func seedDeployment(t *testing.T, document string, rules map[string]string) deployment {
	t.Helper()
	d := seedDeploymentNoServer(t, document, rules)
	d.sock, d.server = startControl(t, func() {})
	d.server.WithConfig(d.configPath, d.rulesDir, "", func() {})
	return d
}

// load reads a seeded deployment back through the one reader, so a test asserts what a
// SERVICE would see rather than what a text file appears to say.
func (d deployment) load(t *testing.T) *config.Config {
	t.Helper()
	load, err := config.LoadV2(d.configPath, d.rulesDir, "")
	if err != nil {
		t.Fatalf("the written deployment does not load: %v", err)
	}
	cfg, _, err := config.FromV2(load.Config, load.Rules)
	if err != nil {
		t.Fatalf("the written deployment does not project: %v", err)
	}
	return cfg
}

func postEnabled(t *testing.T, sock, rule string, body any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := unixClient(sock).Post("http://unix/service/"+rule+"/enabled", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post enabled: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestControlSwitchingARuleOffWritesItToTheFile(t *testing.T) {
	d := seedDeployment(t, twoRuleDocument, map[string]string{
		"youtube.yaml": youtubeRule(),
		"discord.yaml": discordRule(),
	})

	if code := postEnabled(t, d.sock, "discord", map[string]bool{"enabled": false}); code != http.StatusAccepted {
		t.Fatalf("POST enabled=false = %d, want 202", code)
	}
	// The bit must be in the FILE, not merely in memory: the desired state is
	// re-asserted from the config-built registry every few seconds, so a runtime-only
	// override would be undone within a tick and would not survive a restart.
	cfg := d.load(t)
	if _, ok := cfg.Registry.Services["discord"]; ok {
		t.Error("discord is still in the registry after being switched off")
	}
	if _, ok := cfg.Registry.Services["youtube"]; !ok {
		t.Error("switching discord off took youtube with it")
	}
	if len(cfg.DisabledServices) != 1 || cfg.DisabledServices[0] != "discord" {
		t.Errorf("disabled = %v, want [discord]", cfg.DisabledServices)
	}

	// And back on. Switching ON removes the key rather than writing `enabled: true`,
	// so a rule nobody ever touched reads as if it never had been.
	if code := postEnabled(t, d.sock, "discord", map[string]bool{"enabled": true}); code != http.StatusAccepted {
		t.Fatalf("POST enabled=true = %d, want 202", code)
	}
	if _, ok := d.load(t).Registry.Services["discord"]; !ok {
		t.Error("discord did not come back")
	}
}

func TestControlSwitchingKeepsCommentsInTheRuleFile(t *testing.T) {
	// A tile toggle is a one-bit change. Rewriting the whole file for it would expand
	// every zero value the operator never wrote and drop every comment they did — a
	// switch on the dashboard must not reformat the file behind it.
	commented := "# the operator's own note\n" + strings.Replace(youtubeRule(), "enabled: true", "enabled: true\n# keep me", 1)
	// A spare rule, because switching the last one off is refused by name — and this
	// test is about what the rewrite does to the file, not about that refusal.
	d := seedDeployment(t, twoRuleDocument, map[string]string{
		"youtube.yaml": commented,
		"discord.yaml": discordRule(),
	})
	if code := postEnabled(t, d.sock, "youtube", map[string]bool{"enabled": false}); code != http.StatusAccepted {
		t.Fatalf("POST enabled=false = %d, want 202", code)
	}
	body, err := os.ReadFile(filepath.Join(d.rulesDir, "youtube.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"the operator's own note", "keep me"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the toggle dropped the comment %q:\n%s", want, body)
		}
	}
}

func TestControlSwitchingRejectsUnknownRuleAndMissingField(t *testing.T) {
	d := seedDeployment(t, twoRuleDocument, map[string]string{
		"youtube.yaml": youtubeRule(),
		"discord.yaml": discordRule(),
	})
	before, _ := os.ReadFile(filepath.Join(d.rulesDir, "youtube.yaml"))

	if code := postEnabled(t, d.sock, "nosuch", map[string]bool{"enabled": false}); code != http.StatusBadRequest {
		t.Errorf("an unknown rule = %d, want 400", code)
	}
	// An absent field is refused rather than defaulted, because defaulting here switches
	// a rule the wrong way and the caller is never told.
	if code := postEnabled(t, d.sock, "discord", map[string]string{"note": "hi"}); code != http.StatusBadRequest {
		t.Errorf("a missing enabled field = %d, want 400", code)
	}
	after, _ := os.ReadFile(filepath.Join(d.rulesDir, "youtube.yaml"))
	if !bytes.Equal(before, after) {
		t.Error("a rejected request rewrote a rule file")
	}
}

// Validation runs over the WHOLE candidate deployment before any file is touched, so
// switching off the last rule fails with the files still intact.
func TestControlRefusesToSwitchOffTheLastRule(t *testing.T) {
	d := seedDeployment(t, validStarterDocument, map[string]string{"youtube.yaml": youtubeRule()})
	beforeDoc, _ := os.ReadFile(d.configPath)
	beforeRule, _ := os.ReadFile(filepath.Join(d.rulesDir, "youtube.yaml"))

	if code := postEnabled(t, d.sock, "youtube", map[string]bool{"enabled": false}); code != http.StatusBadRequest {
		t.Errorf("switching off the only rule = %d, want 400", code)
	}
	if afterDoc, _ := os.ReadFile(d.configPath); !bytes.Equal(beforeDoc, afterDoc) {
		t.Error("the document was written despite failing validation")
	}
	if afterRule, _ := os.ReadFile(filepath.Join(d.rulesDir, "youtube.yaml")); !bytes.Equal(beforeRule, afterRule) {
		t.Error("the rule was written despite failing validation")
	}
}

func TestControlSwitchingNeedsConfigEditingEnabled(t *testing.T) {
	sock, _ := startControl(t, func() {})
	if code := postEnabled(t, sock, "youtube", map[string]bool{"enabled": false}); code != http.StatusNotImplemented {
		t.Errorf("without WithConfig = %d, want 501", code)
	}
}

// A rule deleted in the editor is deleted in the DOCUMENT but not on disk, and the
// loader reads a DIRECTORY — so the service kept steering the traffic the operator
// asked it to stop steering, while the editor showed the rule as gone.
func TestControlSetConfigRemovesARuleDroppedFromTheCandidate(t *testing.T) {
	dir := seedDeployment(t, validStarterDocument, map[string]string{
		"youtube.yaml": youtubeRule(),
		"discord.yaml": discordRule(),
	})

	body, _ := json.Marshal(map[string]any{
		"yaml": validStarterDocument,
		"rules": []any{
			map[string]string{"file": "youtube.yaml", "yaml": youtubeRule()},
		},
	})
	resp, err := unixClient(dir.sock).Post("http://unix/config", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("set config = %d, want 202", resp.StatusCode)
	}

	if _, err := os.Stat(filepath.Join(dir.rulesDir, "discord.yaml")); err == nil {
		t.Error("a rule the candidate dropped is still on disk")
	}
	// The survivors must survive: a commit that emptied the directory would pass the
	// assertion above.
	cfg := dir.load(t)
	if _, ok := cfg.Registry.Services["youtube"]; !ok {
		t.Error("the rule that was kept did not survive the commit")
	}
	if _, ok := cfg.Registry.Services["discord"]; ok {
		t.Error("the dropped rule is still in the loaded runtime config")
	}
}

// An explicit empty list is how the UI says "delete them all", and it must actually
// empty the directory rather than leaving it as a cache nobody reads.
func TestControlSetConfigEmptiesTheRulesDirectoryForAnEmptyList(t *testing.T) {
	dir := seedDeployment(t, validStarterDocument, map[string]string{
		"youtube.yaml": youtubeRule(),
		"discord.yaml": discordRule(),
	})

	body, _ := json.Marshal(map[string]any{"yaml": validStarterDocument, "rules": []any{}})
	resp, err := unixClient(dir.sock).Post("http://unix/config", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("set config with an explicit empty list = %d, want 202", resp.StatusCode)
	}
	names, err := ruleFilesIn(dir.rulesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Errorf("explicit [] left %d rule files: %v", len(names), names)
	}
}

// A file that is not a plain rule is a thing to notice, not to remove on the
// operator's behalf.
func TestControlSetConfigLeavesNonRuleFilesAlone(t *testing.T) {
	dir := seedDeployment(t, validStarterDocument, map[string]string{"youtube.yaml": youtubeRule()})
	notes := filepath.Join(dir.rulesDir, "NOTES.md")
	if err := os.WriteFile(notes, []byte("scratch"), 0o600); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"yaml":  validStarterDocument,
		"rules": []any{},
	})
	resp, err := unixClient(dir.sock).Post("http://unix/config", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post config: %v", err)
	}
	defer resp.Body.Close()
	if _, err := os.Stat(notes); err != nil {
		t.Errorf("a non-rule file was removed by the commit: %v", err)
	}
}

// A JSON response that arrives as application/octet-stream makes the frontend
// treat it as text, which is a bug report that reads as "the server is broken"
// rather than as "a header was set one line too late".
func TestControlConfigResponsesCarryTheirContentType(t *testing.T) {
	dir := seedDeployment(t, validStarterDocument, map[string]string{"youtube.yaml": youtubeRule()})
	body, _ := json.Marshal(map[string]any{
		"yaml":  validStarterDocument,
		"rules": []any{map[string]string{"file": "youtube.yaml", "yaml": youtubeRule()}},
	})
	resp, err := unixClient(dir.sock).Post("http://unix/config", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("POST /config Content-Type = %q, want application/json", ct)
	}

	get, err := unixClient(dir.sock).Get("http://unix/config")
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	if ct := get.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET /config Content-Type = %q, want application/json", ct)
	}

	d2 := seedDeployment(t, twoRuleDocument, map[string]string{
		"youtube.yaml": youtubeRule(),
		"discord.yaml": discordRule(),
	})
	if code := postEnabled(t, d2.sock, "youtube", map[string]bool{"enabled": false}); code != http.StatusAccepted {
		t.Fatalf("toggle = %d, want 202", code)
	}
	resp2, err := unixClient(d2.sock).Post("http://unix/service/youtube/enabled", "application/json",
		bytes.NewReader([]byte(`{"enabled":true}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if ct := resp2.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("POST /service/{name}/enabled Content-Type = %q, want application/json", ct)
	}
}

// -config accepts a deployment DIRECTORY, and the NixOS module now uses that shape
// because it is what the v2 schema is built around. The editor read the path as a
// file, so GET /config answered 500 "is a directory" — the config editor was broken
// in precisely the deployment the installer writes.
func TestControlConfigEditingWorksOnADirectoryDeployment(t *testing.T) {
	d := seedDeploymentNoServer(t, validStarterDocument, map[string]string{"youtube.yaml": youtubeRule()})
	dir := filepath.Dir(d.configPath)

	sock, server := startControl(t, func() {})
	// The directory, exactly as `-config /etc/lotsman` passes it.
	server.WithConfig(dir, "", "", func() {})
	defer server.Close()

	resp, err := unixClient(sock).Get("http://unix/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /config on a directory deployment = %d, want 200", resp.StatusCode)
	}
	var got configDoc
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.YAML == "" {
		t.Error("no document text came back")
	}
	if len(got.Rules) != 1 {
		t.Errorf("got %d rules, want 1", len(got.Rules))
	}
	// And the rules directory must have been derived, since the caller passed none.
	if got.RulesDir == "" || filepath.Base(got.RulesDir) != "rules" {
		t.Errorf("rules dir %q was not derived from the deployment directory", got.RulesDir)
	}
}

// The document path itself must keep working — the other accepted spelling.
func TestControlConfigEditingStillWorksOnADocumentPath(t *testing.T) {
	d := seedDeployment(t, validStarterDocument, map[string]string{"youtube.yaml": youtubeRule()})
	resp, err := unixClient(d.sock).Get("http://unix/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /config on a document path = %d, want 200", resp.StatusCode)
	}
}
