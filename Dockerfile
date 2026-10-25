# syntax=docker/dockerfile:1
#
# Multi-stage build:
#   1. frontend  -> builds the Vue SPA with vite
#   2. backend   -> compiles the Go server with the SPA embedded
#   3. runtime   -> minimal alpine image running as a non-root user
#
# The SQLite driver is pure Go (modernc.org/sqlite), so CGO is off. That means
# no gcc/musl-dev anywhere and a statically linked binary.

# ---------- stage 1: frontend ----------
FROM node:22-alpine AS frontend
WORKDIR /src/frontend
# Install dependencies (dev dependencies are required for the vite build).
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install --include=dev --no-audit --no-fund
COPY frontend/ ./
# vite writes the bundle straight into the Go embed directory.
RUN mkdir -p /src/internal/webui/dist && npm run build

# ---------- stage 2: backend ----------
FROM golang:1.26-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Bring in the freshly built SPA so go:embed picks it up.
COPY --from=frontend /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=0.1.0
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ENV CGO_ENABLED=0
RUN go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/natpilot .

# ---------- stage 3: runtime ----------
FROM alpine:3.20
# No sqlite-libs: the SQLite engine is compiled into the binary.
RUN apk add --no-cache ca-certificates tzdata iproute2 wget \
 && addgroup -S natpilot && adduser -S -G natpilot -h /var/lib/natpilot natpilot
COPY --from=backend /out/natpilot /usr/local/bin/natpilot
RUN mkdir -p /var/lib/natpilot && chown natpilot:natpilot /var/lib/natpilot
USER natpilot
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
