#!/bin/sh
# Cross-platform build helper for NatPilot.
#
#   ./build.sh                 build frontend + native binaries into bin/
#   ./build.sh --all           build ready-to-run bundles for all six targets
#   ./build.sh --frontend-only rebuild the SPA only
#   ./build.sh --docker-multi  build a multi-arch image (linux/amd64,arm64,arm/v7)
#
# --docker-multi requires buildx with a docker-container builder and a registry
# to push to (a multi-arch manifest cannot be loaded into the local daemon).
# Override with PLATFORMS=... REGISTRY=... IMAGE=... BUILDER=...
#
# There is no C dependency (the SQLite driver is pure Go), so every target
# cross-compiles from any host without a cross toolchain.
#
# --all produces one self-contained directory per target, which is what should
# be handed to users: the Electron shell looks for the server binary next to
# itself, so a bundle always works with no arguments.
#
#   dist/windows-amd64/
#     natpilot.exe            server + web UI (also usable on its own)
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

    out="$ROOT/dist/${GOOS}-${GOARCH}"
    mkdir -p "$out"
    echo "    -> dist/${GOOS}-${GOARCH}/"
    # Plain name on purpose: the Electron shell and docker images expect
    # "natpilot[.exe]", so a bundle runs with no arguments.
    ( cd "$ROOT" && GOOS="$GOOS" GOARCH="$GOARCH" \
        go build -trimpath -ldflags "$LDFLAGS" -o "$out/natpilot${ext}" . )
      done
  echo
  echo "==> bundles"
  for target in $TARGETS; do
    d="$ROOT/dist/${target%%/*}-${target##*/}"
    printf "  %-16s %s\n" "${target}" "$(ls -1 "$d" | tr '\n' ' ')"
  done
}

# build_docker_multi builds and pushes one manifest covering every platform.
# The Go/frontend stages cross-compile for TARGETARCH, so no QEMU is needed.
build_docker_multi() {
  platforms="${PLATFORMS:-linux/amd64,linux/arm64,linux/arm/v7}"
  image="${IMAGE:-natpilot}"
  registry="${REGISTRY:-}"
  builder="${BUILDER:-natpilot-multiarch}"
  tag="$image"
  [ -n "$registry" ] && tag="$registry/$image"
  echo "==> docker buildx (platforms: $platforms)"
  docker buildx inspect "$builder" >/dev/null 2>&1 ||
    docker buildx create --name "$builder" --driver docker-container >/dev/null
  docker buildx build --builder "$builder" \
    --platform "$platforms" \
    --build-arg VERSION="$VERSION" \
    --build-arg COMMIT="$COMMIT" \
    --build-arg BUILD_DATE="$BUILD_DATE" \
    -t "$tag:$VERSION" -t "$tag:latest" \
    --push .
}

case "${1:-}" in
  --frontend-only) build_frontend ;;
  --all) build_frontend; build_all ;;
  --docker-multi) build_docker_multi ;;
  *) build_frontend; build_native ;;
esac
echo "done"
