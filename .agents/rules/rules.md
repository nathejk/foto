# Org-Wide Agent Rules

This file defines conventions shared across all repos in this organisation.
Repo-level `.rules` files reference this document — read it fully before acting.

---

## Infrastructure Overview

All development runs inside Docker containers orchestrated with `docker-compose.yml`.
There is no expectation of anything being installed or runnable directly on the host.

### Shared infrastructure
A dedicated org-wide repo defines all shared resources — do not redefine these
in individual repos:
- **Traefik** reverse proxy and routing
- **JetStream** (NATS) messaging
- A shared external Docker network named `traefik` (and, where messaging is
  needed, `jetstream`) is sourced from that repo — never create a local
  `traefik` or `jetstream` network in a repo's compose file. Each web-exposed
  service joins `traefik` directly.

### Shared infrastructure contract
These values mirror the infra repo's `docker-compose.yml` (the source of truth —
do not diverge). They are the stable, non-secret facts a repo needs to route
traffic and connect to messaging.

**Traefik** (`traefik:v3.0`, Docker provider, dashboard at `local.nathejk.dk`)
- External network: `traefik` (declare it `external: true` in your compose).
- Entrypoints: `web` (:80) and `websecure` (:443), bound to `127.0.0.1` on the
  host.
- TLS is available via the ACME cert resolver **`desec`** and a shared
  **`redirect-to-https`** middleware (both defined in the infra repo). HTTPS in
  dev is **not for security** (everything is on localhost) but to give the SPA a
  browser **secure context**: `*.local.nathejk.dk` isn't treated as `localhost`,
  so secure-context-only APIs (Navigation API, geolocation, clipboard, service
  workers, WebCrypto) require HTTPS. User-facing SPAs should use the redirect
  pattern; internal-only dev tools can stay on plain HTTP.
- Base labels every web-exposed service needs: `traefik.enable=true`,
  `traefik.docker.network=traefik`, and
  `traefik.http.services.<svc>.loadbalancer.server.port=<port>` **only** when
  the container's web port is not 80 (e.g. MailHog → 8025).
- Hostnames: any subdomain of `local.nathejk.dk`.

Pick one of three exposure patterns per service:

1. **Plain HTTP** (simplest; fine for internal dev tools). One router, no
   `entrypoints`/`tls` — it attaches to all entrypoints, no TLS termination.
   Use `http://` URLs.
   ```yaml
   traefik.http.routers.<svc>.rule: Host(`<sub>.local.nathejk.dk`)
   ```

2. **Redirect HTTP → HTTPS** (recommended for user-facing services). Two
   routers: the `web` one redirects, the `websecure` one serves with the cert.
   Use `https://` URLs.
   ```yaml
   # HTTP: redirect to HTTPS
   traefik.http.routers.<svc>.rule: Host(`<sub>.local.nathejk.dk`)
   traefik.http.routers.<svc>.entrypoints: web
   traefik.http.routers.<svc>.middlewares: redirect-to-https
   # HTTPS: serve with the LE/desec cert
   traefik.http.routers.<svc>-secure.rule: Host(`<sub>.local.nathejk.dk`)
   traefik.http.routers.<svc>-secure.entrypoints: websecure
   traefik.http.routers.<svc>-secure.tls.certresolver: desec
   ```

3. **Serve both HTTP and HTTPS** (no redirect — both schemes work). Same as (2)
   but drop the `redirect-to-https` middleware so the `web` router serves
   directly instead of redirecting.

When a service has two routers, point both at the same service explicitly
(`traefik.http.routers.<svc>.service=<svc>` /
`...<svc>-secure.service=<svc>`) if Traefik can't infer it.

**JetStream / NATS** (`nats:2.10-alpine -js`)
- Network: `jetstream` (declare it `external: true`).
- Client DSN (dev): `nats://jetstream:4222`; monitoring on `:8222`.
- A NATS UI (`nui`) is available at `jetstream.local.nathejk.dk`.

### Container architecture (standard web repos)
Most repos follow this pattern:

| Stage | Technology | Purpose |
|---|---|---|
| Frontend | Vue 3 (JS/TS) | Single page application |
| BFF | Go | Backend-for-frontend — serves the JSON API and (in prod) the SPA |
| Dev containers | Node or Go (multistage) | One per service, from shared Dockerfile |
| Production | Single bundled container | Frontend assets served by Go binary |

The `docker/Dockerfile` uses multistage builds. The frontend dev target is
`ui-dev` and the backend dev target is `base` (built on an `api-dev` Go toolchain
stage); intermediate `build` / `ui-builder` stages produce the artefacts baked
into the single `prod` target. Always use a dev target during development —
never build the `prod` image for local work.

---

## Docker & Compose conventions

- All services are defined in `docker-compose.yml` at repo root
- Development services must mount source code as volumes for hot reload
- Each service that serves web traffic declares **its own** Traefik config:
  - Join the external `traefik` network (in addition to the repo-local `local`
    network).
  - Set `traefik.enable`, `traefik.docker.network=traefik`, and a router
    `Host(...)` rule. Add a `loadbalancer.server.port` **only** when the
    container listens on a non-80 port (Traefik defaults to 80).
  - Use repo-scoped router/service names (e.g. `<repo>`, `<repo>-<service>`)
    to avoid collisions on the shared Traefik.
  - Choose an HTTPS strategy — plain HTTP, redirect HTTP→HTTPS, or serve both
    — per the three patterns in the Traefik contract above (redirect/serve-both
    use two routers + the `desec` cert resolver).
  - Never publish ports to the host with `ports:` — Traefik handles routing.
- Services reached only internally (e.g. the Go `api` via the frontend's dev
  proxy, the database) stay on `local` only and get no Traefik labels.
- Committed environment defaults live in `docker-compose.yml`; per-developer
  overrides and secrets go in `docker-compose.override.yml`, which is gitignored
- Never hardcode secrets or hostnames — always use environment variables

### Per-service Traefik label pattern
```yaml
<service>:
  networks:
    - local
    - traefik
  labels:
    traefik.enable: true
    traefik.docker.network: traefik
    # only when the container port is not 80:
    # traefik.http.services.<repo>-<service>.loadbalancer.server.port: <port>

    # --- Option 1: plain HTTP (attaches to all entrypoints) ---
    traefik.http.routers.<repo>-<service>.rule: Host(`<sub>.local.nathejk.dk`)

    # --- Option 2: redirect HTTP -> HTTPS (recommended for user-facing) ---
    # traefik.http.routers.<repo>-<service>.rule: Host(`<sub>.local.nathejk.dk`)
    # traefik.http.routers.<repo>-<service>.entrypoints: web
    # traefik.http.routers.<repo>-<service>.middlewares: redirect-to-https
    # traefik.http.routers.<repo>-<service>-secure.rule: Host(`<sub>.local.nathejk.dk`)
    # traefik.http.routers.<repo>-<service>-secure.entrypoints: websecure
    # traefik.http.routers.<repo>-<service>-secure.tls.certresolver: desec
    #
    # Option 3 (serve both): as Option 2 without the redirect-to-https middleware.
networks:
  traefik:
    external: true
```

---

## Developer groups

| Group | Name | Access |
|---|---|---|
| **Core** | Production developers | Full repo access — may write and deploy production code |
| **Support** | Supporting systems | Access limited to infra/, tools/, scripts/, config/ — not application code |

When creating tasks or reviewing changes, note which group the work belongs to.

---

## Task board

Tasks are tracked using the file-based kanban system under `roadmap/tasks/`
(PRDs live alongside them in `roadmap/prd/`). Before picking up, creating, or
updating any task, read `roadmap/tasks/TASKS.md`. The `file-task-board` skill
describes the full workflow.

Commit message format: `task(<id>): <action> — <title>`
Actions: `create` · `pick up` · `update` · `done` · `reopen`

PRDs follow the same pattern in `roadmap/prd/{draft,doing,done}/`: the folder is
the status, and the `Status` header must always agree with it. See
`roadmap/prd/README.md` and the `prd` skill.

Commit message format: `prd(<number>): <action> — <title>`
Actions: `create` · `update` · `approve` · `done` · `reopen`

---

## Git conventions

- Branch format: `<task-id>-<short-slug>` — e.g. `003-add-jwt-middleware`
- Commits are small and focused — one logical change per commit
- Never commit directly to `main` — always branch and PR
- `.env` files are always gitignored — `.env.example` is always committed

---

## General agent behaviour

- Always work inside Docker — never run Node, Go, or build tools directly on the host
- Use `docker compose run --rm <service> <command>` for one-off commands
- When in doubt about the dev environment, check `docker-compose.yml` first
- If a shared resource (Traefik network, JetStream) is missing, the org-wide
  infra repo needs to be running — flag this rather than working around it
