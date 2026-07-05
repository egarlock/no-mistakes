---
title: iOS / Xcode Testing
description: Run xcodebuild tests through the no-mistakes gate on macOS.
---

Testing an iOS app through no-mistakes needs **no special engine support** — it is a
configuration story. The built-in [test step](/no-mistakes/reference/pipeline-steps/#test)
already runs a `commands.test` string through the platform shell, treats a non-zero
exit as an auto-fixable gate, and its fix loop already asks the agent to triage a
failure as a real product bug, a fixable environment problem, or flaky infrastructure.
`xcodebuild test` is just another shell command, so it works on released code today.

There are two ways to wire it up, depending on how much control you want:

1. **[`commands.test`](#option-1-commandstest)** — the simplest path. One line; runs as
   the test step's baseline.
2. **[A dedicated `ios-test` command step](#option-2-a-dedicated-ios-test-command-step)** —
   a separate, reorderable [custom command step](/no-mistakes/reference/repo-config/#custom-command-steps)
   with its own `timeout`, so a hung simulator can't wedge the run.

Both require a macOS daemon host with Xcode installed. See
[Preconditions](#preconditions-a-doctor-style-checklist) before you start — simulator
availability is a host-environment problem no config can fix.

## Option 1: `commands.test`

Point the test step at `xcodebuild`:

```yaml
# .no-mistakes.yaml
commands:
  test: >-
    xcodebuild test
    -project App.xcodeproj
    -scheme App
    -destination 'platform=iOS Simulator,name=iPhone 16'
    -quiet
```

That's the whole integration. The test step runs this command first as its baseline
and checks the exit code. A non-zero exit produces an `error` finding and gates the
run; because the test step marks its findings auto-fixable, the fix loop then drives
your agent to run the tests, read the failures, and decide whether they are a real
code bug to fix, a setup problem it can repair (e.g. a missing scheme flag), or flaky
infra — the same triage the step applies to any other test command.

`-quiet` keeps `xcodebuild`'s notoriously verbose output down to failures and the
final summary, which is what lands in the finding and the PR's `## Testing` section.

:::note[`commands.*` is a trusted, default-branch field]
`commands.test` executes arbitrary shell on the daemon host, so — like all
`commands.*`, `agent`, and `steps` — the daemon reads it from your **default branch**
(e.g. `origin/main`), never from a contributor's pushed SHA. Commit the `xcodebuild`
line to your default branch for the gate to honor it. A pushed branch cannot inject or
change the test command unless you opt in with
[`allow_repo_commands: true`](/no-mistakes/reference/repo-config/#allow_repo_commands).
See the security note in the [Repo Config Reference](/no-mistakes/reference/repo-config/).
:::

## Option 2: a dedicated `ios-test` command step

`commands.test` is a single baseline. If you want your fast checks (unit tests,
`swiftlint`) to run and gate **before** you pay for a simulator boot, promote the
iOS run to its own [custom command step](/no-mistakes/reference/repo-config/#custom-command-steps),
introduced in the `steps:` mapping form:

```yaml
# .no-mistakes.yaml
commands:
  test: "xcodebuild test -project App.xcodeproj -scheme AppUnitTests -destination 'platform=macOS' -quiet"

steps:
  - rebase
  - review
  - test                       # fast: logic-only unit tests, gates first
  - name: ios-test             # slow: full simulator run, gated after fast checks pass
    command: >-
      xcodebuild test
      -project App.xcodeproj
      -scheme AppUITests
      -destination 'platform=iOS Simulator,name=iPhone 16'
      -quiet
    timeout: 30m
  - push
  - pr
  - ci
```

The `ios-test` step is a first-class gate: a non-zero `xcodebuild` exit parks the run
with a finding, exactly like the built-in test step. Ordering it **after** `test` (and
any `swiftlint`/format steps) means a broken unit test or lint error fails fast without
ever booting a simulator.

### Why the per-step `timeout` matters here

This is the reason to prefer a dedicated step over `commands.test` for the simulator
run. A simulator can hang — a stuck first boot, a modal that never dismisses, a UI test
waiting on an element that never appears — and `xcodebuild` will happily sit there.
Without a bound, that step blocks the run until someone notices and runs
[`axi abort`](/no-mistakes/reference/cli/). The `timeout` (default 30m; set it
explicitly to match your suite) kills the hung command and gates with a clear
`ios-test timed out after 30m` finding instead. Timeouts are **not** auto-fixable — a
hung simulator is rarely resolved by another code edit — so they park for a human or
agent to decide.

By default `auto_fix` is `false` on a custom command step, so `ios-test` findings park
for a decision. Add `auto_fix: true` to the step if you want the fix loop to drive your
agent at objective failures the same way the built-in test step does.

## Running a subset of tests

`xcodebuild`'s `-only-testing:` flag scopes a run to a target, class, or single test,
and it composes with everything above. A common shape is a fast smoke step that runs
the tests you actually touch plus a full-suite step:

```yaml
steps:
  - rebase
  - review
  - test
  - name: ios-test-smoke
    command: >-
      xcodebuild test
      -project App.xcodeproj
      -scheme App
      -destination 'platform=iOS Simulator,name=iPhone 16'
      -only-testing:AppTests/CheckoutFlowTests
      -only-testing:AppUITests/LoginUITests/testHappyPath
      -quiet
    timeout: 20m
  - name: ios-test-full
    command: >-
      xcodebuild test
      -project App.xcodeproj
      -scheme App
      -destination 'platform=iOS Simulator,name=iPhone 16'
      -quiet
    timeout: 45m
  - push
  - pr
  - ci
```

Two current limitations to know about:

- **Per-push selection only covers built-in steps.** `git push no-mistakes -o
  no-mistakes.skip=<steps>` accepts built-in step names only, so you cannot skip
  `ios-test-full` on one push and run it on the next. The step list is fixed by the
  trusted config; vary *what a step does* with a repo script (below) if you need
  dynamism.
- **A repo script's body runs from the pushed worktree.** A step like
  `command: ./scripts/run-selected-tests.sh` (e.g. mapping `git diff --name-only`
  output to `-only-testing:` flags) works, but while the command *string* is read from
  the trusted default branch, the script *contents* execute from the pushed branch. On
  a single-developer repo that is fine; on a multi-contributor repo, prefer inline
  commands or treat such scripts as part of your review surface.

## Validating without publishing

The push chain is optional. To use the gate purely as a local validator — for example
on a machine where you never want the branch forwarded or a PR opened — either skip
the publish steps per push:

```sh
git push no-mistakes my-branch -o no-mistakes.skip=push,pr,ci
```

or omit `push`/`pr`/`ci` from the repo's `steps:` list entirely. The chain rules only
require `push` before `pr`, and `pr` before `ci`, *when those steps are present*.
Pipeline fixes still land in the gate's copy of your branch, so pull them back into
your working repo with `git fetch no-mistakes <branch>` followed by a merge or reset.

## Preconditions: a doctor-style checklist

The engine runs whatever you configure; it cannot conjure a working Xcode toolchain.
These are **host-environment** problems that live on the daemon machine, and no
`.no-mistakes.yaml` value fixes them:

- **Pin `-destination` to a simulator that actually exists.** A `name=iPhone 16` that
  isn't installed fails with `Unable to find a device matching the provided
  destination specifier`. List what's really there with `xcrun simctl list devices
  available`, and prefer pinning by a stable name (or a `id=<UDID>`) over "latest",
  which drifts across Xcode updates.
- **Pre-install the platform runtime.** `xcodebuild -downloadPlatform iOS` fetches the
  iOS simulator runtime up front. Run it as a provisioning step on the daemon host so
  the first gated run doesn't fail on a missing runtime (or silently pay a multi-GB
  download inside a step timeout).
- **First-boot latency is real.** A cold simulator boot can take minutes before your
  tests even start. Size the [`timeout`](#why-the-per-step-timeout-matters-here) with
  that headroom, and consider pre-booting a simulator (`xcrun simctl boot <device>`)
  during provisioning.
- **Xcode version, license, and command-line tools are host state.** `xcodebuild`
  needs an accepted license (`sudo xcodebuild -license accept`), the right selected
  toolchain (`xcode-select -p`), and a project whose deployment target the installed
  SDK supports. A version mismatch surfaces as an `xcodebuild` failure the gate reports
  faithfully — it is not something config repairs.

Be honest with yourself: if the daemon host's Xcode is misconfigured, the gate will
correctly report red. That's the tool doing its job, not a bug to configure around.

## Richer per-test findings (future option)

By default a failing `xcodebuild` run produces **one** finding from its exit code — the
whole build log as a single blob. That is enough to gate and to drive the fix loop, but
it doesn't give a reviewer a per-test breakdown.

The growth path already exists in the custom-command contract:
[`findings_json`](/no-mistakes/reference/repo-config/#custom-command-steps). If you
point a command step at a file of structured findings, the step ingests real
per-file/per-line findings instead of the exit-code blob. You can get there today with
a thin wrapper that turns Xcode's result bundle into that JSON — for example, run
`xcodebuild test -resultBundlePath out.xcresult`, then
`xcrun xcresulttool get --format json --path out.xcresult`, and map each failed test to
a finding object — and set `findings_json:` to the wrapper's output path.

A built-in `.xcresult` summarizer that does this mapping for you is a candidate for a
future release. It is deliberately **not** shipped in-engine yet: `xcresulttool`'s JSON
schema drifts across Xcode versions, so baking a parser into the daemon trades the
stable `findings_json` seam for version-coupled surface area. Until then, the wrapper
approach keeps that coupling in your repo, where you control the Xcode version.
