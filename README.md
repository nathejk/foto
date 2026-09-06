# foto

The entrypoint for photographs into the nathejk ecosystem.

`foto` receives a callback from the camera app ([`nathejk/kamera`](https://github.com/nathejk/kamera))
when a photo has been taken, stores the original, derives a cache of downscaled
renditions, and publishes `NATHEJK.<year>.patrulje.<teamID>.photographed` on the
internal event stream. Other services read photo metadata off the stream and
fetch the bytes from here.

See **PRD 001** (`roadmap/prd/`) for the design and the decisions behind it, and
`roadmap/tasks/` for execution state.

## What must be backed up

**The blob store, and nothing else.**

Every table in MariaDB is a projection and is rebuilt by replaying the event
stream on boot. The photo objects are not: the event carries a *reference*, not
the bytes. Losing the `blobs` volume loses the photographs permanently, and no
replay will bring them back.

Two consequences:

- `docker compose down -v` destroys the dev blob store. That is fine in dev and
  catastrophic in production.
- The stored originals are kept **exactly as uploaded, with metadata intact**
  (PRD 001 §6), so the volume can contain EXIF GPS coordinates of where a child
  was photographed. Originals are never served — only re-encoded renditions are,
  and re-encoding cannot carry metadata — and the store is `0700`/`0600`. Treat
  the volume accordingly.

## Running it

Everything runs in containers. Nothing — Go, mysql, nats — is expected on the
host.

```sh
docker compose up -d          # api + db + phpmyadmin
docker compose logs -f api    # the dev loop
```

The `api` container's entrypoint (`docker/init/api-dev`) runs the gates
(`go test`, `go vet`, `go tool staticcheck`, `go build`, plus a `GOWORK=off`
pinned build) and restarts the binary on any `.go`/`.sql` change. You do not
normally restart it by hand.

One-off commands:

```sh
docker compose run --rm api go test ./...
docker compose run --rm --entrypoint go api tool staticcheck ./...
```

Requires the org infra repo's external `traefik` and `jetstream` networks.

## Layout

```
go/
├── cmd/api/              # the binary: wiring, routes, handlers
│   ├── main.go           # startup order and the readiness rule
│   ├── eventing.go       # the cqrs seam: Publisher / Writer / Reader
│   ├── patrulje.go       # mounts shared-go's patrulje projection
│   ├── env.go database.go routes.go healthcheck.go
│   └── app/              # transport helpers (embed app.JsonApi)
└── internal/
    ├── teamnumber/       # kamera's teamNumber → domain teamID
    └── vcs/              # build-time version stamping
```

There is **no `vue/` workspace**: `foto` is headless by decision (PRD 001 §7).
Do not scaffold the standard Nathejk SPA here without a PRD change.

`shared-go` resolves from the sibling `../shared-go` checkout via `go/go.work` in
dev, and from the version pinned in `go.mod` in CI and production. A change that
only builds with the workspace active is a broken build — the dev loop's
`pinned_build` gate catches it. This matters here because `foto` is the first
service to mount a shared-go `tables` entity.

## Configuration

Read from the environment in `cmd/api/env.go` only, and passed down through the
`config` struct. Committed dev defaults live in `docker-compose.yml`; secrets go
in `docker-compose.override.yml`, which is gitignored.

| Variable | Purpose |
|---|---|
| `PORT`, `ENV` | Server port and environment |
| `DB_DSN` | MariaDB. Needs `parseTime=true` **and** `multiStatements=true` |
| `JETSTREAM_DSN` | NATS JetStream, e.g. `nats://jetstream:4222` |
| `EVENT_YEAR` | The **season**, not the calendar year — see below |
| `BLOB_PATH` | Where photo objects live |
| `WEBHOOK_SECRET` | Shared secret `kamera` sends as `X-Webhook-Secret` |

`EVENT_YEAR` is not derivable from the clock. `shared-go`'s patrulje projection
takes `year` from the event *subject* — the season a team signed up for — which
stops matching the calendar year once a season opens in the preceding one.
`kamera`, meanwhile, derives its paths from `DateTime.UtcNow`. Resolving a team
number against the calendar year would silently fail during exactly that window.

Missing `DB_DSN` or `JETSTREAM_DSN` are legitimate startup modes: the process
starts and answers `/api/healthcheck` so a container becomes healthy before its
dependencies do. It reports `"ready": false` and refuses to accept photos in that
state rather than accepting one it cannot record.
