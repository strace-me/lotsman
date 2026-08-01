package mobile

import (
	"testing"

	"github.com/sagernet/sing-box/experimental/libbox"
	"github.com/strace-me/lotsman/pkg/registry"
	"github.com/strace-me/lotsman/pkg/singbox"
)

// TestGeneratedAndroidConfigLoadsInLibbox feeds the ACTUAL config shape this
// package will hand libbox — the Android tun ingress from androidTun(), the Clash
// API with its Bearer secret, and no socks inbound — through the pinned libbox's
// own parser and box constructor.
//
// It exists because everything else in this package is a claim about a config
// libbox has never seen. sing-box changed tun option names between minor versions
// (inet4_address was removed in 1.12), so "the generator emits a tun block" and
// "libbox 1.13.14 accepts this tun block" are different statements, and only one
// of them is worth anything.
//
// Requires -tags with_clash_api: without it sing-box's clash server constructor is
// a stub that errors, which is exactly the failure this test should surface.
func TestGeneratedAndroidConfigLoadsInLibbox(t *testing.T) {
	opts := singbox.DefaultOptions()
	opts.DefaultMark = 0
	opts.TproxyPort = 0
	opts.RuleSetDir = t.TempDir()
	opts.ClashAPIListen = "127.0.0.1:19090"
	opts.ClashAPISecret = "test-secret"
	opts.Tun = androidTun()
	// Left empty deliberately: a non-empty SocksProbeListen is what makes the
	// generator emit the unauthenticated "probe-in" inbound, which must never exist
	// on Android.
	opts.SocksProbeListen = ""

	res, err := singbox.Generate(
		[]registry.Service{{Name: "test", Domains: []string{"example.com"}}},
		nil, nil, nil, opts,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if err := libbox.Setup(&libbox.SetupOptions{
		BasePath:    t.TempDir(),
		WorkingPath: t.TempDir(),
		TempPath:    t.TempDir(),
	}); err != nil {
		t.Fatalf("libbox setup: %v", err)
	}
	if err := libbox.CheckConfig(string(res.JSON)); err != nil {
		t.Fatalf("libbox %s rejected the generated Android config: %v\n%s",
			libbox.Version(), err, res.JSON)
	}
}
