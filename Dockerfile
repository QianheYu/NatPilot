# syntax=docker/dockerfile:1
#
# Multi-stage, multi-architecture build.
#
#   1. frontend  -> builds the Vue SPA with vite
#   2. backend   -> compiles the Go server with the SPA embedded
#   3. runtime   -> minimal alpine image (runs as root by design, see below)
#
# Cross-compilation strategy:
#   The frontend and backend stages run on the *build* platform
#   (--platform=$BUILDPLATFORM) and emit artifacts for the *target* platform
#   (TARGETOS/TARGETARCH/TARGETVARIANT). This is much faster than running the
#   compilers under QEMU emulation, and it works because the SQLite driver is
#   pure Go (modernc.org/sqlite) so CGO stays off: no gcc, no libc concerns,
#   one static binary per architecture.
#
#   Single-arch image for the host:
#       docker build -t natpilot:dev .
#   Multi-arch manifest (buildx + a registry to push to):
#       docker buildx build --platform linux/amd64,linux/arm64 \
#         -t registry.example.com/natpilot:0.1.0 --push .
#   Or via make:
#       make docker-multi       # amd64 + arm64 (+ arm/v7), pushes
#       make docker-bake        # same, driven by docker-bake.hcl
#
# Docker cannot load a multi-arch manifest into the local daemon, so multi-arch
# targets always push. Use the single-arch target for local runs.

# TARGET* are supplied by BuildKit for every build (they default to the build
# platform for a plain `docker build`), so single-arch builds keep working.
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

# ---------- stage 1: frontend ----------
# Runs on the build platform: the SPA is architecture independent, there is no
# reason to emulate another architecture just to run vite.
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend
WORKDIR /src/frontend
# Install dependencies (dev dependencies are required for the vite build).
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install --include=dev --no-audit --no-fund
COPY frontend/ ./
# vite writes the bundle straight into the Go embed directory.
RUN mkdir -p /src/internal/webui/dist && npm run build

# ---------- stage 2: backend ----------
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS backend
# Re-declare in this stage: ARGs are per-stage scoped.
ARG TARGETOS=linux
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=0.1.0
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Bring in the freshly built SPA so go:embed picks it up.
COPY --from=frontend /src/internal/webui/dist ./internal/webui/dist
ENV CGO_ENABLED=0
# GOARM only matters for linux/arm/v6|v7: it is derived from TARGETVARIANT
# ("v7" -> "7") and stays empty for amd64/arm64, where Go ignores it.
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/natpilot .

# ---------- stage 3: runtime ----------
# No --platform here: this stage must be the *target* architecture.
FROM alpine:3.20
# No sqlite-libs: the SQLite engine is compiled into the binary.
RUN apk add --no-cache ca-certificates tzdata iproute2 wget
COPY --from=backend /out/natpilot /usr/local/bin/natpilot
# 不创建额外的用户与组，容器默认以 root 运行：
# 这样绑定挂载宿主目录时不会因容器内 uid 与宿主属主不一致而出现
# SQLITE_CANTOPEN (14)。需要非 root 运行时用 `docker run --user <uid>:<gid>`
# 覆盖即可，前提是该 uid 对 /var/lib/natpilot 有写权限。
RUN mkdir -p /var/lib/natpilot
# NATPILOT_SERVER=1 → server 模式 → 默认要求登录（auth.enabled 未设置时按模式推导）。
# 首次启动会创建管理员并打印随机密码；也可以用 NATPILOT_ADMIN_PASSWORD 指定。
# 想让容器免登录（不推荐）可显式设置 NATPILOT_AUTH=false。
ENV NATPILOT_DATA_DIR=/var/lib/natpilot \
    NATPILOT_HOST=0.0.0.0 \
    NATPILOT_PORT=8787 \
    NATPILOT_SERVER=1
VOLUME ["/var/lib/natpilot"]
EXPOSE 8787
# The web UI and the REST API share one port.
# NOTE: run with --network host (Linux) so SSDP/UPnP discovery reaches the router.
HEALTHCHECK --interval=30s --timeout=4s --start-period=6s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8787/api/health >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/natpilot"]
CMD ["serve"]
