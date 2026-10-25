#!/bin/sh
# Cross-platform build helper for NatPilot.
#
#   ./build.sh                 build frontend + native binaries into bin/
#   ./build.sh --all           build ready-to-run bundles for all six targets
#   ./build.sh --frontend-only rebuild the SPA only
#
# There is no C dependency (the SQLite driver is pure Go), so every target
# cross-compiles from any host without a cross toolchain.
#
# --all produces one self-contained directory per target, which is what should
# be handed to users: the desktop shell looks for the server binary next to
# itself, so a bundle always works with no arguments.
#
#   dist/windows-amd64/
#     natpilot.exe            server + web UI (also usable on its own)
#     natpilot-desktop.exe    desktop shell (launches the server above)
set -eu

ROOT="$(cd "$(dirname "$0")" && pwd)"
VERSION="${VERSION:-0.1.0}"
COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}"

export CGO_ENABLED=0

TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

build_frontend() {
  echo "==> building frontend"
  ( cd "$ROOT/frontend" && npm install --include=dev --no-audit --no-fund && npm run build )
}

build_native() {
  echo "==> building native binaries"
  mkdir -p "$ROOT/bin"
  ( cd "$ROOT" && go build -trimpath -ldflags "$LDFLAGS" -o bin/natpilot . )
  ( cd "$ROOT" && go build -trimpath -ldflags "$LDFLAGS" -o bin/natpilot-desktop ./cmd/desktop )
  ls -lh "$ROOT/bin"
}

build_all() {
  echo "==> cross building release bundles (no cross toolchain required)"
  rm -rf "$ROOT/dist"
  for target in $TARGETS; do
    GOOS="${target%%/*}"
    GOARCH="${target##*/}"
    ext=""
    [ "$GOOS" = "windows" ] && ext=".exe"
    # The desktop shell must not pop a console window on Windows, so it is linked
    # as a GUI-subsystem binary; other platforms have no equivalent flag.
    dflags=""
    [ "$GOOS" = "windows" ] && dflags="-H windowsgui"
    out="$ROOT/dist/${GOOS}-${GOARCH}"
    mkdir -p "$out"
    echo "    -> dist/${GOOS}-${GOARCH}/"
    # Plain names on purpose: the desktop shell resolves "natpilot[.exe]"
    # alongside itself, so a bundle runs with no arguments.
    ( cd "$ROOT" && GOOS="$GOOS" GOARCH="$GOARCH" \
        go build -trimpath -ldflags "$LDFLAGS" -o "$out/natpilot${ext}" . )
    ( cd "$ROOT" && GOOS="$GOOS" GOARCH="$GOARCH" \
        go build -trimpath -ldflags "$LDFLAGS $dflags" -o "$out/natpilot-desktop${ext}" ./cmd/desktop )
  done
  echo
  echo "==> bundles"
  for target in $TARGETS; do
    d="$ROOT/dist/${target%%/*}-${target##*/}"
    printf "  %-16s %s\n" "${target}" "$(ls -1 "$d" | tr '\n' ' ')"
  done
}

case "${1:-}" in
  --frontend-only) build_frontend ;;
  --all) build_frontend; build_all ;;
  *) build_frontend; build_native ;;
esac
echo "done"
