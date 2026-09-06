---
name: prd
description: >
  Author and maintain Product Requirements Documents (PRDs) in the tilmelding
  repo, using the team's standard template and the draft/doing/done folder
  lifecycle under `roadmap/prd/`. Use this when a user wants to spec, plan, or
  write a PRD for a new feature or significant change before implementation,
  and also when an existing PRD changes state — approving it, starting or
  finishing implementation, or revising its requirements. Trigger phrases:
  "write a PRD", "spec a feature", "plan a feature", "approve the PRD",
  "PRD status", "move the PRD to doing", "the PRD is shipped", "update the
  PRD".
---

# Writing a Product Requirements Document

Use this skill to produce a Product Requirements Document (PRD) for a new feature
or significant change before implementation begins.

## When to use

- The user asks to "write a PRD", "spec a feature", "plan a feature", or similar.
- A new feature or non-trivial change is being proposed and needs an agreed
  definition before code is written.
- A PRD's state changes: it is approved, implementation starts or finishes, or
  its requirements shift. See "Lifecycle" below — keeping an existing PRD
  current is part of this skill, not just authoring a new one.

Do **not** use this skill for small bug fixes or trivial changes — those belong
directly on the task board (`roadmap/tasks/`).

## Where PRDs live

PRDs live under `roadmap/prd/` at the repo root, in one of three folders that
mirror the task board's `open/doing/done`:

```
roadmap/prd/
  draft/      ← being written; not yet agreed
  doing/      ← agreed and being implemented
  done/       ← shipped
```

Name each file with a zero-padded sequence and a slug:

```
roadmap/prd/draft/002-team-leader-dashboard.md
roadmap/prd/done/001-seat-based-billing-and-member-identity.md
```

The number is permanent and never changes as the file moves. Check the highest
existing number **across all three folders** before assigning a new one. Create
a folder if it does not exist — git does not track empty directories, so
`draft/` and `doing/` may be absent.

New PRDs start in `draft/`.

**Refer to a PRD by number, not by path**, in task files, commit messages and
code comments. Paths go stale as PRDs move between folders; "PRD 003" does not.

## Lifecycle

The folder is the status. Moving the file between folders is how status changes.

**The `Status` field in the document header must always match the folder the
file is in.** It is one piece of information recorded twice, so never move a
file without updating the header, and never change the header without moving
the file. A PRD sitting in `draft/` whose tasks are all `done` is a bug in the
board.

### draft/ → doing/ (the approval gate)

When the PRD has been reviewed and agreed and implementation is starting:

1. Set `Status: doing`
2. Set `Approved` to today's date
3. Update `Last updated`
4. Move the file to `roadmap/prd/doing/`
5. Create the tasks from "Rollout / Task Breakdown" in `roadmap/tasks/open/`
6. Commit: `prd(002): approve — team leader dashboard`

### doing/ → done/

When every task derived from the PRD is in `roadmap/tasks/done/` and the
feature is shipped:

1. Set `Status: done`
2. Set `Shipped` to today's date
3. Update `Last updated`
4. Move the file to `roadmap/prd/done/`
5. Commit: `prd(002): done — team leader dashboard`

PRDs stay in `done/` — they are a record of intent and decisions.

### While a PRD is in doing/

Requirements shift during implementation. When they do, edit the PRD and bump
`Last updated`; a PRD in `doing/` that no longer describes what is being built
is worse than no PRD. If a change is large enough to invalidate the agreement,
move the file back to `draft/` — reset `Status`, clear `Approved` — rather than
quietly rewriting an approved document. Commit:
`prd(002): reopen — scope changed, needs re-agreement`.

### Commit message format

```
prd(<number>): <action> — <short title>
```

Actions: `create` · `update` · `approve` · `done` · `reopen`

This mirrors the task board's `task(<id>): <action> — <title>` convention.

## Process

1. **Gather context first.** Before drafting, make sure you understand:
   - The problem being solved and who has it (which user type — participant,
     team leader, organizer, etc.).
   - Any related existing code in `go/` (BFF) and `vue/` (frontend).
   - Whether the feature touches API endpoints, UI, or both.
   Ask the user targeted clarifying questions for anything you cannot determine
   from the codebase. Do not invent requirements.

2. **Draft the PRD** using the template in `prd-template.md` (in this skill's
   directory), into `roadmap/prd/draft/`. Fill in every section; if a section
   genuinely does not apply, keep the heading and write "N/A" with a one-line
   reason rather than deleting it. Leave `Approved` and `Shipped` blank.

3. **Respect repo conventions:**
   - This is a backend-for-frontend setup: a Vue 3 (TS) frontend backed by a
     dedicated Go BFF. Call out clearly which side owns each piece of work.
   - **Every new or changed API endpoint must have OpenAPI annotations** — note
     this explicitly in the Technical Considerations section for any endpoint
     the feature introduces.
   - Use `YYYY-MM-DD` for all dates, matching the task board.

4. **Link to the task board.** A PRD defines *what* and *why*; the task board
   (`roadmap/tasks/`) tracks *execution*. In the "Rollout / Task Breakdown"
   section, list the concrete tasks that should be created in
   `roadmap/tasks/open/`, but do not create those task files unless the user
   asks — they are created when the PRD is approved and moves to `doing/`.

5. **Review with the user.** After drafting, summarize the key decisions and
   open questions and ask the user to confirm before considering the PRD final.
   Do not move a PRD out of `draft/` on your own judgement; approval is the
   user's call.

## Template

The full template lives alongside this file at `prd-template.md`. Read it and
use it verbatim as the starting structure for every PRD.
