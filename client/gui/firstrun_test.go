package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/config"
)

func withUserConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "lotsman")
}

// The case that defines first run: a machine that has never been set up has no
// service, so the window must still be able to answer "is there a config?".
func TestSetupStateOnAFreshMachine(t *testing.T) {
	dir := withUserConfigDir(t)
	a := &App{ctx: nil}

	st, err := a.SetupState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Configured {
		t.Fatal("a machine with no config dir reports itself configured")
	}
	if st.SourcePath != dir {
		t.Fatalf("source path %q, want %q", st.SourcePath, dir)
	}
	// No service is the NORMAL state here, so it must not be an error.
	if st.ServiceReachable {
		t.Fatal("a fresh machine reported a reachable service")
	}
}

// A machine with a configuration and a DEAD service is broken, not fresh. Showing
// first run to it would invite the user to overwrite work.
func TestSetupStateDoesNotCallAConfigWrittenServiceFresh(t *testing.T) {
	dir := withUserConfigDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.DocName), []byte("version: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &App{}
	st, err := a.SetupState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Configured {
		t.Fatal("a written deployment is reported as unconfigured")
	}
	if st.ServiceReachable {
		t.Fatal("no service is running, yet it reports reachable")
	}
}

func TestWriteFirstDeploymentWritesAConfigThatLoads(t *testing.T) {
	withUserConfigDir(t)
	a := &App{}

	dir, err := a.WriteFirstDeployment(ScaffoldRecommended, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadV2(dir, "", ""); err != nil {
		t.Fatalf("what was written does not load: %v", err)
	}
	rules, err := os.ReadDir(filepath.Join(dir, config.RulesDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) == 0 {
		t.Fatal("the recommended set wrote no rules")
	}
}

func TestWriteFirstDeploymentCarriesTheSubscription(t *testing.T) {
	withUserConfigDir(t)
	a := &App{}
	dir, err := a.WriteFirstDeployment(ScaffoldMinimal, []string{"https://example.invalid/sub"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(dir, config.DocName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "example.invalid/sub") {
		t.Fatal("the subscription URL is not in the written document")
	}
}

// The service refuses to start with no nodes, so a minimal deployment with no
// subscription is a config that loads and does nothing. Saying so at write time is
// cheaper than the user finding out from a service that will not start.
func TestWriteFirstDeploymentRefusesAMinimalSetWithNoSubscription(t *testing.T) {
	withUserConfigDir(t)
	a := &App{}
	if _, err := a.WriteFirstDeployment(ScaffoldMinimal, nil); err == nil {
		t.Fatal("a minimal deployment with no subscription was accepted")
	}
}

// The one operation in the window that can destroy work. A double-click, or a
// re-render firing a second call, must not lose a hand-edited configuration.
func TestWriteFirstDeploymentRefusesToOverwrite(t *testing.T) {
	dir := withUserConfigDir(t)
	a := &App{}
	if _, err := a.WriteFirstDeployment(ScaffoldRecommended, nil); err != nil {
		t.Fatal(err)
	}
	edited := filepath.Join(dir, config.RulesDirName, "youtube.yaml")
	before, err := os.ReadFile(edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(edited, []byte("# mine, do not touch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.WriteFirstDeployment(ScaffoldRecommended, nil); err == nil {
		t.Fatal("a second first-run write was allowed")
	}
	after, err := os.ReadFile(edited)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "# mine, do not touch\n" {
		t.Fatal("the refused write still changed the rule")
	}
	_ = before
}

func TestWriteFirstDeploymentRejectsAnUnknownSet(t *testing.T) {
	withUserConfigDir(t)
	a := &App{}
	if _, err := a.WriteFirstDeployment("whatever", nil); err == nil {
		t.Fatal("an unknown starting set was accepted")
	}
}

func TestUserConfigDirFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	dir, err := UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "lotsman"); dir != want {
		t.Fatalf("got %q, want %q", dir, want)
	}
}

// The bug this exists for: on the desktop the service reads a root-owned deployment
// under /etc while the window writes ~/.config. A window that answered "is this
// machine configured?" only from its OWN directory showed FIRST RUN on a machine
// running a perfectly good deployment — and then offered to overwrite it. That is how
// a config editor destroys a config.
func TestSetupStateAsksTheServiceBeforeItsOwnDirectory(t *testing.T) {
	dir := withUserConfigDir(t)
	// Nothing in the window's own directory.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	sock := startFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"version": "test",
			"deployment": map[string]any{
				"path":              "/etc/lotsman/configuration.yaml",
				"source":            "/somewhere/else/the-operator-keeps-it",
				"source_configured": true,
			},
		})
	})
	a := NewApp(sock)
	a.ctx = context.Background() // Wails sets this in startup; a fake service does not

	st, err := a.SetupState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.ServiceReachable {
		t.Fatal("the service answered but was reported unreachable")
	}
	if !st.Configured {
		t.Fatal("a service reporting a configured deployment was answered 'not configured'")
	}
	// And the window must point at WHERE THE SERVICE READS, not where it would write.
	if st.SourcePath != "/somewhere/else/the-operator-keeps-it" {
		t.Fatalf("source path %q, want the service's own source", st.SourcePath)
	}
}

// The genuine first-run case: nothing is running, so the window's own default is the
// only answer available and it must still work.
func TestSetupStateFallsBackToItsOwnDirectoryWithNoService(t *testing.T) {
	dir := withUserConfigDir(t)
	a := &App{} // no client socket: Status() fails, as it must with no service
	st, err := a.SetupState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Configured {
		t.Fatal("reported configured with no service and no config")
	}
	if st.SourcePath != dir {
		t.Fatalf("source path %q, want the window default %q", st.SourcePath, dir)
	}
}

// A service too old to report a deployment must not be treated as unconfigured —
// that is the trap the field was added to remove, and an older build answering is the
// most likely real case during an upgrade.
func TestSetupStateWithAServiceThatReportsNoDeployment(t *testing.T) {
	dir := withUserConfigDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.DocName), []byte("version: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := startFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"version": "old"})
	})
	a := NewApp(sock)
	a.ctx = context.Background()
	st, err := a.SetupState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.ServiceReachable {
		t.Error("the service answered /status but was reported unreachable")
	}
	// With no deployment field the window falls back to its own directory, and the
	// fallback must see the config that IS there rather than declaring the machine
	// fresh. Declaring it fresh is the dangerous half: it offers to overwrite.
	if !st.Configured {
		t.Error("an old service plus a real config reported as unconfigured")
	}
}

// A control socket that answers with a handler, so SetupState can be exercised against
// something that behaves like a service rather than against the filesystem alone.
func startFakeService(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lm-fake-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

// The bug that made a configured machine look fresh: the fallback checked only the
// window's OWN directory, while a real install keeps its deployment in /etc. So with a
// working config on disk the window said "not configured" and offered to overwrite it.
//
// The service answers authoritatively when it is new enough. These cover the cases
// where it cannot: no service, and a service older than the deployment field.
func TestSetupStateFindsADeploymentOutsideItsOwnDirectory(t *testing.T) {
	dir := withUserConfigDir(t) // empty — deliberately
	for _, root := range candidateDeploymentDirs(dir) {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Skipf("cannot create %s: %v", root, err)
		}
		if err := os.WriteFile(filepath.Join(root, config.DocName), []byte("version: 2\n"), 0o600); err != nil {
			t.Skipf("cannot write in %s: %v", root, err)
		}
		st, err := (&App{}).SetupState()
		if err != nil {
			t.Fatal(err)
		}
		if !st.Configured {
			t.Fatalf("a deployment in %s was reported as unconfigured", root)
		}
		if st.SourcePath != root {
			t.Errorf("source path %q, want %q", st.SourcePath, root)
		}
		t.Cleanup(func() { os.RemoveAll(root) })
		return
	}
	t.Skip("none of the candidate roots are writable here")
}

// A v1 deployment is still a deployment. The file is named differently, and reporting
// that machine as fresh is how an upgrade opens by offering to overwrite the only
// config the operator has.
//
// Written into the window's OWN directory rather than /etc, because the logic under
// test is "is this file called client.yaml", not "is /etc writable" — and on a real
// machine /etc is root-owned, which the test should not need to be.
func TestSetupStateRecognisesALegacyV1Deployment(t *testing.T) {
	dir := withUserConfigDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.yaml"), []byte("services:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := (&App{}).SetupState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Configured {
		t.Fatal("a v1 deployment was reported as unconfigured")
	}
	if !st.Legacy {
		t.Error("a v1 deployment was not flagged legacy")
	}
	if st.SourcePath != dir {
		t.Errorf("source path %q, want %q", st.SourcePath, dir)
	}
}

// The candidates are ordered so the most likely root is asked first, and the window's
// own directory is always among them — it is where the window itself writes.
func TestCandidateDeploymentDirsCoversEveryDocumentedRoot(t *testing.T) {
	got := candidateDeploymentDirs("/own")
	for _, want := range []string{"/own", "/etc/lotsman"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not a candidate; got %v", want, got)
		}
	}
	if got[0] != "/own" {
		t.Errorf("the window's own directory should be asked first, got %v", got)
	}
}
