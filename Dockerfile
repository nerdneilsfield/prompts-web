# syntax=docker/dockerfile:1
# Two build stages (Bun for the SPA, Go for the binary with the SPA embedded)
# and a runtime that contains neither Bun nor Node.
#
# Build from the repository root:
#   container build -f Dockerfile -t prompts-web:local .

# --- frontend -----------------------------------------------------------------
FROM docker.io/oven/bun:1.4.3-alpine AS web
WORKDIR /src
# Manifest and lockfile first so the dependency layer survives source edits.
COPY package.json bun.lock ./
# Fails if package.json and bun.lock disagree, which is what we want.
RUN bun install --frozen-lockfile
COPY tsconfig.json vite.config.ts ./
COPY web ./web
# Rebuild dist from source; a dist in the build context is never trusted.
RUN bun run build && test -f web/dist/index.html

# --- backend ------------------------------------------------------------------
FROM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/embed.go
# go:embed requires the directory to exist; place the freshly built SPA there.
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/prompts-web ./cmd/server

# --- runtime ------------------------------------------------------------------
FROM docker.io/library/alpine:3.22
# ca-certificates is a runtime dependency: the service calls an HTTPS model endpoint.
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 -h /app app && \
    mkdir -p /data && chown app:app /data && chmod 0700 /data
COPY --from=build /out/prompts-web /usr/local/bin/prompts-web

WORKDIR /app
# Non-root, fixed, straight into the Go binary: no privilege-fixing wrapper.
# A fresh named volume inherits this image directory (owner 10001), so mount one
# at /data. A host bind mount must be owned by the uid the container runs as;
# see README "Apple container" for the explicit --uid/--gid form.
USER 10001:10001
ENV DATA_DIR=/data \
    LISTEN_ADDR=0.0.0.0:8080 \
    APP_ORIGIN=http://localhost:8080
VOLUME ["/data"]
EXPOSE 8080
# busybox wget is already in the base image; no extra tooling is added.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -q -T 3 -t 1 -O /dev/null http://127.0.0.1:8080/api/session || exit 1
ENTRYPOINT ["/usr/local/bin/prompts-web"]
