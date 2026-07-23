# Rebase onto upstream v1.41.0 — notes

Branch `rebase-v1.41` replays the fork's work onto upstream tag **v1.41.0**
(`6e9aaf2`). The previous fork line was based on `78c7e60` (upstream v1.33.0
content); upstream had advanced 69 commits since.

```
v1.41.0 (6e9aaf2)
  └── 10 replayed fork commits + 1 new compatibility fix
```

`git log --oneline v1.41.0..HEAD`:

| # | Commit | Origin |
|---|---|---|
| 1 | `feat(pipeline): per-repo steps: list…` | fork `9c8914c` (+ merge `717b4e8`, flattened) |
| 2 | `feat(pipeline): custom command steps + per-step options (#2)` | fork `09b74f3` |
| 3 | `docs(steps): iOS/Xcode testing recipe (#3)` | fork `8ef33c4` |
| 4 | `feat(steps): iOS-oriented quick wins (#5)` | fork `bd4e68a` |
| 5 | `feat(pipeline): skill-driven read-only validation steps (#4)` | fork `1189ff7` |
| 6 | `feat(steps): mode: revise skill steps (#6)` | fork `6db3a22` |
| 7 | `feat(profiles): shared gate profiles PR-A (#7)` | fork `b39e825` |
| 8 | `fix(security): fail-closed fixes… (#8)` | fork `72064a7` |
| 9 | `Work-computer readiness: … drop telemetry + installer (#9)` | fork `b62abda` |
| 10 | `test(e2e): pin daemon environment… (#10)` | fork `8d25a58` |
| 11 | `fix(branchsync): fail-safe equivalence detection on git < 2.40` | **new**, see below |

The fork's PR-1 merge commit was flattened by the rebase, so 11 fork commits
become 10 replayed commits with no content loss.

Net: **143 files changed, 8149 insertions(+), 4663 deletions(-)** vs. v1.41.0.

## Verification

All green on this host (macOS 15.7.7, go 1.26.4, Apple Git 2.39.5):

- `gofmt -l .` — empty
- `go vet ./...` — clean
- `make lint` (skill drift check + vet) — clean, `SKILL.md` up to date
- `go test ./...` — no failures
- `go test -race ./...` — no failures
- `go build -o ./bin/no-mistakes ./cmd/no-mistakes` — ok
- `make e2e` — see the e2e section below
- `./bin/no-mistakes --version`, `doctor` against a throwaway `NM_HOME`,
  and `profile list` / `profile --help` (fork feature) — all work

Constraint proofs (all zero unless noted):

```
internal/telemetry/                      absent
grep -r 'no-mistakes/internal/telemetry' *.go        0
grep -r 'trackReadSurface|trackCommand|trackAxiSurface' *.go   0
docs/install.sh                          absent
StepSpec 109 · IsSkill 16 · ProfileConfig 7 · LocalProfile 44
SkillStep 77 · CommandStep 31 · ComposeProfileSteps 13
ProfileSpliceSentinel 7 · stepInstructionsPromptSection 15
```

All 23 files the fork added still exist; all 11 files the fork deliberately
deleted are still deleted.

## Conflict hotspots

Ranked by pain, matching the scout report's §3 prediction:

1. **`internal/config/config.go`** — conflicted on 4 of 10 commits. Upstream
   grew `agentList` fallback agents, `document.instructions`,
   `disable_project_settings`, `commit.fix_message`, and
   `daemon_connect_timeout` in exactly the struct regions the fork's
   `Steps`/`Profile`/`StepSpec` occupy. Every hunk was additive; resolution was
   union, not choice. One non-obvious trap is documented below.
2. **`internal/daemon/manager.go`** — conflicted on 4 commits.
   `startRun`/`loadTrustedRepoConfig` is the fork's trust boundary *and*
   upstream's agent-resolution + `assertGateTrustedConfigReadable` boundary.
   Also required a real code change (below), not just a union merge.
3. **`internal/e2e/harness.go`** — conflicted on 3 commits. Upstream's
   `e2edaemon` ownership field and login-shell PATH seeding land next to the
   fork's `repoConfigExtra`/`repoExtraFiles`/`profiles` fields and helpers.
4. **`internal/db/{run,step,schema}.go`** — dual column additions, see the
   schema section.
5. **Docs** (`repo-config.md`, `pipeline.md`, `introduction.md`,
   `troubleshooting.md`, `environment.md`, `cli.md`) and `AGENTS.md` — both
   sides rewrote the same security caution and capability bullets. Composed by
   hand every time.
6. **`internal/pipeline/steps/{review,document}.go`** — prompt composition, see
   below.

## Judgment calls

### 1. Telemetry (re-done against the v1.41 surface, not replayed)

The fork's `b62abda` removed telemetry against the v1.33 surface. Upstream
roughly tripled that surface since, so the patch could not simply apply. The
telemetry-only conflicted files were resolved to upstream's side and then
re-cleaned by hand, which also preserved the upstream fixes that had been
*inside* the wrappers (e.g. `guardDestructiveDaemonLifecycle` and
`logLifecycleInvocation` live inside `trackCommand("daemon.stop", …)`; taking
the fork's side wholesale would have silently reverted the destructive-lifecycle
guard from `493fc69`).

Removed: the whole `internal/telemetry` package (client, config, `readgate*`,
`readgate_lock*`), `internal/cli/telemetry.go` + its tests,
`internal/{daemon,pipeline}/telemetry_test.go`, `paths.TelemetryGateFile`,
the `NO_MISTAKES_TELEMETRY` / `NO_MISTAKES_UMAMI_*` docs and test env sets, and
the `UMAMI_HOST`/`UMAMI_WEBSITE_ID` ldflags in `.github/workflows/release.yml`.

Unwrapped in place: `axi` home/status/logs, `status`, `runs`, `stats`,
`update`, `doctor`, `daemon start|stop|restart|status`, the executor's
approval/fix/step events, the daemon's run-lifecycle events, `cli/sync.go`'s
`trackSyncAttempt`, and the TUI's `trackTUISyncAttempt`.

Also removed as telemetry-only plumbing: the read-surface **state
fingerprints**. `runAxiHome`/`runAxiStatus`/`runAxiLogs` went back to returning
plain `error`, and `runStateFingerprint`/`renderedRunsFingerprint`/
`statusFingerprint` are gone — they existed solely to dedupe remote events.

**Kept deliberately: `agent_invocations` and `runs.parked_ms`.** These are
*local* SQLite evidence read by `no-mistakes stats --agents` / `--run <id>`;
nothing about them touches the network. The task allowed keeping entangled
schema, but this was not even an entanglement call — it is a distinct feature
that happens to share the word "telemetry" in its comments. `no-mistakes stats`
therefore keeps its full upstream capability. Comment wording that implied a
remote path ("never sent to remote telemetry") was corrected to say the build
sends nothing anywhere.

**No columns were dropped.** The upstream telemetry-era columns
(`agent_invocations.model_provider`, `fallback_reason`, the token/tool
histogram, etc.) all back `stats --agents`, so there was nothing telemetry-only
to remove and no risky migration surgery to do.

### 2. Prompt composition (constraint 4) — composed, not chosen

Upstream refactored `document.go`'s inline prompt into
`DocumentStep.buildPrompt`. The fork's change to that file was a one-token
addition to the history section. Resolution: keep upstream's `buildPrompt`
refactor and add the fork's clause inside it.

- `review.go`: `executionContextPromptSection() + roundHistoryPromptSection +
  userIntentPromptSection + intentConformanceReviewClause(sctx) +
  pipelineDeliveryPhaseClause() + stepInstructionsPromptSection(sctx)`
- `document.go` (`buildPrompt`): `executionContextPromptSection() +
  roundHistoryPromptSection + userIntentPromptSection +
  stepInstructionsPromptSection(sctx)`

The fork's clause is appended last so upstream's intent-conformance and
delivery-phase clauses keep their adjacency.

### 3. Schema reconciliation (constraint 5)

`runs` carries **both** column sets. `runColumns` was merged by hand with
`profile` inserted immediately before `created_at` to match `scanRun`'s field
order:

```
… , error, awaiting_agent_since, COALESCE(parked_ms, 0),
intent, intent_source, intent_session_id, intent_score, profile,
created_at, updated_at
```

The `CREATE TABLE` body carries `parked_ms` and `profile`; the migration list
carries every upstream `ALTER TABLE` (push/custody, `parked_ms`, step-activity,
agent-invocation) plus the fork's `runs.profile` and `repos.local_profile`.
`db_test.go`'s legacy-migration test was split back into two independent tests
(`TestOpenMigratesStepActivityColumns`, `TestOpenMigratesReposLocalProfileColumn`)
— git had interleaved them into one broken function.

`InsertStepResult` keeps the fork's 3-arg `(runID, stepName, stepOrder)`
signature (required for arbitrary `steps:` lists) and gained upstream's
`stepResultColumns` const. ~40 upstream test call sites were updated to pass
`0` (which falls back to the canonical ordinal).

### 4. Two real bugs the merge would have introduced silently

Both were caught by the fork's own tests, which is the argument for running the
suites before trusting a clean build:

- **`profile:` was being silently dropped.** Upstream added a custom
  `RepoConfig.UnmarshalYAML` (for `agentList`) after the fork branched. The
  fork's `Profile` field auto-merged into the struct but not into upstream's
  private `repoConfigRaw`, so `profile: team-ios` parsed to `""` — the shared
  gate profile would have been silently ignored. `TestProfileShow` caught it.
- **Strict parsing (`KnownFields`) was defeated.** A custom `UnmarshalYAML`
  bypasses the outer decoder's `KnownFields(true)`, and `yaml.Node.Decode` has
  no strict mode, so a typo'd `comands:` parsed clean again. Fixed by
  re-encoding the node and decoding it through a strict decoder — which
  restores nested strictness too, not just top-level.
  `TestLoadRepo_UnknownKeyFails` caught it.

### 5. Recovered-run step rebuild (`prepareRecoveredRun`)

Upstream's parked-gate recovery calls `m.steps()`; the fork's `StepFactory` is
`func(*config.Config) ([]pipeline.Step, error)` because the pipeline is
config-driven. Reordered so the merged config is loaded *before* the step list
is rebuilt, and a bad `steps:`/profile now fails recovery rather than
validating against the wrong pipeline.

### 6. `branchsync` equivalence detection (the one new commit)

Four `internal/branchsync` tests and one `internal/cli` test fail on
**unmodified upstream v1.41.0** on any host with git < 2.40 (verified in a
throwaway v1.41.0 worktree). Cause: `git merge-tree --write-tree --merge-base`
landed in git 2.40, and Apple Git 2.39.5 — the macOS default, and the only git
on this host — rejects `--merge-base`.

The *product* behavior is already correct and fail-safe: the probe errors, the
branch stays `blocked_diverged`, and `sync` refuses to touch refs. Only the
tests asserted the convenience path unconditionally. Added
`branchsync.EquivalenceDetectionSupported` as an explicit capability probe,
documented the version requirement at the call site, and skipped the affected
tests through it. This is inherited upstream breakage surfaced by the rebase,
not a rebase regression, and it is fixed rather than merely reported.

### 7. Things upstream changed that the fork now inherits

Worth flagging because they are behavior changes the fork did not ask for, all
kept as upstream shipped them:

- `internal/branchsync` (~6k lines), `no-mistakes sync`, `axi sync`, the TUI
  sync panel, and 12 `runs` custody columns — the scout report's Wave 6, which
  arrives for free in a rebase.
- `daemon run` now **requires explicit argv**; the fork's `NM_DAEMON=1`
  fallback in `cmd/no-mistakes/main.go` is gone (upstream `b876724`).
- Single-daemon ownership per `NM_HOME` (`daemon.lock`), the
  destructive-lifecycle guard, credential redaction in stored URLs,
  `disable_project_settings`, ordered fallback agent lists, GHES detection,
  and `internal/winproc`.
- The fork's `TestForkRouting` e2e failure (red on the old fork baseline per
  the scout report §9) is resolved here: `8d25a58`'s
  `NM_TEST_SKIP_LOGIN_SHELL_ENV=1` pin replayed cleanly onto upstream's
  reworked harness.

## e2e

`make e2e` runs against upstream's reworked harness (`internal/e2edaemon`,
`scripts/e2e.sh`) with the fork's `NM_TEST_SKIP_LOGIN_SHELL_ENV=1` daemon-env
pin applied on top. **Green, exit 0:**

```
ok  github.com/kunchenguid/no-mistakes/internal/e2e            152.061s
ok  github.com/kunchenguid/no-mistakes/internal/pipeline/steps  49.866s
```

`TestForkRouting` — red on the old fork baseline (scout report §9) — passes
here, as do the fork's own e2e tests (`TestSkillStepGateFlow`,
`TestProfileSpliceComposition`, `TestSharedProfileAcrossTwoRepos`).

## Landing

This branch is **not** the pipeline's output and `main` was not touched.
Landing is a captain-approved reset of `main` to this branch (a history
rewrite), done separately after review.
