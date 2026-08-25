#!/usr/bin/env bash
# M2 phase gate (T2.10; spec §19 M2): prove via curl alone that a multi-step
# workflow (agent → command → manual gate → command publish to a local bare
# remote) runs to the gate with an agent question round-tripping
# awaiting_input → answer → resume (scenario 1), that kill -9 mid-step
# recovers correctly (scenario 2), and that both concurrency caps hold under
# load (scenario 3), that branch naming and its recovery path hold (scenario
# 4), that an agent usage limit re-queues rather than blocks, is reported per
# adapter on /v1/agents, and recovers unattended (scenario 5), and that archiving deletes a branch that carries no
# commits past its base while keeping every branch that does (scenario 6), and
# that a workflow restricted to other platforms is listed but never offered here
# (scenario 7), and that a step requiring mid-run interaction refuses an agent
# that cannot provide it (scenario 8), that an ad-hoc repair runs in the blocked
# task's own worktree and leaves it at the same step (scenario 9), that a
# follow-up run works on a finished task's own branch before it is archived
# (scenario 10), and that a step carrying `retry_backoff` paces its retry
# through the same admission hold — releasing its slot and recovering
# unattended (scenario 11).
# Runs against the committed fakeagent so CI never calls a real
# API; run manually with VINCENT_GATE_AGENT=claude to exercise the real CLI
# (scenario 1 only — killing a paid run and 8× cap spend prove nothing extra).
# VINCENT_GATE_SCENARIO=1..11 runs a single scenario for debugging.
#
# Each scenario gets fresh config/data/repo dirs and its own daemon:
# FAKEAGENT_SCENARIO is read from the daemon's environment, so it can only
# change at daemon start, and isolation keeps one scenario's leftovers out of
# another's assertions (PR G decision).
#
# Requirements: bash, go, git, curl, jq (all present on the GitHub runners,
# incl. Git Bash on Windows).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
BIN="$TMP/bin"

VINCENT="$BIN/vincent"
FAKEAGENT="$BIN/fakeagent"
if [[ "${OS:-}" == "Windows_NT" ]]; then
  VINCENT+=".exe"
  FAKEAGENT+=".exe"
fi

REAL_AGENT=0
[[ "${VINCENT_GATE_AGENT:-fake}" == "claude" ]] && REAL_AGENT=1

fail() { echo "GATE FAIL: $*" >&2; exit 1; }

cleanup() {
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

# hostpath converts a bash path for consumption by vincent — Git Bash /tmp
# paths are meaningless to a native Windows binary. -m keeps forward slashes
# so nothing needs YAML/JSON escaping.
hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

echo "== build vincent + fakeagent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent ./cmd/fakeagent)

# ---------------------------------------------------------------------------
# Per-scenario plumbing. scenario_dirs re-points the phase-1 env-override
# knob at fresh config/data dirs; daemon_up starts the daemon (inheriting the
# FAKEAGENT_* variables exported by the caller) and primes PORT/TOKEN/BASE.
# ---------------------------------------------------------------------------

CONFIG_DIR=""
DATA_DIR=""
scenario_dirs() { # scenario_dirs NAME
  CONFIG_DIR="$TMP/$1/config"
  DATA_DIR="$TMP/$1/data"
  mkdir -p "$CONFIG_DIR" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
}

PORT="" TOKEN="" BASE=""
daemon_up() {
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
}

api() { # api METHOD PATH [JSON_BODY] — prints the body, fails loudly on non-2xx
  local method="$1" path="$2" body="${3:-}" out status
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  [[ "$status" == 2* ]] || fail "$method $path -> HTTP $status: $out"
  printf '%s' "$out"
}

make_repo() { # make_repo PATH — init a commit-ready repo on main
  git init -q -b main "$1"
  git -C "$1" config user.name gate
  git -C "$1" config user.email gate@example.invalid
  git -C "$1" config commit.gpgsign false
  printf 'gate repo\n' > "$1/README.md"
  git -C "$1" add . && git -C "$1" commit -qm init
}

register_project() { # register_project REPO_PATH [EXTRA_JSON_FIELDS] -> id
  local extra="${2:-}"
  local body
  body="$(jq -cn --arg p "$(hostpath "$1")" "{path: \$p $extra}")"
  api POST /projects "$body" | jq -r .id
}

wait_for_state() { # wait_for_state TASK_ID STATE TRIES
  local id="$1" want="$2" tries="$3" state=""
  for _ in $(seq 1 "$tries"); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "blocked" || "$state" == "aborted" ]] && [[ "$want" != "blocked" ]]; then
      api GET "/tasks/$id" | jq . >&2
      fail "task $id reached $state while waiting for $want"
    fi
    sleep 1
  done
  api GET "/tasks/$id" | jq . >&2
  fail "task $id never reached $want (still $state)"
}

# ---------------------------------------------------------------------------
# Scenario 1 — multi-step workflow with an input-request round-trip:
# agent (asks a question, then edits README.md) → command (commit) → manual
# gate → command (push to a local bare remote). Spec §19 M2, §7.4.
# ---------------------------------------------------------------------------
scenario1() {
  echo "=== scenario 1: workflow + input round-trip + gate + publish"
  scenario_dirs s1

  local agent_path model_line=""
  agent_path="$(hostpath "$FAKEAGENT")"
  if (( REAL_AGENT )); then
    agent_path="" # empty = resolve the real claude from PATH
    model_line="  model: haiku"
  else
    export FAKEAGENT_SCENARIO=ask-question
    export FAKEAGENT_ASK_MULTI=1 # second, multi-select question over the wire
    export FAKEAGENT_EDIT_FILE=README.md
  fi
  cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$agent_path"
EOF

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/m2-flow.yaml" <<EOF
name: m2-flow
description: M2 gate — ask, commit, gate, publish.
defaults:
  agent: claude
  max_retries: 0
$model_line
steps:
  - id: ask
    type: agent
    prompt: |
      Use the AskUserQuestion tool to ask the user which color they prefer
      (options: Red, Blue). After the answer arrives, append one line stating
      the chosen color to README.md in the current directory. Do not do
      anything else. Task: {{.Task.Title}}
  - id: commit
    type: command
    run: 'git add -A && git commit -m "m2 gate: record the answer"'
  - id: review
    type: manual
    instructions: |
      Inspect the diff of task #{{.Task.ID}} before it is published.
  - id: publish
    type: command
    run: git push publish {{.Task.BranchName}}
EOF

  daemon_up

  local repo="$TMP/s1/repo" remote="$TMP/s1/remote.git"
  make_repo "$repo"
  git init -q --bare "$remote"
  git -C "$repo" remote add publish "$(hostpath "$remote")"

  local project_id task_id
  project_id="$(register_project "$repo")"
  task_id="$(api POST /tasks "{\"project_id\":$project_id,\"workflow\":\"m2-flow\",\"title\":\"M2 gate flow\",\"description\":\"Answer the question, then publish.\"}" | jq -r .id)"
  [[ "$task_id" =~ ^[0-9]+$ ]] || fail "task creation returned no id"

  echo "== wait for awaiting_input"
  wait_for_state "$task_id" awaiting_input 180
  local task pending
  task="$(api GET "/tasks/$task_id")"
  pending="$(jq .pending_input <<<"$task")"
  [[ "$(jq -r .kind <<<"$pending")" == "question" ]] || fail "pending_input is not a question: $pending"
  jq -e '.available_actions | index("answer")' <<<"$task" >/dev/null \
    || fail "available_actions misses answer: $task"

  echo "== answer (first option per question, built from pending_input)"
  local answer_body
  answer_body="$(jq -c '{answers: [.questions[]
    | (.options[0] // "Blue") as $pick
    | {key: .text, value: (if .multi_select then [$pick] else $pick end)}]
    | from_entries}' <<<"$pending")"
  api POST "/tasks/$task_id/answer" "$answer_body" >/dev/null

  echo "== wait for the gate, approve"
  wait_for_state "$task_id" awaiting_gate 180
  local gate_row
  gate_row="$(api GET "/tasks/$task_id/steps" | jq '[.[] | select(.step_id == "review")][-1]')"
  [[ "$(jq -r .state <<<"$gate_row")" == "running" ]] || fail "gate row not open: $gate_row"
  api POST "/tasks/$task_id/approve" >/dev/null

  echo "== wait for done"
  wait_for_state "$task_id" done 180

  echo "== assert step rows (input wait accounted, gate approved, publish succeeded)"
  local steps ask_row
  steps="$(api GET "/tasks/$task_id/steps")"
  ask_row="$(jq '[.[] | select(.step_id == "ask")][-1]' <<<"$steps")"
  [[ "$(jq -r .state <<<"$ask_row")" == "succeeded" ]] || fail "ask step not succeeded: $ask_row"
  jq -e '.input_wait_ms > 0' <<<"$ask_row" >/dev/null || fail "ask step has no input wait: $ask_row"
  [[ "$(jq -r '[.[] | select(.step_id == "review")][-1].state' <<<"$steps")" == "approved" ]] \
    || fail "gate row not approved: $steps"
  [[ "$(jq -r '[.[] | select(.step_id == "publish")][-1].state' <<<"$steps")" == "succeeded" ]] \
    || fail "publish step not succeeded: $steps"

  echo "== assert request/answer transcript lines"
  local run_id transcript
  run_id="$(jq -r .id <<<"$ask_row")"
  transcript="$(api GET "/tasks/$task_id/steps/$run_id/transcript")"
  grep -q '"vincent.input_request"' <<<"$transcript" || fail "transcript has no vincent.input_request"
  grep -q '"vincent.input_response"' <<<"$transcript" || fail "transcript has no vincent.input_response"

  echo "== assert the bare remote got the branch"
  local branch
  branch="$(api GET "/tasks/$task_id" | jq -r .branch_name)"
  git -C "$remote" rev-parse --verify "refs/heads/$branch" >/dev/null \
    || fail "branch $branch missing in the bare remote"
  git -C "$remote" log -1 --format=%s "$branch" | grep -q 'm2 gate: record the answer' \
    || fail "published tip is not the gate commit"
  if (( ! REAL_AGENT )); then
    git -C "$remote" show --name-only --format= "$branch" | grep -qx 'README.md' \
      || fail "published commit does not carry the agent's README.md edit"
  fi

  echo "== assert the doctor report (task 006, §17)"
  # Over curl, with no TUI and no hand-extracted token beyond the one this
  # gate already holds — which is the point of the endpoint existing.
  local doctor
  doctor="$(api GET /doctor)"
  jq -e '.daemon.status == "running" and (.daemon.pid > 0) and (.daemon.port > 0)' <<<"$doctor" >/dev/null \
    || fail "doctor daemon group is wrong: $(jq -c .daemon <<<"$doctor")"
  jq -e '.database.known and .database.integrity_check == "ok"
         and .database.schema_version == .database.newest_migration' <<<"$doctor" >/dev/null \
    || fail "doctor database group is wrong: $(jq -c .database <<<"$doctor")"
  jq -e '.tasks.known and .tasks.counts.done == 1' <<<"$doctor" >/dev/null \
    || fail "doctor task counts are wrong: $(jq -c .tasks <<<"$doctor")"
  jq -e '.storage.orphans_known and (.storage.orphans | length) == 0' <<<"$doctor" >/dev/null \
    || fail "a live task's worktree was called an orphan: $(jq -c .storage <<<"$doctor")"
  jq -e '.paths.config_parses and (.log.exists) and (.agents | length) > 0' <<<"$doctor" >/dev/null \
    || fail "doctor paths/log/agents groups are incomplete: $doctor"
  jq -e '(.problems | length) == 0' <<<"$doctor" >/dev/null \
    || fail "a healthy installation reported problems: $(jq -c .problems <<<"$doctor")"

  echo "== assert --fix reclaims an orphaned worktree"
  # Id 9999 owns no task row, which is exactly what a forced project delete
  # leaves behind (§10, task 005).
  mkdir -p "$DATA_DIR/worktrees/9999"
  printf 'residue\n' > "$DATA_DIR/worktrees/9999/leftover.txt"
  doctor="$(api GET /doctor)"
  jq -e '[.storage.orphans[] | select(.task_id == 9999)] | length == 1' <<<"$doctor" >/dev/null \
    || fail "doctor missed the orphaned worktree: $(jq -c .storage <<<"$doctor")"
  jq -e '(.problems | length) > 0' <<<"$doctor" >/dev/null \
    || fail "orphans present but the report is healthy"
  local fixed
  fixed="$(api POST /doctor/fix '{"force":true}')"
  jq -e '[.actions[] | select(.action == "remove_worktree" and .status == "done")] | length == 1' <<<"$fixed" >/dev/null \
    || fail "--fix did not remove the orphan: $(jq -c .actions <<<"$fixed")"
  [[ ! -d "$DATA_DIR/worktrees/9999" ]] || fail "the orphan directory survived --fix"
  jq -e '[.actions[] | select(.action == "compact_database" and .status == "done")] | length == 1' <<<"$fixed" >/dev/null \
    || fail "--fix did not compact an idle database: $(jq -c .actions <<<"$fixed")"
  jq -e '(.report.storage.orphans | length) == 0 and (.report.problems | length) == 0' <<<"$fixed" >/dev/null \
    || fail "the report taken after the fix still shows the orphan: $(jq -c .report.storage <<<"$fixed")"

  echo "== assert durable events over SSE replay (Last-Event-ID: 1)"
  # Cursor 1, not 0: ids start at 1 and a 0/absent cursor means live-only by
  # design (PR D — genesis catch-up is REST snapshot, then follow). Resuming
  # from the first event replays every later state change.
  local events
  events="$(curl -sS -N --max-time 3 -H "Authorization: Bearer $TOKEN" \
    -H "Last-Event-ID: 1" "$BASE/events?types=task.state_changed" || true)"
  grep -q '"to":"awaiting_input"' <<<"$events" || fail "SSE replay misses the awaiting_input transition"
  grep -q '"kind":"question"' <<<"$events" || fail "awaiting_input event carries no {kind}"
  grep -q '"to":"awaiting_gate"' <<<"$events" || fail "SSE replay misses the awaiting_gate transition"

  "$VINCENT" daemon stop
  unset FAKEAGENT_SCENARIO FAKEAGENT_ASK_MULTI FAKEAGENT_EDIT_FILE
  echo "=== scenario 1 PASS (task $task_id, branch $branch)"
}

# ---------------------------------------------------------------------------
# Scenario 2 — kill -9 mid-step recovers correctly: hang agent with a spawned
# grandchild, hard-kill the daemon, restart, assert the orphan tree is gone
# and the task re-ran the same step to done. adhoc pins max_retries: 0, so
# the re-run existing at all proves §7.2's interrupted-excluded-from-budget.
# ---------------------------------------------------------------------------
scenario2() {
  echo "=== scenario 2: hard-kill recovery"
  scenario_dirs s2
  cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
EOF

  export FAKEAGENT_SCENARIO=hang
  export FAKEAGENT_SPAWN_CHILD=1
  daemon_up

  local repo="$TMP/s2/repo" project_id task_id
  make_repo "$repo"
  project_id="$(register_project "$repo")"
  task_id="$(api POST /tasks "{\"project_id\":$project_id,\"title\":\"M2 gate hang\",\"description\":\"Hang until killed.\"}" | jq -r .id)"

  echo "== wait for the agent (and its child) to spawn"
  local child_pid=""
  for _ in $(seq 1 60); do
    local run_id
    run_id="$(api GET "/tasks/$task_id/steps" | jq -r '.[0].id // empty')"
    if [[ -n "$run_id" ]]; then
      child_pid="$(api GET "/tasks/$task_id/steps/$run_id/transcript" 2>/dev/null \
        | grep '"fakeagent.child"' | head -1 | jq -r .pid || true)"
      [[ -n "$child_pid" ]] && break
    fi
    sleep 1
  done
  [[ "$child_pid" =~ ^[0-9]+$ ]] || fail "never saw the fakeagent child pid in the transcript"

  echo "== hard-kill the daemon (pid from daemon.json)"
  local daemon_pid
  daemon_pid="$(jq -r .pid "$DATA_DIR/daemon.json")"
  if [[ "${OS:-}" == "Windows_NT" ]]; then
    # Git Bash kill -9 can't reliably kill a native process; // stops MSYS
    # from mangling the flags into paths (phase 2 decision).
    taskkill //F //PID "$daemon_pid" >/dev/null
  else
    kill -9 "$daemon_pid"
  fi
  for _ in $(seq 1 20); do
    kill -0 "$daemon_pid" 2>/dev/null || break
    sleep 0.5
  done

  echo "== restart (scenario flips to success for the re-run)"
  export FAKEAGENT_SCENARIO=success
  unset FAKEAGENT_SPAWN_CHILD
  daemon_up

  echo "== assert the orphaned child is gone"
  local alive=1
  for _ in $(seq 1 20); do
    if [[ "${OS:-}" == "Windows_NT" ]]; then
      tasklist //FI "PID eq $child_pid" 2>/dev/null | grep -q " $child_pid " || { alive=0; break; }
    else
      kill -0 "$child_pid" 2>/dev/null || { alive=0; break; }
    fi
    sleep 0.5
  done
  (( alive == 0 )) || fail "orphaned child $child_pid still alive after recovery"

  echo "== assert the task re-ran the same step to done"
  wait_for_state "$task_id" done 60
  local steps
  steps="$(api GET "/tasks/$task_id/steps")"
  jq -e '.[] | select(.step_index == 0 and .attempt == 1
    and .state == "interrupted" and .failure_reason == "interrupted")' <<<"$steps" >/dev/null \
    || fail "no interrupted attempt-1 row: $steps"
  jq -e '.[] | select(.step_index == 0 and .attempt == 2 and .state == "succeeded")' <<<"$steps" >/dev/null \
    || fail "no succeeded attempt-2 row: $steps"

  "$VINCENT" daemon stop
  unset FAKEAGENT_SCENARIO
  echo "=== scenario 2 PASS (task $task_id, child $child_pid reaped)"
}

# ---------------------------------------------------------------------------
# Scenario 3 — caps honored under load: 8 one-step `sleep 2` command tasks
# across two projects (per-project cap 2, global cap 3; both bind). No agent
# processes — deterministic durations, no scenario-env coupling.
# ---------------------------------------------------------------------------
scenario3() {
  echo "=== scenario 3: cap stress"
  scenario_dirs s3
  cat > "$CONFIG_DIR/config.yaml" <<EOF
max_parallel_tasks: 3
EOF
  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/m2-caps.yaml" <<EOF
name: m2-caps
description: M2 gate cap stress — sleep, valid in sh and pwsh alike.
steps:
  - id: nap
    type: command
    run: sleep 2
EOF

  daemon_up

  local repo_a="$TMP/s3/repo-a" repo_b="$TMP/s3/repo-b" pa pb
  make_repo "$repo_a"
  make_repo "$repo_b"
  pa="$(register_project "$repo_a" ', max_parallel_tasks: 2')"
  pb="$(register_project "$repo_b" ', max_parallel_tasks: 2')"

  echo "== create 8 tasks (4 per project)"
  local i
  for i in 1 2 3 4; do
    api POST /tasks "{\"project_id\":$pa,\"workflow\":\"m2-caps\",\"title\":\"cap a$i\",\"description\":\"nap\"}" >/dev/null
    api POST /tasks "{\"project_id\":$pb,\"workflow\":\"m2-caps\",\"title\":\"cap b$i\",\"description\":\"nap\"}" >/dev/null
  done

  echo "== sample running counts until all 8 finish"
  local max_global=0 max_project=0 done_count=0 snap g p
  for _ in $(seq 1 450); do # 450 × 0.2 s = 90 s budget
    snap="$(api GET /tasks)"
    g="$(jq '[.[] | select(.state == "running")] | length' <<<"$snap")"
    p="$(jq '[.[] | select(.state == "running")] | group_by(.project_id) | map(length) | max // 0' <<<"$snap")"
    done_count="$(jq '[.[] | select(.state == "done")] | length' <<<"$snap")"
    (( g > max_global )) && max_global=$g
    (( p > max_project )) && max_project=$p
    jq -e '.[] | select(.state == "blocked" or .state == "aborted")' <<<"$snap" >/dev/null \
      && fail "a cap-stress task failed: $snap"
    (( done_count == 8 )) && break
    sleep 0.2
  done
  (( done_count == 8 )) || fail "only $done_count/8 tasks finished"
  (( max_global <= 3 )) || fail "global cap violated: saw $max_global running (cap 3)"
  (( max_project <= 2 )) || fail "per-project cap violated: saw $max_project running (cap 2)"
  (( max_global >= 2 )) || fail "never saw parallelism (max $max_global) — scheduler serialized?"

  "$VINCENT" daemon stop
  echo "=== scenario 3 PASS (max global $max_global/3, max per-project $max_project/2)"
}

# ---------------------------------------------------------------------------
# Scenario 4 — configurable branch names (task 001, spec §5.3/§10).
#
# The interesting half is the recovery path. A `branch_exists` block used to be
# unreachable, because `vincent/{id}-{slug}` cannot collide; now it is routine, and
# the creation-time check that catches most collisions is explicitly *not* a
# guarantee — a branch can appear between that check and admission. So this drives
# the real race rather than a simulation of it: the global cap is 1, a slow task
# holds the only slot, the conflicting branch is created while the second task sits
# queued, and admission is what discovers it.
# ---------------------------------------------------------------------------
scenario4() {
  echo "=== scenario 4: configurable branch names"
  scenario_dirs s4
  cat > "$CONFIG_DIR/config.yaml" <<EOF
max_parallel_tasks: 1
EOF
  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/m2-branch.yaml" <<EOF
name: m2-branch
description: M2 gate branch naming — one command step, valid in sh and pwsh alike.
steps:
  - id: nap
    type: command
    run: sleep 1
EOF

  daemon_up

  local repo="$TMP/s4/repo" proj
  make_repo "$repo"
  proj="$(register_project "$repo" ', branch_template: "feat/{{.Slug}}"')"

  echo "== a project template names the branch"
  local t1 b1
  t1="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-branch\",\"title\":\"Retry logic\"}" | jq -r .id)"
  b1="$(api GET "/tasks/$t1" | jq -r .branch_name)"
  [[ "$b1" == "feat/retry-logic" ]] \
    || fail "project template ignored: branch is $b1, want feat/retry-logic"
  wait_for_state "$t1" done 60
  git -C "$repo" rev-parse --verify --quiet refs/heads/feat/retry-logic >/dev/null \
    || fail "the templated branch was never created in the repo"

  echo "== a per-task literal beats the template"
  local t2 b2
  t2="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-branch\",\"title\":\"Second\",\"branch_name\":\"release/hotfix\"}" | jq -r .id)"
  b2="$(api GET "/tasks/$t2" | jq -r .branch_name)"
  [[ "$b2" == "release/hotfix" ]] || fail "literal ignored: branch is $b2"
  wait_for_state "$t2" done 60

  echo "== the same template output twice is rejected at creation, not silently renamed"
  local status
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "{\"project_id\":$proj,\"workflow\":\"m2-branch\",\"title\":\"Retry logic\"}" \
    "$BASE/tasks")"
  [[ "$status" == "400" ]] \
    || fail "a repeat of an existing branch should be 400 at creation, got $status"

  echo "== an illegal branch name is refused with 400"
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "{\"project_id\":$proj,\"workflow\":\"m2-branch\",\"title\":\"bad\",\"branch_name\":\"feat/../escape\"}" \
    "$BASE/tasks")"
  [[ "$status" == "400" ]] || fail "an illegal ref name should be 400, got $status"

  # The race the creation check cannot close. The slow task holds the only slot,
  # so the second one is still queued when the branch appears under it.
  echo "== a branch appearing after creation blocks the task at admission"
  local hold late
  hold="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-branch\",\"title\":\"Holder\",\"branch_name\":\"gate/holder\"}" | jq -r .id)"
  late="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-branch\",\"title\":\"Late\",\"branch_name\":\"gate/contended\"}" | jq -r .id)"
  git -C "$repo" branch gate/contended
  wait_for_state "$late" blocked 90
  local reason
  reason="$(api GET "/tasks/$late" | jq -r .block_reason)"
  [[ "$reason" == "branch_exists" ]] \
    || fail "task $late blocked with $reason, want branch_exists"
  wait_for_state "$hold" done 90

  echo "== retry with branch_override recovers the blocked task, keeping its id"
  local recovered
  recovered="$(api POST "/tasks/$late/retry" '{"branch_override":"gate/recovered"}' | jq -r .branch_name)"
  [[ "$recovered" == "gate/recovered" ]] \
    || fail "branch_override ignored: branch is $recovered"
  wait_for_state "$late" done 90
  git -C "$repo" rev-parse --verify --quiet refs/heads/gate/recovered >/dev/null \
    || fail "the recovered branch was never created"
  # The rename kept the task rather than replacing it: same id, block cleared, and
  # it went on to actually run a step on the new branch.
  #
  # Note what is deliberately *not* asserted. This task blocked at worktree
  # creation, which happens before any step starts, so it had no step runs to
  # preserve — what survived is the task row, its id and its event trail. A task
  # that blocks mid-workflow is where step history matters, and scenario 2 already
  # covers attempt rows surviving a restart.
  [[ "$(api GET "/tasks/$late" | jq -r '.block_reason // "null"')" == "null" ]] \
    || fail "task $late still carries a block reason after a successful retry"
  [[ "$(api GET "/tasks/$late/steps" | jq 'length')" -ge 1 ]] \
    || fail "task $late ran no step after the rename, so it never used the new branch"

  echo "== a still-colliding override is refused and the task stays blocked"
  local t3
  t3="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-branch\",\"title\":\"Third\",\"branch_name\":\"gate/second-clash\"}" | jq -r .id)"
  git -C "$repo" branch gate/second-clash
  wait_for_state "$t3" blocked 90
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{"branch_override":"gate/recovered"}' "$BASE/tasks/$t3/retry")"
  [[ "$status" == "400" ]] || fail "a colliding override should be 400, got $status"
  [[ "$(api GET "/tasks/$t3" | jq -r .state)" == "blocked" ]] \
    || fail "task $t3 should still be blocked after a refused override"

  "$VINCENT" daemon stop
  echo "=== scenario 4 PASS"
}

# ---------------------------------------------------------------------------
# Scenario 5 — agent usage limits (task 003, spec §7.2/§11/§18).
#
# The whole path over curl: an agent that reports a spent quota must not burn
# its retry budget, must give its slot back, and must recover with nobody
# pressing anything. The recheck interval is squeezed to two seconds so the
# unattended half is observable in a gate rather than in five hours.
#
# The global cap is 1 and the second task sleeps, so "the slot was released" is
# a fact the scenario forces rather than infers: the sleeper can only be
# running because the walled task stopped holding one, and the walled task can
# only finish afterwards because the scheduler came back to it on its own.
# ---------------------------------------------------------------------------
scenario5() {
  echo "=== scenario 5: usage limit — no retry burned, slot released, unattended recovery"
  scenario_dirs s5

  export FAKEAGENT_SCENARIO=usage-limit
  # The marker is the fake CLI's own record that the window has reopened, so
  # the recovery is the daemon's doing and not the script's.
  export FAKEAGENT_USAGE_LIMIT_MARKER
  FAKEAGENT_USAGE_LIMIT_MARKER="$(hostpath "$TMP/s5-window-spent")"

  cat > "$CONFIG_DIR/config.yaml" <<EOF
max_parallel_tasks: 1
usage_limit_recheck_interval: 2s
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
EOF

  mkdir -p "$CONFIG_DIR/workflows"
  # max_retries: 0 — so a quota stop that wrongly counted as a failure would
  # block the task on the spot, and this scenario would fail loudly.
  cat > "$CONFIG_DIR/workflows/m2-quota.yaml" <<EOF
name: m2-quota
description: M2 gate — one agent step against a quota-exhausted CLI.
defaults:
  agent: claude
  max_retries: 0
steps:
  - id: work
    type: agent
    prompt: "Do the work for {{.Task.Title}}"
EOF
  cat > "$CONFIG_DIR/workflows/m2-sleeper.yaml" <<EOF
name: m2-sleeper
description: M2 gate — occupies the only slot while the held task waits.
steps:
  - id: nap
    type: command
    run: sleep 3
EOF

  daemon_up

  local repo="$TMP/s5/repo" proj walled sleeper
  make_repo "$repo"
  proj="$(register_project "$repo")"

  # The walled task is created first, so §11 offers it the only slot first.
  walled="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-quota\",\"title\":\"Quota walled\"}" | jq -r .id)"
  sleeper="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-sleeper\",\"title\":\"Sleeper\"}" | jq -r .id)"

  echo "== wait for the quota stop to park the task on an admission hold"
  local task=""
  local ok=0
  for _ in $(seq 1 90); do
    task="$(api GET "/tasks/$walled")"
    if [[ "$(jq -r '.queued_reason // "null"' <<<"$task")" == "usage_limit" ]]; then ok=1; break; fi
    [[ "$(jq -r .state <<<"$task")" == "blocked" ]] && { jq . <<<"$task" >&2; fail "task $walled blocked instead of waiting on the quota window"; }
    sleep 1
  done
  (( ok )) || { jq . <<<"$task" >&2; fail "task $walled never picked up queued_reason=usage_limit"; }
  [[ "$(jq -r .state <<<"$task")" == "queued" ]] || fail "held task is $(jq -r .state <<<"$task"), want queued"
  [[ "$(jq -r '.admit_not_before // "null"' <<<"$task")" != "null" ]] \
    || fail "held task carries no admit_not_before: $task"
  [[ "$(jq -r '.block_reason // "null"' <<<"$task")" == "null" ]] \
    || fail "a quota-held task must not carry a block_reason: $task"

  # Captured here rather than after the step assertions: the recheck interval
  # is two seconds, so the successful re-run that clears the observation is
  # already on its way. Task 026's claim is that the fact *outlives the hold*,
  # and the hold is what was just detected.
  echo "== the daemon reports the spent window per adapter (task 026)"
  local quota
  quota="$(api GET /agents | jq -c '.agents[] | select(.name == "claude") | .quota')"
  [[ "$quota" != "null" && -n "$quota" ]] \
    || fail "GET /v1/agents carries no quota block while a task is held on one"
  [[ "$(jq -r .source <<<"$quota")" == "observed" ]] \
    || fail "quota.source = $(jq -r .source <<<"$quota"), want observed: $quota"
  # This scenario sets no FAKEAGENT_USAGE_LIMIT_RESET, so the reset is the
  # recheck interval's estimate and must not claim the CLI stated it.
  [[ "$(jq -r .resets_at_reported <<<"$quota")" == "false" ]] \
    || fail "quota.resets_at_reported is true for a reset the CLI never named: $quota"
  # Declared, permanently unfillable: no CLI has a non-interactive quota
  # surface, and a zero here would read as "empty" (§9.2/§9.3/§9.7).
  [[ "$(jq -r .used_percent <<<"$quota")" == "null" ]] \
    || fail "quota.used_percent is not null: $quota"
  [[ "$(jq -r .window <<<"$quota")" == "null" ]] \
    || fail "quota.window is not null: $quota"

  echo "== assert the attempt is recorded interrupted and spent no retry"
  local steps
  steps="$(api GET "/tasks/$walled/steps")"
  [[ "$(jq 'length' <<<"$steps")" == "1" ]] || fail "attempts = $(jq 'length' <<<"$steps"), want 1: $steps"
  [[ "$(jq -r '.[0].state' <<<"$steps")" == "interrupted" ]] || fail "attempt not interrupted: $steps"
  [[ "$(jq -r '.[0].failure_reason' <<<"$steps")" == "usage_limit" ]] \
    || fail "attempt reason is $(jq -r '.[0].failure_reason' <<<"$steps"), want usage_limit"

  echo "== the released slot lets the next task run"
  wait_for_state "$sleeper" done 120

  echo "== the held task recovers with no human action"
  wait_for_state "$walled" done 120
  steps="$(api GET "/tasks/$walled/steps")"
  [[ "$(jq 'length' <<<"$steps")" == "2" ]] || fail "attempts = $(jq 'length' <<<"$steps"), want 2: $steps"
  [[ "$(jq -r '.[1].state' <<<"$steps")" == "succeeded" ]] || fail "the re-run did not succeed: $steps"
  [[ "$(api GET "/tasks/$walled" | jq -r '.queued_reason // "null"')" == "null" ]] \
    || fail "the hold outlived the queued period it belonged to"

  # And the observation goes with it. A successful run on that adapter is
  # first-hand evidence the window reopened, which matters most here: this
  # reset was an estimate, so nothing else would ever retire it (task 026).
  [[ "$(api GET /agents | jq -r '.agents[] | select(.name == "claude") | .quota')" == "null" ]] \
    || fail "the observed window outlived the successful re-run"

  "$VINCENT" daemon stop
  unset FAKEAGENT_SCENARIO FAKEAGENT_USAGE_LIMIT_MARKER
  echo "=== scenario 5 PASS (task $walled recovered unattended)"
}

# ---------------------------------------------------------------------------
# Scenario 6 — archive-time branch cleanup (task 008, spec §10/§13.2).
#
# The one exception to "vincent never deletes a branch", proven against a real
# daemon on all three platforms: a task that never commits anything leaves no
# ref behind, a task that commits keeps every one of them, and the escape hatch
# actually reaches the running daemon. The two workflows differ in exactly one
# thing — whether a commit is made — so a difference in the outcome can only
# come from the rule under test.
# ---------------------------------------------------------------------------
scenario6() {
  echo "=== scenario 6: archive deletes a branch with no commits past its base"
  scenario_dirs s6

  export FAKEAGENT_SCENARIO=success
  export FAKEAGENT_EDIT_FILE=README.md

  # No delete_empty_branch_on_archive line: the default is what ships, and the
  # default is what this asserts.
  cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
EOF

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/m2-idle.yaml" <<EOF
name: m2-idle
description: M2 gate — a workflow that never writes to the repository.
steps:
  - id: nap
    type: command
    run: sleep 1
EOF
  cat > "$CONFIG_DIR/workflows/m2-commits.yaml" <<EOF
name: m2-commits
description: M2 gate — the same shape, but it commits.
defaults:
  agent: claude
  max_retries: 0
steps:
  - id: edit
    type: agent
    prompt: "Edit README.md for {{.Task.Title}}"
  - id: commit
    type: command
    run: 'git add -A && git commit -m "m2 gate: real work"'
EOF

  daemon_up

  echo "== both keys are visible in the config view, with the shipped defaults"
  local cfg
  cfg="$(api GET /config)"
  [[ "$(jq -r .delete_empty_branch_on_archive <<<"$cfg")" == "true" ]] \
    || fail "delete_empty_branch_on_archive is not true by default: $cfg"
  [[ "$(jq -r .delete_remote_branch_on_archive <<<"$cfg")" == "false" ]] \
    || fail "delete_remote_branch_on_archive is not false by default: $cfg"

  local repo="$TMP/s6/repo" proj
  make_repo "$repo"
  proj="$(register_project "$repo")"

  echo "== a task that commits nothing loses its branch on archive"
  local idle idle_branch body
  idle="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-idle\",\"title\":\"Idle\"}" | jq -r .id)"
  idle_branch="$(api GET "/tasks/$idle" | jq -r .branch_name)"
  wait_for_state "$idle" done 90
  git -C "$repo" rev-parse --verify --quiet "refs/heads/$idle_branch" >/dev/null \
    || fail "the branch was never created, so archiving proves nothing"
  body="$(api POST "/tasks/$idle/archive")"
  [[ "$(jq -r .state <<<"$body")" == "archived" ]] || fail "archive did not archive: $body"
  [[ "$(jq -r '.branch.result' <<<"$body")" == "deleted" ]] \
    || fail "archive reported $(jq -c .branch <<<"$body"), want result deleted"
  git -C "$repo" rev-parse --verify --quiet "refs/heads/$idle_branch" >/dev/null \
    && fail "branch $idle_branch survived an archive that reported it deleted"

  echo "== a task that commits keeps its branch, and the commit with it"
  local work work_branch tip
  work="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-commits\",\"title\":\"Work\"}" | jq -r .id)"
  work_branch="$(api GET "/tasks/$work" | jq -r .branch_name)"
  wait_for_state "$work" done 180
  tip="$(git -C "$repo" rev-parse "refs/heads/$work_branch")"
  body="$(api POST "/tasks/$work/archive")"
  [[ "$(jq -r '.branch.result' <<<"$body")" == "has_commits" ]] \
    || fail "archive reported $(jq -c .branch <<<"$body"), want result has_commits"
  [[ "$(git -C "$repo" rev-parse "refs/heads/$work_branch")" == "$tip" ]] \
    || fail "branch $work_branch did not survive the archive at the same commit"

  echo "== delete_empty_branch_on_archive: false restores the pre-008 behaviour"
  cat > "$CONFIG_DIR/config.yaml" <<EOF
delete_empty_branch_on_archive: false
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
EOF
  local reloaded=0
  for _ in $(seq 1 30); do
    [[ "$(api GET /config | jq -r .delete_empty_branch_on_archive)" == "false" ]] \
      && { reloaded=1; break; }
    sleep 1
  done
  (( reloaded )) || fail "the daemon never picked up the edited config"

  local kept kept_branch
  kept="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-idle\",\"title\":\"Kept\"}" | jq -r .id)"
  kept_branch="$(api GET "/tasks/$kept" | jq -r .branch_name)"
  wait_for_state "$kept" done 90
  body="$(api POST "/tasks/$kept/archive")"
  [[ "$(jq -r '.branch // "null"' <<<"$body")" == "null" ]] \
    || fail "the branch step ran with the policy off: $(jq -c .branch <<<"$body")"
  git -C "$repo" rev-parse --verify --quiet "refs/heads/$kept_branch" >/dev/null \
    || fail "branch $kept_branch was deleted with delete_empty_branch_on_archive off"

  "$VINCENT" daemon stop
  unset FAKEAGENT_SCENARIO FAKEAGENT_EDIT_FILE
  echo "=== scenario 6 PASS"
}

# ---------------------------------------------------------------------------
# Scenario 7 — a workflow restricted to other platforms (§8.1.1, task 010) is
# listed with the daemon's verdict, refused at task creation, and does not
# stop the same registry serving a workflow that does run here. The "foreign"
# platform is chosen from the host, so this asserts the same thing on all
# three CI legs instead of passing vacuously on two of them.
# ---------------------------------------------------------------------------
scenario7() {
  echo "=== scenario 7: platform-restricted workflows are listed, not offered"
  scenario_dirs s7

  local here foreign
  if [[ "${OS:-}" == "Windows_NT" ]]; then here=windows; foreign=posix; else here=posix; foreign=windows; fi

  cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
EOF

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/m2-elsewhere.yaml" <<EOF
name: m2-elsewhere
description: M2 gate — restricted to a platform this host is not.
platforms: [$foreign]
steps:
  - id: gate
    type: manual
    instructions: never reached
EOF
  cat > "$CONFIG_DIR/workflows/m2-here.yaml" <<EOF
name: m2-here
description: M2 gate — restricted to this very host.
platforms: [$here]
steps:
  - id: nap
    type: command
    run: sleep 1
EOF

  daemon_up

  local repo="$TMP/s7/repo" proj list
  make_repo "$repo"
  proj="$(register_project "$repo")"

  echo "== the registry lists both, with the daemon's own verdict on each"
  list="$(api GET "/workflows?project_id=$proj")"
  [[ "$(jq -r '.workflows[] | select(.name=="m2-elsewhere") | .platform_supported' <<<"$list")" == "false" ]] \
    || fail "the foreign-platform workflow is not marked unsupported: $list"
  [[ "$(jq -r '.workflows[] | select(.name=="m2-elsewhere") | .platforms[0]' <<<"$list")" == "$foreign" ]] \
    || fail "the entry does not carry its platforms: $list"
  [[ "$(jq -r '.workflows[] | select(.name=="m2-elsewhere") | .error' <<<"$list")" == "null" ]] \
    || fail "a platform restriction was reported as a validation error: $list"
  [[ "$(jq -r '.workflows[] | select(.name=="m2-here") | .platform_supported' <<<"$list")" == "true" ]] \
    || fail "a workflow naming this host is not supported on it: $list"

  echo "== creating a task on it is refused with 400"
  local status
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "{\"project_id\":$proj,\"workflow\":\"m2-elsewhere\",\"title\":\"Nope\"}" \
    "$BASE/tasks")"
  [[ "$status" == "400" ]] \
    || fail "a workflow this host cannot run should be 400 at creation, got $status"

  echo "== the workflow restricted to this host runs to completion"
  local ok
  ok="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-here\",\"title\":\"Yes\"}" | jq -r .id)"
  wait_for_state "$ok" done 90

  "$VINCENT" daemon stop
  echo "=== scenario 7 PASS"
}

# ---------------------------------------------------------------------------
# Scenario 8 (task 013): a step declaring `on_input: require` only runs on an
# agent that can stop and ask. Three layers, one daemon: the workflow pinning
# codex fails §8.2 validation outright, the unpinned one advertises
# requires_input, and a task naming codex on it is refused with 400 while the
# same workflow on claude runs to completion.
# ---------------------------------------------------------------------------
scenario8() {
  echo "=== scenario 8: workflows that require mid-run interaction gate their agent"
  scenario_dirs s8

  cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
  codex:
    path: "$(hostpath "$FAKEAGENT")"
EOF

  mkdir -p "$CONFIG_DIR/workflows"
  # The requiring step leaves its agent to the task, which is what makes the
  # task's choice the thing being gated.
  cat > "$CONFIG_DIR/workflows/m2-interactive.yaml" <<'EOF'
name: m2-interactive
description: M2 gate — a step that needs a human mid-run.
steps:
  - id: ask
    type: agent
    on_input: require
    max_retries: 0
    prompt: Decide what to build.
EOF
  # Pinned to an adapter with no control channel: broken on every host, and
  # decidable at load without probing anything.
  cat > "$CONFIG_DIR/workflows/m2-impossible.yaml" <<'EOF'
name: m2-impossible
description: M2 gate — requires input from an agent that can never give it.
steps:
  - id: ask
    type: agent
    agent: codex
    on_input: require
    prompt: Decide what to build.
EOF

  export FAKEAGENT_SCENARIO=success
  daemon_up

  local repo="$TMP/s8/repo" proj list
  make_repo "$repo"
  proj="$(register_project "$repo")"

  echo "== the impossible workflow is listed with a validation error naming the agent"
  list="$(api GET "/workflows?project_id=$proj")"
  [[ "$(jq -r '.workflows[] | select(.name=="m2-impossible") | .error' <<<"$list")" != "null" ]] \
    || fail "require on codex validated clean: $list"
  jq -e '.workflows[] | select(.name=="m2-impossible") | .errors[0].path == "steps[0].agent"' \
    <<<"$list" >/dev/null \
    || fail "the finding does not point at the agent field: $list"

  echo "== the runnable one advertises that it needs an interactive agent"
  [[ "$(jq -r '.workflows[] | select(.name=="m2-interactive") | .requires_input' <<<"$list")" == "true" ]] \
    || fail "a workflow with an unpinned requiring step does not report requires_input: $list"
  [[ "$(jq -r '.workflows[] | select(.name=="adhoc") | .requires_input' <<<"$list")" == "false" ]] \
    || fail "the built-in adhoc workflow reports requires_input: $list"

  echo "== /v1/agents publishes the verdict the gate uses"
  local agents
  agents="$(api GET /agents)"
  [[ "$(jq -r '.agents[] | select(.name=="codex") | .input_verdict' <<<"$agents")" == "unsupported" ]] \
    || fail "codex does not report an unsupported input verdict: $agents"
  [[ "$(jq -r '.agents[] | select(.name=="claude") | .input_verdict' <<<"$agents")" == "supported" ]] \
    || fail "the fake claude does not report a supported input verdict: $agents"

  echo "== a task picking codex for it is refused with 400"
  local status
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "{\"project_id\":$proj,\"workflow\":\"m2-interactive\",\"title\":\"Nope\",\"agent\":\"codex\"}" \
    "$BASE/tasks")"
  [[ "$status" == "400" ]] \
    || fail "an agent that cannot answer questions should be 400 at creation, got $status"

  echo "== the same workflow on an agent that can ask runs to completion"
  local ok
  ok="$(api POST /tasks \
    "{\"project_id\":$proj,\"workflow\":\"m2-interactive\",\"title\":\"Yes\",\"agent\":\"claude\"}" | jq -r .id)"
  wait_for_state "$ok" done 90

  unset FAKEAGENT_SCENARIO
  "$VINCENT" daemon stop
  echo "=== scenario 8 PASS"
}

# ---------------------------------------------------------------------------
# Scenario 9 — an ad-hoc repair agent unblocks a step nothing else could
# (§6, §7.2, §13.2, task 025).
#
# The blocked step is a `command` step on purpose. It has no agent selection of
# its own, which is exactly why a repair's agent/model/effort stand in for the
# step level of §8.6's chain rather than inheriting from the step being
# repaired — and it means the only agent process this scenario runs is the
# repair itself, so "the repair is what changed the worktree" needs no
# inference.
#
# The step's check greps a tracked file for a line only the fake agent writes
# (`FAKEAGENT_EDIT_FILE`, which appends to an existing tracked file). So the
# task blocks on check_failed, the repair puts the line there, the task returns
# to blocked with the *same* reason, and only then does a retry pass.
# ---------------------------------------------------------------------------
scenario9() {
  echo "=== scenario 9: an ad-hoc repair agent unblocks a failing check"
  scenario_dirs s9

  cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
EOF

  mkdir -p "$CONFIG_DIR/workflows"
  # Both run: bodies are one git command each — the sh∩pwsh intersection the
  # daemon's shell holds every workflow to (§8.3). `git grep -q` exits 1 when
  # the pattern is absent, which is the whole failing condition.
  cat > "$CONFIG_DIR/workflows/m2-repair.yaml" <<'EOF'
name: m2-repair
description: M2 gate — a check only a repair can satisfy.
defaults:
  agent: claude
  max_retries: 0
steps:
  - id: work
    type: command
    run: 'git commit --allow-empty -m "m2 gate: the work"'
    check: 'git grep -q "fakeagent was here" -- fixture.txt'
  - id: land
    type: command
    run: 'git commit --allow-empty -m "m2 gate: the repair held"'
EOF

  export FAKEAGENT_SCENARIO=success
  export FAKEAGENT_EDIT_FILE=fixture.txt
  daemon_up

  local repo="$TMP/s9/repo" proj task_id task steps
  make_repo "$repo"
  printf 'pending\n' > "$repo/fixture.txt"
  git -C "$repo" add fixture.txt
  git -C "$repo" commit -qm fixture
  proj="$(register_project "$repo")"

  task_id="$(api POST /tasks \
    "{\"project_id\":$proj,\"workflow\":\"m2-repair\",\"title\":\"Repair me\"}" | jq -r .id)"

  echo "== the check fails and the task blocks"
  wait_for_state "$task_id" blocked 90
  task="$(api GET "/tasks/$task_id")"
  [[ "$(jq -r .block_reason <<<"$task")" == "check_failed" ]] \
    || fail "expected a check_failed block: $task"
  jq -e '.available_actions | index("repair")' <<<"$task" >/dev/null \
    || fail "available_actions misses repair while blocked: $task"
  local blocked_at
  blocked_at="$(jq -r .current_step <<<"$task")"

  echo "== a repair with no prompt is refused"
  local status
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{"prompt":"   "}' "$BASE/tasks/$task_id/repair")"
  [[ "$status" == "400" ]] || fail "an empty repair prompt should be 400, got $status"

  echo "== repair: one agent run in this task's existing worktree"
  api POST "/tasks/$task_id/repair" \
    '{"prompt":"Put the line the check is looking for into fixture.txt."}' >/dev/null

  echo "== it comes back to the same step with the same reason"
  wait_for_state "$task_id" blocked 90
  task="$(api GET "/tasks/$task_id")"
  [[ "$(jq -r .block_reason <<<"$task")" == "check_failed" ]] \
    || fail "the repair changed the block reason: $task"
  [[ "$(jq -r .current_step <<<"$task")" == "$blocked_at" ]] \
    || fail "the repair moved the cursor off step $blocked_at: $task"

  echo "== the repair is its own row, not an attempt of the blocked step"
  steps="$(api GET "/tasks/$task_id/steps")"
  jq -e --argjson i "$blocked_at" \
    '[.[] | select(.step_id == "__repair" and .step_index == $i and .state == "succeeded")] | length == 1' \
    <<<"$steps" >/dev/null || fail "no succeeded __repair row at step $blocked_at: $steps"
  jq -e '[.[] | select(.step_id == "work")] | length == 1' <<<"$steps" >/dev/null \
    || fail "the repair was counted as an attempt of the blocked step: $steps"

  echo "== only now does a retry pass, and the workflow finishes"
  api POST "/tasks/$task_id/retry" >/dev/null
  wait_for_state "$task_id" done 90
  steps="$(api GET "/tasks/$task_id/steps")"
  jq -e '[.[] | select(.step_id == "land" and .state == "succeeded")] | length == 1' \
    <<<"$steps" >/dev/null || fail "the workflow did not reach the step after the repair: $steps"

  echo "== repair is refused outside blocked, with the state in the envelope"
  local out body
  out="$(curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" -d '{"prompt":"too late"}' \
    -w $'\n%{http_code}' "$BASE/tasks/$task_id/repair")"
  status="${out##*$'\n'}"
  body="${out%$'\n'*}"
  [[ "$status" == "409" ]] || fail "repair from done should be 409, got $status: $body"
  [[ "$(jq -r .error.details.state <<<"$body")" == "done" ]] \
    || fail "the 409 does not carry details.state: $body"

  unset FAKEAGENT_SCENARIO FAKEAGENT_EDIT_FILE
  "$VINCENT" daemon stop
  echo "=== scenario 9 PASS"
}

# ---------------------------------------------------------------------------
# Scenario 10 — a follow-up run on a finished task (§6, §13.2, task 027).
#
# The task 027 brief calls this "scenario 9"; that number was already the
# ad-hoc repair's by the time this landed, so it is 10.
#
# Everything here is a `command` follow-up: no agent process is involved, so
# "the follow-up ran in the finished task's own worktree" is proved by a commit
# on that task's branch rather than inferred from an agent's output. Both `run:`
# bodies are one git command each — the sh∩pwsh intersection the daemon's shell
# holds every workflow to (§8.3).
#
# What it pins is decision 2's placement: a round's rows sit past the
# snapshot's last index, `step_total` does not grow, and a second follow-up is
# round 2 rather than attempt 2 of round 1.
# ---------------------------------------------------------------------------
scenario10() {
  echo "=== scenario 10: a follow-up run on a finished task"
  scenario_dirs s10

  cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
EOF

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/m2-followup.yaml" <<'EOF'
name: m2-followup
description: M2 gate — two steps that pass, so the task reaches done.
defaults:
  max_retries: 0
steps:
  - id: work
    type: command
    run: 'git commit --allow-empty -m "m2 gate: the work"'
  - id: land
    type: command
    run: 'git commit --allow-empty -m "m2 gate: landed"'
EOF
  cat > "$CONFIG_DIR/workflows/m2-followup-blocked.yaml" <<'EOF'
name: m2-followup-blocked
description: M2 gate — one step that fails, for the 409 half.
defaults:
  max_retries: 0
steps:
  - id: nope
    type: command
    run: 'exit 7'
EOF

  daemon_up

  local repo="$TMP/s10/repo" proj task_id task steps branch total status
  make_repo "$repo"
  proj="$(register_project "$repo")"

  task_id="$(api POST /tasks \
    "{\"project_id\":$proj,\"workflow\":\"m2-followup\",\"title\":\"Follow me up\"}" | jq -r .id)"

  echo "== the workflow finishes"
  wait_for_state "$task_id" done 90
  task="$(api GET "/tasks/$task_id")"
  total="$(jq -r .step_total <<<"$task")"
  branch="$(jq -r .branch_name <<<"$task")"
  [[ "$total" == "2" ]] || fail "step_total = $total, want 2: $task"
  jq -e '.available_actions | index("follow_up")' <<<"$task" >/dev/null \
    || fail "available_actions misses follow_up on a done task: $task"

  echo "== a follow-up that names nothing to run is refused"
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{}' "$BASE/tasks/$task_id/follow_up")"
  [[ "$status" == "400" ]] || fail "an empty follow-up should be 400, got $status"

  echo "== a follow-up that names two things to run is refused"
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{"prompt":"do it","run":"git --version"}' "$BASE/tasks/$task_id/follow_up")"
  [[ "$status" == "400" ]] || fail "a two-form follow-up should be 400, got $status"

  echo "== round 1: a command runs in the finished task's own worktree"
  api POST "/tasks/$task_id/follow_up" \
    '{"run":"git commit --allow-empty -m \"m2 gate: follow-up one\""}' >/dev/null
  wait_for_state "$task_id" done 90

  task="$(api GET "/tasks/$task_id")"
  [[ "$(jq -r .step_total <<<"$task")" == "2" ]] \
    || fail "the follow-up grew the workflow snapshot: $task"
  steps="$(api GET "/tasks/$task_id/steps")"
  jq -e --argjson i "$total" \
    '[.[] | select(.step_index == $i and .state == "succeeded")] | length == 1' \
    <<<"$steps" >/dev/null || fail "no succeeded follow-up row at index $total: $steps"
  # A here-string, not a pipe: `grep -q` exits at the first match and closes
  # the pipe under it, so `git log | grep -q` fails the whole pipeline under
  # `set -o pipefail` on a *successful* match.
  grep -q "m2 gate: follow-up one" <<<"$(git -C "$repo" log --format=%s "$branch")" \
    || fail "the follow-up did not commit on the task's branch"

  echo "== round 2 is a round, not a second attempt of round 1"
  api POST "/tasks/$task_id/follow_up" \
    '{"run":"git commit --allow-empty -m \"m2 gate: follow-up two\""}' >/dev/null
  wait_for_state "$task_id" done 90
  steps="$(api GET "/tasks/$task_id/steps")"
  jq -e --argjson i "$((total + 1))" \
    '[.[] | select(.step_index == $i and .attempt == 1 and .state == "succeeded")] | length == 1' \
    <<<"$steps" >/dev/null || fail "round 2 is not attempt 1 at index $((total + 1)): $steps"
  jq -e --argjson i "$total" '[.[] | select(.step_index == $i)] | length == 1' \
    <<<"$steps" >/dev/null || fail "round 2 was recorded against round 1's index: $steps"
  grep -q "m2 gate: follow-up two" <<<"$(git -C "$repo" log --format=%s "$branch")" \
    || fail "the second follow-up did not commit on the task's branch"

  echo "== follow_up is refused outside done and aborted, with the state in the envelope"
  local other out body
  other="$(api POST /tasks \
    "{\"project_id\":$proj,\"workflow\":\"m2-followup-blocked\",\"title\":\"Blocked\"}" | jq -r .id)"
  wait_for_state "$other" blocked 90
  out="$(curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" -d '{"run":"git --version"}' \
    -w $'\n%{http_code}' "$BASE/tasks/$other/follow_up")"
  status="${out##*$'\n'}"
  body="${out%$'\n'*}"
  [[ "$status" == "409" ]] || fail "follow_up from blocked should be 409, got $status: $body"
  [[ "$(jq -r .error.details.state <<<"$body")" == "blocked" ]] \
    || fail "the 409 does not carry details.state: $body"

  "$VINCENT" daemon stop
  echo "=== scenario 10 PASS"
}

# ---------------------------------------------------------------------------
# Scenario 11 — paced retries (task 028, spec §7.2/§11).
#
# Scenario 5's shape for a failure rather than a quota wall: a step that fails
# its first attempt with a 2 s `retry_backoff` returns the task to `queued`
# carrying `queued_reason: retry_backoff` and an `admit_not_before`, releases
# the only slot so the queue keeps moving, and comes back on its own. The
# attempt row is the difference from a quota wall — `failed`, not
# `interrupted`, because a paced retry costs a retry and a quota wall does not.
#
# The step body is one command whose own exit code says everything: pwsh parses
# `... && exit 0` as a command named `exit`, so the template picks the command
# rather than composing one (§8.3).
# ---------------------------------------------------------------------------
scenario11() {
  echo "=== scenario 11: retry_backoff — paced retry, slot released, unattended recovery"
  scenario_dirs s11

  cat > "$CONFIG_DIR/config.yaml" <<EOF
max_parallel_tasks: 1
EOF

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/m2-backoff.yaml" <<'YAML'
name: m2-backoff
description: M2 gate — a step that fails once and is retried after a wait.
steps:
  - id: flaky
    type: command
    max_retries: 1
    retry_backoff: 2s
    run: '{{ if eq .Step.Attempt 1 }}exit 1{{ else }}exit 0{{ end }}'
YAML
  # Long enough that the hold expiring is not what ends the wait: the paced
  # task stays queued while this one owns the only slot, which is what gives
  # the poll below something to see.
  cat > "$CONFIG_DIR/workflows/m2-sleeper11.yaml" <<'YAML'
name: m2-sleeper11
description: M2 gate — occupies the only slot while the paced task waits.
steps:
  - id: nap
    type: command
    run: sleep 5
YAML

  daemon_up

  local repo="$TMP/s11/repo" proj paced sleeper
  make_repo "$repo"
  proj="$(register_project "$repo")"

  # The paced task is created first, so §11 offers it the only slot first.
  paced="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-backoff\",\"title\":\"Paced retry\"}" | jq -r .id)"
  sleeper="$(api POST /tasks "{\"project_id\":$proj,\"workflow\":\"m2-sleeper11\",\"title\":\"Sleeper\"}" | jq -r .id)"

  echo "== wait for the failed attempt to park the task on an admission hold"
  local task="" ok=0
  for _ in $(seq 1 90); do
    task="$(api GET "/tasks/$paced")"
    if [[ "$(jq -r '.queued_reason // "null"' <<<"$task")" == "retry_backoff" ]]; then ok=1; break; fi
    [[ "$(jq -r .state <<<"$task")" == "blocked" ]] && { jq . <<<"$task" >&2; fail "task $paced blocked instead of pacing its retry"; }
    sleep 1
  done
  (( ok )) || { jq . <<<"$task" >&2; fail "task $paced never picked up queued_reason=retry_backoff"; }
  [[ "$(jq -r .state <<<"$task")" == "queued" ]] || fail "paced task is $(jq -r .state <<<"$task"), want queued"
  [[ "$(jq -r '.admit_not_before // "null"' <<<"$task")" != "null" ]] \
    || fail "paced task carries no admit_not_before: $task"
  [[ "$(jq -r '.block_reason // "null"' <<<"$task")" == "null" ]] \
    || fail "a paced task must not carry a block_reason: $task"

  echo "== assert the attempt is recorded failed, and spent its retry"
  local steps
  steps="$(api GET "/tasks/$paced/steps")"
  [[ "$(jq 'length' <<<"$steps")" == "1" ]] || fail "attempts = $(jq 'length' <<<"$steps"), want 1: $steps"
  [[ "$(jq -r '.[0].state' <<<"$steps")" == "failed" ]] \
    || fail "attempt is $(jq -r '.[0].state' <<<"$steps"), want failed — a paced retry is still a failure: $steps"
  [[ "$(jq -r '.[0].failure_reason' <<<"$steps")" == "nonzero_exit" ]] \
    || fail "attempt reason is $(jq -r '.[0].failure_reason' <<<"$steps"), want nonzero_exit"

  echo "== the released slot lets the next task run"
  wait_for_state "$sleeper" done 120

  echo "== the paced task recovers with no human action"
  wait_for_state "$paced" done 120
  steps="$(api GET "/tasks/$paced/steps")"
  [[ "$(jq 'length' <<<"$steps")" == "2" ]] \
    || fail "attempts = $(jq 'length' <<<"$steps"), want exactly 2 (max_retries: 1): $steps"
  [[ "$(jq -r '.[1].state' <<<"$steps")" == "succeeded" ]] || fail "the paced retry did not succeed: $steps"
  [[ "$(api GET "/tasks/$paced" | jq -r '.queued_reason // "null"')" == "null" ]] \
    || fail "the hold outlived the queued period it belonged to"

  "$VINCENT" daemon stop
  echo "=== scenario 11 PASS (task $paced retried after a paced wait)"
}

WHICH="${VINCENT_GATE_SCENARIO:-all}"
if (( REAL_AGENT )); then
  echo "== real-agent mode: scenario 1 only (PR G decision)"
  WHICH=1
fi
case "$WHICH" in
  1) scenario1 ;;
  2) scenario2 ;;
  3) scenario3 ;;
  4) scenario4 ;;
  5) scenario5 ;;
  6) scenario6 ;;
  7) scenario7 ;;
  8) scenario8 ;;
  9) scenario9 ;;
  10) scenario10 ;;
  11) scenario11 ;;
  all) scenario1; scenario2; scenario3; scenario4; scenario5; scenario6; scenario7; scenario8
     scenario9; scenario10; scenario11 ;;
  *) fail "unknown VINCENT_GATE_SCENARIO: $WHICH" ;;
esac

echo "M2 GATE PASS"
