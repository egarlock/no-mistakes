---
title: Repo Config Reference
description: All fields for .no-mistakes.yaml.
---

Per-repo configuration lives in `.no-mistakes.yaml` at the root of your repository.

:::caution[Security: gate-control fields are read from the default branch]
`commands.*` execute arbitrary shell on the daemon host via `sh -c` / `cmd.exe /c`, `agent` selects which process launches there (including ordered fallback lists, ACP aliases such as `cursor`, and `acp:` targets) with the maintainer's credentials, and `steps` selects which validation steps run at all.
To prevent a supply-chain attack where a contributor lands a hostile value on a gated branch, the daemon always reads **`commands`, `agent`, and `steps` from your default branch** (e.g. `origin/main`), never from the pushed SHA, and reads them at the exact commit a fresh fetch resolved (so a stale `origin/<default>` ref cannot serve a value the live default branch removed).
The daemon also reads `document.instructions`, `disable_project_settings`, and `profile` only from that trusted copy.
`ignore_patterns` is not code-executing but selects whether validation executes at all (a pushed `ignore_patterns: ["*"]` would turn every review-type gate into a no-op), so it rides the same trusted channel.
If the default branch cannot be fetched and resolved to a readable commit, or its present `.no-mistakes.yaml` cannot be read and parsed, the run aborts before launching an agent.
A readable default-branch tree with no `.no-mistakes.yaml` is valid and uses defaults.
Commit the gate-control settings you want to your default branch.
Non-executing fields (`auto_fix`, `commit`, `intent`, `test`) are still read from the pushed branch.

If you genuinely want per-branch `commands`, `agent`, `steps`, and `ignore_patterns` (for example, a single-developer repo where you trust your own feature branches), opt in with [`allow_repo_commands: true`](#allow_repo_commands) in this same file on your default branch. This re-enables the previous behavior with eyes open. The switch is read only from the trusted default-branch copy, so a contributor cannot self-enable it from a pushed branch.
:::

```yaml
# .no-mistakes.yaml

agent: codex

commands:
  lint: "golangci-lint run ./..."
  # Targeted local validation only - not a full-repo CI-parity suite.
  test: "go test ./internal/cli -run '^TestDoctor' -count=1"
  format: "gofmt -w ."

ignore_patterns:
  - "*.generated.go"
  - "vendor/**"

# Optional documentation ownership policy, read only from the trusted default branch.
document:
  instructions: |
    docs/ owns detailed product guidance; README.md owns the introduction.

# For orchestration repos whose project instructions would misidentify gate agents.
# Read only from the trusted default branch. Defaults to false.
disable_project_settings: true

# Optional: enable/disable/reorder the built-in pipeline steps.
# Omit for the full default pipeline.
# steps: [rebase, test, push, pr, ci]

auto_fix:
  rebase: 3
  review: 3
  test: 3
  document: 3
  lint: 5
  ci: 3

commit:
  fix_message: "chore(no-mistakes-{{.Step}}): {{.Summary}}"

intent:
  enabled: true
  threshold: 0.2
  slack_days: 3
  disabled_readers: []

test:
  evidence:
    store_in_repo: true
    dir: .no-mistakes/evidence
```

## Fields

### agent

Override the default agent for this repo and its setup-wizard suggestions.

| | |
|---|---|
| Type | `string` or `string[]` |
| Values | `auto`, `claude`, `codex`, `rovodev`, `opencode`, `pi`, `copilot`, `cursor`, `acp:<target>` |
| Default | Inherits from global config |

`auto` resolves to the first supported native agent or ACP alias in this order: `claude`, `codex`, `opencode`, `acli` with `rovodev` support, `pi`, `copilot`, then `cursor`.
`cursor` is an ACP alias for the `cursor` target with default command `cursor-agent acp`.
Its availability uses the global `acpx_path` and `acp_registry_overrides.cursor` settings when present.
`acp:<target>` uses the user-installed `acpx` binary configured in global config; `acp:cursor` uses the same default command as `cursor`.
Arbitrary `acp:<target>` agents are opt-in and are not considered by `agent: auto`.
The effective agent configuration must resolve to a runnable runner before a new validation gate starts.
If the selected explicit agent or `auto` is unavailable, the gate fails before its first pipeline step rather than reporting partial validation as passed.

You can also set an ordered fallback list:

```yaml
agent: [codex, claude]
```

The list is filtered to entries available to the daemon at run startup, and the first available entry becomes the primary agent.
After resolving `auto`, entries that resolve to the same ACP target are deduplicated in list order, so `cursor` and `acp:cursor` provide one fallback and preserve whichever spelling appears first.
If no entry is available, the gate fails before its first pipeline step.
If a pipeline invocation fails because that agent process cannot start or exits with an error, no-mistakes retries that invocation with the next available fallback.
Structured findings and schema/output validation problems do not trigger fallback.
This per-repo `agent` value, including every fallback entry, is still read from the trusted default-branch `.no-mistakes.yaml` unless `allow_repo_commands` is enabled there.

### allow_repo_commands

Opt in to honoring the code-executing selection fields (`commands.{test,lint,format}`, `agent`, and `steps`) from a contributor's pushed branch instead of the trusted default-branch copy.

| | |
|---|---|
| Type | `bool` |
| Default | `false` |

This field is itself read **only from the trusted default-branch copy** of `.no-mistakes.yaml`, never from the pushed SHA, so a contributor cannot self-enable it by setting it on a feature branch. By default the daemon reads `commands`, `agent`, and `steps` from your default branch (e.g. `origin/main`) so a pushed SHA cannot inject shell, pick the launched agent on the daemon host, or drop validation steps from the pipeline. Leave this `false` for any repo that accepts contributions. Set it to `true` only for a single-developer environment where you trust every branch you push (for example, a personal repo gated by your own daemon).

### disable_project_settings

Suppress project-level agent settings and instructions for every gate-agent start and resumed session.

| | |
|---|---|
| Type | `bool` |
| Default | `false` |

This opt-in is intended for agent-orchestration repositories whose `AGENTS.md`, `CLAUDE.md`, or harness-specific project settings would give a validation agent an operator identity and authority that it must not adopt.
When enabled, no-mistakes suppresses the target checkout's project settings for every agent-driven gate step while preserving user-level agent configuration.
Codex and Claude are the currently verified agents: Codex receives `project_doc_max_bytes=0` and `--ignore-rules`, while Claude loads only its user setting source.
The setting applies to both new and resumed sessions.

The gate fails before launching an agent if any resolved agent or fallback lacks a verified suppression mechanism.
It also fails if `agent_args_override` defeats suppression, such as a nonzero Codex `project_doc_max_bytes` or Claude setting sources that include `project` or `local`.
When this option is `false`, missing, or `null`, all agents retain their existing project-setting behavior.

This field is honored **only from the trusted default-branch copy** of `.no-mistakes.yaml`, regardless of `allow_repo_commands`.
A pushed branch cannot enable it or disable a trusted opt-in.
If the trusted commit or its present config file cannot be read and parsed, the run aborts rather than guessing that the option is disabled.

### commands.test

Explicit **targeted** local test command. Run via the platform shell - `sh -c` on POSIX, `cmd.exe /c` on Windows.

| | |
|---|---|
| Type | `string` |
| Default | Empty (agent selects the smallest relevant tests and evidence checks) |

`commands.test` is local **targeted validation** of the change and requested intent, not a CI-parity repository-wide regression command.
Broad regression belongs in remote CI and remains mandatory before a PR is ready; do not put a complete-suite walk here just to mirror CI.
no-mistakes does not guess whether an arbitrary shell string is "too broad" - the contract is documented and dogfooded, not enforced with language- or filename-specific heuristics.

When set, the test step runs this exact command first as the baseline and checks the exit code.
When empty, the agent detects and runs the smallest relevant tests itself (and is instructed never to run the complete repository suite).
When user intent is available, the agent may still run after a successful baseline command to gather evidence-oriented validation, still under the same targeted-validation contract.

### commands.lint

Explicit lint command. Run via the platform shell - `sh -c` on POSIX, `cmd.exe /c` on Windows.

| | |
|---|---|
| Type | `string` |
| Default | Empty (agent auto-detects) |

When set, the lint step runs this exact command and checks the exit code.
When empty, the agent-driven lint duty is folded into the document step's combined housekeeping pass: one agent invocation covers both documentation and lint, and the lint step consumes that result, reporting lint-category findings with the same gate semantics (blocking findings park for a decision).
Neither responsibility is skipped: when the document step has nothing to run against (or its structured output cannot be trusted), the lint step runs its own agent pass as before.

### commands.format

Formatter command run before the push step commits agent fixes.

| | |
|---|---|
| Type | `string` |
| Default | Empty (no separate push-step formatter) |

This does not prevent empty `commands.lint` from detecting and running formatters during the combined housekeeping pass, or during the lint step when that pass cannot provide a result.

### document.instructions

Repository-specific documentation ownership policy for the document step.

| | |
|---|---|
| Type | `string` (multiline) |
| Default | Empty (built-in placement policy only) |

The document step always applies a built-in placement policy: every fact has exactly one authoritative owner document, stale duplicates are removed or reduced to pointers instead of synchronized, no new documentation surfaces are created merely to close perceived gaps, and incident lessons live as invariants near their owner (with a pointer to the regression test), never as AGENTS.md postmortems.
`document.instructions` states this repository's ownership map or extra placement rules (for example, which file owns which class of facts).
It augments or clarifies the built-in policy; it cannot disable documentation integrity.

Like `commands.*` and `agent`, this field steers gate behavior, so it is honored **only from the trusted default-branch copy** of `.no-mistakes.yaml`: a contributor's pushed branch cannot weaken the documentation rules that gate its own review.

### Command process lifetime

All configured `commands.*` entries are scoped to their step.
After no-mistakes starts one of these commands, it terminates any remaining child processes from that command when the command exits, fails, or the step is cancelled.
Do not rely on a configured command to leave a background server or watcher running after it returns; keep that service inside the command lifetime or start it outside no-mistakes.

### ignore_patterns

Paths to exclude from review and documentation checks.

| | |
|---|---|
| Type | `string[]` |
| Default | Empty (no ignores) |

Because ignore patterns decide whether review-type gates run at all, they are read from the trusted default-branch copy of `.no-mistakes.yaml` — never the pushed branch — unless [`allow_repo_commands`](#allow_repo_commands) is enabled. A pushed branch cannot widen the ignore list to skip its own review.

Pattern matching rules:

| Pattern | Rule |
|---|---|
| `*.generated.go` | No slash - matches by basename |
| `vendor/**` | Ends with `/**` - matches entire subtree |
| `some/path/file.go` | Contains a slash - full path glob |

### profile

Select a [shared gate profile](/no-mistakes/guides/shared-profiles/) — a pipeline defined once under `<NM_HOME>/profiles/<name>/` and applied across many repos — instead of (or spliced into) this repo's own `steps:`.

| | |
|---|---|
| Type | `string` |
| Default | Empty (no profile) |

```yaml
profile: team-ios
```

With no repo `steps:`, the profile's step list is the pipeline. To keep repo-local steps too, add a `- use: profile` splice sentinel to `steps:` (see [Shared Gate Profiles](/no-mistakes/guides/shared-profiles/#composing-profile--repo-steps)).

`profile` is a **trusted-only** selection: it is read only from the trusted default-branch copy of `.no-mistakes.yaml`, so a pushed branch can never set, switch, or drop a profile. Unlike `commands`/`agent`/`steps`, it stays trusted-only **even when** [`allow_repo_commands`](#allow_repo_commands) is enabled. A `profile:` naming a directory the daemon host has not provisioned (missing, unparsable, carrying unknown keys, or defining no steps) fails the run at start — there is no silent fallback to the default pipeline. The same applies when the default branch cannot be fetched to verify the selection: a repo that names a profile refuses to run rather than run ungated.

A **host-local binding** set with [`no-mistakes profile use <name>`](/no-mistakes/reference/cli/#no-mistakes-profile-use) **takes precedence over this field**. The binding is authored by the machine owner (same trust level as the global config), lives in the local database rather than the repo, and works with no `.no-mistakes.yaml` committed at all — see [Bind a repo locally](/no-mistakes/guides/shared-profiles/#bind-a-repo-locally-nothing-committed-to-the-repo). When both are set and differ, the daemon logs the override and the locally bound profile gates the run.

### steps

Enable, disable, or reorder the built-in pipeline steps for this repo. Each entry is a step name; the pipeline runs exactly the listed steps, in list order.

| | |
|---|---|
| Type | `string[]` |
| Values | `intent`, `rebase`, `review`, `test`, `document`, `lint`, `push`, `pr`, `ci` |
| Default | Empty (the full default pipeline: `intent → rebase → review → test → document → lint → push → pr → ci`) |

```yaml
steps: [rebase, test, push, pr, ci]
```

Names must be unique and name a built-in step. The push chain keeps its data-loss guarantees: `ci` requires `pr` earlier in the list, `pr` requires `push`, and `push` requires `rebase` (the rebase step's fetch anchors the push step's force-push lease). A list that violates these rules fails the run at start with an error listing every problem — there is no silent fallback. Placing `intent` after the steps that consume it, or a fixing step (`review`, `test`, `document`, `lint`) after `push`, is allowed but logged as a warning since those fixes never reach the remote.

Like `commands` and `agent`, `steps` selects which code executes, so it is read from the trusted default-branch copy of `.no-mistakes.yaml` unless [`allow_repo_commands`](#allow_repo_commands) is enabled. A `steps:` value on a pushed branch is otherwise ignored.

Per-run skips (`--skip`, `git push -o no-mistakes.skip=<steps>`) still apply on top of the configured list.

#### Custom command steps

A `steps` entry may be a mapping instead of a plain name. A mapping carrying a `command` defines a **custom command step** that runs an arbitrary shell command (e.g. `swiftlint`, `xcodebuild test`) as part of the pipeline, reporting pass/fail through the normal gate:

```yaml
steps:
  - rebase
  - name: swiftlint
    command: swiftlint lint --quiet
    findings_json: build/swiftlint.json   # optional: structured findings the step ingests
    timeout: 5m                            # optional: bounds a long/hung command
    auto_fix: true                         # optional: mark findings auto-fixable
    instructions:                          # optional: guidance injected into agent steps
      - .no-mistakes/swift-review.md
  - review
  - push
  - pr
  - ci
```

| Field | Type | Meaning |
|---|---|---|
| `name` | `string` | Step identity (lowercase letters, digits, `-`, `_`). Must be unique and must not collide with a built-in step name. |
| `command` | `string` | Shell command to run. Its presence marks the entry as a custom step. Executes on the daemon host, so — like `commands` — it is honored only from the trusted default-branch copy. |
| `findings_json` | `string` | Optional worktree-relative path the command writes findings JSON to. When present, the step parses it into real per-file/per-line findings instead of mapping a bare exit code to one finding. Accepts the full findings object (`{"findings": [...], "summary": ...}`) or a bare array of finding objects. If the file is absent, the step falls back to exit-code mapping. |
| `timeout` | `duration` | Optional per-step timeout (Go duration, e.g. `5m`, `90s`). Defaults to 30m. A step that exceeds it is killed and gates with a clear timeout finding. |
| `auto_fix` | `bool` | Optional. When `true` the step's findings are marked auto-fixable (the executor may drive an agent to resolve them, up to the built-in default of 3 attempts). Default `false`: findings park for an agent/human decision, consistent with the built-in review step. |
| `instructions` | `string[]` | Optional instruction-file paths whose contents are injected into the built-in agent steps (review, test, lint, document). See below. |

A mapping with only a `name` (no `command`) is a built-in step; it may still carry `instructions`.

For a worked example — running `xcodebuild test` either as `commands.test` or as a dedicated `ios-test` command step with a `timeout` — see the [iOS / Xcode Testing guide](/no-mistakes/guides/ios-testing/).

#### Per-step instructions

`instructions` lets a repo inject maintainer-authored guidance into the built-in agent steps (for example, review conventions specific to your codebase). The file **contents** are read at the trusted default-branch SHA — never the pushed worktree — and sanitized before injection, so a pushed branch cannot rewrite the guidance the gate injects into its own agent steps. Instruction files absent on the trusted default branch simply contribute nothing. This trusted-SHA read is enforced regardless of `allow_repo_commands`.

#### Skill-driven steps

A `steps` entry carrying a `skill` path defines a **skill-driven step**: the same machinery the built-in `review` step uses (a prompt template + a findings JSON schema handed to the agent), except the prompt body comes from a repo-owned skill file. This is how you add a domain-specific review — security, API-compatibility, design-system/UX-copy conformance, architecture rules — whose "check" is prose rather than a deterministic command. It is agent-agnostic: the skill body is inlined into the prompt, so it works with any agent (there is no skill-invocation channel and none is needed).

```yaml
steps:
  - intent
  - rebase
  - review
  - name: security-review
    type: skill
    skill: .no-mistakes/skills/review.md
    mode: review          # read-only findings pass (see mode: revise below)
    auto_fix: false        # optional; default false (findings park, like built-in review)
  - test
  - lint
  - push
  - pr
  - ci
```

| Field | Type | Meaning |
|---|---|---|
| `name` | `string` | Step identity (lowercase letters, digits, `-`, `_`). Must be unique and must not collide with a built-in step name. |
| `skill` | `string` | Repo-relative path to the skill markdown file. Its presence marks the entry as a skill step. Must not be absolute or escape the repo. |
| `mode` | `string` | Skill execution mode: `review` (read-only findings pass; the default when omitted) or `revise` (may edit + commit, then re-review — see below). |
| `require_review` | `bool` | Revise-mode only. When `true` the step parks with the committed diff after any mutation so the revision is approved before the pipeline continues. Default `false`: the revise commit lands silently and the step parks only on unresolved findings (matching the document step). No effect in review mode. |
| `type` | `string` | Optional explicit kind (`skill` or `command`). Advisory — the entry is a skill step whenever it carries a `skill`. A `type` that disagrees with the payload is an error. |
| `auto_fix` | `bool` | Optional. When `true` the step's findings are marked auto-fixable. Default `false`: findings park for an agent/human decision, matching the built-in review step. |

The step composes the agent prompt from three fixed layers: (1) an engine-owned context header (branch, base/target commit, review scope, default branch, ignore patterns, plus execution/round-history/user-intent context) identical to the built-in review step; (2) the repo-owned **skill body** (your domain instructions, with the file's YAML frontmatter stripped); (3) an engine-owned output contract that enforces the structured-findings schema and the `ask-user` / `auto-fix` / `no-op` action vocabulary. Findings gate exactly like the built-in review, so the `axi respond` surface (approve / fix / skip) works unchanged — `axi respond --action fix --instructions "..."` feeds your guidance into a fix round.

**Security — trusted skill loading:** like `commands`, `agent`, and per-step `instructions`, the skill **body** is read at the trusted default-branch SHA (`git show <trustedSHA>:<path>`), never from the pushed worktree, so a contributor's branch can never rewrite the prompt that drives the maintainer's agent. The `steps:` list itself (including the skill path) is a trusted-default-branch-only field under the secure default. If the skill body cannot be resolved from the trusted default branch (fetch failed, no default branch, or the file is absent there), the step fails closed — it parks with a misconfiguration finding rather than running an empty or pushed-branch prompt.

**Read-only guard (`mode: review`):** a review-mode skill must not change the worktree. After the review pass the step checks `git status --porcelain`; if the skill agent dirtied the tree, the changes are discarded (`git reset --hard` + `git clean -fd`) and a warning finding is recorded ("skill modified the worktree during a review-mode step; changes were discarded"). The read-only contract is enforced, not hoped. When a user chooses `fix` on a skill finding, the follow-up fix round is *allowed* to edit and commits its changes through the normal fix loop — the guard only applies to the read-only review pass.

**`mode: revise` (revision + review):** a revise-mode skill *may* edit the worktree. Its output contract instructs the agent to apply safe revisions directly, then re-review its own result and report only the findings it did not resolve, returning a one-line commit summary. After the run the step commits whatever changed through the **same commit protocol** the built-in `document`/`lint` steps and every fix round use (`commitAgentFixes`): it advances the run's head SHA, persists it, and fast-forwards the local branch ref. Because the revise commit is just additional local history the run knowingly built on its own base, it composes with the push step's force-push lease without weakening it. By default the commit lands silently and the step parks only on unresolved findings; set `require_review: true` to force a park with the committed diff after any mutation.

```yaml
steps:
  - intent
  - rebase
  - review
  - name: house-style
    type: skill
    skill: .no-mistakes/skills/revise.md
    mode: revise
    require_review: false   # true forces a park with the diff after any mutation
  - test
  - lint
  - push
  - pr
  - ci
```

**Ordering:** a `mode: revise` step **must** be listed before `push`. Because it mutates and commits, placing it after `push` would mean its commits never reach the remote (and would land a mutating commit after the force-push lease is anchored). Unlike the built-in mutating steps — which only warn when placed after `push` — an out-of-order revise step is a hard configuration error.

Starter skill templates ship at [`.no-mistakes/skills/review.md`](https://github.com/kunchenguid/no-mistakes/blob/main/.no-mistakes/skills/review.md) (read-only) and [`.no-mistakes/skills/revise.md`](https://github.com/kunchenguid/no-mistakes/blob/main/.no-mistakes/skills/revise.md) (revision + review); copy one and replace the body with your repo's conventions.

### auto_fix

Override auto-fix attempt limits for specific steps. Fields not set here inherit from global config.

| | |
|---|---|
| Type | `object` |

| Field | Type | Default |
|---|---|---|
| `auto_fix.rebase` | `int` | Inherits from global (default `3`) |
| `auto_fix.review` | `int` | Inherits from global (default `0`) |
| `auto_fix.test` | `int` | Inherits from global (default `3`) |
| `auto_fix.document` | `int` | Inherits from global (default `3`) |
| `auto_fix.lint` | `int` | Inherits from global (default `3`) |
| `auto_fix.ci` | `int` | Inherits from global (default `3`) |

Set to `0` to disable the follow-up auto-fix loop for a step (findings require manual approval).
The document step attempts documentation fixes during its initial pass, so unresolved documentation findings pause for approval instead of using an automatic follow-up loop.
For empty `commands.lint`, the document step's combined housekeeping pass also attempts safe lint fixes, and the lint step consumes its result; unresolved blocking lint findings pause for approval instead of starting another automatic fix loop.

`auto_fix.ci` covers the CI step's CI failure and merge-conflict auto-fix attempts.

Legacy alias: `auto_fix.babysit`.

### commit.fix_message

Override the auto-fix commit subject template for this repository.

| | |
|---|---|
| Type | `string` |
| Default | Inherits from global config, whose default is `no-mistakes({{.Step}}): {{.Summary}}` |

The value follows the [global `commit.fix_message` template syntax and validation rules](/no-mistakes/reference/global-config/#commitfix_message).
That includes the 1,024-byte template limit, 16-placeholder limit, 4,096-byte summary and rendered-subject limits, and rejection of bidi and invisible Unicode format characters.
The setting applies to the Review, Test, Document, and Lint fix path, not commits created by the Rebase, CI, or Push steps.

This non-executing field is read from the pushed branch, so a branch can adopt its own commit convention without enabling `allow_repo_commands`.

### intent

Override transcript-based user-intent extraction settings for this repo.
Fields not set here inherit from global config and then the built-in defaults.

| Field | Type | Default |
|---|---|---|
| `intent.enabled` | `bool` | Inherits from global (default `true`) |
| `intent.threshold` | `float` | Inherits from global (default `0.2`) |
| `intent.slack_days` | `int` | Inherits from global (default `3`) |
| `intent.disabled_readers` | `string[]` | Adds to globally disabled readers |

Valid `disabled_readers` values are `claude`, `codex`, `opencode`, `rovodev`, `pi`, and `copilot`.

### test.evidence

Override where evidence artifacts from the test step are stored.
Fields not set here inherit from global config and then the built-in defaults.

| Field | Type | Default |
|---|---|---|
| `test.evidence.store_in_repo` | `bool` | Inherits from global (default `false`) |
| `test.evidence.dir` | `string` | Inherits from global (default `.no-mistakes/evidence`) |

By default, test evidence stays in a temporary directory keyed by run ID and is referenced by local path.
Set `store_in_repo: true` to write evidence under `<dir>/<branch-slug>` inside the worktree so push can commit and publish it with the branch.
Branch slashes become nested directories, unsafe branch characters are replaced, and an empty branch slug falls back to the run ID.
If `dir` is absolute, escapes the worktree, points into `.git`, crosses a symlink, or is ignored by Git, no-mistakes falls back to temporary evidence storage for that run.
