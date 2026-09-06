# File-Based Task Board — Instructions

This document defines the conventions for a file-based kanban task system designed for use by LLMs and human developers working together in a git repository.

---

## Folder Structure

```
roadmap/tasks/
  open/       ← tasks available to be picked up
  doing/      ← tasks actively being worked on
  done/       ← completed tasks (kept for reference)
  TASKS.md    ← this file; conventions and instructions
```

Each task is a single Markdown file. Moving a file between folders signals a status change and produces a clean, readable git diff.

---

## Naming Convention

Files are named with a zero-padded ID and a short slug:

```
001-setup-database-schema.md
002-implement-auth-endpoints.md
003-write-unit-tests-for-api.md
```

The ID is permanent and never changes, even as the file moves between folders. IDs are assigned sequentially — check the highest existing ID across all folders before creating a new task.

---

## Task File Format

Every task file must follow this structure:

```markdown
# <ID> — <Short title>

**Status:** open | doing | done
**Priority:** high | medium | low
**Created:** YYYY-MM-DD
**Picked up by:** <session ID, agent name, or developer name>
**Started:** YYYY-MM-DD
**Completed:** YYYY-MM-DD

## Description

A clear explanation of what needs to be done and why.
Include relevant context, links to related files or tasks, and any
constraints or requirements the implementor should know about.

## Acceptance Criteria

- [ ] Criterion one
- [ ] Criterion two
- [ ] Criterion three

## Progress Log

<!-- Append entries here — never edit or delete existing entries -->

- YYYY-MM-DD HH:MM — Task created.
```

Fields marked with a dash (`-`) that are not yet applicable should be left blank or omitted. Only fill in **Started** when the task moves to `doing/`, and **Completed** when it moves to `done/`.

---

## Workflow

### Picking Up a Task

1. Scan `open/` for available tasks. Choose based on priority and dependencies.
2. Update the file:
   - Set `Status` to `doing`
   - Set `Picked up by` to your session ID or name
   - Set `Started` to today's date
   - Append a progress log entry: `- YYYY-MM-DD HH:MM — Picked up. Starting <brief plan>.`
3. Move the file from `open/` to `doing/`.
4. Commit with message: `task(003): pick up — implement auth endpoints`

### Updating Progress

Progress must be logged **continuously** throughout the work — not just at the start and end. The goal is that anyone (human or LLM) reading the file at any moment can understand exactly where things stand and what has been done.

**When to add a progress log entry:**

- When you begin a distinct sub-task or phase of the work
- When you make a meaningful decision (e.g. chose approach A over B, and why)
- When you hit a blocker or unexpected complication
- When you complete an acceptance criterion — check it off and log it
- When you pause or hand off the task to someone else
- At least once every significant chunk of work, even if just a brief status note

**Format for log entries:**

```markdown
- YYYY-MM-DD HH:MM — <What was done or decided. Be specific.>
```

**Example of a well-maintained progress log:**

```markdown
## Progress Log

- 2025-06-04 09:00 — Task created.
- 2025-06-04 11:15 — Picked up. Plan: create schema migration, then seed script.
- 2025-06-04 11:40 — Migration file written at db/migrations/001_schema.sql. Reviewing constraints.
- 2025-06-04 12:05 — Decided to use UUIDs instead of integer IDs — aligns with existing user table.
- 2025-06-04 12:30 — ✅ Criterion 1 complete: schema migration tested locally.
- 2025-06-04 14:00 — Blocker: seed script fails on FK constraint. Investigating insert order.
- 2025-06-04 14:45 — Blocker resolved: reordered inserts. Seed script working.
- 2025-06-04 15:10 — ✅ Criterion 2 complete: seed script runs cleanly.
- 2025-06-04 15:20 — All criteria met. Moving to done.
```

### Completing a Task

1. Confirm all acceptance criteria are checked off.
2. Update the file:
   - Set `Status` to `done`
   - Set `Completed` to today's date
   - Append a final progress log entry summarising the outcome
3. Move the file from `doing/` to `done/`.
4. Commit with message: `task(003): done — implement auth endpoints`

---

## Creating a New Task

1. Assign the next available ID.
2. Create the file in `open/` following the format above.
3. Fill in `Description`, `Acceptance Criteria`, and the first progress log entry.
4. Commit with message: `task(004): create — add rate limiting to API`

---

## Commit Message Convention

```
task(<id>): <action> — <short title>
```

Actions: `create`, `pick up`, `update`, `done`, `reopen`

Example: `task(007): update — noted blocker on DB connection pooling`

---

## Rules

- **Never edit past progress log entries.** Only append.
- **Never reuse an ID**, even if a task is deleted.
- **Only one owner at a time.** If a task in `doing/` has no recent log entries and needs to be reassigned, log the handoff explicitly before updating `Picked up by`.
- **Keep tasks atomic.** If a task grows too large, split it and reference the new IDs in the original task's log.
- **Commit on every meaningful state change.** The git log is the audit trail.
