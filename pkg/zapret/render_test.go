package zapret

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strace-me/lotsman/pkg/strategy"
)

func TestRenderLauncherShape(t *testing.T) {
	def := strategy.Definition{
		ID:        "disc-multisplit-abcd1234",
		Class:     strategy.ClassZapret,
		NFQWSArgs: []string{"--dpi-desync=multisplit", "--dpi-desync-split-pos=1"},
		Notes:     "discovered via curl_test_https_tls12 on youtube.com ipv4",
	}
	out, err := RenderLauncher(def, LauncherOpts{
		FilterTCP:       "80,443",
		HostlistDomains: []string{"youtube.com", "youtu.be"},
	})
	if err != nil {
		t.Fatalf("RenderLauncher: %v", err)
	}

	wantContains := []string{
		"#!/bin/sh\n",
		"QNUM=\"${1:-200}\"",
		"N=" + DefaultNfqwsPath,
		"exec \"$N\" --qnum=\"$QNUM\"",
		"--filter-tcp=80,443",
		"--hostlist-domains=youtube.com,youtu.be",
		"--dpi-desync=multisplit --dpi-desync-split-pos=1",
		def.ID, // recorded in the header comment
	}
	for _, w := range wantContains {
		if !strings.Contains(out, w) {
			t.Errorf("rendered launcher missing %q\n---\n%s", w, out)
		}
	}
	// Scope must precede the desync args on the exec line (filter applies first).
	if i, j := strings.Index(out, "--filter-tcp"), strings.Index(out, "--dpi-desync"); i < 0 || j < 0 || i > j {
		t.Errorf("filter scope should come before desync args; got %s", out)
	}
}

func TestRenderLauncherUDPAndCustomBinary(t *testing.T) {
	def := strategy.Definition{ID: "disc-fake-1", Class: strategy.ClassZapret,
		NFQWSArgs: []string{"--dpi-desync=fake", "--dpi-desync-repeats=6"}}
	out, err := RenderLauncher(def, LauncherOpts{NfqwsPath: "/usr/bin/nfqws", FilterUDP: "443"})
	if err != nil {
		t.Fatalf("RenderLauncher: %v", err)
	}
	if !strings.Contains(out, "N=/usr/bin/nfqws") {
		t.Errorf("custom nfqws path not used:\n%s", out)
	}
	if !strings.Contains(out, "--filter-udp=443") {
		t.Errorf("udp filter missing:\n%s", out)
	}
}

// TestRenderLauncherIsValidShell writes the generated launcher and runs `sh -n`
// on it — a generator's output must be syntactically valid shell (validate the
// artifact, not just the string).
func TestRenderLauncherIsValidShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	def := strategy.Definition{ID: "disc-fake_multisplit-1", Class: strategy.ClassZapret,
		NFQWSArgs: []string{"--dpi-desync=fake,multisplit", "--dpi-desync-split-pos=1", "--dpi-desync-fooling=ts"}}
	out, err := RenderLauncher(def, LauncherOpts{FilterTCP: "443", HostlistDomains: []string{"discord.media"}})
	if err != nil {
		t.Fatalf("RenderLauncher: %v", err)
	}
	path := filepath.Join(t.TempDir(), "disc.sh")
	if err := os.WriteFile(path, []byte(out), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if b, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("sh -n rejected generated launcher: %v\n%s\n---\n%s", err, b, out)
	}
}

func TestRenderLauncherErrors(t *testing.T) {
	// No NFQWSArgs: nothing to render.
	if _, err := RenderLauncher(strategy.Definition{ID: "alt12", Class: strategy.ClassZapret}, LauncherOpts{FilterTCP: "443"}); err == nil {
		t.Error("expected error for script-backed def (no NFQWSArgs)")
	}
	// No filter scope: would desync everything.
	if _, err := RenderLauncher(strategy.Definition{ID: "x", NFQWSArgs: []string{"--dpi-desync=fake"}}, LauncherOpts{}); err == nil {
		t.Error("expected error for missing filter scope")
	}
}

func TestRenderComposed(t *testing.T) {
	args := []string{
		"--new", "--filter-tcp=443", "--hostlist-domains=discord.media", "--dpi-desync=fake",
		"--new", "--filter-tcp=443", "--hostlist-domains=epicgames.com", "--dpi-desync=split2",
	}
	out, err := RenderComposed(args, "")
	if err != nil {
		t.Fatalf("render composed: %v", err)
	}
	if !strings.HasPrefix(out, "#!/bin/sh\n") {
		t.Error("missing shebang")
	}
	if !strings.Contains(out, DefaultNfqwsPath) {
		t.Error("default nfqws path missing")
	}
	if !strings.Contains(out, `--qnum="$QNUM"`) || !strings.Contains(out, "--hostlist-domains=epicgames.com") {
		t.Errorf("composed exec line wrong:\n%s", out)
	}
	// Empty args -> error (caller should keep the existing config).
	if _, err := RenderComposed(nil, ""); err == nil {
		t.Error("expected error for empty composed args")
	}
}
