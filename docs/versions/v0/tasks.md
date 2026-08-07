# vincent v1 — Task Breakdown & Progress

Derived from [spec.md](./spec.md). This file is the single source of truth for
implementation progress; the executing agent updates it in place as work proceeds.

## How to update this file (rules for the agent)

- Statuses: `- [ ]` not started · `- [~]` in progress · `- [x]` done (append `✓ YYYY-MM-DD`) · `- [!]` blocked (append one-line reason).
- Mark a task `[~]` **before** starting it; mark `[x]` only when every done-criterion is verified (tests actually run, not assumed).
- Update the dashboard table in the same edit as any status change.
- Order within a phase implies dependency unless a `Depends:` tag says otherwise. Don't start a task whose dependencies aren't `[x]`.
- Never delete a task. Descoped: strike through (`~~T1.4~~`) with a dated note. Newly discovered work: append to the phase with the next free ID.
- No time estimates are tracked — only status.

## Dashboard

| Phase | Scope | Done | Status |
|---|---|---|---|
| 0 — Scaffolding | 4 tasks | 4/4 | ✅ done |
| 1 — Spine (M1) | 9 tasks | 4/9 | 🔨 in progress |
| 2 — Workflow engine (M2) | 12 tasks | 0/12 | ⬜ not started |
| 3 — TUI (M3) | 8 tasks | 0/8 | ⬜ not started |
| 4 — Polish (M4) | 6 tasks | 0/6 | ⬜ not started |
| **Total** | | **8/39** | |

---

## Phase 0 — Scaffolding

Repo foundation the spec assumes but doesn't itemize. Module path: `github.com/lezli01/vincent`.

**Phase 0 decisions (grill session, 2026-08-06):**

- *Go version:* latest stable minor, minor-only directive (`go 1.26`); CI reads `go-version-file: go.mod`; bumped manually each Go release. No multi-version matrix.
- *CLI framework:* `spf13/cobra` for the §12.1 command tree.
- *Task runner:* Mage, zero-install. Both `go run mage.go <target>` (committed bootstrap `mage.go`) and `go tool mage <target>` work; mage is pinned via a `go.mod` `tool` directive so tidy can never prune it. Wherever T0.2/T0.4 say `make X`, read `go run mage.go X`.
- *Lint:* golangci-lint v2 pinned as a `go.mod` `tool` directive — `go tool golangci-lint run` is the single invocation path locally and in CI (no golangci-lint-action). Curated strict set (v2 standard defaults + errorlint, gocritic, revive, copyloopvar, intrange, misspell, unconvert, unparam, nolintlint, sqlclosecheck) + `gofumpt` formatter.
- *CI:* runs the same mage targets as local; `-race` enabled in CI only (local Windows may lack a C toolchain); docs-only `go.mod` gating removed; `checkout`/`setup-go` bumped to v7 in-branch (supersedes the dependabot PRs; the lint-action bump becomes moot); PR runs cancel superseded runs.
- *Skeleton:* placeholder packages carry a `doc.go` stating their future role; `internal/cli` and `internal/version` are real from T0.1.
- *Repo hygiene:* `.gitattributes` forces LF on source files (scope addition to T0.2). `vincent version` prints one line (`vincent version X (commit Y, built Z)`) with `debug.ReadBuildInfo` fallback so plain `go build` binaries never print empty fields.
- *Local toolchain:* Go installed via winget (1.26.5 at time of writing); no other global tools required — lint and mage ride the `go.mod` tool directives.

- [x] **T0.1 — Go module & repo layout.** `go mod init github.com/lezli01/vincent`; directory skeleton: `cmd/vincent/`, `internal/{config,store,daemon,api,workflow,agent,worktree,scheduler,taskrun,tui,cli}/`; root command routing (`vincent`, `vincent daemon`, `vincent version` stubs); build info injected via ldflags. (§12.1) ✓ 2026-08-06
  *Done when:* `go build ./...` succeeds; `vincent version` prints version/commit/date. *(Verified: `vincent version 9ad1de4-dirty (commit 9ad1de4, built 2026-08-06)`; also added `internal/version` with a unit test.)*
- [x] **T0.2 — Dev tooling.** `.gitignore`, `.editorconfig`, `golangci-lint` config, Makefile (or magefile) with `build` / `test` / `lint` targets. ✓ 2026-08-06
  *Done when:* `make lint test build` passes clean locally. *(Verified as `go run mage.go lint test build` — 0 lint issues, tests pass, binary built. Scope addition: `.gitattributes` LF normalization.)*
- [x] **T0.3 — CI.** GitHub Actions: build + test + lint matrix on ubuntu-latest, macos-latest, windows-latest. ✓ 2026-08-06
  *Done when:* workflow green on all three OSes for a PR touching Go code. *(Verified: PR #6 matrix green on all three OSes. CI runs the same mage targets as local, with `-race`.)*
- [x] **T0.4 — Phase gate.** Fresh clone → `make build` → working `vincent version` on all 3 OSes (via CI matrix). ✓ 2026-08-06
  *Done when:* gate verified; dashboard updated. *(Verified on PR #6: each matrix job does a fresh checkout, `go run mage.go build`, and smoke-tests `vincent version` output.)*

## Phase 1 — Spine (M1)

Milestone acceptance (§19 M1): via `curl` alone — register a repo, create a one-step agent task, watch it finish, see the branch and diff.

**Phase 1 decisions (grill session, 2026-08-07):**

- *Delivery:* 4 grouped PRs along dependency seams — (1) T1.1+T1.2 config+store, (2) T1.3+T1.4 daemon+API, (3) T1.5+T1.6 projects+worktrees, (4) T1.7–T1.9 adapter+run+gate. Each independently green in CI; tasks.md updated per PR.
- *Git access:* git CLI only (no go-git) — `exec` via one internal `gitx` helper (run/capture/error-mapping). git version logged at daemon start; warn (not fail) below 2.31.
- *Platform dirs:* `os.UserConfigDir()` for config; hand-rolled ~30-line data-dir resolver (`XDG_DATA_HOME`/`~/.local/share`, `%LOCALAPPDATA%`, `~/Library/Application Support/vincent/data`). `VINCENT_CONFIG_DIR` / `VINCENT_DATA_DIR` env overrides (used by tests and the T1.9 gate). No xdg dependency.
- *YAML:* `goccy/go-yaml` everywhere (strict decode now; its line/column-annotated errors are what T2.1 needs later).
- *Config bootstrap/reload:* first start writes a fully-commented `config.yaml` with §12.3 defaults. Invalid config at startup = fatal; invalid on hot-reload = keep last-good + log rejection; `listen:` changes ignored until restart (warn). fsnotify watches the config *directory* (survives rename-on-save), ~100 ms debounce.
- *SQLite:* `modernc.org/sqlite` via `database/sql`; WAL + `busy_timeout` via DSN pragmas; `MaxOpenConns(1)` (single connection — zero SQLITE_BUSY surface; revisit only if profiling demands).
- *Migrations:* hand-rolled embedded runner — `embed.FS` of `NNNN_name.sql`, applied in-transaction at startup into the spec §14 `schema_migrations` table. Up-only; no library.
- *Store:* hand-written repository-style `database/sql` code (no sqlc/ORM); queries as consts beside their methods; covered by CRUD round-trip tests.
- *Daemon lifecycle:* detached start = self-exec `vincent daemon` with platform `SysProcAttr` (DETACHED_PROCESS / setsid). Single-instance via `gofrs/flock` on `daemon.lock` (auto-releases on death). **Spec addition:** authenticated `POST /v1/daemon/stop` endpoint — `daemon stop` calls it, waits for exit, `--force` kills as fallback; same graceful path (§12.4) on all 3 OSes. `token` + `daemon.json` written atomically (temp+rename); `daemon.json` removed on graceful shutdown.
- *Token perms:* 0600 enforced + tested on POSIX; Windows relies on `%LOCALAPPDATA%` per-user ACL inheritance (documented; no DACL code in v1) — test asserts existence/location there.
- *Logging:* `slog` TextHandler everywhere (file + foreground stderr); rotation via `natefinch/lumberjack`.
- *API:* stdlib `net/http` ServeMux with Go 1.22+ method/wildcard patterns (no chi); middleware chain auth → log → recover; constant-time token compare (`crypto/subtle`); `/v1/health` exempt; CORS disabled; stable `snake_case` error codes.
- *default_branch detection:* `origin/HEAD` → local `main` → local `master` → current HEAD branch → reject registration with clear error (detached/unborn HEAD requires explicit `default_branch`).
- *Worktree slug edge:* title sanitizing to empty → branch `vincent/{id}` (no trailing dash); otherwise §5.3 rules.
- *Agent CLI location:* **spec addition** to §12.3 — `agents: { claude: { path }, codex: { path } }`; empty = `exec.LookPath`. `Detect()` reports the resolved path in `/v1/info`. Tests/gate substitute the fake via this knob (no PATH surgery).
- *Fake agent:* committed `cmd/fakeagent` main package — scenario-driven (success / error-event / nonzero-exit / hang-until-killed / big-usage) via env/flag, emits Claude-style stream-json, prompt from stdin. Always compiled by `./...`; excluded from release packaging in T4.5; grows a codex dialect for T2.9.
- *Transcripts:* agent stream-json written verbatim (lossless/replayable); vincent's own annotations as namespaced `{"type":"vincent.…"}` JSONL lines. Parsing is tolerant — unknown event types transcripted but not normalized; exact CLI flags pinned at implementation time (§9.2).
- *M1 task shape:* `workflow` param optional (default `"adhoc"`); daemon synthesizes a real one-step workflow YAML (agent step; prompt = fixed template embedding title+description) stored as `workflow_snapshot` — DB rows shape-final; T2.3 swaps synthesis for registry lookup.
- *M1 admission:* interim FIFO (`created_at`) runner bounded by global `max_parallel_tasks`; priority ignored until T2.5 replaces it wholesale. Real agents cost money — no unbounded spawn even in M1.
- *Diff endpoint:* `git -C {worktree} diff <merge-base(base, HEAD)>` — committed + staged + unstaged tracked changes; untracked files excluded (documented limitation).
- *M1 gate:* `scripts/m1-gate.sh` (bash + curl + jq) run in CI on all 3 OSes (Git Bash on Windows): build vincent+fakeagent → temp dirs via env overrides → start daemon → register temp repo → create task → poll to done → assert branch/diff/transcript → stop daemon.

**Agent/model/effort selection decisions (grill session, 2026-08-07; spec §8.6, §9.6):**

- *Discovery:* adapter `Options()` probes the installed CLI ad hoc — claude: `--help` parse yields the `--effort` enum + model aliases (verified against 2.1.224); codex: `--help` enumerates nothing (only `-c key=value`, verified against 0.142.5) — merged with a curated per-adapter catalog; every option provenance-tagged `cli`/`curated`; free text always accepted.
- *Effort scale:* adapter-native strings, no normalized vincent scale (claude `low…max`, codex `minimal…high`). Known-invalid (agent, model/effort) pairs are validation errors; values unknown to the catalog warn and pass through — the CLI is the final authority at run time.
- *Selection surfaces & precedence:* workflow `defaults` + per-step fields (existing) + new optional task-level override on `POST /v1/tasks`; resolution `step > task override > defaults > adapter default` — explicit step pins always win (cross-agent workflows stay intact).
- *Inheritance:* agent-scoped — `model`/`effort` never leak across an agent switch; they reset to the new adapter's default unless set alongside.
- *Freshness:* `GET /v1/agents` cache keyed by binary identity (resolved path + mtime + version) — never stale by construction since help output is a pure function of the binary; `?refresh=true` re-probes.
- *Phasing:* interfaces shaped final in phase 1 (T1.7: `RunSpec.Effort`, `--model`/`--effort`, `Options()` probe; T1.8: override params + `*_override`/StepRun columns); catalog endpoint + resolution/validation engine as new T2.11; codex probe in T2.9; TUI pickers in T3.5. M1 gate unchanged — the override is optional.

**Agent input-request decisions (grill session, 2026-08-07; spec §7.4):**

- *Trigger:* structured, machine-readable requests only — `question` (AskUserQuestion-style, options/multi-select/free text) and `permission` (restricted-mode prompt); nothing inferred from output text.
- *State:* new `awaiting_input`, **holds** its concurrency slot (live agent process idle mid-step); `answer` human action returns it to `running` in place; `cancel` valid from it.
- *Clocks:* step `timeout` pauses while waiting; new `input_timeout` (global default 24h in config `defaults`, overridable in workflow `defaults`/per step); expiry kills the process and fails the attempt (`input_timeout`) under normal retry/blocked policy.
- *API:* normalized `InputRequest` (kind, questions/options/multiSelect, adapter-native payload in `raw`) stored as task `pending_input`; durable `task.awaiting_input` event (kind + summary; full request via task GET); `POST /v1/tasks/{id}/answer` (409 outside `awaiting_input`).
- *Opt-out:* `on_input: wait | deny` (workflow `defaults` + per step, default `wait`); `deny` = adapter auto-answers ("no user available — best judgment" / permission denied) so unattended workflows never stall.
- *Capability:* per-adapter `supports_input` — claude yes via bidirectional stream-json (`--input-format stream-json`, stdin kept open; control-message wire format is undocumented → pinned per detected CLI version, fixture-tested, degrades to `supports_input: false` if unverifiable); codex `exec` is strictly non-interactive → never enters `awaiting_input`. Unparseable control request = attempt fails `input_protocol_error`, never hangs.
- *Alerts:* TUI-level only (board pins needs-attention tasks + badge, header attention count, terminal bell on `task.awaiting_input`); OS desktop notifications stay post-v1.
- *Recovery/metrics:* crash while `awaiting_input` = `interrupted` (request discarded, fresh re-run may re-ask); request+answer transcripted as `vincent.*` lines; wait time in new `input_wait_ms` StepRun column, excluded from durations.
- *Phasing:* interface shape final in T1.7 (`InputRequest`/`InputResponse` types, `Respond()` on RunHandle, `RunSpec.OnInput`, `supports_input` in Detect — claude may report `false` until the engine lands); full engine as new T2.12 (incl. `pending_input_json`/`input_wait_ms` migration); TUI in T3.2/T3.4; fake agent grows an ask-question scenario.

- [x] **T1.1 — Config & platform dirs.** Platform-native config/data dir resolution (§12.2); `config.yaml` load with defaults + strict validation (§12.3); hot-reload via fsnotify. ✓ 2026-08-07
  *Done when:* unit tests cover defaults, overrides, invalid config rejection, and live reload. *(Verified: 28 unit tests — defaults, partial/full overrides, 13 invalid-config rejections, live reload incl. invalid-edit keep-last-good, per-OS data-dir table, generated default file loads back to `Default()`.)*
- [x] **T1.2 — SQLite store & migrations.** Pure-Go driver; WAL + busy_timeout; embedded migrations applying schema §14; typed store layer (CRUD + scheduler query) for projects, tasks, step_runs, events. ✓ 2026-08-07
  *Done when:* migration idempotence test + CRUD round-trip tests pass on all 3 OSes in CI. *(Verified: PR #7 matrix green on ubuntu/macos/windows — idempotence via re-migrate + reopen, pragma checks, CRUD round-trips incl. nullable columns, scheduler ordering, FK enforcement.)*
**T1.3/T1.4 decisions (grill session, 2026-08-07):**

- *Start handshake:* `daemon start` first probes for a healthy daemon (already running = success), then spawns the detached child and polls `daemon.json` + `GET /v1/health` for ~10 s; on failure it prints the tail of `daemon.log` and exits non-zero. No pipe IPC.
- *Status truth:* try-acquire `daemon.lock` — acquirable means no daemon (a leftover `daemon.json` is reported as stale); held means alive, then `/v1/health` decides responsiveness. Exit codes: 0 healthy · 1 not running · 2 alive-but-unresponsive. Human-readable output; `--json` deferred to T4.2.
- *Idempotency:* desired-state semantics — `start` on a running daemon and `stop` with none running both print what happened and exit 0.
- *Stop flow:* `daemon stop` POSTs `/v1/daemon/stop` (202; response completes before shutdown begins), then waits for the lock to become acquirable (~20 s, longer than the daemon's own kill grace); `--force` falls back to killing the recorded PID.
- *Foreground = daemon:* `vincent daemon` is the real daemon in both modes — always takes the lock, writes token/`daemon.json`, always logs to the rotating file; foreground additionally mirrors logs to stderr. Log level rides a `slog.LevelVar` so `log_level` applies on hot-reload; a `listen` change is warned and the effective value kept.
- *Middleware order:* recover outermost → request log → auth → handler (panics always logged and enveloped as 500; unauthorized requests still logged). `/v1/health` is auth-exempt and logged at debug so poll loops don't spam the log.
- *Error vocabulary (initial):* `unauthorized`, `not_found`, `method_not_allowed`, `invalid_json`, `validation_failed`, `invalid_state`, `internal`; ServeMux fallback 404/405 wrapped so every error body is the §13.1 envelope (405 carries `Allow`).
- *`/v1/info` (initial):* version/commit/built, pid, `started_at`, uptime, effective listen address, `max_parallel_tasks`; agent availability joins in T1.7 (additive).
- *`/v1/config`:* effective config as snake_case JSON mirroring `config.yaml`; durations rendered as Go duration strings.
- *Token:* 32 bytes `crypto/rand` as hex, written atomically (temp+rename) 0600 at first start, reused unchanged on later starts.

- [x] **T1.3 — Daemon lifecycle.** `vincent daemon` (foreground); `daemon start/stop/status` (detached start, graceful stop, status via `daemon.json`); single-instance lock file; bearer token generated 0600 at first start; structured logging with rotation. (§12.1, §12.2, §13.1) ✓ 2026-08-07
  *Done when:* start/status/stop cycle works on all 3 OSes; second daemon refuses to start; token/`daemon.json` created with correct permissions. *(Verified: PR #10 matrix green on ubuntu/macos/windows — binary e2e drives the full start/status/stop cycle incl. idempotency and exit codes; in-process tests cover second-instance refusal, API stop, context cancel, fatal invalid config; token 0600 asserted on POSIX, existence on Windows.)*
- [x] **T1.4 — API foundation.** `net/http` server bound `127.0.0.1`; bearer-auth middleware (health exempt); JSON error envelope with stable codes; `GET /v1/health`, `/v1/info` (agent availability wired later), `/v1/config`. (§13.1–13.2) ✓ 2026-08-07
  *Done when:* auth rejection, health-without-auth, and error-shape tests pass. *(Verified: PR #10 — httptest suite covers auth rejection (missing/wrong token), health without auth, 404/405/401/500 envelope shapes incl. Allow header, panic recovery, and the stop endpoint with and without auth.)*
**T1.5/T1.6 decisions (grill session, 2026-08-07):**

- *Repo shape:* strict toplevel only — registered path must equal `git rev-parse --show-toplevel`; bare repos and linked worktrees rejected; a subdirectory is rejected with an error naming the real toplevel (explicit beats magic).
- *Path identity:* absolute path required (client cwd ≠ daemon cwd); stored cleaned as-given; duplicates rejected at the handler by identity comparison (`os.SameFile` — sees through symlinks and Windows case aliases) against all existing projects, error names the existing project. No DB unique on `path` (a string constraint can't see aliases). Default-name collision on `name` UNIQUE → `validation_failed` suggesting an explicit name.
- *default_branch:* local branches only — every detection candidate and explicit value must resolve under `refs/heads/`; `origin/HEAD`'s short name counts only if a local branch of that name exists, else fall through (`main` → `master` → current HEAD branch → reject). Invariant: a stored `default_branch` always resolves locally at write time.
- *PATCH:* pointer-field JSON — absent = unchanged, explicit `null` clears (`default_workflow`, `max_parallel_tasks`); `null` on required fields is `validation_failed`. Path repoint re-runs full registration validation + duplicate check, and the effective `default_branch` (stored, or supplied in the same PATCH) must resolve in the new repo.
- *DELETE:* hard delete with explicit cascade in one tx (events → step_runs → tasks → project; FKs stay strict). Guard without `force`: 409 `invalid_state` with the non-archived count. `?force`: 409 if any task `running`; otherwise archives each non-archived task force-removing its worktree (force *is* the dirty confirmation), then cascades. Branches never deleted. Transcript-file cleanup joins in T1.8.
- *Dirty rule:* untracked files count — dirty = `git status --porcelain` non-empty, matching `git worktree remove`'s own refusal; force maps to `--force`. Remove is idempotent: worktree dir already gone → prune + succeed.
- *Create residue:* prune-then-fail — `git worktree prune` before `worktree add`; if the target dir still exists non-empty, typed `worktree_path_occupied` error. Nothing deleted without an explicit force.
- *Error surfacing:* one vocabulary — `worktree.Error.Reason` snake_case strings (`base_branch_missing`, `branch_exists`, `worktree_dirty`, `worktree_missing`, `worktree_path_occupied`, `project_path_missing`, `git_error`) double as future task `block_reason` values; HTTP layer reuses the existing code set (400 `validation_failed`, 409 `invalid_state`, 404 `not_found`) — no new codes.
- *gitx:* `internal/gitx` run/capture helper with typed exec errors; context-scoped timeouts (30 s queries, 5 min worktree ops); git version probed and logged at daemon start, warn below 2.31 (phase 1 decision, lands here as first git use).

- [~] **T1.5 — Projects API.** `GET/POST /v1/projects`, `GET/PATCH/DELETE /v1/projects/{id}`; registration validation (path exists, is git repo); `default_branch` auto-detection with fallback (§5.1); delete guarded by non-archived tasks.
  *Done when:* endpoint tests against temp git repos cover happy path + each validation failure.
- [~] **T1.6 — Worktree manager.** Create worktree + branch `vincent/{id}-{slug}` from base (§10); slug rules (§5.3); remove + prune on archive with dirty detection and `force`; error taxonomy for missing base branch, pre-existing branch, missing project path (§18).
  *Done when:* integration tests with temp repos cover create/remove/dirty/each error case.
- [ ] **T1.7 — AgentAdapter + Claude Code adapter.** Interface per §9.1 (incl. `Options()`, `RunSpec.Model`/`Effort`, and the §7.4 input-request surface shaped final: `InputRequest`/`InputResponse` types, `Respond()` on RunHandle, `RunSpec.OnInput`, `supports_input` in `Detect()` — claude may report `false` until T2.12 lands the engine); Claude implementation: prompt via stdin, `stream-json` event parsing, full-auto flag, `--model`/`--effort` passthrough, usage/cost extraction, kill support; `Options()` help-probe (effort enum + model aliases parsed from `--help`, merged with the curated catalog, §9.6); `Detect()` wired into `/v1/info`. Include a **fake agent** test binary emitting scripted stream-json so CI never calls a real API.
  *Done when:* adapter unit tests against the fake binary cover success, error event, nonzero exit, kill, and usage parsing; options probe parses a captured `--help` fixture and falls back to curated-only on an unparseable one; the input-request interface compiles and is stubbed (behavior exercised in T2.12).
- [ ] **T1.8 — Minimal task run.** `POST /v1/tasks` (validation, snapshot placeholder, immediate queue, optional `agent`/`model`/`effort` task-level override persisted to the `*_override` columns via a new migration) with a hardcoded single-agent-step execution path: worktree create → adapter run (agent/model/effort resolved per §8.6, recorded on the StepRun) → StepRun + transcript file → `done`/`blocked`; `GET /v1/tasks[,/{id}]`, `/steps`, `/steps/{run_id}/transcript`, `/diff` (§13.2). Depends: T1.2, T1.5–T1.7.
  *Done when:* end-to-end test with fake agent produces correct DB rows, transcript file, diff output; an override round-trips into StepRun `model`/`effort`.
- [ ] **T1.9 — Phase gate (M1 acceptance).** Scripted curl flow per §19 M1 against the fake agent in CI; run once manually with real `claude` on Windows and record the result here.
  *Done when:* script committed and green in CI; manual run noted.

## Phase 2 — Workflow engine (M2)

Milestone acceptance (§19 M2): a multi-step workflow (gate + command publish) runs unattended to the gate; an agent question round-trips awaiting_input → answer → resume; `kill -9` mid-step recovers correctly; caps honored under load.

- [ ] **T2.1 — Workflow registry.** Strict YAML decode with unknown-key rejection (§8.1–8.2); validation with file/line errors; global + project scopes with shadowing (§5.2); fsnotify reload keeping valid entries when one file is broken; `GET /v1/workflows`, `POST /v1/workflows/validate`.
  *Done when:* table-driven validation tests + scope-shadowing and broken-file-isolation tests pass.
- [ ] **T2.2 — Template engine.** Context assembly per §8.4 (`.Task`, `.Project`, `.Workflow`, `.Step`, `.Steps`, `.Worktree`, `.LastFailure`); render-fails-before-spawn; command/check env vars (§8.5); retry failure block appending (§8.4).
  *Done when:* unit tests cover every context variable, missing-field failure, and the retry block.
- [ ] **T2.3 — Task state machine.** Full FSM §6 with persisted transitions (incl. `awaiting_input`, §7.4), guarded invalid transitions (409), workflow snapshot at creation replacing T1.8's placeholder, `block_reason`, timestamps.
  *Done when:* exhaustive transition-table test (every state × every action, incl. `answer`) passes.
- [ ] **T2.4 — Step executors.** Agent step (any adapter; agent/model/effort resolved per §8.6 via T2.11), command step (platform shell selection + `shell:` pin, §8.3), manual gate, `check` execution, per-step timeouts with kill, retry loop honoring `max_retries`, transcript capture for all output. (§7)
  *Done when:* integration tests cover each step type, check pass/fail, timeout kill, retry-then-blocked, and Windows + POSIX shells in CI.
- [ ] **T2.5 — Scheduler.** Global + per-project caps counting only `running`; admission by priority DESC, created_at ASC; re-evaluation on every state change and config reload (§11).
  *Done when:* concurrency tests prove both caps and ordering under simultaneous task load.
- [ ] **T2.6 — Human actions API.** `cancel` (process kill incl. Windows tree-kill), `pause` (step-boundary), `resume`, `retry` with prompt/run overrides recorded, `skip`, `approve`, `reject`, `archive` (+dirty `force`), `PATCH` priority (§6, §13.2).
  *Done when:* each action tested from every valid state plus one invalid-state 409 each.
- [ ] **T2.7 — Events & SSE.** Durable state events in `events` table; `GET /v1/events` with `Last-Event-ID` resume and type/project filters; per-task stream with live agent/command output, coalesced ~10 Hz (§13.3); transcript-then-follow catch-up documented behavior.
  *Done when:* reconnect-resume test misses zero state events; output streaming test shows coalescing.
- [ ] **T2.8 — Crash recovery.** PID + start-time journaling before spawn; startup orphan detection/kill; `interrupted` runs re-queued without consuming retries; graceful shutdown (admission stop, 15 s term-then-kill) (§12.4).
  *Done when:* test kills the daemon hard mid-step and asserts full recovery; graceful path tested.
- [ ] **T2.9 — Codex adapter.** Per §9.3, behind the same interface; model/effort via `-c model=` / `-c model_reasoning_effort=`; `Options()` serves the curated catalog (the CLI enumerates nothing); nil cost handling; fake-binary tests mirroring T1.7. Depends: T1.7 (interface only — can run parallel to T2.1+).
  *Done when:* same test matrix as the Claude adapter passes.
- [ ] **T2.11 — Agent option catalog & selection resolution.** `GET /v1/agents` (§9.6): per-adapter availability + provenance-tagged model/effort options; cache keyed by binary identity (resolved path + mtime + version) with `?refresh=true`; probe-failure fallback to curated with `probe_error`. §8.6 resolution engine (step > task > defaults > adapter default, agent-scoped inheritance) consumed by T2.4; catalog validation wired into T2.1 workflow checks and `POST /v1/tasks` (§8.2: known-invalid = error, unknown = warning). Depends: T1.7, T2.9.
  *Done when:* resolution table-tests cover every precedence and inheritance case incl. agent-switch resets; cache test proves invalidation on binary swap and a hit otherwise; known-invalid pair rejected while a free-text model passes with a warning.
- [ ] **T2.12 — Interactive input requests (§7.4).** Claude adapter bidirectional stream-json input (stdin kept open, `--input-format stream-json`; question/permission control messages normalized to `InputRequest`; `Respond()` translation back; wire protocol pinned against the detected CLI version with fixtures, degrading to `supports_input: false` when unverifiable); `awaiting_input` FSM wiring (slot retained, step clock paused, `input_wait_ms` accounting); `input_timeout` enforcement; `on_input: wait|deny` resolution (deny = adapter auto-answer, no state change); migration adding `pending_input_json` + `input_wait_ms`; `POST /v1/tasks/{id}/answer`; durable `task.awaiting_input` event; request/answer transcript lines; fake-agent ask-question scenario (blocks until answered on stdin). Depends: T1.7, T2.3, T2.4, T2.6, T2.7.
  *Done when:* fake-agent test drives request → `awaiting_input` (slot held, clock paused) → answer → resume → done; `input_timeout` expiry fails the attempt with the right reason; deny mode auto-answers with no state change; unparseable control request fails `input_protocol_error`; answer in wrong state returns 409.
- [ ] **T2.10 — Phase gate (M2 acceptance).** Automated: multi-step workflow (agent → command → gate → command publish to a local bare remote) with fake agents, incl. an input-request round-trip (§7.4); kill -9 recovery; cap stress test. Manual: one real-workflow run with real `claude`, result recorded here.
  *Done when:* acceptance script green in CI; manual run noted.

## Phase 3 — TUI (M3)

Milestone acceptance (§19 M3): the full loop — register project, author workflow, run 3 parallel tasks, approve a gate, archive — without leaving the TUI.

- [ ] **T3.1 — TUI foundation.** Bubble Tea shell; API client (token + `daemon.json` discovery); daemon auto-start when unreachable (§12.1); `/v1/events` subscription driving re-render; view routing, global keys, help overlay (§15).
  *Done when:* TUI connects, auto-starts a stopped daemon, and reflects an externally-made state change live.
- [ ] **T3.2 — Board view.** Task table (id, project, title, state color-coded, step k/n, elapsed, cost), filters, scheduler-order sort; header with daemon status, agent availability, running/cap counts, needs-attention count; needs-attention tasks (`awaiting_input`/`awaiting_gate`/`blocked`) pinned on top with a distinct badge; terminal bell on `task.awaiting_input` (§7.4, §15).
  *Done when:* board renders live updates from SSE without polling; filters work; an awaiting-input fake-agent task visibly alerts (pin + badge + bell).
- [ ] **T3.3 — Task detail: timeline & tail.** Step timeline with attempts/durations/tokens/cost; live output tail with follow mode; scrollback into past transcripts via ranged transcript endpoint.
  *Done when:* a running fake-agent task shows live tail; historical attempts browsable.
- [ ] **T3.4 — Task detail: diff & actions.** Diff tab (syntax highlighted); action bar offering exactly the state-valid actions; gate approve/reject with rendered instructions; answer form for `awaiting_input` (question/permission rendered with options, multi-select, free-text entry; submits `POST /answer` and the run resumes in place, §7.4); edit+retry through `$EDITOR` (§6, §15).
  *Done when:* every action reachable and functional from the detail view; invalid actions never shown; a fake-agent question is answerable from the detail view and the run resumes live.
- [ ] **T3.5 — New-task flow.** Project picker → workflow picker (description, step list, unavailable-agent flags) → title → description (inline or `$EDITOR`) → fields → base branch → priority → optional agent/model/effort pickers fed by `GET /v1/agents` (provenance-tagged options, free-text entry, note that step pins are not overridden) (§15, §8.6).
  *Done when:* task created through the flow runs end-to-end; unavailable agent visibly flagged; override pickers populate from a live probe and accept free text.
- [ ] **T3.6 — Projects & Workflows views.** Project list/add/edit/remove with per-project cap; workflow registry with scope badges + validation status, `e` opens `$EDITOR`, live reload visible (§15).
  *Done when:* editing a workflow file externally or via `e` updates the view without restart.
- [ ] **T3.7 — Daemon view & first-run warning.** Version/uptime/config/adapters/log tail; one-time full-auto risk notice on first run (§16); quit reminder with running-task count.
  *Done when:* warning shows exactly once (persisted flag); view reflects live daemon info.
- [ ] **T3.8 — Phase gate (M3 acceptance).** Manual scripted walkthrough of the §19 M3 loop on Windows + one POSIX OS, using fake agents for parallelism and real `claude` for one task; results recorded here.
  *Done when:* walkthrough passes on both; notes committed.

## Phase 4 — Polish (M4)

Milestone acceptance (§19 M4): fresh machine → first completed task in under 10 minutes on each OS.

- [ ] **T4.1 — Service install.** `vincent service install/uninstall`: Windows Service, launchd agent, systemd user unit (§12.1).
  *Done when:* install → reboot-survival → uninstall verified on each OS (manual, recorded here).
- [ ] **T4.2 — CLI subcommands.** `project add/ls`, `task add/ls/show/cancel`, `workflow ls/validate` as thin API clients with table/JSON output (§12.1).
  *Done when:* CLI integration tests against a live daemon pass in CI.
- [ ] **T4.3 — Retention & limits.** Transcript pruning for archived tasks past `transcript_retention_days`; per-run transcript size cap failing the step past the limit; daemon log rotation verified (§17, §18).
  *Done when:* pruning and cap behavior covered by tests with shrunk thresholds.
- [ ] **T4.4 — Docs & examples.** README (install, quickstart, prominent full-auto risk section §16); workflow authoring guide; 2–3 example workflows shipped in `examples/` (e.g. `feature-pr`, `fix-and-test`).
  *Done when:* a reviewer can follow the quickstart cold; examples validate via `vincent workflow validate`.
- [ ] **T4.5 — Release packaging.** goreleaser (or equivalent): versioned, signed binaries for all 3 OSes; checksums; install instructions per OS.
  *Done when:* tagged pre-release produces working artifacts installed and smoke-tested on each OS.
- [ ] **T4.6 — Phase gate (M4 acceptance).** Fresh-machine (VM) timed test per §19 M4 on each OS; results recorded here.
  *Done when:* all three runs under 10 minutes; notes committed. **v1 complete.**
