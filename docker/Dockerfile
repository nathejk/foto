# syntax=docker/dockerfile:1
#
# Multistage build for the Nathejk foto service.
#
#   golang:1.25 ──► api-dev ──► base ──► build ──► alpine ──► prod
#
# There is no ui-dev / ui-builder stage and no SPA in the prod image: foto is
# headless by decision (PRD 001 §7). Do not add the standard Nathejk frontend
# stages here without a PRD change.
#
# Dev `docker compose up` only ever targets `base`. `prod` is the only stage that
# ships.

##########################################################################
# API (Go BFF)
##########################################################################

# api-dev: Go toolchain + inotify for the hot-reload dev loop. The linters
# (staticcheck/gosec/govulncheck) are `tool` directives in go/go.mod and run via
# `go tool`, so nothing extra is installed here — that also keeps them on the
# same toolchain as the build.
FROM golang:1.25 AS api-dev
RUN apt-get update \
    && apt-get install -y --no-install-recommends inotify-tools \
    && rm -rf /var/lib/apt/lists/*
ENV GOCACHE=/go/.cache/go-build
WORKDIR /app
COPY docker/init/api-dev /usr/local/bin/api-dev
RUN chmod +x /usr/local/bin/api-dev
ENTRYPOINT ["api-dev"]

# base: the image the `api` compose service runs. Modules are pre-downloaded so
# cold starts are fast; the real source is bind-mounted in dev.
FROM api-dev AS base
COPY go/go.mod go/go.sum ./
RUN go mod download
COPY go/ ./

# build: prod-only. Runs the gates, then compiles a static binary for the bare
# alpine prod image.
FROM base AS build
ARG BUILD_VERSION=dev
# Ignore go.work: CI and prod resolve shared-go from the module proxy at the
# version pinned in go.mod, not from the sibling checkout go.work points at and
# that does not exist in this image. This is also the gate that catches a change
# which only builds with the dev workspace active — which matters here, because
# foto is the first service to mount a shared-go `tables` entity.
ENV GOWORK=off
RUN go test -timeout 60s ./... \
    && go tool staticcheck ./...
# The build context has no .git, so runtime/debug VCS stamping cannot work —
# inject the CI-supplied version into vcs.version instead. Keep this path in sync
# with go/internal/vcs/vcs.go.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X foto.nathejk.dk/internal/vcs.version=${BUILD_VERSION}" \
    -o /foto-api \
    foto.nathejk.dk/cmd/api

##########################################################################
# Production image
##########################################################################

FROM alpine:3.20 AS prod
ARG BUILD_VERSION=dev
ENV SENTRY_RELEASE=${BUILD_VERSION}
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /foto-api /foto-api
ENV ENV=production
# Serve HTTP on the default port 80 (overridable via PORT). Traefik's docker
# provider discovers the service via this exposed port.
ENV PORT=80
EXPOSE 80
ENTRYPOINT ["/foto-api"]
