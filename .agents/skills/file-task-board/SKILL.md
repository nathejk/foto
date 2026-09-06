---
name: file-task-board
description: >
  Sets up and manages a file-based kanban task board inside a git repository,
  using open/doing/done folders with structured Markdown task files. Use this
  skill whenever the user wants to track tasks, issues, or work items in a git
  repo without an external tool — including phrases like "task tracker in git",
  "kanban in repo", "file-based issues", "LLM task board", "tasks folder",
  "track progress in git", or "set up a task system". Also use when an LLM
  agent needs to pick up, update, or complete tasks using this system.
---

# File-Based Task Board

A lightweight kanban system that lives entirely inside a git repo. Tasks are
Markdown files in `open/`, `doing/`, or `done/` folders. Moving a file between
folders signals a status change and produces a clean, readable git diff.

---

## Actions

This skill covers four distinct actions. Identify which one the user (or agent)
needs and jump to that section.

1. **[Scaffold](#scaffold)** — Set up the folder structure and conventions file in a repo
2. **[Create task](#create-task)** — Add a new task to `open/`
3. **[Pick up task](#pick-up-task)** — Claim a task from `open/` and move it to `doing/`
4. **[Update / complete task](#update--complete-task)** — Log progress or close a task

---

## Scaffold

Run when the user wants to initialise the system in a repo.

### Steps

1. Create the folder structure:

```bash
mkdir -p roadmap/tasks/open roadmap/tasks/doing roadmap/tasks/done
touch roadmap/tasks/open/.gitkeep roadmap/tasks/doing/.gitkeep roadmap/tasks/done/.gitkeep
```

2. Copy the conventions file from this skill's assets into the repo:

```bash
cp <skill-dir>/assets/TASKS.md roadmap/tasks/TASKS.md
```

> **`assets/TASKS.md`** is the canonical conventions reference. Read it before
> doing anything else — it defines the task file format, commit conventions,
> rules, and progress log expectations.

3. Create an example task so the board is not empty:

```bash
# Use the next available ID — scan all three folders first
```

Create `roadmap/tasks/open/001-example-task.md` using the task file format
defined in `assets/TASKS.md`.

4. Stage and commit:

```bash
git add roadmap/tasks/
git commit -m "task: initialise file-based task board"
```

5. Tell the user what was created and point them to `roadmap/tasks/TASKS.md` for
   the full conventions.

---

## Create Task

Run when the user wants to add a new task to the board.

### Steps

1. Scan all three folders to find the highest existing task ID, then increment by 1.
2. Ask for (or infer from context): title, description, acceptance criteria, priority.
3. Create the file in `roadmap/tasks/open/<id>-<slug>.md` using the format in `assets/TASKS.md`.
4. Commit: `git add` + `git commit -m "task(<id>): create — <title>"`
5. Confirm the task ID and file path to the user.

---

## Pick Up Task

Run when an LLM agent or developer is starting work on a task.

### Steps

1. List tasks in `roadmap/tasks/open/`. If no task is specified, pick the highest-priority
   available one (high → medium → low, then lowest ID).
2. Open the task file and update:
   - `Status` → `doing`
   - `Picked up by` → session ID, agent name, or developer name
   - `Started` → today's date
   - Append to progress log: `- YYYY-MM-DD HH:MM — Picked up. Plan: <brief summary of approach>.`
3. Move the file: `git mv roadmap/tasks/open/<file> roadmap/tasks/doing/<file>`
4. Commit: `git commit -m "task(<id>): pick up — <title>"`
5. Return the full task content so the agent has all context.

---

## Update / Complete Task

Run continuously during work, and when closing a task.

### Progress update (during work)

Append a new line to the `## Progress Log` section:

```
- YYYY-MM-DD HH:MM — <What was done, decided, or blocked. Be specific.>
```

**When to log** (critical — do not skip these):
- Starting a distinct sub-task or phase
- Making a non-obvious decision (log the decision AND the reason)
- Hitting a blocker or unexpected issue
- Completing an acceptance criterion (check it off with `[x]` AND log it)
- Pausing or handing off
- At least every significant chunk of work, even a brief status note

Commit after each meaningful update:
`git commit -m "task(<id>): update — <one-line summary>"`

### Completing a task

1. Verify all acceptance criteria are checked off (`[x]`).
2. Update the file:
   - `Status` → `done`
   - `Completed` → today's date
   - Append final log entry: `- YYYY-MM-DD HH:MM — Completed. <Brief outcome summary>.`
3. Move: `git mv roadmap/tasks/doing/<file> roadmap/tasks/done/<file>`
4. Commit: `git commit -m "task(<id>): done — <title>"`

---

## Rules (always enforce)

- **Never edit past progress log entries.** Append only.
- **Never reuse an ID**, even if a task is deleted.
- **One owner at a time.** Log any handoff explicitly before changing `Picked up by`.
- **Keep tasks atomic.** Split large tasks; reference sibling IDs in the log.
- **Commit on every meaningful state change.** Git history is the audit trail.
