# NatPilot build & operations
#
# Targets:
#   make build        build the native binaries into bin/
#   make frontend     build the SPA into internal/webui/dist
#   make all          frontend + native binaries
#   make cross        build ready-to-run bundles for the six supported targets
#   make dev          build and run the API on :8787
#   make test         run go tests
#   make vet          run go vet
#   make docker       build the container image
#   make clean        remove build artifacts

SHELL      := /bin/sh
BINARY     := natpilot
VERSION    ?= 0.1.0
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

# The SQLite driver is pure Go (modernc.org/sqlite), so no C toolchain is needed
# and every target below cross-compiles from any host.
export CGO_ENABLED = 0

TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build frontend cross cross-extra dev test vet fmt tidy docker docker-builder docker-multi docker-bake docker-load run clean help desktop

all: frontend build

help:
	@grep -E '^[a-zA-Z_-]+:' $(MAKEFILE_LIST) | sed 's/:.*//' | sort -u

frontend:
	cd frontend && npm install --include=dev --no-audit --no-fund && npm run build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

# Package the tray-resident desktop edition (Electron). Requires Node.js and,
# on first run, network access for the electron/electron-builder downloads.
# The Go server binary is bundled from bin/ via `extraResources`.
desktop: build
	cd desktop && npm install --no-audit --no-fund && npm run dist

# Build one self-contained directory per target. Each bundle holds the server
# under their plain names, which is what the Electron shell and docker images expect
# next to itself and runs with no arguments.
cross: frontend
	@rm -rf dist
	@for t in $(TARGETS); do \
	  os=$${t%%/*}; arch=$${t##*/}; ext=""; \
	  [ "$$os" = "windows" ] && ext=".exe"; \
	  out=dist/$$os-$$arch; mkdir -p $$out; \
	  echo "  -> $$out/"; \
	  GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out/$(BINARY)$$ext . || exit 1; \
	done
	@echo "==> bundles"; @ls -1 dist

# cross-extra adds the architectures that are not part of the default six:
# 32-bit ARM (arm/v7, common on routers/NAS) and 386 (legacy x86).
#
# freebsd is intentionally absent: internal/netutil implements route/neighbour
# lookups per platform (linux/darwin/windows) and has no freebsd file yet, so the
# build fails with "undefined: platformRouteGateway". Add netutil/route_freebsd.go
# + neigh_freebsd.go first if that target is needed.
EXTRA_TARGETS := linux/arm linux/386
cross-extra: frontend
	@for t in $(EXTRA_TARGETS); do \
	  os=$${t%%/*}; arch=$${t##*/}; \
	  out=dist/$$os-$$arch; mkdir -p $$out; \
	  echo "  -> $$out/"; \
	  GOOS=$$os GOARCH=$$arch GOARM=7 go build -trimpath -ldflags "$(LDFLAGS)" -o $$out/$(BINARY) . || exit 1; \
	done
	@echo "==> extra bundles"; @ls -1 dist

dev: build
	./bin/$(BINARY) serve --port 8787

frontend-dev:
	cd frontend && npm run dev

test:
	go test ./... -count=1

vet:
	go vet ./...
	@for os in darwin windows; do GOOS=$$os go vet ./... || exit 1; done
	# 32-bit targets catch `int` overflow of large constants (1<<31 etc.) that
	# amd64 silently accepts — this is what broke the linux/arm multi-arch image.
	@for arch in 386 arm; do GOOS=linux GOARCH=$$arch go vet ./... || exit 1; done

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './frontend/*' -not -path './.gocache/*')

tidy:
	go mod tidy

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) -t natpilot:$(VERSION) -t natpilot:latest .
# ---------- multi-architecture images ----------
# PLATFORMS lists the manifest entries to build. arm/v7 is included because the
# Go toolchain cross-compiles it for free (GOARM is derived from TARGETVARIANT);
# drop it if you do not need 32-bit ARM boards.
PLATFORMS ?= linux/amd64,linux/arm64,linux/arm/v7
IMAGE     ?= natpilot
BUILDER   ?= natpilot-multiarch
REGISTRY  ?=

# A docker-container builder is required for multi-platform output; the default
# "docker" driver cannot produce a manifest list.
docker-builder:
	@docker buildx inspect $(BUILDER) >/dev/null 2>&1 || \
		docker buildx create --name $(BUILDER) --driver docker-container >/dev/null

# docker-multi builds one manifest covering every platform in PLATFORMS.
# --push is mandatory: the local daemon cannot store a multi-arch manifest.
docker-multi: docker-builder
	docker buildx build --builder $(BUILDER) \
		--platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(if $(REGISTRY),$(REGISTRY)/,)$(IMAGE):$(VERSION) \
		-t $(if $(REGISTRY),$(REGISTRY)/,)$(IMAGE):latest \
		--push .

# docker-bake is the same build driven by docker-bake.hcl (declarative, easier
# to extend with more tags/registries). Override with VERSION=/COMMIT=/REGISTRY=.
docker-bake: docker-builder
	@docker buildx create --name $(BUILDER) --driver docker-container >/dev/null 2>&1 || true
	VERSION=$(VERSION) COMMIT=$(COMMIT) BUILD_DATE=$(BUILD_DATE) REGISTRY=$(REGISTRY) \
		docker buildx bake --builder $(BUILDER) -f docker-bake.hcl --push

# docker-load builds only the host platform and loads it into the local daemon,
# which is what you want for `docker compose -f docker-compose.hostnet.yml up`.
docker-load: docker-builder
	docker buildx build --builder $(BUILDER) --load \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

run: build
	./bin/$(BINARY) --data-dir ./data serve

install: build
	install -m 0755 bin/$(BINARY) /usr/local/bin/$(BINARY)

clean:
	rm -rf bin dist internal/webui/dist/assets internal/webui/dist/index.html
