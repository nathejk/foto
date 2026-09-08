# Product Requirements Documents

PRDs define *what* and *why*. The task board (`roadmap/tasks/`) tracks *execution*.

## Folders are the status

```
roadmap/prd/
  draft/      ← being written; not yet agreed
  doing/      ← agreed and being implemented
  done/       ← shipped
```

The `Status` field in a document's header must always match the folder it sits
in. It is one fact recorded twice: never move a file without updating the
header, and never change the header without moving the file.

## Naming

`<zero-padded number>-<slug>.md`, e.g. `001-foto-photo-entrypoint.md`. The
number is permanent and does not change as the file moves. Check the highest
existing number **across all three folders** before assigning a new one.

**Refer to a PRD by number, not by path** — in task files, commit messages and
code comments. Paths go stale as PRDs move; "PRD 001" does not.

## Lifecycle

| Transition | What to do |
|---|---|
| new | Create in `draft/` from the `prd` skill's template. Leave `Approved` and `Shipped` blank. |
| `draft/` → `doing/` | Set `Status: doing`, set `Approved` to today, bump `Last updated`, move the file, then create the tasks from "Rollout / Task Breakdown" in `roadmap/tasks/open/`. |
| `doing/` → `done/` | Every derived task is in `roadmap/tasks/done/`. Set `Status: done`, set `Shipped`, bump `Last updated`, move the file. |
| `doing/` → `draft/` | Scope changed enough to invalidate the agreement. Reset `Status`, clear `Approved`, move it back — do not quietly rewrite an approved document. |

Approval is a human's call. An agent does not move a PRD out of `draft/` on its
own judgement.

PRDs stay in `done/` — they are the record of intent and decisions.

## Commit messages

```
prd(<number>): <action> — <short title>
```

Actions: `create` · `update` · `approve` · `done` · `reopen`

All dates are `YYYY-MM-DD`.
