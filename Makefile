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

.PHONY: all build frontend cross dev test vet fmt tidy docker run clean help desktop

all: frontend build

help:
	@grep -E '^[a-zA-Z_-]+:' $(MAKEFILE_LIST) | sed 's/:.*//' | sort -u

frontend:
	cd frontend && npm install --include=dev --no-audit --no-fund && npm run build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-desktop ./cmd/desktop

# Package the tray-resident desktop edition (Electron). Requires Node.js and,
# on first run, network access for the electron/electron-builder downloads.
# The Go server binary is bundled from bin/ via `extraResources`.
desktop: build
	cd desktop && npm install --no-audit --no-fund && npm run dist

# Build one self-contained directory per target. Each bundle holds the server
# and the launcher under their plain names, so the launcher finds the server
# next to itself and runs with no arguments.
cross: frontend
	@rm -rf dist
	@for t in $(TARGETS); do \
	  os=$${t%%/*}; arch=$${t##*/}; ext=""; \
	  [ "$$os" = "windows" ] && ext=".exe"; \
	  out=dist/$$os-$$arch; mkdir -p $$out; \
	  echo "  -> $$out/"; \
	  GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out/$(BINARY)$$ext . || exit 1; \
	  GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out/$(BINARY)-desktop$$ext ./cmd/desktop || exit 1; \
	done
	@echo "==> bundles"; @ls -1 dist

dev: build
	./bin/$(BINARY) serve --port 8787

frontend-dev:
	cd frontend && npm run dev

test:
	go test ./... -count=1

vet:
	go vet ./...
	@for os in darwin windows; do GOOS=$$os go vet ./... || exit 1; done

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './frontend/*' -not -path './.gocache/*')

tidy:
	go mod tidy

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) -t natpilot:$(VERSION) -t natpilot:latest .

run: build
	./bin/$(BINARY) --data-dir ./data serve

install: build
	install -m 0755 bin/$(BINARY) /usr/local/bin/$(BINARY)

clean:
	rm -rf bin dist internal/webui/dist/assets internal/webui/dist/index.html
