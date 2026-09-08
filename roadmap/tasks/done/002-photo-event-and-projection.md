# 002 — The photograph event and its projection

**Status:** done
**Priority:** high
**Created:** 2026-09-07
**Picked up by:** agent session (Zed / Claude Opus 5)
**Started:** 2026-09-07
**Completed:** 2026-09-07

## Description

Relates to **PRD 001**, phase 2. Depends on task 001.

Define the event `foto` publishes when a patrulje is photographed, the subjects it
travels on, and the projection that folds it into a read model — in
`go/nathejk/table/photo/`, written so it can be lifted into `shared-go` unchanged.

The shape follows `hej`'s `PortraitCaptured` (`go/nathejk/table/person/portrait.go`)
with one structural difference that drives the whole design: **a patrulje has several
photographs**, so this is not "the team's current photo" the way a portrait is a
person's current portrait.

Constraints:

- The package may not import `foto.nathejk.dk/internal/...` — Go forbids importing
  another module's internal tree, so such an import would block the move to
  shared-go. Ref validation is therefore duplicated from `internal/blob`.
- Its only non-stdlib dependency is `github.com/jrgensen/cqrs`.
- `cqrs.Writer.Consume` takes a finished statement, not a statement plus arguments,
  so the projection must do its own SQL quoting.

## Acceptance Criteria

- [x] `PatruljePhotographed`, `PhotoRendition`, `PhotoOriginal`, `PhotoSource` and
      `PatruljePhotoPurged`, with the reasoning for each field recorded
- [x] Subjects `NATHEJK.<year>.patrulje.<teamId>.photographed` and `.photopurged`,
      with subject-token validation that rejects anything containing `.`, whitespace,
      `*` or `>`
- [x] The publisher's subjects and the projection's `Consumes` patterns are proven to
      agree by a test, since nothing at runtime would report it if they drifted
- [x] `photo` table keyed `(year, teamId, type, ref)` so several photographs per team
      coexist and a replay converges
- [x] Purge deletes named refs only — never a whole team
- [x] Publish-side validation, and `ErrNoPublisher` when the broker is absent
- [x] Reads: `ByTeam`, `Servable`, `OriginalRefs`
- [x] `Servable` admits display images and renditions but **not** originals
- [x] Own SQL quoting, with a test asserting quote balance rather than grepping for
      attack strings
- [x] 27 tests green; `go vet`, `staticcheck` and `gofmt` clean, with and without the
      workspace

## Progress Log

- 2026-09-07 14:30 — Task created.
- 2026-09-07 14:45 — Verified the stream configuration before designing for multiple
  events per subject: `NATHEJK` has `max_msgs_per_subject: -1`, `limits` retention, no
  `max_age`, no `max_msgs`. So many `photographed` messages on one team's subject are
  all retained. Had this been last-value-per-subject, the per-team subject would have
  silently discarded every photo but the newest — worth checking rather than assuming.
- 2026-09-07 14:50 — Read `jrgensen/cqrs` and `stream/subject`. `SubjectFromStr`
  replaces the *first* `:` with `.`, so `NATHEJK:*.patrulje.*` and
  `NATHEJK.*.patrulje.*` parse identically; the colon is documentation of where the
  stream name ends. Adopted shared-go's convention of the colon form in `Consumes` and
  the dotted form in `Match`.
- 2026-09-07 15:05 — Decision: primary key `(year, teamId, type, ref)`. The content
  hash is the photograph's identity, which is what makes a replay converge —
  re-delivery rewrites one row, a different photo gets its own. `type` is included so
  the same bytes filed as `start` and `finish` stay two facts. Noted the one
  consequence: re-ingesting after a JPEG encoder change yields a new ref and a second
  row; `originalRef`/`sourceUrl` make that diagnosable.
- 2026-09-07 15:15 — Decision: renditions as JSON in a column, not a side table. Small
  set, always read with its photo, written by one event. Recorded the trigger for
  changing that (a query across renditions) rather than leaving it implicit.
- 2026-09-07 15:20 — Decision: `Renditions` is a list from the start. `hej` shipped a
  single `thumbRef`, and because the log is append-only it now carries a deprecated
  field forever. Not repeating that.
- 2026-09-07 15:30 — Decision: purge takes refs and refuses an empty list, at both the
  command and handler ends. A purge that silently widened to a whole team because its
  ref list failed to decode is the worst outcome available here.
- 2026-09-07 15:40 — Decision: wrote a real SQL `quote()` rather than reusing
  shared-go's `%q`. `%q` is *Go* quoting — double quotes and Go escapes — which MySQL
  tolerates by accident. A trailing backslash or `ANSI_QUOTES` mode turns that into
  either a syntax error or a different statement. The injection surface is `Type`,
  `TeamNumber` and `sourceUrl`, none of which is validated like a ref is.
- 2026-09-07 15:50 — Decision: `Photo` (the read type) has no original field at all,
  and `Servable` refuses an original's ref. Since originals keep their EXIF by
  product decision, "never serve an original" is enforced by the type and the query
  rather than by each handler remembering.
- 2026-09-07 16:00 — Blocker: my own injection test was wrong, not the code — it
  asserted `!strings.Contains(stmt, "'; DROP")`, which matches the correctly escaped
  `\'; DROP`. Replaced it with a scanner that verifies every quote either delimits a
  literal or is escaped, plus a test that the scanner can actually fail. Grepping for
  attack strings only ever catches the payloads somebody thought of.
- 2026-09-07 16:10 — ✅ All criteria met. 27 tests green; vet, staticcheck and gofmt
  clean both with the workspace and with `GOWORK=off`.
- 2026-09-07 16:15 — Completed. `New` returns an error rather than calling
  `log.Fatalf` the way shared-go's entities do — noted as a deliberate divergence to
  raise when this package is contributed upstream.
