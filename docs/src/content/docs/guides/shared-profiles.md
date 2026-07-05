---
title: Shared Gate Profiles
description: Define one gate profile once and apply it across many repos.
---

A **shared gate profile** lets you define a pipeline once — steps, skills, and
instructions — and apply it to many repos with a single trusted line in each
repo's config, instead of copy-pasting `.no-mistakes.yaml` and skill files into
every repo and re-copying on every change.

A profile lives on the daemon host under `<NM_HOME>/profiles/<name>/` and is
selected either by a repo's `profile: <name>` field or by a **host-local
binding** (`no-mistakes profile use <name>`, see
[below](#bind-a-repo-locally-nothing-committed-to-the-repo)). Because the
repo-config field decides which shell commands and which agent prompts run, it
is a **trusted-only** selection: it is read from the repo's default branch,
never from a pushed branch (see [Trust model](#trust-model)).

## Layout

```
<NM_HOME>/profiles/team-ios/
  profile.yaml                 # a version marker + a steps: list
  skills/ios-review.md         # skill bodies (resolved relative to the profile dir)
  instructions/swift.md        # instruction files (resolved relative to the profile dir)
```

`NM_HOME` defaults to `~/.no-mistakes`. A convenient way to keep the profile
fresh across a fleet is to make `<NM_HOME>/profiles/team-ios/` a git clone of a
team-owned repo: updating everyone's gate is then a `git pull`.

## profile.yaml

`profile.yaml` carries a `version` marker plus a `steps:` list in the exact same
schema a repo's own [`steps:`](/no-mistakes/reference/repo-config/#steps) uses —
built-in steps, [custom command steps](/no-mistakes/reference/repo-config/#custom-command-steps),
and [skill-driven steps](/no-mistakes/reference/repo-config/#skill-driven-steps):

```yaml
version: 3                     # informational; stamped into the run log
steps:
  - rebase
  - review
  - name: ios-review
    type: skill
    skill: skills/ios-review.md    # resolved against THIS profile dir
    mode: review
  - name: swiftlint
    type: command
    command: swiftlint --strict --reporter json > .nm-swiftlint.json || true
    findings_json: .nm-swiftlint.json
  - test
  - push
  - pr
  - ci
```

`skill:` and `instructions:` paths inside a profile step resolve **against the
profile directory**, and must not escape it. The bodies are read from
host-local disk, never from a repo worktree.

Skill steps in a profile must use `mode: review`. A `mode: revise` step —
which mutates worktrees and auto-commits — is **rejected at profile load** and
fails the run at start: one profile edit silently rewriting code across every
repo pointing at it is a blast radius v1 deliberately does not take on. Define
revise-mode steps in a repo's own `steps:` list instead.

## Selecting a profile in a repo

Add one line to the repo's `.no-mistakes.yaml` on the **default branch**:

```yaml
profile: team-ios
```

With no `steps:` of its own, the repo's pipeline **is** the profile's step list.

## Bind a repo locally (nothing committed to the repo)

Some repos cannot carry a `.no-mistakes.yaml` at all — a work repo on Azure
DevOps where you can't (or won't) commit tool config, an upstream you don't
control, a one-off clone. For those, bind the repo to a profile **on this
machine only**:

```bash
cd ~/work/some-ado-repo
no-mistakes profile use team-ios
```

The binding is stored in the local no-mistakes database — **zero files are
committed to the repo** — and from then on the profile's full pipeline (steps +
skills + instructions) gates every run for that repo. With no repo
`.no-mistakes.yaml` the profile's steps are the whole pipeline; if the repo
does have trusted `steps:`, they compose with the profile via the same
`- use: profile` splice sentinel as the repo-config path.

Precedence: a host-local binding **wins** over the repo config's `profile:`
field. Both are trusted selections, but the binding is the machine owner's more
specific, host-side decision. When both are set and differ, the daemon logs
that the local binding overrode the repo-config profile.

Because nothing about the binding comes from the repo, it does **not** require
the trusted default-branch fetch to succeed — unlike a repo-config `profile:`,
which fails the run when it cannot be verified. The bound profile itself is
still loaded fail-closed: if it is missing or invalid, the run fails at start
(never a silent fall back to the default pipeline).

Related commands (see the [CLI reference](/no-mistakes/reference/cli/#no-mistakes-profile)):

```bash
no-mistakes profile use <name>      # bind the current repo (host-local)
no-mistakes profile use --clear     # remove the binding
no-mistakes profile show            # current selection and which source wins
no-mistakes profile list            # profiles under <NM_HOME>/profiles
no-mistakes profile lint <name>     # validate a profile with the daemon's rules
no-mistakes init --profile <name>   # bind at init time
```

## Composing profile + repo steps

If a repo also wants its own steps, it must say **where** the profile's steps
go, using the `- use: profile` splice sentinel:

```yaml
profile: team-ios
steps:
  - use: profile               # ← the team-ios steps splice in here
  - name: repo-special-check
    type: command
    command: ./scripts/check-generated.sh
```

Two rules (v1):

1. `profile:` and **no** repo `steps:` → the profile's list is the pipeline.
2. `profile:` **plus** repo `steps:` → the list must contain exactly one
   `- use: profile` sentinel, which expands in place to the profile's steps. A
   repo `steps:` list with a profile selected but **no** sentinel is an error —
   dropping the shared gate is too consequential to infer.

The merged list is validated exactly like any `steps:` list (unique names,
push-chain ordering).

## Fail-closed behavior

A profile is a team gate, so a broken one **fails the run at start** rather
than silently dropping to the default pipeline:

- a **missing or unparsable** `profile.yaml` (a host that has not provisioned
  the profile directory cannot gate that repo until it does);
- a profile.yaml with an **unknown key** — `profile.yaml` has exactly two legal
  keys (`version`, `steps`), so a typo like `step:` fails loudly instead of
  parsing to zero steps;
- a profile that **defines no steps** — an empty steps list would otherwise be
  indistinguishable from "no `steps:` configured" and silently run the default
  pipeline in place of the shared gate;
- a profile step with **`mode: revise`** (see above);
- a **default-branch fetch failure** on a repo that names a profile in its
  config — the selection cannot be verified, so the run stops instead of
  running ungated. (A host-local binding is exempt from this one check: nothing
  about it comes from the repo, so there is nothing to verify against the
  default branch. A missing/invalid *bound* profile still fails the run.)

A missing skill *file* inside an otherwise-valid profile parks the individual
skill step with a misconfiguration finding, matching built-in skill steps.

## Trust model

The `profile:` **reference** rides the same trusted channel as `commands`,
`agent`, and `steps`: it is read only from the repo's trusted default-branch
`.no-mistakes.yaml`, so a pushed branch can never set, switch, or drop a
profile. Unlike those fields, `profile:` stays trusted-only **even when**
[`allow_repo_commands`](/no-mistakes/reference/repo-config/#allow_repo_commands)
is set — the safer v1 default.

The profile's **content** is never read from a pushed worktree at all: it lives
under `<NM_HOME>/profiles/`, a path no pushed commit can address. Its trust
anchor is filesystem ownership on the daemon host — the same class as
`~/.no-mistakes/config.yaml`, which already selects the agent binary that runs
with the maintainer's credentials.

A **host-local binding** (`no-mistakes profile use`) sits in that same class:
it is authored by the machine owner through the CLI and stored in the local
database, so it is trusted by definition — exactly like the global config that
already selects `agent`. It does not weaken the pushed-branch trust model:
pushed branches still control nothing about profile selection or content.

`ignore_patterns` is read from the trusted default-branch config as well, so a
pushed branch cannot hollow out the profile's review-type gates by ignoring
every changed file (`ignore_patterns: ["*"]` on a pushed branch is ignored).

## Auditing which profile gated a run

Each run that used a profile is stamped with `<name>@<ref>` — the profile
checkout's `HEAD` when the profile directory is a git repo, else a content hash
of `profile.yaml`. The stamp is stored on the run record (visible via
`axi status` / the run info) and logged at run start, so a consumer can confirm
which profile revision enforced a given gate.
