package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sagernet/sing-box/experimental/libbox"
	"github.com/strace-me/lotsman/client/core"
	"github.com/strace-me/lotsman/pkg/config"
	"github.com/strace-me/lotsman/pkg/singbox"
)

// This file is the ONLY surface Kotlin touches. gomobile bind exports a narrow
// set of types — string, int, bool, []byte, error, and interfaces/structs built
// from those — so every signature here stays inside it.
//
// There is exactly one VpnService process and one Core in it, so the state is a
// package-level singleton rather than a handle Kotlin has to keep. That also
// keeps the bound surface to plain functions.
//
// PANICS: a Go panic that crosses the gomobile boundary does not become a Java
// exception, it aborts the process. Every exported entry point below therefore
// ends in a recover, including the Go->Kotlin event callback (a Java exception
// thrown inside a bound callback surfaces on the Go side as a panic).

// EventSink receives one-way JSON notifications from Go. Kotlin implements it.
//
// It carries LIFECYCLE events only — started, stopped, failed. It is deliberately
// not a status stream: client/core keeps its event bus unexported and offers no
// subscription, so a "status stream" here could only be a Go-side poll of
// StatusJSON pushed back over the wire, which is strictly worse than Kotlin
// polling StatusJSON on its own schedule. Poll StatusJSON for status.
type EventSink interface {
	OnEvent(payloadJSON string)
}

// hostlistFetchTimeout bounds the one-off fetch of domain packs that have no file
// yet, so an unreachable source delays a first run by seconds instead of hanging
// it. Same value as the desktop client.
const hostlistFetchTimeout = 30 * time.Second

var (
	mu       sync.Mutex
	instance *core.Core
	box      *AndroidProxyCore
	cancel   context.CancelFunc
	platform libbox.PlatformInterface
	sink     EventSink
	logger   = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
)

// SetPlatform hands Go the Kotlin-side libbox.PlatformInterface. It MUST be
// called before Start.
//
// This is a fifth entry point beyond Start/Stop/StatusJSON/SetEventSink and it is
// not optional: libbox obtains the tun fd exclusively through
// PlatformInterface.OpenTun (VpnService.Builder().establish() on the Kotlin side),
// the fd is never carried in config JSON, and libbox offers no other injection
// point. Kotlin must implement the full method set — see the package README note
// in this file's companion, and platform.go in libbox for the authoritative list.
//
// libbox.PlatformInterface is bindable here only because ONE `gomobile bind`
// invocation binds both libbox and this package.
func SetPlatform(iface libbox.PlatformInterface) {
	defer recoverCrash("SetPlatform")
	mu.Lock()
	defer mu.Unlock()
	platform = iface
}

// SetEventSink installs the one-way Go->Kotlin callback. Pass nil to detach.
func SetEventSink(s EventSink) {
	defer recoverCrash("SetEventSink")
	mu.Lock()
	defer mu.Unlock()
	sink = s
}

// Start parses the Lotsman config, brings sing-box up in-process and runs the
// autonomy loop. filesDir is the app-private directory (Context.getFilesDir);
// EVERY path derives from it, because os.TempDir() (/data/local/tmp) and
// os.UserCacheDir() (/sdcard) are both unwritable on Android.
func Start(configYAML string, filesDir string) (err error) {
	defer recoverErr("Start", &err)
	mu.Lock()
	defer mu.Unlock()
	if instance != nil {
		return fmt.Errorf("mobile: already started")
	}
	if platform == nil {
		return fmt.Errorf("mobile: SetPlatform must be called before Start")
	}
	if filesDir == "" {
		return fmt.Errorf("mobile: filesDir is required (all state lives under it)")
	}

	var (
		workDir   = filepath.Join(filesDir, "work")
		tempDir   = filepath.Join(filesDir, "temp")
		singboxJS = filepath.Join(filesDir, "singbox.json")
	)
	for _, d := range []string{workDir, tempDir, filepath.Join(filesDir, "rule-sets")} {
		if mkErr := os.MkdirAll(d, 0o700); mkErr != nil {
			return fmt.Errorf("mobile: create %s: %w", d, mkErr)
		}
	}

	// libbox keeps these as package globals and reads them while building the base
	// context, so Setup has to run before NewAndroidProxyCore. The command server's
	// secret is set even though we never start its listener: if a later change does
	// start it, it must not come up unauthenticated by default.
	secret := libbox.RandomHex(16)
	if setupErr := libbox.Setup(&libbox.SetupOptions{
		BasePath:    filesDir,
		WorkingPath: workDir,
		TempPath:    tempDir,
		// Works around golang/go#68760 on Android; SagerNet's own client sets it.
		FixAndroidStack: true,
		// 0 means the command server would listen on a unix socket under BasePath —
		// but only if CommandServer.Start() is called, which it is not.
		CommandServerListenPort: 0,
		CommandServerSecret:     secret.Value,
		LogMaxLines:             100,
	}); setupErr != nil {
		return fmt.Errorf("mobile: libbox setup: %w", setupErr)
	}

	conf, err := config.Parse([]byte(configYAML))
	if err != nil {
		return fmt.Errorf("mobile: parse config: %w", err)
	}
	// A service's domain_lists are merged into its domains at PARSE time, so a pack
	// whose file is missing is absent for the whole run. Fetch the missing ones and
	// re-parse before anything consumes the config. (The Out paths in the config must
	// themselves be under filesDir; nothing else on Android is writable.)
	if core.EnsureDomainLists(context.Background(), conf, hostlistFetchTimeout, logger) {
		if conf, err = config.Parse([]byte(configYAML)); err != nil {
			return fmt.Errorf("mobile: re-parse after fetching domain lists: %w", err)
		}
	}

	box, err = NewAndroidProxyCore(singboxJS, platform, logger)
	if err != nil {
		return err
	}

	c := core.New(conf, box, core.Options{
		// Loopback Clash-API. On Android ANY installed app can reach 127.0.0.1, so the
		// mandatory random Bearer secret core generates is what keeps a co-resident app
		// from steering this tunnel. It is not optional here; it is more load-bearing
		// than on desktop.
		ClashListen: "127.0.0.1:9090",
		Interval:    10 * time.Second,

		// Everything that touches the filesystem hangs off filesDir.
		StateFile:     filepath.Join(filesDir, "state.json"),
		KBFile:        filepath.Join(filesDir, "kb.json"),
		RuleSetDir:    filepath.Join(filesDir, "rule-sets"),
		SingboxConfig: singboxJS,
		BaselineFile:  singboxJS + ".baseline",

		// Android ingress. libbox does not create this tun: it calls back into
		// Kotlin (PlatformInterface.OpenTun) for the fd that VpnService.Builder
		// established, then attaches to it. AutoRoute stays true because libbox
		// derives the route ranges Kotlin feeds to VpnService.Builder from it — no
		// kernel route is installed by sing-box on Android.
		//
		// Per-app routing is NOT expressed here: libbox rejects include_uid /
		// exclude_uid in the tun JSON. It goes through AndroidProxyCore.SetPackageRouting.
		Tun: androidTun(),

		// NOT set, each for a reason:
		//   ProbeProxy / ProxyListen — either would make the generator emit the socks
		//     "probe-in" inbound, which has NO authentication. On Android every
		//     installed app can dial 127.0.0.1, so that inbound is an open proxy for
		//     the whole device. This is exactly how several RU VPN clients leaked in
		//     2025. Leave both empty; probing then goes direct.
		//   HostDNS — rewrites /etc/resolv.conf and needs root. The tun already
		//     captures DNS device-wide on Android.
		//   NfqwsBin / ZapretFiles / HostlistDir / QNum / WAN / DesyncExclude —
		//     the DPI-desync rung needs NFQUEUE, which unrooted Android has not.
		//     core.newZapretExec returns nil off Linux and dropUnsupportedRungs
		//     renumbers each service's chain, so the chains stay valid without it.
		//   KBDir — per-network KB needs netid fingerprinting, which leans on
		//     net.Interfaces(); that is permission-denied on Android 11+. One KB file.
		//   MetricsAddr — a Prometheus listener on a phone is a surface, not a feature.
		//   SingboxBin — there is no sing-box binary on Android. See the note on
		//     RefreshEvery below.

		// Subscription refresh. WARNING, and it is a real one: pkg/reconcile validates
		// every candidate config by asking its Runner to run `<SingboxBin> check -c`,
		// and client/core's boxRunner only routes that to a subprocess — never to
		// ProxyCore.Check. On Android that exec cannot succeed, so each pass will fail
		// its check and skip the apply. The failure is loud (a warning per tick) and
		// harmless (nothing is written), and refresh is left ON rather than silently
		// disabled, because silently never noticing a node rotate out is worse than a
		// visible recurring warning. Fixing it means teaching boxRunner to use
		// box.Check — a client/core change, out of scope for this package.
		RefreshEvery: 5 * time.Minute,
	}, logger)

	ctx, cancelFn := context.WithCancel(context.Background())
	if err = c.Start(ctx); err != nil {
		cancelFn()
		box.Close()
		box = nil
		emitLocked("failed", err.Error())
		return fmt.Errorf("mobile: start: %w", err)
	}
	instance, cancel = c, cancelFn
	emitLocked("started", "")
	return nil
}

// Stop tears the client down. Idempotent.
func Stop() (err error) {
	defer recoverErr("Stop", &err)
	mu.Lock()
	defer mu.Unlock()
	if instance == nil {
		return nil
	}
	cancel()
	err = instance.Stop()
	if box != nil {
		box.Close()
		box = nil
	}
	instance, cancel = nil, nil
	emitLocked("stopped", "")
	if err != nil {
		return fmt.Errorf("mobile: stop: %w", err)
	}
	return nil
}

// StatusJSON returns core.Report marshalled. When nothing is running it returns
// the same shape with a "down" verdict rather than an empty string, so the Kotlin
// decoder has one case instead of two.
func StatusJSON() (out string) {
	defer recoverStr("StatusJSON", &out, `{"running":false,"verdict":{"state":"down"}}`)
	mu.Lock()
	c := instance
	mu.Unlock()

	var report core.Report
	if c != nil {
		report = c.Report(context.Background())
	} else {
		report.Verdict.State = "down"
	}
	js, err := json.Marshal(report)
	if err != nil {
		// Report is plain data; this cannot fail in practice, and saying so beats
		// returning a plausible-looking empty status.
		return fmt.Sprintf(`{"running":false,"verdict":{"state":"down"},"error":%q}`, err.Error())
	}
	return string(js)
}

// Version reports the sing-box version linked in, so a bug report names the
// engine instead of guessing at it.
//
// It returns "unknown" unless the build stamps it: sing-box carries its version in
// constant.Version, set at link time via -ldflags. Verified — an unstamped build
// here reports exactly that. Pass
// -ldflags "-X github.com/sagernet/sing-box/constant.Version=1.13.14" to gomobile
// if the UI is going to show this.
func Version() string {
	return libbox.Version()
}

// androidTun is the client ingress. It deliberately does NOT reuse core's desktop
// default: that one calls net.Interfaces() to exclude the host's LAN subnets from
// auto_route, and net.Interfaces() is permission-denied on Android 11+ — it would
// return nothing and the exclusion would silently not happen.
//
// The multicast/broadcast excludes are kept: they are static, and without them the
// tun swallows mDNS/SSDP so local discovery breaks asymmetrically (this device sees
// its neighbours, nobody sees it).
func androidTun() *singbox.TunOptions {
	return &singbox.TunOptions{
		MTU:       9000,
		Address:   []string{"172.19.0.1/30"},
		Stack:     "system",
		AutoRoute: true,
		ExcludeRoutes: []string{
			"224.0.0.0/4",
			"ff00::/8",
			"255.255.255.255/32",
		},
	}
}

// emitLocked pushes one lifecycle event to Kotlin. Caller holds mu; the callback
// runs on this goroutine, so a Kotlin implementation that blocks blocks Start.
func emitLocked(kind, message string) {
	if sink == nil {
		return
	}
	payload, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Message string `json:"message,omitempty"`
		At      int64  `json:"at"`
	}{Kind: kind, Message: message, At: time.Now().UnixMilli()})
	if err != nil {
		return
	}
	func() {
		// A Java exception thrown inside a bound callback comes back as a panic. It
		// must not kill the VpnService.
		defer recoverCrash("EventSink.OnEvent")
		sink.OnEvent(string(payload))
	}()
}

// recoverCrash swallows a panic at a boundary that has no error to report it on.
func recoverCrash(where string) {
	if r := recover(); r != nil {
		logger.Error("mobile: recovered a panic at the gomobile boundary", "at", where, "panic", r)
	}
}

// recoverErr turns a panic into the error the caller already returns.
func recoverErr(where string, err *error) {
	if r := recover(); r != nil {
		logger.Error("mobile: recovered a panic at the gomobile boundary", "at", where, "panic", r)
		*err = fmt.Errorf("mobile: %s panicked: %v", where, r)
	}
}

// recoverStr turns a panic into a caller-supplied fallback string.
func recoverStr(where string, out *string, fallback string) {
	if r := recover(); r != nil {
		logger.Error("mobile: recovered a panic at the gomobile boundary", "at", where, "panic", r)
		*out = fallback
	}
}
