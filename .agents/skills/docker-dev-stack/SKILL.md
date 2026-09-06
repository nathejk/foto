---
name: docker-dev-stack
description: >
  How standard Nathejk repos are containerised for development and
  production: the multistage Dockerfile, the docker-compose service graph,
  the per-service Traefik routing, and the dev-loop init scripts. Apply this
  skill when adding/editing services, build stages, environment variables,
  Traefik labels, or the dev hot-reload setup. Trigger phrases:
  "docker-compose", "Dockerfile", "build target", "add a service",
  "Traefik", "routing", "hot reload", "dev container", "prod build",
  "compose override".
---

# Docker Dev Stack

All Nathejk repos run entirely in containers. Nothing — Go, Node, npm, mysql,
nats — is expected to be installed on the host. Org-wide rules for this live
in `nathejk/conventions/rules.md` (Traefik network, label patterns, env var
handling). This skill describes the *concrete shape* a repo's Docker setup
should have.

---

## Files

```
docker-compose.yml              # service graph (committed)
docker-compose.override.yml     # local secrets / dev overrides (gitignored)
docker/
├── Dockerfile                  # multistage: api-dev → base → build,
│                               #             ui-dev → ui-builder, prod
├── init/
│   ├── api-dev                 # entrypoint for the api dev container
│   └── ui-dev                  # entrypoint for the ui dev container
└── bin/
    ├── init                    # entrypoint for the prod container
    └── init-dev                # alt dev entrypoint variant
```

---

## Multistage Dockerfile

The build graph (matches `docker/Dockerfile`):

```
golang:1.25 ──► api-dev ──► base ──► build ─┐
                                            │
node:20-alpine ──► ui-dev ──► ui-builder ───┤
                                            ▼
                                  alpine ──► prod
```

| Stage        | Used by                        | Notes                                                |
|--------------|--------------------------------|------------------------------------------------------|
| `api-dev`    | intermediate (base for `base`) | Go toolchain + `inotify-tools` + `staticcheck`       |
| `base`       | `api` service in compose       | `api-dev` + `go mod download` + source copied        |
| `build`      | intermediate, prod build only  | runs `go test` + `staticcheck`, builds static binary |
| `ui-dev`     | `ui` service in compose        | Node 20 alpine + sources                             |
| `ui-builder` | intermediate, prod build only  | `npm ci` + `npm run build` → `/app/dist`             |
| `prod`       | the production image           | alpine + Go binary + built SPA + `docker/bin/init`   |

Rules:

- Dev `up` only ever needs `api-dev` and `ui-dev`. **Never** target `prod`
  (or `build`/`ui-builder`) for `docker compose up`.
- `prod` is the only stage that ships. It contains the binary at
  `/tilmelding-api` and the SPA at `/www`, served by the Go binary as a
  fallback after `/api/...`.
- The `prod` stage declares `ARG BUILD_VERSION` and exposes it as
  `SENTRY_RELEASE`. CI (`.github/workflows/build-and-publish.yml`) passes
  `BUILD_VERSION` (plus `GIT_COMMIT`, `GIT_BRANCH`, `BUILD_NUMBER`) as
  build-args, so `SENTRY_RELEASE` carries the `<branch>.<run_number>` tag in
  published images. Keep the arg name in sync between the Dockerfile and the
  workflow.
- The Go binary in `build` is statically linked
  (`-extldflags "-static" -w -s`, `CGO_ENABLED=1`, `GOARCH=amd64`) so it
  runs on the bare alpine `prod` image.

Adding a new build stage: chain `FROM <existing-stage> AS <new-stage>`
inside the same Dockerfile — don't introduce a second Dockerfile.

---

## docker-compose.yml — service graph

Standard services in a Nathejk repo:

| Service     | Image / build           | Role                                                |
|-------------|-------------------------|-----------------------------------------------------|
| `ui`        | build target `ui-dev`   | Vue 3 dev server on :80 (web-exposed via Traefik)  |
| `api`       | build target `base`     | Go BFF — `go run ./cmd/api` (internal only)        |
| `db`        | `mariadb:10.8`          | MariaDB 10.8 (internal only)                       |
| `phpmyadmin`| `phpmyadmin`            | DB inspection UI (web-exposed via Traefik)         |
| `mail`      | `mailhog/mailhog`       | Captures dev SMTP; UI web-exposed via Traefik      |

### Networks

- `local` — created by this repo, internal traffic only.
- `traefik` — **external**, declared in the org-wide infra repo. Every
  web-exposed service joins it directly. Never recreate `traefik` in a repo's
  compose file.
- `jetstream` — **external**, same story. Only services that need NATS
  (typically `api`) join it.

### Routing

Each service that needs to be reachable from the browser declares **its own**
Traefik labels and joins the external `traefik` network. There is no gateway
service — Traefik routes straight to each container. Use repo-scoped
router/service names (`<repo>`, `<repo>-<service>`) so they don't collide with
other repos on the shared Traefik.

The user-facing SPA (`ui`) is served over **HTTPS via a redirect** so it always
loads in a browser secure context (see the Vue skill / org rules for why
`*.local.nathejk.dk` needs this). Two routers — the `web` one redirects, the
`-secure` one serves with the `desec` cert:

```yaml
ui:
  networks:
    - local
    - traefik
  labels:
    traefik.enable: true
    traefik.docker.network: traefik
    traefik.http.routers.tilmelding.rule: Host(`tilmelding.local.nathejk.dk`)
    traefik.http.routers.tilmelding.entrypoints: web
    traefik.http.routers.tilmelding.middlewares: redirect-to-https
    traefik.http.routers.tilmelding-secure.rule: Host(`tilmelding.local.nathejk.dk`)
    traefik.http.routers.tilmelding-secure.entrypoints: websecure
    traefik.http.routers.tilmelding-secure.tls.certresolver: desec
```

Internal dev tools that don't need a secure context use **plain HTTP** — a
single Host-rule router, no `entrypoints`/`tls` (attaches to all entrypoints):

```yaml
phpmyadmin:
  labels:
    traefik.enable: true
    traefik.docker.network: traefik
    traefik.http.routers.tilmelding-sql.rule: Host(`sql.tilmelding.local.nathejk.dk`)
```

A third option, **serve both HTTP and HTTPS**, is the redirect pattern without
the `redirect-to-https` middleware. Only add
`traefik.http.services.<name>.loadbalancer.server.port` when the container
listens on a **non-80** port — Traefik defaults to 80, so `ui`/`phpmyadmin`
omit it while `mail` sets `8025`.

Current host mappings: `tilmelding.local.nathejk.dk` → `ui` (HTTPS, redirected),
`sql.tilmelding.local.nathejk.dk` → `phpmyadmin` and
`mail.tilmelding.local.nathejk.dk` → `mail` (port 8025), both plain HTTP.
Services reached only internally — the Go `api` (via the Vite dev proxy for
`/api` + `/callback`) and `db` — stay on `local` only and get **no** Traefik
labels.

### Volumes

- `./go:/app` and `./vue:/app` — source mounts for hot reload. Keep them.
- `../shared-go:/shared-go` — sibling checkout of
  `github.com/nathejk/shared-go`, resolved via `go/go.work` so edits to shared
  code are picked up live. Prod/CI builds set `GOWORK=off` and resolve it from
  the module proxy instead.
- `api:/go` — named volume for the Go module/build cache. Speeds up restart
  loops.
- `ui-node_modules:/app/node_modules` — named volume so the host's
  (possibly missing) `node_modules` doesn't shadow the container's.

### Environment variables

- Committed defaults live in `docker-compose.yml`.
- Secrets and per-developer overrides live in `docker-compose.override.yml`,
  which **is gitignored**. Never put real secrets in `docker-compose.yml`.
- Read env at the binary's entrypoint (Go `flag.StringVar(... os.Getenv ...)`,
  Vite `import.meta.env`). Do not read env vars deep in call trees.

---

## Dev loop

### `api`

`docker/init/api-dev` runs an `inotifywait` loop:

1. At startup, once: `go tool gosec ./...` and `go tool govulncheck ./...`
   (report-only, non-blocking).
2. `go get -v ./...` → `go test -timeout 10s ./...` → `go vet ./...` →
   `go tool staticcheck ./...` → `go build ./...`
3. If everything passes, `go run $GO_BUILD_FLAGS nathejk.dk/cmd/api &`.
4. `inotifywait` blocks on `*.go` / `*.sql` changes.
5. On change: kill the running binary (and children) and loop.

Dev tools are `tool` directives in `go/go.mod`, run via `go tool` (see the
`go-bff-layout` skill), so they always match the current toolchain. The Go
build cache is kept in the `api:/go` volume via `GOCACHE=/go/.cache/go-build`
so tools aren't recompiled on every start.

`GO_BUILD_FLAGS` (e.g. `-race`) is set via env in compose if you want it.

### `ui`

`docker/init/ui-dev` is just:

```sh
npm ci --no-audit --no-fund
npm run dev
```

The container uses the npm bundled with Node 20 (v10) — do **not** re-pin an
older npm. Two things keep `npm ci` from hanging on every container start:
`--no-audit --no-fund` (the registry audit call would otherwise stall after the
install), and `CYPRESS_INSTALL_BINARY=0` in the `ui` service env (skips
Cypress's large binary download; e2e runs separately, not in this container).

Vite handles HMR. If `package-lock.json` changes you usually want
`docker compose run --rm ui npm ci` or `docker compose build ui` to refresh
the volume.

---

## Common commands

```sh
# bring everything up
docker compose up -d

# rebuild a single image after Dockerfile / lockfile change
docker compose build api
docker compose build ui

# tail logs
docker compose logs -f api ui

# one-off in a container (preferred over host commands)
docker compose run --rm api go test ./...
docker compose run --rm ui  npm run lint

# nuke local volumes (DB, node_modules cache, go cache)
docker compose down -v
```

To build the production image locally:

```sh
docker build -f docker/Dockerfile --target prod -t tilmelding:local .
```

---

## Adding a service

1. Add the service block to `docker-compose.yml`.
2. Put it on `local`. Add `traefik` / `jetstream` only if it actually needs
   the shared external networks.
3. If it must be reachable from the browser, join the `traefik` network and add
   its own labels: `traefik.enable`, `traefik.docker.network=traefik`, and a
   `traefik.http.routers.<repo>-<service>.rule=Host(`<sub>.local.nathejk.dk`)`.
   Add `traefik.http.services.<repo>-<service>.loadbalancer.server.port=<port>`
   **only** if the container listens on a non-80 port (Traefik defaults to 80).
   Choose an HTTPS strategy (plain HTTP / redirect / serve-both) per the Routing
   section above. If it's internal-only, leave it on `local` with no labels.
4. If it needs a build, extend `docker/Dockerfile` with a new stage rather
   than adding a sibling Dockerfile.
5. Document any required env vars in `docker-compose.yml` with a sane dev
   default. Real secrets go in `docker-compose.override.yml`.

---

## Don'ts

- Don't run Go or Node directly on the host.
- Don't `docker compose up` the `prod` target — it has no source mounts and
  no hot reload.
- Don't define a local `traefik` or `jetstream` network — they are
  `external` and owned by the infra repo.
- Don't give the same router/service name to two repos on the shared Traefik —
  scope names with the repo prefix (`tilmelding`, `tilmelding-sql`, …).
- Don't commit `docker-compose.override.yml`. Don't move secrets out of it
  into `docker-compose.yml`.
- Don't publish container ports to the host with `ports:` in dev — web-exposed
  services are reached through Traefik, internal ones through the `local`
  network. (`EXPOSE` in the Dockerfile is fine and expected — Traefik's docker
  provider uses it to discover a service's port.)
