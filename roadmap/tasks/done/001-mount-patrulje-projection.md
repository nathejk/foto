# 001 — Mount the patrulje projection and resolve teamNumber → teamID

**Status:** done
**Priority:** high
**Created:** 2026-09-07
**Picked up by:** agent session (Zed / Claude Opus 5)
**Started:** 2026-09-07
**Completed:** 2026-09-07

## Description

Relates to **PRD 001** (still in `draft/` — this slice was requested directly and
built ahead of approval; the remaining phases wait on the PRD).

`kamera`'s webhook carries `teamNumber` — "42", what the photo crew typed — but
the event `foto` publishes is subject-addressed by `teamID`
(`NATHEJK.<year>.patrulje.<teamID>.photographed`). Nothing in the webhook bridges
the two. Build that bridge by mounting `github.com/nathejk/shared-go`'s
`tables/patrulje` projection and resolving against it.

Mount the shared entity rather than projecting the events again locally:
`shared-go` already derives `year` and `teamNumber` from
`NATHEJK.*.patrulje.*.{signedup,updated,numberassigned,started}` and its
`table.sql` already indexes `(year, teamNumber)`. A second projection of the same
events would be a duplicate that can disagree with the first.

This also required scaffolding the repo, since it had no Go module, no Docker
stack and no dev loop.

Constraints that shaped the work:

- `foto` is the **first** Nathejk service to mount a shared-go `tables` entity
  (`hej` imports only `messages` and `types` and defines its own tables).
- `patrulje.New` returns an **unexported** type and calls `log.Fatalf` on schema
  failure. Neither can be changed from here.
- `shared-go`'s querier has **no lookup by team number** — only `GetAll`,
  `GetByID` and `GetLastWithNumber`, and the last of those finds the highest
  allocated number for `AssignNumber`, it is not a lookup.
- No Vue frontend, per decision.

## Acceptance Criteria

- [x] Go module scaffolded (`foto.nathejk.dk`), with `go.work` resolving the
      sibling `../shared-go` checkout in dev and `go.mod` pinning it for CI
- [x] Docker stack: multistage `Dockerfile` (no `ui-dev`/`ui-builder` stages),
      `docker-compose.yml` (`api`, `db`, `phpmyadmin`, `blobs` volume, external
      `traefik` + `jetstream` networks), and the `docker/init/api-dev` hot-reload
      loop including the `GOWORK=off` pinned-build gate
- [x] The cqrs seam wired: `metatagger` publisher (producer `foto-api`),
      `deadletter`-wrapped `sqlpersister` writer, `*sql.DB` reader, `xstream.Mux`
- [x] `shared-go/tables/patrulje` mounted and registered on the mux, held behind
      a local `patruljeProjection` interface because its type is unexported
- [x] `internal/teamnumber.Resolver` resolves `(year, teamNumber)` → `teamID`
- [x] Ambiguity fails loudly (`ErrAmbiguous`) instead of picking a row
- [x] Unit tests cover resolve, whitespace trimming, leading zeros, not-found,
      ambiguous, empty year, empty number, empty teamId, query error, nil reader
- [x] `go build`, `go test`, `go vet`, `go tool staticcheck` all green — **both**
      with the workspace and with `GOWORK=off`
- [x] Verified against a real MariaDB and the shared JetStream: the projection
      creates its schema and converges

## Progress Log

- 2026-09-07 10:00 — Task created, covering the slice requested directly: patrulje
  projection + teamNumber→teamID mapping, no Vue.
- 2026-09-07 10:20 — Read `kamera`'s `PhotoController.cs`. Confirmed the webhook
  carries `imageUrl` (a URL, not bytes) and `teamNumber` (not `teamID`). Both shape
  this task.
- 2026-09-07 10:40 — Surveyed `shared-go`. Found `tables/patrulje` already projects
  `year` + `teamNumber` and indexes `(year, teamNumber)`. Decided to mount it rather
  than build a `patrulje_number` projection: two projections of one event stream can
  disagree, and nothing would arbitrate.
- 2026-09-07 11:00 — Surveyed `hej` for conventions. It does **not** mount any
  shared-go `tables` entity, so `foto` is first. Noted the three costs: unexported
  return type, `log.Fatalf` on schema failure, no lookup by number.
- 2026-09-07 11:30 — Decision: the number lookup belongs in `shared-go` as
  `querier.GetByNumber`, but adding it there means push + pin bump. Implemented
  `internal/teamnumber` locally as the temporary half, behind a signature that will
  not change when it delegates upstream. Follow-up task noted in PRD 001 §10.
- 2026-09-07 11:45 — Decision: ambiguous `(year, teamNumber)` is an error, not a
  choice. `idx_patrulje_year_number` is a plain `KEY`, not `UNIQUE`, and
  `AssignNumber` allocates with no constraint behind it — so duplicates are possible.
  Picking whichever row sorts first would attribute a child's photograph to the wrong
  team, invisibly. `ErrAmbiguous` puts it in the log while it can still be fixed.
- 2026-09-07 12:00 — Decision: `LIMIT 2`, not `LIMIT 1`. With `LIMIT 1` a duplicate
  is indistinguishable from a unique match, so the check above could not exist.
- 2026-09-07 12:10 — Decision: no numeric coercion on `teamNumber`. It is stored as a
  string and `AssignNumber` sorts it by `length()`; `CAST` would make "042" match "42"
  here while they stay distinct everywhere else.
- 2026-09-07 12:30 — Scaffolded the module, docker stack and dev loop, mirroring
  `hej` (including the `go run` → fixed-path-binary reasoning in `api-dev`, and the
  `pinned_build` gate).
- 2026-09-07 13:00 — Blocker: `go get` pulled staticcheck v0.8.1, which requires Go
  1.26 and tried to force a toolchain upgrade. Pinned `honnef.co/go/tools@v0.7.0`
  (matching `hej`) and documented in `go.mod` that bumping it is its own task.
- 2026-09-07 13:15 — Blocker: `ev.held.Store(&publisher)` failed to compile —
  `metatagger.New` returns a concrete type, so `&publisher` is pointer-to-concrete,
  not the pointer-to-interface the atomic holds. Fixed with an explicitly typed
  interface variable.
- 2026-09-07 13:30 — ✅ Gates green with the workspace: build, test, vet, staticcheck.
- 2026-09-07 13:35 — ✅ Gates green with `GOWORK=off` too, so nothing here depends on
  the sibling checkout. This is the CI-parity check.
- 2026-09-07 13:50 — ✅ Verified live. `docker compose up` → "patrulje projection
  mounted, subjects 4", JetStream connected as producer `foto-api`, projections
  running, dead-letter queue empty. `patrulje` and `deadletter` tables created, and
  `idx_patrulje_year_number` present.
- 2026-09-07 14:00 — ✅ Verified resolution against real data. The projection replayed
  **719 patruljer** from the shared stream. Inserted the same `teamNumber` in two
  years; the resolver returned the 2026 team, and `EXPLAIN` confirms MariaDB uses
  `idx_patrulje_year_number`. Test rows removed.
- 2026-09-07 14:10 — Completed. Two things deliberately left open and recorded in
  PRD 001 §11 Q2: whether `(year, teamNumber)` is guaranteed unique (currently
  handled by failing loudly), and which number space `kamera`'s `teamNumber` belongs
  to — if a klan can be photographed, `…patrulje.<teamID>.photographed` is the wrong
  subject for it.
