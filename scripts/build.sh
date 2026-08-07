#!/usr/bin/env bash
# Build every Lotsman target from one version.
#
# The targets live in four Go modules — the root, and client/{gui,tray,mobile},
# split so sing-box's dependency graph never reaches the daemon — so `go build
# ./...` from the root cannot produce them all. That is why this script exists:
# without it "the version" is whatever each binary was last built with by hand,
# which is how the router ended up reporting `dev` while the changelog counted
# releases.
#
#   scripts/build.sh                    # host platform, all targets
#   GOOS=linux GOARCH=arm64 scripts/build.sh   # the R5S
#   scripts/build.sh lotsmand           # one target
#
# The GUI is built here TOO when the toolchain is present. It used to be left to
# a printed command a human was expected to remember, and the predictable happened:
# on 2026-08-07 the window on the daily driver was two commits behind its own
# service, invisibly, because the frontend had not been rebuilt. Two artefacts
# built by two different procedures drift, and the one nobody automates is the one
# that drifts. Without wails on PATH it says so and skips, which is a state you can
# see rather than an instruction you can forget.
set -euo pipefail

cd "$(dirname "$0")/.."
OUT=${OUT:-dist}

# git describe is the single source of truth. --dirty is deliberate: a binary
# built from uncommitted work must not claim to be the tag.
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "")
DATE=$(git log -1 --format=%cd --date=short 2>/dev/null || echo "")

V=github.com/strace-me/lotsman/pkg/version
LDFLAGS="-s -w -X $V.Version=$VERSION -X $V.Commit=$COMMIT -X $V.Date=$DATE"

# GOHOSTOS/GOHOSTARCH, not GOOS/GOARCH: once GOOS is exported for a cross-build,
# `go env GOOS` reports the TARGET, so comparing the two would always say "this
# is the host" and try to cgo-build the tray for the router.
hostos=$(go env GOHOSTOS)
hostarch=$(go env GOHOSTARCH)
goos=${GOOS:-$hostos}
goarch=${GOARCH:-$hostarch}
suffix=""
[ "$goos" = windows ] && suffix=".exe"

# CGO stays off for the service binaries: the router runs musl/busybox and a
# dynamically-linked binary would not start there. The tray is the exception —
# it links a native desktop toolkit and cannot build without cgo.
build() { # name, package, module dir, cgo
  local name=$1 pkg=$2 dir=${3:-.} cgo=${4:-0}
  printf '  %-16s %s/%s%s\n' "$name" "$goos" "$goarch" "$([ "$cgo" = 1 ] && echo ' (cgo)')"
  ( cd "$dir" && CGO_ENABLED=$cgo GOOS=$goos GOARCH=$goarch \
      go build -trimpath -ldflags "$LDFLAGS" -o "$OLDPWD/$OUT/$name$suffix" "$pkg" )
}

mkdir -p "$OUT"
echo "lotsman $VERSION"

targets=${*:-all}
# build_gui runs BOTH steps, in order. `wails build` embeds frontend/dist, which
# is a build artefact and not in git, so skipping `npm run build` ships whatever
# dist happened to be lying there — a window that looks like the code did not land.
build_gui() {
  if ! command -v wails >/dev/null 2>&1; then
    echo "  lotsman-gui      SKIPPED: no wails on PATH (enter client/gui/shell.nix), would have been:"
    echo "                   npm --prefix client/gui/frontend run build && wails build -tags webkit2_41 -ldflags \"$LDFLAGS\""
    return
  fi
  echo "  lotsman-gui      npm run build + wails build"
  ( cd client/gui/frontend && npm run build >/dev/null ) || { echo "gui: frontend build failed" >&2; exit 1; }
  ( cd client/gui && wails build -tags webkit2_41 -ldflags "$LDFLAGS" ) || { echo "gui: wails build failed" >&2; exit 1; }
  cp client/gui/build/bin/lotsman-gui "$OUT/" 2>/dev/null || true
}

for t in $targets; do
  case "$t" in
    all)
      build lotsmand      ./cmd/lotsmand
      build lotsmanctl    ./cmd/lotsmanctl
      build lotsman-client ./client/desktop
      # The tray is its own module (fyne/systray pulls a UI graph the service
      # must never link) and only cross-compiles cleanly for the host.
      if [ "$goos" = "$hostos" ] && [ "$goarch" = "$hostarch" ]; then
        build lotsman-tray . client/tray 1
        # The GUI belongs in `all` for one reason: it is the artefact the operator
        # READS the version off, and leaving it out of the default build is exactly
        # how it came to be older than the service it renders.
        build_gui
      else
        echo "  lotsman-tray     skipped (host-only: it links a desktop UI toolkit)"
        echo "  lotsman-gui      skipped (host-only: wails links a webkit toolchain)"
      fi
      ;;
    lotsmand)       build lotsmand ./cmd/lotsmand ;;
    lotsmanctl)     build lotsmanctl ./cmd/lotsmanctl ;;
    client)         build lotsman-client ./client/desktop ;;
    tray)           build lotsman-tray . client/tray 1 ;;
    gui)            build_gui ;;
    *) echo "unknown target: $t" >&2; exit 2 ;;
  esac
done

echo
echo "built into $OUT/ at $VERSION"
