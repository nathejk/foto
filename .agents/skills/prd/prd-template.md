# PRD <NNN> — <Feature title>

**Status:** draft | doing | done
**Author:** <name or agent session>
**Created:** YYYY-MM-DD
**Last updated:** YYYY-MM-DD
**Approved:** YYYY-MM-DD
**Shipped:** YYYY-MM-DD
**Target users:** <participant | team leader | organizer | ...>

<!--
Status must match the folder this file is in: draft/, doing/ or done/.
Leave Approved blank until the PRD moves to doing/, and Shipped blank until it
moves to done/. See roadmap/prd/README.md for the lifecycle.
-->

---

## 1. Summary

One or two sentences describing the feature in plain language. If someone reads
only this section, they should understand what is being built and for whom.

## 2. Problem & Motivation

- **What problem does this solve?** Describe the user pain or business need.
- **Why now?** What makes this worth doing at this point.
- **Evidence.** Link to feedback, support tickets, metrics, or requests that
  motivate the work.

## 3. Goals

What this feature must achieve. Keep these outcome-oriented, not solution-oriented.

- Goal 1
- Goal 2

## 4. Non-Goals

Explicitly list what is out of scope, to prevent scope creep.

- Non-goal 1
- Non-goal 2

## 5. User Stories & Scenarios

Describe the feature from the user's perspective.

- As a `<user type>`, I want to `<action>` so that `<outcome>`.
- Walk through the primary happy-path scenario end to end.
- Note important edge cases and error scenarios.

## 6. Requirements

### Functional

- [ ] Requirement 1
- [ ] Requirement 2

### Non-Functional

- Performance, accessibility, i18n, security, privacy, etc. as relevant.

## 7. UX / UI Notes

Describe the intended user experience. Reference screens, flows, or components
in `vue/`. Link mockups if available. Note any new or changed frontend routes,
views, or components.

## 8. Technical Considerations

- **Frontend (Vue 3 / TS):** components, state, routes affected.
- **BFF (Go):** services, handlers, data flow affected.
- **API endpoints:** list new/changed endpoints. **Every endpoint must have
  OpenAPI annotations** — confirm this is accounted for.
- **Data / storage:** schema or persistence changes.
- **Dependencies & risks:** external services, migrations, backwards
  compatibility concerns.

## 9. Success Metrics

How will we know this worked? Prefer measurable signals.

- Metric 1 (with target, if known)
- Metric 2

## 10. Rollout / Task Breakdown

- Sequencing, feature flags, or phased rollout notes.
- Proposed tasks to create in `roadmap/tasks/open/` (one line each). These map
  to the file-based task board defined in `roadmap/tasks/TASKS.md`:
  - [ ] Task: <short title>
  - [ ] Task: <short title>

## 11. Open Questions

Track unresolved decisions here until they are answered.

- Question 1
- Question 2
