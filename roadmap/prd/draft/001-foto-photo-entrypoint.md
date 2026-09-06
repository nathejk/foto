# PRD 001 — `foto`: the photo entrypoint

**Status:** draft
**Author:** agent session (Zed / Claude Opus 5)
**Created:** 2026-09-07
**Last updated:** 2026-09-07
**Approved:**
**Shipped:**
**Target users:** organizer (photo crew at start/finish), and every downstream nathejk service that wants to show a team photo

<!--
Status must match the folder this file is in: draft/, doing/ or done/.
Leave Approved blank until the PRD moves to doing/, and Shipped blank until it
moves to done/. See roadmap/prd/README.md for the lifecycle.
-->

---

## 1. Summary

`foto` is the single entrypoint for photographs into the nathejk ecosystem. It
receives a callback from the camera app ([`nathejk/kamera`](https://github.com/nathejk/kamera))
when a photo has been taken, stores the original in a content-addressed blob
store, derives a cache of downscaled renditions, and publishes
`NATHEJK.<year>.patrulje.<teamID>.photographed` on the internal event stream.
Every other service reads photo *metadata* off the stream and fetches the
*bytes* from `foto`.

## 2. Problem & Motivation

**What problem does this solve?** Photos are currently a dead end. `kamera`
writes JPEGs to a mounted directory, names them `Team-42_1.jpg`, resizes one
copy into `fb/`, and serves them straight off disk. Consequences:

- **Nothing else knows a photo exists.** The only signal is a webhook that
  `kamera` fires and forgets; if the receiver is down the payload lands in
  `webhook-failed.jsonl` and needs a human with a `while read` loop.
- **A photo is not addressable as a domain fact.** There is no event, so a
  service that wants "the start photo for this patrulje" has to guess a
  filename from a team number and a `type`, and re-guess it when the naming
  changes.
- **The filename is the only identity.** `Team-42_1.jpg` collides across years
  by construction (the year is a directory), and the counter that avoids
  collisions is a `File.Exists` loop — a photo's identity depends on the order
  uploads happened to arrive.
- **Renditions are fixed at upload.** One 2000px copy in `fb/`. An app that
  wants a 256px grid downloads a 2000px JPEG per team, and adding a size only
  ever applies to photos taken after the change.
- **The bytes are the only copy, and they carry EXIF.** A phone photograph of a
  scout carries the location it was taken. Nothing strips it today.

**Why now?** `kamera` grew a webhook (`WebhookUrl` / `WebhookSecret`) with no
receiver on the other end. The contract exists and is unconsumed; this PRD is
the consumer. Building it now also fixes the identity problem *before* another
season's photos are filed under filenames.

- **Evidence.** `kamera`'s `Controllers/PhotoController.cs` — the `prefixCounter`
`File.Exists` loop, the single `fb/` resize, `NotifyWebhook`, and
`LogFailedNotification`. `nathejk/hej` PRD 003/008 solved the same problem for
person portraits (`go/nathejk/table/person/portrait.go`); this is that design
applied to patrulje photos, and deliberately so — see §8. Note one deliberate
divergence from `hej`: it strips metadata from stored originals, `foto` does
not (§6).

## 3. Goals

- A photo taken in `kamera` becomes a durable domain fact on the event stream,
  without a human replaying a JSONL file.
- Any service can render a team photo at a sensible size without knowing where
  bytes live, what a filename means, or how big the original was.
- The original survives untouched, so the archive holds the photograph as taken
  and a new rendition size can be produced for photos already taken.
- Metadata that travels with a photograph never reaches a consumer: what is
  served is always a re-encode, which cannot carry it.
- Everything except the blob store can be rebuilt by replaying the stream.
- The message and projection code is written so it can be lifted into
  `nathejk/shared-go` unchanged once stable.

## 4. Non-Goals

- **Changing `kamera` at all.** Decided 2026-09-07: the `kamera` repo is not
  touched. Its webhook payload is the input contract exactly as it is today, it
  keeps its own photo volume, and it keeps serving `/photos/...` — because that
  is where `foto` fetches the bytes from. A future push-based ingest
  (`POST /api/photos` with the bytes) is therefore also out of scope: building an
  endpoint nothing calls is speculative surface.
- **A photo browsing/curation SPA.** `foto` is headless in this PRD. The gallery
  that PRD-something-later wants is a consumer, not this service.
- **Face recognition, tagging, or moderation.** `attention` is carried through
  as a flag; acting on it belongs to whoever consumes the event.
- **Migrating the existing on-disk archive.** Backfilling previous years' photos
  is a separate PRD; the schema here does not preclude it.
- **Retention/purge enforcement.** The event shape reserves room for it (§8,
  `PatruljePhotoPurged`) but the job that runs it is out of scope.
- **Public/unauthenticated photo delivery.** Serving is for internal services in
  this PRD.

## 5. User Stories & Scenarios

- As a **photo crew organizer**, I want to keep shooting in `kamera` and have the
  photo appear everywhere else automatically, so that I never think about files.
- As a **downstream service**, I want a photo's dimensions and a thumbnail URL
  from the event alone, so that I can lay out a grid without fetching 800 JPEGs
  to find out how big they are.
- As a **data protection reviewer**, I want to know that a photo of a minor
  carries no GPS coordinates, so that we can say where a photo was taken is not
  retained.

### Happy path

1. Crew photographs patrulje 42 at start. `kamera` stores it and POSTs its
   webhook to `foto`.
2. `foto` authenticates the callback (`X-Webhook-Secret`) and fetches the bytes
   from `imageUrl`.
3. `foto` stores the original **byte-for-byte as uploaded**, then decodes it to
   derive a display image and the rendition set, storing each as its own
   content-addressed object.
4. `foto` resolves `teamNumber` 42 → the patrulje's `teamID` via the
   `shared-go/tables/patrulje` projection it maintains from
   `NATHEJK.*.patrulje.*` events.
5. `foto` publishes `NATHEJK.<year>.patrulje.<teamID>.photographed`.
6. **Only now** does `foto` answer the webhook `200`. Decided 2026-09-07: the
   callback is answered after — and only after — the bytes are fetched, the
   objects are stored and the event is published. Anything else would have
   `kamera` treat a photo as delivered while it is still in flight, and
   `kamera`'s `webhook-failed.jsonl` is the only retry mechanism there is; a
   premature `200` throws away the one safety net.
7. A consumer receives the event and renders `GET /photos/<thumbRef>` from
   `foto`.

### Edge cases and error scenarios

| Scenario | Behaviour |
|---|---|
| `teamNumber` does not resolve to a `teamID` | **Store the bytes, do not publish, answer `2xx`.** The photo is parked as `pending` and retried as patrulje events arrive. The bytes are safe, so re-delivery would add nothing; failing the callback would make `kamera` retry something no retry can fix. A photo is never dropped because the domain has not caught up, and publishing under a fabricated teamID would put an unerasable row in the log. |
| The same webhook is delivered twice | Same bytes → same hash → `blob.Put` is a no-op, same event body, projection converges. Idempotent by content addressing, not by a dedupe table. |
| `foto` is down when `kamera` fires | `kamera` appends to `webhook-failed.jsonl` (existing behaviour). Replaying those lines later is safe, per the row above. |
| Bytes cannot be acquired (404/timeout) | `5xx` the callback so the failure lands in `kamera`'s retry log rather than being silently swallowed. Nothing is published. |
| Anything in the pipeline fails | The callback returns non-`2xx`. The photo lands in `webhook-failed.jsonl` and is replayable. This is why the response is not sent early. |
| The photo is slow to process | Accepted: the crew waits rather than the photo being lost. See the latency note in §6 — the budget is `kamera`'s HTTP timeout, and the work must fit inside it. |
| Unsupported / undecodable image | `400`, nothing stored, nothing published. Logged with the source URL. |
| Metadata cannot be stripped for a format | The original is **not** kept (`Original` is nil); the re-encoded display image and renditions still are. Losing future re-renders beats retaining GPS. |
| A team is photographed again | Another `photographed` event with a different `Ref`. There is no "replaced" event. Both objects stay in the blob store; that is the retention job's problem. |
| `attention` is true | Carried on the event as a flag. `foto` does not hide, delay or reject the photo. |

## 6. Requirements

### Functional

- [ ] `POST /callback/kamera` accepts `kamera`'s notification payload
      (`teamNumber`, `type`, `attention`, `imageUrl`, `createdAt`) and
      authenticates it with the `X-Webhook-Secret` header. It responds only
      after the bytes are stored and the event is published.
- [ ] Bytes are stored in a content-addressed blob store keyed by sha256, with
      the ref being 64 lowercase hex characters and validated as such on every
      path that accepts one.
- [ ] **The original is stored exactly as received — nothing is stripped,
      re-encoded or normalised.** Decided 2026-09-07. The stored original is
      byte-identical to what `kamera` served, including all EXIF/IPTC/XMP/ICC
      data (`kamera` writes IPTC credit and copyright tags it would be wrong to
      discard, and the archive is the canonical copy of the photograph). The
      EXIF orientation is *also* recorded on the event, so a consumer need not
      parse the file to know which way up it goes.
- [ ] A display rendition and a downscale cache are derived at ingest. Initial
      set: `thumb256`, `thumb1024`, `photo2000` (names derived from the longest
      edge). Each is its own content-addressed object with its own recorded
      `contentType`, `bytes`, `width`, `height`.
- [ ] `NATHEJK.<year>.patrulje.<teamID>.photographed` is published per photo,
      carrying **references, never bytes**.
- [ ] `foto` maintains the **`shared-go/tables/patrulje` projection** and uses it
      to resolve `(year, teamNumber)` → `teamID`. Decided 2026-09-07: the
      mapping is not a new read model — `shared-go` already projects `teamNumber`
      and `year` onto the `patrulje` table and indexes `(year, teamNumber)`. A
      second projection of the same events would be a duplicate that can
      disagree with the first.
- [ ] `foto` projects its own `photographed` events into a `photo` read model,
      so it can answer "the photos for this team/year/type".
- [ ] `GET /photos/{ref}` and `GET /photos/{ref}/{name}` serve bytes with a
      long-lived immutable `Cache-Control` (safe: the URL is a content hash).
- [ ] `GET /api/patruljer/{teamId}/photos` lists a team's photos with every
      rendition's metadata.
- [ ] Renditions are regenerable from the stored original without re-uploading.

### Non-Functional

- **Privacy.** Originals retain their metadata by decision (above), so **the
  stored archive may contain GPS coordinates of where a child was
  photographed**. Two consequences follow and are requirements, not advice:
  (a) originals are never served to a consumer — only the re-encoded display
  image and renditions are, and those lose all metadata as a side effect of
  being re-encoded, so the leak cannot happen by forgetting a strip step;
  (b) the blob store directory is `0o700` and its files `0o600`, so the volume
  is not readable by anything else that can reach it.
- **Access control.** With metadata retained, "who may fetch bytes" stops being
  a deployment detail. A content-hash URL is unguessable but not
  access-controlled — see §11 Q7, now load-bearing.
- **Durability.** The blob store is the only state that cannot be rebuilt from
  the log, and therefore the only thing that must be backed up. Say so in the
  README.
- **Idempotence.** A full stream replay must converge without re-uploading or
  duplicating an object.
- **Security.** Pulling `imageUrl` is a server-side fetch of an
  attacker-influenceable URL: the host must be checked against an allowlist,
  redirects not followed off it, response size capped, and timeouts bounded.
  Refs interpolate into filesystem paths and URLs, so `../` must be impossible
  by validation, not by escaping.
- **Latency.** The callback should answer in well under `kamera`'s HTTP timeout.
  If rendition generation cannot meet that, the event is published first and
  renditions are filled in by a follow-up event — never by blocking the crew.
- **OpenAPI.** Every endpoint above carries OpenAPI annotations (repo rule).

## 7. UX / UI Notes

N/A — `foto` is a headless service. Confirmed 2026-09-07: **no Vue frontend**,
now or as part of this PRD. There is no `vue/` workspace, no `ui` compose
service, and no `ui-dev`/`ui-builder` Dockerfile stages — the standard Nathejk
SPA scaffolding is deliberately absent, and the `prod` image serves no static
bundle. The capture UI stays in `kamera`; a future gallery is a consumer of the
event and a separate PRD (§4).

## 8. Technical Considerations

- **Frontend (Vue 3 / TS):** none — see §7. Deliberately **no** `vue/`
  directory: do not scaffold the standard SPA workspace for this repo.

### BFF (Go)

Standard Nathejk Go layout (`go-bff-layout` skill), single `cmd/api` binary:

```
go/
├── cmd/api/            # main.go, routes.go, callback.go, photos.go, app/
├── internal/
│   ├── blob/           # content-addressed store: Put, Get, Ref.Valid
│   ├── imaging/        # decode, rendition set, EXIF orientation (read only)
│   ├── fetcher/        # allowlisted HTTP pull of kamera's imageUrl
│   └── vcs/ validator/
└── nathejk/table/photo/    # ← the part bound for shared-go
    ├── messages.go     # PatruljePhotographed, PhotoRendition, PhotoOriginal
    ├── subject.go      # PhotoSubject, PhotoPurgeSubject, token validation
    ├── table.go        # schema + projection of photographed/purged
    └── commands.go     # publish-side API used by cmd/api
```

**Written to be lifted to `shared-go`.** `nathejk/table/photo/` may not import
`nathejk.dk/internal/...` — Go forbids importing another module's `internal`
tree, so such an import blocks the move. It depends only on
`github.com/jrgensen/cqrs` (`Publisher`, `Writer`, `Reader`, `Message`,
`Subject`, `SubjectFromStr`) and the standard library. Anything else it needs
from the application is declared as an interface in an `interfaces.go` next to
it and satisfied by `cmd/api`. That is the same constraint hej's
`nathejk/table/person/portrait.go` works under, and the reason the ref
validation is duplicated there rather than calling `blob.Ref.Valid`.

**The event type is owned by the projection that consumes it.** `foto` both
publishes and consumes `photographed`, so the struct lives beside the handler,
not in `internal/`. Two structs that must agree on JSON tags with nothing to
catch them drifting is the worse trade.

### The message

Subject: `NATHEJK.<year>.patrulje.<teamID>.photographed`.

On `NATHEJK` because this is a small, low-frequency domain fact about a
patrulje, and `NATHEJK.>` already claims the subject — no broker topology
change. Per team so `nats stream purge --subject` can erase one patrulje's
photo history and nothing else. Every subject token is validated to contain no
`.`, whitespace, `*` or `>`: an ID with a dot still publishes and still matches
`NATHEJK.>`, while silently no longer matching the per-team purge pattern —
which would make that team's photos unerasable.

```go
// PatruljePhotographed says that a patrulje has been photographed.
//
// "Photographed", not "uploaded": the event records a fact about the patrulje,
// not the success of an HTTP request. Carries references, never bytes — image
// payloads make replay expensive and put an opaque blob in a log optimised for
// small messages.
type PatruljePhotographed struct {
	TeamID string `json:"teamId"`
	Year   string `json:"year"`

	// TeamNumber is what the crew typed into kamera, kept for provenance: it is
	// how a human finds this photo in the on-disk archive, and how a mis-typed
	// number is diagnosed after the fact.
	TeamNumber string `json:"teamNumber"`

	// Type is kamera's category — "start", "finish", … Free text on purpose:
	// kamera takes it from a query parameter, so an enum here would reject a
	// photo over a label.
	Type string `json:"type"`

	// Attention is kamera's flag (its "XXX_" filename prefix). Carried, not
	// acted upon.
	Attention bool `json:"attention"`

	// Ref is the display image's content hash — a re-encode, not the upload.
	// Untrusted on the way in; the handler enforces the hash shape.
	Ref         string `json:"ref"`
	ContentType string `json:"contentType"`
	Bytes       int    `json:"bytes"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`

	// Renditions are the cached downscales. A list, not a single thumbRef:
	// more sizes are expected, and this is an append-only log, so retrofitting
	// a list later means two shapes to interpret forever. Each carries its own
	// dimensions because a consumer that must fetch an object to learn its size
	// cannot budget. May be empty — consumers degrade to Ref.
	Renditions []PhotoRendition `json:"renditions"`

	// Original is the uploaded file, stored verbatim — same bytes, same format,
	// metadata intact. It exists so renditions can be produced again later, and
	// so the archive holds the photograph as it was taken. Nil means no original
	// was kept. Never served to a consumer; see the privacy note in §6.
	Original *PhotoOriginal `json:"original,omitempty"`

	// Source records where the bytes came from — kamera's imageUrl and when the
	// callback was accepted. Provenance for a photo whose bytes we pulled from
	// somewhere else.
	Source *PhotoSource `json:"source,omitempty"`

	// CapturedAt is kamera's createdAt in UTC — when the photo was taken, not
	// when we processed it. Deriving it from delivery time would change on
	// every replay, and it is the clock any retention job works from.
	CapturedAt time.Time `json:"capturedAt"`
}
```

`PhotoRendition` is `{name, ref, contentType, bytes, width, height}`.
`PhotoOriginal` is that plus `orientation` — the EXIF orientation the file
declares. Since nothing is stripped, that value is still in the file too; it is
recorded on the event anyway so a consumer can reason about the photo without
parsing EXIF, and so the display image's rotation is auditable from the log.
Its `width` and `height` describe the stored bytes *before* rotation, so they
are swapped relative to the display image for a photo taken sideways.

`PatruljePhotoPurged` (`…patrulje.<teamID>.photopurged`) is defined now but not
enforced: a deletion needs its own event rather than a `photographed` with an
empty `Ref`, or a replay could not tell "deleted" from "malformed message".

### API endpoints

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/callback/kamera` | `kamera`'s webhook. Secret-header auth. Answers after the work is done. |
| `GET` | `/photos/{ref}` | Serve an object's bytes. Immutable caching. |
| `GET` | `/photos/{ref}/{name}` | Serve a named rendition. |
| `GET` | `/api/patruljer/{teamId}/photos` | A team's photos + rendition metadata. |
| `GET` | `/healthcheck` | Standard. |

All annotated for OpenAPI — this is a hard repo rule, including for
`/callback/...`.

### Data / storage

- **Blob store** on a mounted volume, `<root>/<aa>/<bb>/<sha256>`, sharded to
  keep directories small. Write-once, never mutated. **The only state that
  cannot be rebuilt from the stream, so the only thing needing backup.**
- **MariaDB** holds projections only, both rebuildable:
  - `photo` — one row per `photographed` event, renditions as JSON in a column
    (small set, always read with its photo, written by one event; a side table
    would add a join and a delete-on-replace path for no read this app makes).
  - `patrulje` — **`shared-go/tables/patrulje`, mounted as-is.** `foto` calls
    `patrulje.New(publisher, writer, reader)` and registers the returned value
    on the mux; the projection then maintains `teamId`, `year` and `teamNumber`
    from `NATHEJK.*.patrulje.*.{signedup,updated,numberassigned,started}`, and
    `table.sql` already carries `KEY idx_patrulje_year_number (year, teamNumber)`
    — the exact index this lookup needs.

  Three things about mounting a shared entity are worth writing down, because
  `foto` is the **first** service to do it (`hej` imports only `shared-go`'s
  `messages` and `types`, and defines its own tables):

  1. `patrulje.New` returns an **unexported** `*table`, so its type cannot be
     named in a field or signature here. `foto` holds it behind a locally
     declared interface (`cqrs.Consumer` plus the query methods it needs) —
     which is the right coupling anyway.
  2. It calls `log.Fatalf` if the schema cannot be created, rather than
     returning an error. `foto` cannot soften that; it just means a broken
     database is a hard stop at boot.
  3. **There is no lookup by team number.** The querier has `GetAll`,
     `GetByID` and `GetLastWithNumber` — and `GetLastWithNumber` finds the
     highest allocated number for `AssignNumber`, it is not a lookup. So
     resolving a number needs `GetByNumber(ctx, year, teamNumber)` added to
     `shared-go/tables/patrulje/querier.go`. Until that is pushed and the pin
     bumped, `foto` satisfies its own `TeamResolver` port with a small adapter
     in `cmd/api` that queries the projection through `cqrs.Reader`. The
     adapter is the temporary half; the port stays either way.
- **Pending photos** — bytes stored, team unresolved. A row, not a queue: it
  must survive a restart and be visible to a human asking "why has team 42 no
  photo".

### Dependencies & risks

| | |
|---|---|
| `kamera` | Its payload is the input contract, unchanged. It carries a **URL, not bytes**, so `foto` fetches back from `kamera` and therefore depends on `kamera` being up during ingest. Accepted 2026-09-07 as the cost of not touching that repo. |
| Season year vs. calendar year | `kamera` derives its year from `DateTime.UtcNow:yyyy`, but `shared-go`'s patrulje projection takes `year` from the **subject** — the season signed up for, which its own code comments note differs from the calendar year once a season opens in the preceding year. Resolution must therefore use a configured season year (`EVENT_YEAR`, as `hej` does), not the photo's calendar year. |
| `shared-go` | `foto` is the first consumer of a shared `tables` entity. Adding `GetByNumber` means the two-repo loop: commit, push, `go get ...@latest`. The `pinned_build` gate in the dev loop catches a build that only works with the workspace active. |
| Version skew | `shared-go` pins `jrgensen/stream v0.1.1`, `hej` pins `v0.1.2`; both pin `cqrs v0.1.0`. MVS resolves to `v0.1.2`. |
| `github.com/jrgensen/cqrs`, `github.com/jrgensen/stream` | From the proxy, pinned in `go.mod`. Not editable locally. |
| `github.com/nathejk/shared-go` | Consumed for patrulje message types; `nathejk/table/photo` is a future contribution to it. Two-repo loop applies (commit, push, bump). |
| Image decoding | A rendition pipeline decoding attacker-supplied images needs bounded dimensions and memory. Prefer the standard library over a CGo dependency; the `prod` image is bare alpine. |
| Infra | Needs the external `traefik` and `jetstream` networks from the infra repo, plus a persistent volume for blobs — the one thing `docker compose down -v` must not take with it. |
| Hostname collision | `kamera` currently answers on `foto.nathejk.dk`. Naming this service `foto` needs that resolved before deploy (§11). |

## 9. Success Metrics

- 100% of photos taken in `kamera` produce a `photographed` event, i.e.
  `webhook-failed.jsonl` stays empty across an event.
- Zero **served** images with EXIF present — assert it in a test over every
  route that returns bytes, not by inspection. (Stored originals keep theirs by
  design; that is the point of the distinction.)
- A consumer can render a 256px grid of ~800 teams without fetching a
  full-size image (bytes transferred, not a stopwatch).
- A stream replay into an empty database reproduces the projections exactly and
  writes nothing to the blob store.
- Callback p95 within `kamera`'s HTTP timeout.
- A new rendition size can be backfilled for photos taken before it existed.

## 10. Rollout / Task Breakdown

Sequencing: the pipeline is built before the ingest that feeds it, so each
step is testable on its own. `kamera` is untouched until the last phase.

**Phase 1 — foundations**
- [ ] Task: scaffold the repo — `docker/Dockerfile` stages, `docker-compose.yml`
      (`api`, `db`, `phpmyadmin`, blob volume, external networks), `docker/init/api-dev`
- [ ] Task: `go/` module skeleton — `cmd/api` (config, routes, `app` helpers,
      healthcheck), `internal/jsonlog`, `vcs`, OpenAPI setup
- [ ] Task: `internal/blob` content-addressed store — `Put`/`Get`/`Ref.Valid`,
      sharded layout, idempotent writes, tests

**Phase 2 — the domain contract**
- [ ] Task: mount the `shared-go/tables/patrulje` projection and resolve
      `(year, teamNumber)` → `teamID` behind a `TeamResolver` port
- [ ] Task: contribute `GetByNumber` to `shared-go/tables/patrulje/querier.go`,
      bump the pin, and delegate the local adapter to it
- [ ] Task: `nathejk/table/photo` messages + subject builders + token
      validation, with the shared-go import constraint enforced
- [ ] Task: `photo` projection — schema, `photographed`/`photopurged` handlers,
      replay idempotence tests via `cqrstest`

**Phase 3 — the pipeline**
- [ ] Task: `internal/imaging` — decode, rendition set, EXIF orientation read,
      bounded dimensions. No strip step: originals are stored verbatim
- [ ] Task: ingest service — store the original untouched, derive renditions,
      resolve the team, publish; pending-photo handling when the team is unknown

**Phase 4 — ingest and delivery**
- [ ] Task: `POST /callback/kamera` — payload binding, secret auth, allowlisted
      + size-capped + timeout-bounded fetch of `imageUrl`, responding only
      after the event is published
- [ ] Task: `GET /photos/{ref}[/{name}]` + `GET /api/patruljer/{teamId}/photos`,
      serving renditions only and never the original
- [ ] Task: pending-photo retry — republish as patrulje events resolve

**Phase 5 — cutover**
- [ ] Task: rendition backfill command — regenerate the set from stored originals
- [ ] Task: README — what needs backing up (the blob store) and what does not
- [ ] Task: point `kamera`'s `WebhookUrl` at `foto`; decide the `foto.nathejk.dk`
      hostname split (separate repo, coordinate)

No feature flag: nothing consumes the event until phase 5, so the switch is
`kamera`'s `WebhookUrl` being set.

## 11. Open Questions

1. ~~**Does `kamera` push bytes, or does `foto` pull them?**~~ **Decided
   2026-09-07: `foto` pulls.** `kamera` is not modified, keeps its volume, and
   `foto` fetches `imageUrl` during the callback. No `POST /api/photos`.
2. ~~**`teamNumber` → `teamID`.**~~ **Decided 2026-09-07:** mount
   `shared-go/tables/patrulje` and resolve against it; no new projection. Two
   sub-questions remain open and are genuinely unanswered:
   - **Is `(year, teamNumber)` unique?** Nothing in `shared-go` enforces it —
     `idx_patrulje_year_number` is a plain `KEY`, not `UNIQUE`, and
     `AssignNumber` computes the next number from `GetLastWithNumber` with no
     constraint behind it. If two rows can share a number in a year, the
     resolver must decide (fail loudly, or prefer one) rather than silently
     picking whichever row sorts first. **Needs an answer before ingest goes
     live.**
   - **Which number space is `kamera`'s `teamNumber` in?** `patrulje` is one
     entity among `klan`, `senior` and `spejder`. If a klan can be photographed
     at start and carries a number from the same sequence, the subject
     `…patrulje.<teamID>.photographed` is wrong for it (see Q3).
3. **What is `type`, really?** `start` and `finish` come from a query parameter.
   Are all types patrulje photos? If a `type` ever means a crew member or a
   location, the subject `…patrulje.<teamID>.photographed` is wrong for it and
   needs a sibling.
4. **Is `photographed` the right verb, given the subject already has `year`?**
   Confirm `NATHEJK.<year>.patrulje.<teamID>.photographed` against whatever
   `NATHEJK.<year>.patrulje.*` subjects `shared-go` already publishes, so this
   does not collide with or contradict an existing pattern.
5. **The rendition set.** `thumb256` / `thumb1024` / `photo2000` is a guess
   informed by `kamera`'s existing 2000px `fb/` copy. What do the actual
   consumers need? Wrong guesses are cheap to fix (backfill) but only if the
   original is kept.
6. **Retention.** hej's portraits have a policy ("the portrait does not outlive
   the event"). Do patrulje photos? `PatruljePhotoPurged` is reserved either
   way, but if there is a policy it should be a task now rather than a PRD later.
7. **Who may fetch bytes?** §4 scopes this to internal services, but a gallery
   or a parent-facing page changes the answer, and a content-hash URL is
   unguessable but not access-controlled. **Raised in priority by the
   keep-everything decision:** the originals now hold EXIF GPS, so the
   "originals are never served" rule in §6 is the only thing standing between
   the archive and a location leak. Worth a test that asserts no route can
   return an original's ref.
8. **Hostname.** `kamera` serves `foto.nathejk.dk` today. Does `foto` take that
   name (and `kamera` move), or does `foto` get another?
9. **When does `nathejk/table/photo` move to `shared-go`?** It is written to be
   liftable, but "stable" needs a trigger — first external consumer of the
   event, or a fixed date?
