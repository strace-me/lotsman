# Lotsman for Android — Stage 1 skeleton

An Android app that runs the **unchanged** `client/core` control plane in-process
by embedding sing-box as a library (`libbox`) instead of executing a binary.

**Status: written, never built.** There is no Android SDK, no NDK and no
`liblotsman.aar` on the machine this was authored on. Treat every symbol that
crosses the Go boundary as a claim to be checked by the first real compile. The
[Unverified](#unverified) section lists each one.

---

## Shape

One process, one service:

```
MainActivity (Compose)  ──JNI──┐
                               ├─►  Mobile.start/stop/statusJSON   (Go facade)
LotsmanVpnService  ────JNI──── ┘         │
   ├─ VpnService: owns the tun fd        ├─► client/core (brain, kb, probing …)
   ├─ PlatformInterface: libbox calls    └─► libbox → sing-box, in-process
   │    back in here for the tun fd,
   │    socket protect(), interfaces
   └─ foreground notification
```

There is **no control socket** on Android. The desktop client splits a
privileged daemon from an unprivileged UI because only root can open the tun;
on Android `VpnService` gives an ordinary app that privilege, so the UI and the
core live in the same process and talk over plain JNI. Do not add
`android:process` to the service — a second process means a second Go runtime
with no tun, and drags the IPC design back in.

## Files

| Path | What it is |
| --- | --- |
| `app/src/main/java/me/strace/lotsman/LotsmanVpnService.kt` | the VpnService + libbox `PlatformInterface` + Go lifecycle |
| `app/src/main/java/me/strace/lotsman/PlatformInterop.kt` | gomobile iterator adapters; `net.Interfaces()` replacement |
| `app/src/main/java/me/strace/lotsman/MainActivity.kt` | one button, consent flow, `StatusJSON()` polling |
| `app/src/main/assets/config.yaml` | placeholder client config copied to `filesDir` on first run |
| `app/libs/` | where `liblotsman.aar` is dropped (gitignored) |

## Build

### 1. Toolchain

- JDK 17
- Android SDK, `compileSdk 35`, build-tools for AGP 8.7
- Android NDK (r26/r27; whatever the pinned sing-box builds against)
- Go 1.26+ (matches `go.mod`)
- **SagerNet's gomobile fork**, not upstream `golang.org/x/mobile`:

```sh
go install github.com/sagernet/gomobile/cmd/gomobile@latest
go install github.com/sagernet/gomobile/cmd/gobind@latest
gomobile init
```

Upstream gomobile does not carry SagerNet's patches for the libbox binding and
will not produce a usable `.aar`.

### 2. Build the .aar

**ONE `gomobile bind` invocation binds BOTH packages.** This is not a style
preference: our facade hands Kotlin's `PlatformInterface` implementation to
libbox, so the two Go packages must be bound together for the types to be the
same types on the Java side. Binding them separately produces two incompatible
class hierarchies. Never write Kotlin wrappers that re-export libbox's API
either — bind it and use it directly.

`client/mobile` is its own Go module (its `go.mod` `replace`s the repo root), so
the bind runs **from `client/mobile/`**, not from the repo root — libbox is only
in *that* module's graph.

```sh
export ANDROID_HOME=$HOME/Library/Android/sdk
export ANDROID_NDK_HOME=$ANDROID_HOME/ndk/<version>

cd client/mobile
gomobile bind -v \
  -target=android/arm64,android/arm,android/amd64 \
  -androidapi 24 \
  -javapkg=me.strace \
  -libname=lotsman \
  -tags "with_gvisor,with_quic,with_utls,with_clash_api" \
  -trimpath \
  -ldflags="-X github.com/sagernet/sing-box/constant.Version=1.13.14 -s -w -buildid=" \
  -o ../../android/app/libs/liblotsman.aar \
  github.com/sagernet/sing-box/experimental/libbox \
  github.com/strace-me/lotsman/client/mobile
```

Notes on the flags that matter:

- `-javapkg=me.strace` ⇒ `me.strace.libbox.*` and `me.strace.mobile.Mobile`.
  Upstream sing-box uses `-javapkg=io.nekohasekai`; we cannot reuse that because
  the same prefix would apply to *our* package too, and squatting
  `io.nekohasekai.mobile` is wrong. **If you change this flag, change the two
  import blocks in `LotsmanVpnService.kt` / `PlatformInterop.kt` to match.**
- `with_clash_api` is **mandatory**, not optional. `client/core` steers sing-box
  entirely over the loopback Clash API (selector flips, `/delay`,
  `/connections`); without the tag the steering layer is silently dead and the
  brain has no data plane to drive.
- `-androidapi 24` must be ≤ the app's `minSdk` (24).
- The output filename must stay `liblotsman.aar` — `app/build.gradle.kts`
  resolves it by name through the `flatDir` repository in `settings.gradle.kts`.

### 3. Assemble the APK

```sh
cd android
./gradlew :app:assembleDebug
```

The Gradle wrapper **jar** is not committed (binary). Generate it once with a
system Gradle 8.9, or let Android Studio do it:

```sh
cd android && gradle wrapper --gradle-version 8.9
```

---

## Binding contract (reconciled against `client/mobile` + libbox 1.13.14)

gobind's naming rules, read out of
`github.com/sagernet/gomobile@v0.1.13/bind/genjava.go` rather than guessed:

- **Methods** → `lowerFirst(GoName)`, where `lowerFirst` lowercases a *leading run*
  of capitals. `StatusJSON` → `statusJSON`, `GetMTU` → `getMTU`,
  `ClearDNSCache` → `clearDNSCache`, `Address` → **`address`** (no `get` prefix —
  that is only for fields).
- **Struct fields** → `get<Field>()` / `set<Field>(v)`, field name verbatim.
  `StringBox.Value` → `getValue()`; `NetworkInterface.MTU` → `setMTU()`.
- A trailing `error` return becomes `throws Exception`; `int32` → Java `int`.

| Go (package `mobile`) | Kotlin call site |
| --- | --- |
| `func SetPlatform(iface libbox.PlatformInterface)` | `Mobile.setPlatform(this)` |
| `func SetEventSink(sink EventSink)` | `Mobile.setEventSink(this)` |
| `func Start(configYAML, filesDir string) error` | `Mobile.start(yaml, filesDir)` — throws |
| `func Stop() error` | `Mobile.stop()` — throws |
| `func StatusJSON() string` | `Mobile.statusJSON()` |
| `func Version() string` | `Mobile.version()` (unused) |
| `type EventSink interface { OnEvent(string) }` | `override fun onEvent(eventJSON: String)` |

`SetPlatform` is mandatory and must be called **before** `Start`: libbox obtains
the tun fd only through `PlatformInterface.OpenTun`.

`AndroidProxyCore` is **not reachable from Kotlin**. Its constructor takes a
`*slog.Logger` and its methods take `context.Context`, none of which gobind can
bind, so the generated class has no constructor and no lifecycle methods.
`SetPackageRouting` is therefore dead in v1 — per-app routing has no Kotlin entry
point until the facade grows one.

### libbox `PlatformInterface` — all 15 methods, verified

Read from
`$(go env GOMODCACHE)/github.com/sagernet/sing-box@v1.13.14/experimental/libbox/platform.go`:

`localDNSTransport`, `usePlatformAutoDetectInterfaceControl`,
`autoDetectInterfaceControl`, `openTun`, `useProcFS`, `findConnectionOwner`,
`startDefaultInterfaceMonitor`, `closeDefaultInterfaceMonitor`, `getInterfaces`,
`underNetworkExtension`, `includeAllNetworks`, `readWIFIState`,
`systemCertificates`, `clearDNSCache`, `sendNotification`.

There is **no** `usePlatformDefaultInterfaceMonitor()` and **no**
`usePlatformInterfaceGetter()` in 1.13 — `platformInterfaceWrapper` returns
`true` for both unconditionally (`experimental/libbox/service.go`), so the
platform side is always asked. Declaring them is a compile error.

Two things the Go side must handle, which Kotlin cannot:

1. **Paths.** Kotlin passes exactly one writable path: `filesDir.absolutePath`.
   Go must derive base/working/temp/rule-set/state dirs from it, including
   calling libbox's own `Setup(...)`. `os.TempDir()` is `/data/local/tmp`,
   `os.UserCacheDir()` is `/sdcard`, and `RuleSetDir` defaults to
   `/etc/sing-box` — all unwritable.
2. **No `probe-in` socks inbound.** The generated config must never emit it on
   Android: it has no auth, and any installed app can dial 127.0.0.1. The Clash
   API's mandatory random Bearer secret matters *more* here for the same reason.
   Kotlin has no way to enforce either; both are Go-side invariants.

---

## What *was* verified

The authoring machine has Go but no JDK, no Android SDK/NDK, no gomobile and no
device (`java -version` fails; `which gomobile` is empty). Executed:

- `pkg/config.Parse` on `app/src/main/assets/config.yaml` — parses clean,
  2 services, 1 subscription, 1 chain step each.
- XML well-formedness of the manifest and both resource files.
- Bracket balance of every `.kt` / `.kts` file (a smoke test, not a compile).
- **Seam reconciliation against source**, in a later pass: every Java symbol this
  app calls was checked against `sing-box@v1.13.14/experimental/libbox/*.go` and
  against gobind's own name generator in `gomobile@v0.1.13/bind/genjava.go`.
  That found and fixed six real breaks (facade name, two phantom overrides, five
  missing overrides, `RoutePrefix` accessor names, `StringBox` vs `String`, and
  the interface-flag encoding). It is source reconciliation, not a compile.

No Kotlin was compiled. No Gradle task was run. No `.aar` exists.

## Resolved by source reconciliation

- **PlatformInterface method set** — 15 methods, listed above, all implemented.
- **`TunOptions` accessor names** — `getMTU()`, `getInet4Address()`,
  `getInet4RouteAddress()`, `getAutoRoute()`, `getIncludePackage()`,
  `getExcludePackage()` are right (Go methods already named `Get…`).
  `getDNSServerAddress()` returns `libbox.StringBox`, **not** `String` — read
  `.value`.
- **`RoutePrefix`** exposes `address()` / `prefix()`, not `getAddress()` /
  `getPrefix()`: they are Go *methods*, and only *fields* get a `get` prefix.
- **`InterfaceUpdateListener.updateDefaultInterface`** is the four-argument form
  (`platform.go:39`).
- **`StringIterator`** is `len()/hasNext()/next()`; `RoutePrefixIterator` and
  `NetworkInterfaceIterator` have `hasNext()/next()` only, no `len()`.
- **`NetworkInterface.Flags` takes raw Linux `IFF_*`**, run through libbox's
  `linkFlags()`. Now written from `android.system.OsConstants`.

## Unverified

In rough order of how likely it is to bite:

1. **The `gomobile bind` itself.** Nothing here has been through gobind. Two
   packages from one module graph, `-javapkg=me.strace`, cross-package interface
   parameters — all standard, none executed.
2. **`libbox.NetworkInterface.Type` is never set**, so it stays 0, which is
   `C.InterfaceTypeWIFI`. Every interface, including cellular and loopback,
   reports as Wi-Fi to sing-box. Harmless while no rule matches on
   `network_type`; wrong the moment one does.
3. **`netip.MustParsePrefix` on our address strings.** `NetworkInterfaces()`
   parses each `"addr/len"` we emit with the *Must* variant — a malformed entry
   panics inside Go and aborts the process rather than erroring.
4. **tun fd ownership.** `openTun` returns `pfd.fd` and Kotlin keeps the
   `ParcelFileDescriptor`, matching the SagerNet clients. If the pinned sing-box
   also closes that descriptor on shutdown, our close is a double close; it is
   ordered immediately after `Mobile.stop()` returns to keep the window closed,
   but the alternative (`pfd.detachFd()`, Go owns it, leaks if Go forgets) may be
   the correct choice. **Decide on a device.**
5. **Calling `statusJSON()` before `Start()`.** Guarded by only polling while the
   service reports STARTING/RUNNING, but a facade that panics when uninitialised
   would still take the process down from the UI thread pool.
6. ~~The placeholder `assets/config.yaml`~~ — **verified**, see above. What is
   still unverified is whether the Go facade accepts it end to end: it declares
   VPN-only chains against a subscription URL that resolves nowhere, so `Start()`
   will come up with an empty node pool.
7. **Version catalog.** AGP 8.7.3 / Kotlin 2.0.21 / Compose BOM 2024.10.01 were
    never resolved against a repository from here.
8. **flatDir resolution.** `implementation(group = "", name = "liblotsman", ext =
    "aar")` against a `flatDir` declared in `dependencyResolutionManagement`. If
    AGP rejects it, the fallback is
    `implementation(fileTree(mapOf("dir" to "libs", "include" to listOf("*.aar"))))`.
9. **Foreground-service type.** `specialUse` + the
    `PROPERTY_SPECIAL_USE_FGS_SUBTYPE=vpn` property is what VPN apps ship on API
    34+, but Play requires a written justification at submission time.
10. **Notification icon** is the platform drawable `ic_lock_lock` so the skeleton
    needs no drawable resources. It will look wrong; Stage 3 ships a real one.
11. **R8.** Release builds have `isMinifyEnabled = false`. The keep rules in
    `proguard-rules.pro` are a starting guess — every gomobile class is reached
    only from JNI, so R8 cannot see the references.

### Deliberately not done

- No per-app routing UI, and no per-app routing *at all*. `openTun` honours
  `TunOptions.include/excludePackage` (libbox rejects `include_uid`/`exclude_uid`
  in the tun JSON, so the lists arrive through the platform interface), but the
  only thing that can populate them is `AndroidProxyCore.SetPackageRouting`,
  which gobind leaves unreachable from Kotlin. The plumbing is real; the entry
  point does not exist yet.
- No DPI-desync rung. Unrooted Android has no NFQUEUE; `client/core` already
  returns nil from `newZapretExec` off Linux and renumbers each chain.
- No app icon, no theming, no subscription entry, no widget, no tile. Stage 3.
