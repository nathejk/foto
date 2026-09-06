---
name: go-bff-layout
description: >
  Conventions and directory layout for the Go backend-for-frontend (BFF) used
  across Nathejk repos. Apply this skill when adding HTTP handlers, routes,
  data models, domain tables, jetstream consumers, or wiring new dependencies
  into the `cmd/api` entrypoint of a Go service. Trigger phrases: "add an
  endpoint", "new handler", "new route", "extend the API", "add a model",
  "wire a new table/projection", "add a jetstream consumer", "BFF",
  "backend-for-frontend".
---

# Go BFF Layout

The backend is a single Go binary (`cmd/api`) that serves both the SPA's JSON
API and the static SPA bundle in production. It speaks to MariaDB for state
and to NATS JetStream for events. It is always developed and run inside the
`api` container — never on the host.

---

## Top-level layout

```
go/
├── cmd/api/              # main binary — wiring, HTTP server, route handlers
│   ├── main.go           # config, dependency wiring, mux startup
│   ├── routes.go         # httprouter routes; /api, /callback, SPA fallback
│   ├── home.go signup.go klan.go patrulje.go personnel.go orders.go
│   │                     # one file per resource, one handler per route
│   ├── database.go env.go
│   └── app/              # transport-layer helpers (errors, json, middleware,
│                         #   server, healthcheck) — embed `app.JsonApi` on
│                         #   the application struct to inherit them
├── internal/             # private packages — not importable from outside
│   ├── data/             # read-side facade handed to handlers: entity querier
│   │                     #   interfaces + the shared error aliases. Holds no
│   │                     #   SQL of its own any more; new reads go on an entity
│   │                     #   querier, not here.
│   ├── jsonlog/          # structured logger
│   ├── mailer/           # SMTP via go-mail; template-driven (`templates/`)
│   ├── payment/          # mobilepay client + payment abstractions
│   ├── sms/              # SMS provider abstraction (cpsms today)
│   ├── templates/        # text/html templates
│   ├── validator/        # request validation helpers
│   └── vcs/              # build-time version embedding
├── nathejk/table/personnel/
│                         # the one entity still local. The root `table` and
│                         #   `nathejk/config` packages are gone; the other ten
│                         #   entities live in shared-go, see "Where the
│                         #   entities live".
└── www/                  # placeholder static dir for dev (prod replaces it)
```

Empty `internal/commands/` and `internal/messages/` directories used to sit here
and are gone. Write-side APIs are not a package: `cmd/api/main.go` declares a
`commands` struct whose fields are each satisfied by the owning entity's
`Commands` interface (in `commands.go` next to `table.go`). SMS and mail bodies
live with their sender, in `internal/sms` and `internal/mailer/templates`.

Streaming infra is **not** vendored in this repo. It comes from the external
module `github.com/jrgensen/stream` (subpackages `jetstream`, `xstream`,
`subject`, `metatagger`). Shared domain types and messages come from
`github.com/nathejk/shared-go` (`.../types`, `.../messages`). Import these
directly — do not reintroduce a local `superfluids/` package (it has been
retired in favour of `github.com/jrgensen/stream`).

The CQRS infrastructure seam is likewise external: `github.com/jrgensen/cqrs`
(subpackages `sqlpersister`, `deadletter`, `cqrstest`). There is no `pkg/`
directory any more — it held `sqlpersister` and `tablerow`, both of which moved
into that module.

The `internal/` / `nathejk/` split is deliberate:

- **`internal/`** — anything specific to *this* binary that is not a domain
  aggregate (transport, infra clients, validators, loggers).
- **`nathejk/`** — the *domain*. Aggregates live as `table` sub-packages, each
  owning its own SQL schema slice and consuming its own subjects.

If you can't decide, default to `internal/`. Do not create a `pkg/`; genuinely
generic code belongs in an external module.

---

## Where the entities live

Ten of the eleven table entities have moved to `github.com/nathejk/shared-go`
and are imported from there, not reimplemented here:

```
github.com/nathejk/shared-go/tables            # ErrRecordNotFound, Validator, PermittedValue
github.com/nathejk/shared-go/tables/crewmember klan     order    patrulje  payment
github.com/nathejk/shared-go/tables/product    section  senior   signup    spejder
```

Still local, and the only thing under `go/nathejk/table/`:

- `personnel/` — not yet shared (see task 001; it needs a shared-go message
  field first).

The root `table` package is gone. Its projectors (`confirm.go`,
`patruljestatus.go`, `spejderstatus.go`, `pincode.go`, `registrant.go`,
`klan.go`, `patrulje.go`, `signup.go`) either moved to shared-go as entities or
were orphaned and deleted (tasks 027, 028). `errors.go` went too — it aliased
the shared sentinels, but nothing referenced `table.ErrRecordNotFound` once the
projectors left. Note *why* it aliased rather than redeclared, because the same
rule applies to `internal/data`, which still does this: `errors.New` copies
would be distinct values and `errors.Is` would silently stop matching errors
returned by a shared entity.

One remnant: `main.go` creates the `spejderstatus` table inline. It is a
compatibility shim, not a projection — shared-go's `spejder.GetAll` still LEFT
JOINs that table, so it has to exist even though nothing writes to it. Delete
those six lines when the join goes (task 028); do not grow them into a projector
again.

To change an entity, edit it in shared-go, not here. In dev, `go/go.work`
resolves shared-go from the `../../shared-go` sibling checkout so edits are
picked up live; CI/prod build with `GOWORK=off` against the version pinned in
`go.mod`, so a shared-go change must be committed, pushed and the version
bumped here before it reaches production.

## The cqrs seam

The shared entities depend on nothing outside their own module: everything they
need from the infrastructure comes from three interfaces in
`github.com/jrgensen/cqrs`, which `cmd/api/main.go` supplies:

| Interface | Role | Production implementation |
|---|---|---|
| `cqrs.Publisher` | command side — append domain events | `metatagger` over JetStream |
| `cqrs.Writer` | projection side — apply read-model statements | `deadletter` wrapping `sqlpersister` |
| `cqrs.Reader` | query side — read the read model | `*sql.DB` |

An entity constructor therefore reads `New(p cqrs.Publisher, w cqrs.Writer, r
cqrs.Reader, …)`. Never take a `*sql.DB` or a `stream.Publisher` directly.
`cqrs.Message`, `cqrs.Subject`, `cqrs.Consumer` and `cqrs.SubjectFromStr` cover
the projector side, so `jrgensen/stream` need not be imported either. The same
rules apply to the still-local `personnel` package, which is a migration
candidate.

When an entity needs something else from the application — a validator, a
mailer, a payment provider — declare the interface it requires in an
`interfaces.go` beside the entity files and let `cmd/api` satisfy it. Do not
import `internal/`. Existing examples: `shared-go/tables/interfaces.go`
(`Validator`), `shared-go/tables/signup/interfaces.go` (`Mailer`, `SmsSender`),
`shared-go/tables/payment/interfaces.go` (`Provider`, adapted locally in
`cmd/api/mobilepayprovider.go`).

Schema drift is handled by `cqrs.EnsureColumn` / `cqrs.EnsureIndex`, called
from the entity's `New` after the `CREATE TABLE IF NOT EXISTS`. Both are
MySQL/MariaDB-specific.

For tests, `cqrs/cqrstest` provides in-memory `Writer` and `Publisher` fakes,
so a commander or projector can be tested without a database or a broker.

---

## How a request flows

1. `cmd/api/main.go` builds:
   - the cqrs triple: a `cqrs.Reader` (`*sql.DB`), a `cqrs.Writer`
     (`deadletter` wrapping `sqlpersister`), and a `cqrs.Publisher`
     (`metatagger` over JetStream)
   - JetStream connection (`github.com/jrgensen/stream/jetstream`)
   - One projector per aggregate (`nathejk/table/<x>`)
   - An `xstream.Mux` (`github.com/jrgensen/stream/xstream`) that fans
     subjects to the projectors
   - `data.Models` — read-only facade handed to HTTP handlers
   - `commands.Commands` — write-side facade (publishes events)
   - SMS, mailer, payment clients, plus the adapters that bind them to the
     ports the entities declare
2. `routes.go` registers handlers on `httprouter` under `/api/...` and
   `/callback/...`, plus an SPA-fallback `http.FileServer` at `/`.
3. Handlers (`signup.go`, `klan.go`, …) read via `app.models` and write via
   `app.commands`. They never touch SQL or JetStream directly.
4. Commands publish JetStream events. Projectors subscribed via the mux
   update SQL read models. The next read sees the new state.

This is event-sourced-ish: SQL tables are projections, JetStream is the log.

---

## Conventions

### Adding an endpoint

1. Add the route in `cmd/api/routes.go`, grouped with related routes.
2. Create or extend the resource file (`signup.go`, `klan.go`, …). One handler
   func per route, named `<verb><Resource>Handler`
   (e.g. `createSignupHandler`, `showKlanHandler`).
3. Read through `app.models.<X>`; write through `app.commands.<X>`.
4. Use `app.WriteJSON`, `app.ReadJSON`, `app.ServerErrorResponse`,
   `app.NotFoundResponse`, etc. from `cmd/api/app` — do not write
   `http.Error` or `json.NewEncoder` by hand.
5. **All endpoints must have OpenAPI annotations** (repo rule from `.rules`).

### Adding a domain aggregate

New aggregates belong in **shared-go** (`shared-go/tables/<aggregate>/`), not
here — ten of the eleven already live there and the eleventh is a migration
candidate. Create it with at minimum:

1. A `New(p cqrs.Publisher, w cqrs.Writer, r cqrs.Reader, opts...)`
   constructor. Take the interfaces, never `*sql.DB` or a concrete stream.
2. One or more `Consume(...)` methods registered via `xstream.Mux`.
3. A read API used by `internal/data` to expose to handlers.
4. An `interfaces.go` if it needs anything else from the application, rather
   than an `internal/` import. See "The cqrs seam" above.

Then wire it here in `cmd/api/main.go`:

- Construct it.
- Add it to `mux.AddConsumer(...)`.
- Pass it into `data.NewModels(...)` and/or `commands.New(...)`.

Remember the two-repo loop: commit and push shared-go, then bump its version in
`go.mod`, or the `GOWORK=off` build will not see the new package.

### Adding a command

1. Define the command struct in the owning entity's `commands.go`, next to its
   `table.go` — for the ten shared entities that means shared-go, for
   `personnel` it is local. There is no `internal/commands` or
   `nathejk/commands` package; `cmd/api/main.go`'s `commands` struct just
   collects each entity's `Commands` interface for the handlers.
2. Publish the resulting event(s) through the aggregate's `cqrs.Publisher`
   (subjects are built with `cqrs.SubjectFromStr`). Inside `nathejk/table/`,
   do not import `github.com/jrgensen/stream` directly — `cqrs` re-exports
   everything needed.
3. Ensure at least one projector consumes the event so SQL state converges.

### Modules and versions

- Module path: `nathejk.dk` (see `go.mod`). Use that prefix for internal
  imports, e.g. `nathejk.dk/internal/data`.
- Shared code lives in the external module `github.com/nathejk/shared-go`.
  In dev, `go/go.work` resolves it from a sibling `../../shared-go` checkout
  (bind-mounted at `/shared-go` in the `api` container) so edits are picked up
  live. Prod/CI builds run with `GOWORK=off` and resolve the version pinned in
  `go.mod` from the module proxy. Don't commit changes that only build with the
  workspace active.
- `github.com/jrgensen/cqrs` and `github.com/jrgensen/stream` are **not** in
  the workspace — they resolve from the module proxy at the version pinned in
  `go.mod` in every environment. Changing them means cutting a release there
  and bumping here, not editing a local checkout.
- Go version follows `go.mod` — do not bump it ad-hoc; bump it as its own
  task. The dev container image (`golang:1.25` in the Dockerfile) must
  match.

### Dev tools (staticcheck, gosec, govulncheck)

Dev tools are managed as **`tool` directives in `go.mod`** (Go 1.24+) and run via
`go tool <name>`, so they are version-pinned in `go.mod`/`go.sum` and always
build with the current toolchain. Add a new one with
`go get -tool <pkg>` (run inside the `api` container). Do **not** `go install`
tools into the image — the `api:/go` volume would shadow the binary and freeze
it at an old Go version.

Registered tools: `honnef.co/go/tools/cmd/staticcheck`,
`github.com/securego/gosec/v2/cmd/gosec`, `golang.org/x/vuln/cmd/govulncheck`.

### Tests and lint

The dev container re-runs these on every `.go`/`.sql` change (see
`docker/init/api-dev`):

```sh
go test -timeout 10s ./...
go vet ./...            # hard gate
go tool staticcheck ./...   # hard gate
go build ./...
```

If any fail the dev loop will not restart the binary — keep `./...` green.
`gosec` and `govulncheck` also run once at container startup, but **report-only**
(they don't gate the loop, since gosec findings and govulncheck's network vuln-DB
fetch shouldn't block hot reload).

CI does not run a separate `go test` step. The workflow
(`.github/workflows/build-and-publish.yml`) builds the Docker image on every
pull request and push to `main`; the `build` stage runs `go test -timeout 60s
./...` + `go tool staticcheck ./...` before compiling the static binary, so a
red test or lint failure fails the image build.

### Configuration

All configuration is read from environment variables in `cmd/api/main.go` via
`flag.StringVar(..., os.Getenv("..."), ...)`. Never read env vars deeper in
the call tree — pass them down through the `config` struct. Add new vars to
`docker-compose.yml` (with sensible dev defaults) and document them in the
relevant task or PR.

---

## Running things

Always inside containers — see the `docker-dev-stack` skill.

```sh
# rebuild after Dockerfile change
docker compose build api

# tail the API
docker compose logs -f api

# one-off go command
docker compose run --rm api go test ./...
docker compose run --rm --entrypoint go api tool staticcheck ./...
```

The `api` container's entrypoint (`docker/init/api-dev`) already runs
`go test`, `staticcheck`, `go build`, then `go run nathejk.dk/cmd/api`, and
restarts on any `.go`/`.sql` change via `inotifywait`. You normally do not
need to restart the container manually.

---

## Don'ts

- Don't add a second `cmd/<binary>` unless you genuinely need a separate
  process — extra workers belong as JetStream consumers in the same binary.
- Don't bypass `data.Models` / `commands.Commands` from handlers.
- Don't run `go` directly on the host. Always go through `docker compose`.
- Don't import from `cmd/api` into `internal/` or `nathejk/` — dependencies
  flow inward only.
- Don't import `nathejk.dk/internal/...` from anywhere under
  `nathejk/table/`. What remains there (`personnel`, the legacy projectors) is
  either a migration candidate or shared-adjacent, and Go forbids importing
  another module's `internal` tree — such an import blocks the move. Declare
  the interface you need in an `interfaces.go` instead.
- Don't reimplement a shared entity locally, and don't edit one by copying it
  back into `nathejk/table/`. Change it in shared-go and bump the version.
- Don't redeclare the shared sentinel errors with `errors.New`. Alias
  `tables.ErrRecordNotFound` (as `nathejk/table/errors.go` does) or `errors.Is`
  will silently stop matching.
- Don't recreate `pkg/`. Generic, non-domain code belongs in an external
  module (`jrgensen/cqrs`, `jrgensen/stream`).
