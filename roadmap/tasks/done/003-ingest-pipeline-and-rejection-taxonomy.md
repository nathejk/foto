# 003 — Ingest pipeline and the rejection taxonomy

**Status:** done
**Priority:** high
**Created:** 2026-09-07
**Picked up by:** agent session (Zed / Claude Opus 5)
**Started:** 2026-09-07
**Completed:** 2026-09-07

## Description

Relates to **PRD 001**, phases 3 and 4. Depends on tasks 001 and 002.

Make the camera app's webhook work end to end: authenticate it, fetch the image,
store the original and its renditions, publish the event, and serve the bytes back.

Two requirements shaped this beyond the obvious:

1. **The callback is answered only when everything has succeeded.** The camera app is
   the only other copy of the photograph; on a non-2xx it appends the payload to its
   own `webhook-failed.jsonl`. That file is the retry queue, and an early `200`
   throws it away.
2. **Only spejder patruljer are photographed, but test shots with odd team numbers
   arrive, and those must stand out.** Not success, and not a transient error either.

The second requirement is what removed the pending-photos table from the design: once
the camera app keeps the bytes and logs the failed payload, refusing is lossless, so
there is no need for this service to park anything.

`kamera` is not modified, so `imageUrl` must be fetched — meaning an
attacker-influenceable URL from a request body is fetched by our server. That is an
SSRF sink and needed its own hardened package.

## Acceptance Criteria

- [x] `internal/blob` — content-addressed store, sha256, `0700`/`0600`, atomic
      idempotent `Put`, path traversal impossible by ref validation
- [x] `internal/fetcher` — host allowlist that fails closed on an empty list, scheme
      check, per-hop redirect checking, size cap enforced while reading rather than
      trusting `Content-Length`, bounded timeout, distinct sentinel errors
- [x] `internal/imaging` — one decode feeds every rendition, EXIF orientation read and
      applied, dimension bounds against the header before allocating, **no metadata
      stripping** (originals are stored verbatim by decision)
- [x] `POST /callback/kamera` — secret auth in constant time, resolve → fetch →
      decode → store → publish, answering only at the end
- [x] The rejection taxonomy: distinct status per failure, stable `code`,
      `X-Foto-Rejected` header, WARN log line with team number and URL
- [x] Unknown team number → `422 unknown_team_number`, nothing fetched, nothing stored
- [x] `GET /photos/{ref}` serves renditions only, immutable caching; originals refused
- [x] `GET /api/patruljer/{teamId}/photos`
- [x] Bytes are in the store before the event is published — asserted by a test that
      resolves every ref the event names
- [x] The stored original is byte-identical to what the upstream served — asserted
- [x] All gates green with and without the workspace; `gosec` clean

## Progress Log

- 2026-09-07 16:20 — Task created. Split the independent packages (blob, fetcher,
  imaging) out to run in parallel; kept the wiring and the taxonomy here.
- 2026-09-07 16:30 — Ported `internal/blob` and `internal/imaging` from `hej`, which
  had proven implementations. One deliberate divergence in imaging: dropped
  `StripMetadata`/`stripJPEGSegments`/`stripPNGChunks` entirely, because originals here
  are stored verbatim. Documented so a reviewer grepping for a scrubber finds the
  reason rather than an omission.
- 2026-09-07 16:40 — Recorded the invariant that replaces the strip step: renditions
  are re-encoded from pixels and therefore cannot carry metadata, originals keep
  everything and are never served. That asymmetry is enforced by `photo.Servable`, not
  by each handler remembering — which is why it is a query rather than a helper.
- 2026-09-07 16:55 — Added `imaging.Dimensions` so the stored original's pre-rotation
  size and the decode bounds check come from one place. A second `DecodeConfig` at the
  ingest site would have been two definitions of "too large", free to drift.
- 2026-09-07 17:05 — Decision: `422` for an unknown team number, not `404` and not
  `2xx`. The request is well-formed and the endpoint exists, so a `404` would send
  somebody looking at the webhook URL. A `2xx` would make a test photo
  indistinguishable from a real one in the camera app.
- 2026-09-07 17:10 — Decision: four channels for a rejection — status, `code` in the
  body, `X-Foto-Rejected` header, WARN log. The header is specifically so a bare 422
  among 200s is not anonymous in Traefik's access log; nobody should have to join two
  logs to understand one failure.
- 2026-09-07 17:15 — Decision: `retryable` is in the response body. It does not affect
  the status; it tells whoever reads the retry log whether replaying the entry is
  worth their time.
- 2026-09-07 17:20 — Decision: resolve the team *before* fetching, so a test shot costs
  one indexed query instead of a download and three blob writes. Also means nothing is
  stored for a photo about to be refused — unattributable photographs of children with
  no owning team and no retention anchor are not something to accumulate. Asserted by a
  test that counts upstream hits.
- 2026-09-07 17:30 — Decision: `409 ambiguous_team_number` is kept even though
  `(year, teamNumber)` is confirmed unique. Uniqueness is a convention, not a
  constraint — the index is a plain `KEY` — and the failure mode is attributing a
  child's photograph to the wrong team, silently.
- 2026-09-07 17:40 — Decision: `lazyPublisher` looks the publisher up per call. The
  entity is built before the broker connects, so handing it a publisher would capture
  nil and keep it. Its `MessageFunc` returns a builder yielding nil rather than a nil
  builder, so the outage surfaces once, as `ErrNoPublisher`, instead of as a panic.
- 2026-09-07 17:50 — Blocker: `ev.held.Store(&publisher)` would not compile —
  `metatagger.New` returns a concrete type, so `&publisher` is a pointer-to-concrete
  where the atomic wants pointer-to-interface. Fixed with an explicitly typed variable.
- 2026-09-07 18:00 — ✅ 21 handler tests green on the first run, including the
  byte-identical-original assertion and the "every ref resolves" ordering check.
- 2026-09-07 18:10 — Annotated the two `gosec` findings in `internal/blob/file.go`
  rather than leaving them: G302 flags `Chmod(dir, 0700)` with a rule written for files
  (on a directory the execute bit is what makes it traversable), and G304 flags
  `os.Open` on a variable path whose ref is validated as 64 hex chars one call above.
  Both false positives, both now explained so the next person can tell signal from
  noise.
- 2026-09-07 18:20 — ✅ Verified live against the real stack and real data: odd number
  9999 → `422 unknown_team_number`; real team 1 → resolution *succeeded* and the
  request failed later at the fetch (`bad_image_url`, upstream 404), which is what
  proves the resolver found it; an `imageUrl` pointing at `169.254.169.254` → `400
  bad_image_url` with the host refused before any request was made.
- 2026-09-07 18:25 — Completed. Two follow-ups left open in PRD 001 §10: a
  `GET /photos/{ref}/{name}` route for clients that have a photo's identity but not
  its rendition refs, and making `idx_patrulje_year_number` `UNIQUE` in shared-go so
  the ambiguity check becomes an impossibility rather than a runtime guard.
