---
name: revise
description: Example revision-and-review skill — apply safe revisions to the changed code directly, then re-review your own result and report only what you did not resolve.
mode: revise
---

# Revision-and-review skill

You are revising a code change. Unlike a read-only review, you **may edit
files**: apply the safe revisions this skill calls for directly, then re-review
your own result. The pipeline commits whatever you change and, later, pushes it
— so treat every edit as production-bound.

This body is layered between an engine-owned context header (branch, base and
target commits, review scope, default branch, ignore patterns) and an
engine-owned output contract (the commit summary and the `ask-user` /
`auto-fix` / `no-op` action vocabulary). You only need to describe *what to
revise and what to look for* — the engine handles *how the commit is made and
how findings are gated*.

## What to revise

Apply the mechanical, low-risk revisions your repo wants enforced on every
change — for example:

- Tighten error handling (wrap and annotate errors, remove swallowed errors).
- Bring changed code in line with house style and naming conventions.
- Remove dead code, redundant comments, or leftover debug statements the change
  introduced.

Keep every edit minimal and root-cause. Do **not** do unrelated refactoring,
change product behavior, or strip intentional user-facing output — that is the
author's deliberate intent, not something to revise away.

## What to leave for a human

After revising, re-review the result. Report a finding only for issues you did
**not** resolve:

- Anything that changes functional requirements or product behavior.
- Ambiguous intent, conflicting requirements, or a fix that needs a judgment
  call.

Do not report anything you already fixed.

## How to report

- Set the `summary` field to a one-line commit subject (under 10 words)
  describing the revisions you applied.
- Anchor every remaining finding to a specific file and one-indexed line number
  when possible.
- For each remaining finding pick `ask-user` (challenges author intent —
  default when in doubt), `auto-fix` (safely fixable without discussion), or
  `no-op` (informational).
- If you resolved everything, return an empty findings array (but still set
  `summary` to describe your revisions).

## Customizing this skill

Replace the sections above with your repo's own revision rules. Reference it
from `.no-mistakes.yaml` on your **default branch** (the skill body is loaded
from the trusted default-branch commit, never a pushed branch). A `mode: revise`
step must be ordered **before** `push`, or its commits would never reach the
remote:

```yaml
steps:
  - intent
  - rebase
  - review
  - name: house-style
    type: skill
    skill: .no-mistakes/skills/revise.md
    mode: revise
    require_review: false   # optional; true forces a park with the diff after any mutation
  - test
  - lint
  - push
  - pr
  - ci
```
