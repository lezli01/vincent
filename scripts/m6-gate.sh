#!/usr/bin/env bash
# M6 phase gate (task 014; spec §7.5, §7.6): prove via curl alone that
# parallel step groups and workflow fan-out behave as specified.
#
# Phase 1 — `type: parallel`:
#   1. a group's sub-steps run concurrently and the task completes
#   2. a failing sub-step blocks the task without cancelling its siblings
#   3. a retry re-runs only the sub-step that failed
#
# Phase 2 — `type: fan_out`:
#   4. two lanes become real child tasks and both branches are merged back
#   5. an induced conflict reaches `merge_conflict`; resolved by hand and
#      retried, the remaining lanes merge
#   6. a crash mid-merge recovers by aborting and re-merging idempotently
#   7. a depth-2 tree runs to completion
#   8. a cancelled lane blocks the join with `lane_failed`, merging nothing
#   9. both creation-time 400s: a workflow cycle and fan_out.max_tasks
#  10. `schedule: eager` starts a lane while an unrelated sibling still runs
#  11. the merged diff is attributed back to the lane that produced it
#  12. one retry on a parked parent re-admits every blocked lane under it
#
# Every scenario uses command steps only, so no agent CLI is involved at all
# and the gate is as fast on CI as it is locally.
#
# The `run:` bodies are restricted to what `/bin/sh` and `pwsh` both accept —
# `exit N`, `sleep N` and `git ...`, and nothing else, the same rule `m7` and
# `m8` follow. `exit N` may only be the *whole* body: pwsh's `&&` and `||`
# take pipelines, so an `exit` after one is parsed as a command name and dies
# with "the term 'exit' is not recognized" (run 32140128084, windows leg). §8.3 leaves portability to the workflow author, and a gate that
# runs on all three platforms is that author. This is why a lane that has to
# produce a file writes it with `git config -f`: `echo > x` and `Set-Content`
# share no spelling, and `touch` is not a shell builtin anywhere. The original
# bodies were POSIX-only (`touch`, `seq`, `[ -f ]`), which stood only because
# the gate was hand-run on Linux until it was wired into CI (#120).
#
# Each scenario gets fresh config/data/repo dirs and its own daemon, so one
# scenario's leftovers cannot reach another's assertions (PR G decision,
# inherited from m2-gate.sh). VINCENT_GATE_SCENARIO=N runs one scenario.
#
# Requirements: bash, go, git, curl, jq.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
BIN="$TMP/bin"

VINCENT="$BIN/vincent"
if [[ "${OS:-}" == "Windows_NT" ]]; then
  VINCENT+=".exe"
fi

ONLY="${VINCENT_GATE_SCENARIO:-}"

fail() { echo "GATE FAIL: $*" >&2; exit 1; }

cleanup() {
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

echo "== build vincent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent)

CONFIG_DIR="" DATA_DIR=""
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
daemon_down() { "$VINCENT" daemon stop --force >/dev/null 2>&1 || true; }

api() { # api METHOD PATH [JSON_BODY]
  local method="$1" path="$2" body="${3:-}" out status
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  [[ "$status" == 2* ]] || fail "$method $path -> HTTP $status: $out"
  printf '%s' "$out"
}

# api_status prints "STATUS<newline>BODY" without failing on a 4xx, for the
# scenarios whose whole point is the 400.
api_status() { # api_status METHOD PATH [JSON_BODY]
  local method="$1" path="$2" body="${3:-}" out
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  printf '%s\n%s' "${out##*$'\n'}" "${out%$'\n'*}"
}

make_repo() { # make_repo PATH
  git init -q -b main "$1"
  git -C "$1" config user.name gate
  git -C "$1" config user.email gate@example.invalid
  git -C "$1" config commit.gpgsign false
  printf 'gate repo\n' > "$1/README.md"
  git -C "$1" add . && git -C "$1" commit -qm init
}

register_project() { git init >/dev/null 2>&1 || true; api POST /projects \
  "$(jq -cn --arg p "$(hostpath "$1")" '{path: $p}')" | jq -r .id; }

write_workflow() { # write_workflow NAME YAML
  mkdir -p "$CONFIG_DIR/workflows"
  printf '%s' "$2" > "$CONFIG_DIR/workflows/$1.yaml"
}

wait_for_state() { # wait_for_state TASK_ID STATE TRIES
  local id="$1" want="$2" tries="$3" state=""
  for _ in $(seq 1 "$tries"); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "aborted" ]] && [[ "$want" != "aborted" ]]; then
      api GET "/tasks/$id" | jq . >&2
      fail "task $id reached $state while waiting for $want"
    fi
    sleep 1
  done
  api GET "/tasks/$id" | jq . >&2
  fail "task $id never reached $want (last: $state)"
}

create_task() { # create_task PROJECT_ID WORKFLOW TITLE -> id
  api POST /tasks "$(jq -cn --argjson p "$1" --arg w "$2" --arg t "$3" \
    '{project_id: $p, workflow: $w, title: $t}')" | jq -r .id
}

# branch_has reports whether a path exists on a branch's tip.
branch_has() { # branch_has REPO BRANCH PATH
  [[ -n "$(git -C "$1" ls-tree --name-only "$2" "$3")" ]]
}

run_scenario() { # run_scenario N — honours VINCENT_GATE_SCENARIO
  [[ -z "$ONLY" || "$ONLY" == "$1" ]]
}

# --------------------------------------------------------------------------
# Scenario 1: a parallel group runs its sub-steps concurrently.
#
# The overlap is observed from the API, not from inside the steps: each
# sub-step only sleeps, and the gate polls until it sees all three `running`
# at the same instant. Sequential execution tops out at one, so this fails
# deterministically rather than on a wall-clock comparison — and it keeps the
# step bodies to a vocabulary `/bin/sh` and `pwsh` both accept, which is what
# the earlier marker-file version (`touch`, `seq`, `[ -f ]`) did not.
# --------------------------------------------------------------------------
if run_scenario 1; then
  echo "=== scenario 1: a parallel group runs concurrently"
  scenario_dirs s1
  REPO="$TMP/s1/repo"; make_repo "$REPO"
  write_workflow parallel-ok "$(cat <<'YAML'
name: parallel-ok
steps:
  - id: verify
    type: parallel
    max_parallel: 3
    steps:
      - {id: one, type: command, max_retries: 0, run: 'sleep 8'}
      - {id: two, type: command, max_retries: 0, run: 'sleep 8'}
      - {id: three, type: command, max_retries: 0, run: 'sleep 8'}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" parallel-ok "parallel happy path")"

  # The eight-second body is the observation window: a one-second poll sees it
  # several times over, and a sequential run would never show more than one.
  MAX=0
  for _ in $(seq 1 90); do
    BODY="$(api GET "/tasks/$TID")"
    # `.steps` is null until the group's first attempt rows exist.
    N="$(jq '[(.steps // [])[] | select(.state == "running")] | length' <<<"$BODY")"
    if [[ "$N" -gt "$MAX" ]]; then MAX="$N"; fi
    if [[ "$MAX" == 3 || "$(jq -r .state <<<"$BODY")" == "done" ]]; then break; fi
    sleep 1
  done
  [[ "$MAX" == 3 ]] || fail "sub-steps never overlapped: at most $MAX ran at once, want 3"

  wait_for_state "$TID" done 60

  RUNS="$(api GET "/tasks/$TID" | jq '[.steps[] | select(.state == "succeeded")] | length')"
  [[ "$RUNS" == 3 ]] || fail "expected 3 succeeded sub-step runs, got $RUNS"
  # One row per sub-step, all sharing the group's index (decision 16).
  IDX="$(api GET "/tasks/$TID" | jq '[.steps[].step_index] | unique | length')"
  [[ "$IDX" == 1 ]] || fail "sub-steps span $IDX step indexes, want 1"
  daemon_down
  echo "=== scenario 1 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 2: a failing sub-step blocks the group without cancelling siblings.
# Scenario 3: the retry re-runs only what failed.
#
# The failing sub-step is one `git config --get`, which exits 1 while its
# marker file is absent and 0 once it is there. The gate writes that marker
# into the worktree before retrying, the way scenario 5 resolves its conflict
# — a human clearing the fault is what a retry means here. The sibling's
# attempt count is what proves the retry did not re-run work that had already
# succeeded.
# --------------------------------------------------------------------------
if run_scenario 2; then
  echo "=== scenario 2+3: a failing sub-step blocks, and the retry re-runs only it"
  scenario_dirs s2
  REPO="$TMP/s2/repo"; make_repo "$REPO"
  write_workflow parallel-fail "$(cat <<'YAML'
name: parallel-fail
steps:
  - id: verify
    type: parallel
    steps:
      - id: flaky
        type: command
        max_retries: 0
        run: git config -f cleared.marker --get gate.cleared
      - id: steady
        type: command
        max_retries: 0
        run: 'sleep 2'
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" parallel-fail "one sub-step fails")"
  wait_for_state "$TID" blocked 60

  BODY="$(api GET "/tasks/$TID")"
  REASON="$(jq -r .block_reason <<<"$BODY")"
  [[ "$REASON" == "nonzero_exit" ]] || fail "block_reason = $REASON, want nonzero_exit"
  # The sibling ran to completion: a failure does not cancel it (decision 18).
  STEADY="$(jq -r '[.steps[] | select(.step_id == "steady" and .state == "succeeded")] | length' <<<"$BODY")"
  [[ "$STEADY" == 1 ]] || fail "the sibling did not finish (succeeded rows: $STEADY)"

  # Clear the fault by hand, then retry: `git config -f` writes the marker
  # byte-identically on every platform, where `touch` exists on none of the
  # shells that matter.
  WT="$(jq -r .worktree_path <<<"$BODY")"
  [[ -f "$WT/.git" || -d "$WT/.git" ]] || fail "no worktree left to clear the fault in"
  git config -f "$WT/cleared.marker" gate.cleared 1

  api POST "/tasks/$TID/retry" '{}' >/dev/null
  wait_for_state "$TID" done 60

  BODY="$(api GET "/tasks/$TID")"
  STEADY="$(jq -r '[.steps[] | select(.step_id == "steady")] | length' <<<"$BODY")"
  [[ "$STEADY" == 1 ]] || fail "the retry re-ran an already-succeeded sub-step ($STEADY attempts)"
  FLAKY="$(jq -r '[.steps[] | select(.step_id == "flaky")] | length' <<<"$BODY")"
  [[ "$FLAKY" == 2 ]] || fail "the failed sub-step has $FLAKY attempts, want 2"
  daemon_down
  echo "=== scenario 2+3 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 4: two lanes become real child tasks, and both are merged back.
# --------------------------------------------------------------------------
if run_scenario 4; then
  echo "=== scenario 4: fan-out spawns lanes and merges them back"
  scenario_dirs s4
  REPO="$TMP/s4/repo"; make_repo "$REPO"
  write_workflow fan-ok "$(cat <<'YAML'
name: fan-ok
steps:
  - id: build
    type: fan_out
    lanes:
      - id: api
        steps:
          - {id: write-api, type: command, max_retries: 0, run: 'git config -f api.txt gate.lane api && git add -A && git commit -qm api'}
      - id: docs
        steps:
          - {id: write-docs, type: command, max_retries: 0, run: 'git config -f docs.txt gate.lane docs && git add -A && git commit -qm docs'}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" fan-ok "fan out happy path")"

  # The lanes are real tasks, hidden from the default listing (decision 13).
  for _ in $(seq 1 30); do
    LANES="$(api GET "/tasks?parent_id=$TID" | jq 'length')"
    [[ "$LANES" == 2 ]] && break
    sleep 1
  done
  [[ "$LANES" == 2 ]] || fail "expected 2 lanes, got $LANES"
  ROOTS="$(api GET "/tasks" | jq 'length')"
  [[ "$ROOTS" == 1 ]] || fail "the default task list shows $ROOTS tasks, want only the root"
  ALL="$(api GET "/tasks?include_children=true" | jq 'length')"
  [[ "$ALL" == 3 ]] || fail "include_children shows $ALL tasks, want 3"

  wait_for_state "$TID" done 120
  BRANCH="$(api GET "/tasks/$TID" | jq -r .branch_name)"
  branch_has "$REPO" "$BRANCH" api.txt || fail "api.txt is not on the delivered branch"
  branch_has "$REPO" "$BRANCH" docs.txt || fail "docs.txt is not on the delivered branch"
  daemon_down
  echo "=== scenario 4 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 5: a conflict blocks; a hand resolution plus retry completes it.
# --------------------------------------------------------------------------
if run_scenario 5; then
  echo "=== scenario 5: merge_conflict, resolved by hand, retried"
  scenario_dirs s5
  REPO="$TMP/s5/repo"; make_repo "$REPO"
  write_workflow fan-conflict "$(cat <<'YAML'
name: fan-conflict
steps:
  - id: build
    type: fan_out
    lanes:
      - id: left
        steps:
          - {id: w, type: command, max_retries: 0, run: 'git config -f shared.txt gate.side left && git add -A && git commit -qm left'}
      - id: right
        steps:
          - {id: w, type: command, max_retries: 0, run: 'git config -f shared.txt gate.side right && git add -A && git commit -qm right'}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" fan-conflict "conflicting lanes")"
  wait_for_state "$TID" blocked 120

  BODY="$(api GET "/tasks/$TID")"
  REASON="$(jq -r .block_reason <<<"$BODY")"
  [[ "$REASON" == "merge_conflict" ]] || fail "block_reason = $REASON, want merge_conflict"
  WT="$(jq -r .worktree_path <<<"$BODY")"
  [[ -f "$WT/.git" || -d "$WT/.git" ]] || fail "no worktree left to resolve in"
  # The conflict is left in place on purpose: this is a human resolving it.
  # Both lanes *create* the file, so git reports an add/add conflict (AA)
  # rather than a content one (UU). Match any unmerged status.
  # Captured rather than piped into `grep -q` — see m7 scenario 4's comment.
  PORCELAIN="$(git -C "$WT" status --porcelain)"
  grep -qE '^(DD|AU|UD|UA|DU|AA|UU)' <<<"$PORCELAIN" \
    || fail "no conflicted file in the worktree: $PORCELAIN"
  echo resolved > "$WT/shared.txt"
  git -C "$WT" add shared.txt

  api POST "/tasks/$TID/retry" '{}' >/dev/null
  wait_for_state "$TID" done 120
  BRANCH="$(jq -r .branch_name <<<"$BODY")"
  [[ "$(git -C "$REPO" show "$BRANCH:shared.txt")" == "resolved" ]] \
    || fail "the hand resolution is not what landed on the branch"
  daemon_down
  echo "=== scenario 5 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 6: a crash mid-merge recovers by aborting and re-merging.
#
# The daemon is killed while the join is blocked with a conflict, then
# restarted. Recovery must abort the half-finished merge and re-merge from the
# top — every already-merged lane a no-op — rather than leaving the repository
# stuck in a merge nobody owns.
# --------------------------------------------------------------------------
if run_scenario 6; then
  echo "=== scenario 6: a crash mid-merge recovers idempotently"
  scenario_dirs s6
  REPO="$TMP/s6/repo"; make_repo "$REPO"
  write_workflow fan-conflict "$(cat <<'YAML'
name: fan-conflict
steps:
  - id: build
    type: fan_out
    lanes:
      - id: left
        steps:
          - {id: w, type: command, max_retries: 0, run: 'git config -f shared.txt gate.side left && git add -A && git commit -qm left'}
      - id: right
        steps:
          - {id: w, type: command, max_retries: 0, run: 'git config -f shared.txt gate.side right && git add -A && git commit -qm right'}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" fan-conflict "crash mid-merge")"
  wait_for_state "$TID" blocked 120
  WT="$(api GET "/tasks/$TID" | jq -r .worktree_path)"
  [[ -f "$WT/.git" || -d "$WT/.git" ]] || fail "no worktree"

  # A merge is in progress and the daemon dies.
  daemon_down
  daemon_up

  # Resolve as a human would, then retry: the recovered daemon must complete
  # the join rather than refuse it.
  git -C "$WT" checkout --theirs shared.txt 2>/dev/null || echo resolved > "$WT/shared.txt"
  git -C "$WT" add shared.txt
  api POST "/tasks/$TID/retry" '{}' >/dev/null
  wait_for_state "$TID" done 120
  daemon_down
  echo "=== scenario 6 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 7: a depth-2 tree runs to completion.
# --------------------------------------------------------------------------
if run_scenario 7; then
  echo "=== scenario 7: a depth-2 fan-out tree"
  scenario_dirs s7
  REPO="$TMP/s7/repo"; make_repo "$REPO"
  write_workflow inner "$(cat <<'YAML'
name: inner
steps:
  - id: deep
    type: fan_out
    lanes:
      - id: leaf
        steps:
          - {id: w, type: command, max_retries: 0, run: 'git config -f leaf.txt gate.lane leaf && git add -A && git commit -qm leaf'}
YAML
)"
  write_workflow outer "$(cat <<'YAML'
name: outer
steps:
  - id: build
    type: fan_out
    lanes:
      - {id: mid, workflow: inner}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" outer "depth two")"
  wait_for_state "$TID" done 180

  # Three tasks in the tree, and the leaf's work reached the root's branch.
  ALL="$(api GET "/tasks?include_children=true" | jq 'length')"
  [[ "$ALL" == 3 ]] || fail "depth-2 tree has $ALL tasks, want 3"
  BRANCH="$(api GET "/tasks/$TID" | jq -r .branch_name)"
  branch_has "$REPO" "$BRANCH" leaf.txt || fail "the depth-2 lane's work never reached the root branch"
  daemon_down
  echo "=== scenario 7 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 8: a cancelled lane blocks the join with lane_failed, merging
# nothing — a partial merge is indistinguishable downstream from a complete
# one (decision 21).
# --------------------------------------------------------------------------
if run_scenario 8; then
  echo "=== scenario 8: a cancelled lane blocks the join"
  scenario_dirs s8
  REPO="$TMP/s8/repo"; make_repo "$REPO"
  write_workflow fan-slow "$(cat <<'YAML'
name: fan-slow
steps:
  - id: build
    type: fan_out
    lanes:
      - id: quick
        steps:
          - {id: w, type: command, max_retries: 0, run: 'git config -f quick.txt gate.lane quick && git add -A && git commit -qm quick'}
      - id: slow
        steps:
          - {id: w, type: command, max_retries: 0, run: 'sleep 60'}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" fan-slow "a lane is cancelled")"
  for _ in $(seq 1 30); do
    SLOW="$(api GET "/tasks?parent_id=$TID" | jq -r '.[] | select(.lane_id == "slow") | .id')"
    [[ -n "$SLOW" ]] && break
    sleep 1
  done
  [[ -n "$SLOW" ]] || fail "the slow lane never appeared"
  api POST "/tasks/$SLOW/cancel" '{}' >/dev/null
  wait_for_state "$TID" blocked 120

  REASON="$(api GET "/tasks/$TID" | jq -r .block_reason)"
  [[ "$REASON" == "lane_failed" ]] || fail "block_reason = $REASON, want lane_failed"
  BRANCH="$(api GET "/tasks/$TID" | jq -r .branch_name)"
  branch_has "$REPO" "$BRANCH" quick.txt \
    && fail "the successful lane was merged anyway; the branch looks complete"
  daemon_down
  echo "=== scenario 8 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 9: both creation-time 400s.
#
# The whole tree's shape is static at creation, which is what lets a depth
# explosion be a 400 in front of the person typing rather than two hundred
# worktrees discovered six hours later.
# --------------------------------------------------------------------------
if run_scenario 9; then
  echo "=== scenario 9: creation-time cycle and max_tasks refusals"
  scenario_dirs s9
  REPO="$TMP/s9/repo"; make_repo "$REPO"
  cat > "$CONFIG_DIR/config.yaml" <<'YAML'
fan_out:
  max_tasks: 2
YAML
  write_workflow alpha "$(cat <<'YAML'
name: alpha
steps:
  - {id: f, type: fan_out, lanes: [{id: l, workflow: beta}]}
YAML
)"
  write_workflow beta "$(cat <<'YAML'
name: beta
steps:
  - {id: f, type: fan_out, lanes: [{id: l, workflow: alpha}]}
YAML
)"
  write_workflow wide "$(cat <<'YAML'
name: wide
steps:
  - id: f
    type: fan_out
    lanes:
      - {id: a, steps: [{id: sa, type: command, run: 'exit 0'}]}
      - {id: b, steps: [{id: sb, type: command, run: 'exit 0'}]}
      - {id: c, steps: [{id: sc, type: command, run: 'exit 0'}]}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"

  OUT="$(api_status POST /tasks "$(jq -cn --argjson p "$PID" '{project_id: $p, workflow: "alpha", title: "cycle"}')")"
  STATUS="${OUT%%$'\n'*}"; BODY="${OUT#*$'\n'}"
  [[ "$STATUS" == 400 ]] || fail "a cyclic workflow created with HTTP $STATUS"
  grep -q cycle <<<"$BODY" || fail "the 400 does not say cycle: $BODY"
  grep -q alpha <<<"$BODY" || fail "the 400 does not name the path: $BODY"

  OUT="$(api_status POST /tasks "$(jq -cn --argjson p "$PID" '{project_id: $p, workflow: "wide", title: "wide"}')")"
  STATUS="${OUT%%$'\n'*}"; BODY="${OUT#*$'\n'}"
  [[ "$STATUS" == 400 ]] || fail "a tree past max_tasks created with HTTP $STATUS"
  grep -q max_tasks <<<"$BODY" || fail "the 400 does not name the bound: $BODY"
  daemon_down
  echo "=== scenario 9 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 10: `schedule: eager` spawns a dependent lane before an unrelated
# sibling has settled (task 081). The first m6 scenario to drive `needs:` at
# all — task 080 left the DAG scenario out.
#
# Asserted from the API rather than from inside a step body: the gate polls
# the lane list until the dependent lane exists, and reads the unrelated
# lane's state *from the same response*, so "it spawned early" is a fact about
# one snapshot rather than a wall-clock comparison. Under a barrier the
# dependent lane cannot appear until `slow` has settled, so this fails
# deterministically rather than flakily.
# --------------------------------------------------------------------------
if run_scenario 10; then
  echo "=== scenario 10: eager scheduling starts a lane before its siblings settle"
  scenario_dirs s10
  REPO="$TMP/s10/repo"; make_repo "$REPO"
  write_workflow fan-eager "$(cat <<'YAML'
name: fan-eager
steps:
  - id: build
    type: fan_out
    schedule: eager
    lanes:
      - id: quick
        steps:
          - {id: w, type: command, max_retries: 0, run: 'git config -f quick.txt gate.lane quick && git add -A && git commit -qm quick'}
      - id: slow
        steps:
          - {id: w, type: command, max_retries: 0, run: 'sleep 25'}
      - id: dep
        needs: [quick]
        steps:
          - {id: w, type: command, max_retries: 0, run: 'git config -f dep.txt gate.lane dep && git add -A && git commit -qm dep'}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" fan-eager "an eager DAG")"

  DEP="" SLOW_STATE=""
  for _ in $(seq 1 60); do
    LANES="$(api GET "/tasks?parent_id=$TID")"
    DEP="$(jq -r '.[] | select(.lane_id == "dep") | .id' <<<"$LANES" | tr -d '\r')"
    if [[ -n "$DEP" ]]; then
      SLOW_STATE="$(jq -r '.[] | select(.lane_id == "slow") | .state' <<<"$LANES" | tr -d '\r')"
      break
    fi
    sleep 1
  done
  [[ -n "$DEP" ]] || fail "the dependent lane never spawned"
  case "$SLOW_STATE" in
    queued|running) ;;
    *) fail "the dependent lane spawned with the unrelated lane already $SLOW_STATE; that is barrier timing" ;;
  esac

  wait_for_state "$TID" done 180
  BRANCH="$(api GET "/tasks/$TID" | jq -r .branch_name)"
  for f in quick.txt dep.txt; do
    branch_has "$REPO" "$BRANCH" "$f" || fail "$f is missing from the parent's branch"
  done
  daemon_down
  echo "=== scenario 10 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 11: the parent's merged diff is attributed to its lanes.
#
# After the `--no-ff` join the parent's diff is one wall of merged hunks with
# nothing in it saying which lane wrote what. `?by=lane` splits it along the
# merges the parent itself made — the message `Merge lane '{id}' of task {n}`
# §7.6 fixes, which this scenario makes a tested contract rather than a
# convention.
#
# The load-bearing assertion is the negative one on the *second* lane. Both
# lanes are cut from the same tip and joined one after the other, so by the
# time `docs` is merged the first parent of its merge already carries `api`.
# A section that reported `api.txt` inside `docs` would be attribution that
# gets the second lane wrong every time, and would still satisfy every
# positive assertion here.
# --------------------------------------------------------------------------
if run_scenario 11; then
  echo "=== scenario 11: ?by=lane attributes the merged diff to its lanes"
  scenario_dirs s11
  REPO="$TMP/s11/repo"; make_repo "$REPO"
  write_workflow fan-attributed "$(cat <<'YAML'
name: fan-attributed
steps:
  - id: build
    type: fan_out
    lanes:
      - id: api
        steps:
          - {id: write-api, type: command, max_retries: 0, run: 'git config -f api.txt gate.lane api && git add -A && git commit -qm api'}
      - id: docs
        steps:
          - {id: write-docs, type: command, max_retries: 0, run: 'git config -f docs.txt gate.lane docs && git add -A && git commit -qm docs'}
  - id: tail
    type: command
    max_retries: 0
    run: 'git config -f tail.txt gate.tail 1 && git add -A && git commit -qm tail'
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" fan-attributed "attributed fan out")"
  wait_for_state "$TID" done 180

  DIFF="$(api GET "/tasks/$TID/diff?by=lane")"
  # Several lines of jq output: jq writes CRLF on Windows and `$(...)` drops
  # only the trailing one, so the interior CRs would fail the comparison.
  LANES="$(jq -r '.sections[] | select(.remainder | not) | .lane_id' <<<"$DIFF" | tr -d '\r')"
  [[ "$LANES" == $'api\ndocs' ]] || fail "lane sections are [$LANES], want api then docs in merge order"
  COUNT="$(jq '.sections | length' <<<"$DIFF")"
  [[ "$COUNT" == 3 ]] || fail "got $COUNT sections, want two lanes and a remainder"
  REST="$(jq -r '[.sections[] | select(.remainder)] | length' <<<"$DIFF")"
  [[ "$REST" == 1 ]] || fail "got $REST remainder sections, want exactly 1"

  # Each lane carries its own file and nothing another section wrote.
  # Captured and matched against a here-string rather than piped into
  # `grep -q` — see m7 scenario 4's comment.
  #
  # A negative assertion is spelled as an `if`, never as `grep ... && fail`:
  # the `&&` list returns 1 when the match is absent, which is the passing
  # case, and `set -e` would kill the gate on it.
  for lane in api docs; do
    SECTION="$(jq -r --arg l "$lane" '.sections[] | select(.lane_id == $l) | .diff' <<<"$DIFF")"
    grep -qF "$lane.txt" <<<"$SECTION" || fail "lane $lane does not carry $lane.txt: $SECTION"
    for other in api docs tail; do
      if [[ "$other" != "$lane" ]] && grep -qF "$other.txt" <<<"$SECTION"; then
        fail "lane $lane claims $other.txt, which another section wrote: $SECTION"
      fi
    done
  done

  # The remainder holds the parent's own post-join commit, and no lane's work.
  SECTION="$(jq -r '.sections[] | select(.remainder) | .diff' <<<"$DIFF")"
  grep -qF 'tail.txt' <<<"$SECTION" || fail "the remainder lost the parent's own commit: $SECTION"
  for other in api docs; do
    if grep -qF "$other.txt" <<<"$SECTION"; then
      fail "the remainder claims $other.txt, which lane $other wrote: $SECTION"
    fi
  done

  # Absent, the parameter changes nothing: the same task still answers with
  # the flat unified diff every existing client reads.
  FLAT="$(api GET "/tasks/$TID/diff")"
  for f in api docs tail; do
    grep -qF "$f.txt" <<<"$FLAT" || fail "the ungrouped diff is missing $f.txt: $FLAT"
  done
  if grep -qF '"sections"' <<<"$FLAT"; then
    fail "the ungrouped diff came back grouped: $FLAT"
  fi

  # `lane` is the only grouping there is, so anything else is a 400 rather
  # than a silent fall-through to the ungrouped body.
  OUT="$(api_status GET "/tasks/$TID/diff?by=file")"
  STATUS="${OUT%%$'\n'*}"
  [[ "$STATUS" == 400 ]] || fail "by=file -> HTTP $STATUS, want 400"
  CODE="$(jq -r .error.code <<<"${OUT#*$'\n'}" | tr -d '\r')"
  [[ "$CODE" == "validation_failed" ]] || fail "by=file error code = $CODE, want validation_failed"

  daemon_down
  echo "=== scenario 11 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 12: one retry on the parked parent re-admits every blocked lane.
#
# Both lanes fail on the same missing repository setting, so the parent parks
# with two `blocked` children under it — `blocked` is not settled, so the join
# stays open rather than failing `lane_failed` the way scenario 8's cancelled
# lane does. Before task 088 that shape needed one retry per lane, walked by
# hand; the one assertion this scenario exists for is that a single retry on
# the *parent* clears all of them, and says how many it cleared.
#
# The fault is one repository setting rather than a per-worktree marker
# because a linked worktree shares the repository's config: `git config`
# written once on the repo is seen by both lanes, which is a human fixing one
# root cause rather than the same cause twice. It is also a single command
# whose own exit code is the pass/fail condition, which is what §8.3's
# sh∩pwsh vocabulary allows — no `[ -f ]`, no `if`, no `exit` after an `&&`.
#
# The empty commit ahead of it is what makes the join observable. The lanes
# write nothing, and `git merge --no-ff` of a branch that is already an
# ancestor reports "Already up to date." and records no commit, so a join over
# empty lanes would leave nothing on the parent's branch to assert against.
# One `--allow-empty` commit per lane gives each merge something to do, and
# the `Merge lane '{id}' of task {n}` subjects §7.6 fixes are then the proof
# that both lanes actually landed.
# --------------------------------------------------------------------------
if run_scenario 12; then
  echo "=== scenario 12: a retry on a parked parent cascades to its blocked lanes"
  scenario_dirs s12
  REPO="$TMP/s12/repo"; make_repo "$REPO"
  write_workflow fan-cascade "$(cat <<'YAML'
name: fan-cascade
steps:
  - id: build
    type: fan_out
    lanes:
      - id: left
        steps:
          - {id: gate, type: command, max_retries: 0, run: 'git commit -q --allow-empty -m left && git config --get vincent.gate.go'}
      - id: right
        steps:
          - {id: gate, type: command, max_retries: 0, run: 'git commit -q --allow-empty -m right && git config --get vincent.gate.go'}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" fan-cascade "cascading retry")"

  wait_for_state "$TID" awaiting_children 60
  BLOCKED=""
  for _ in $(seq 1 90); do
    BLOCKED="$(api GET "/tasks?parent_id=$TID" \
      | jq '[.[] | select(.state == "blocked")] | length' | tr -d '\r')"
    [[ "$BLOCKED" == 2 ]] && break
    sleep 1
  done
  [[ "$BLOCKED" == 2 ]] || fail "expected both lanes blocked, got $BLOCKED"
  # The parent is still parked with them: a blocked lane holds the join open.
  STATE="$(api GET "/tasks/$TID" | jq -r .state | tr -d '\r')"
  [[ "$STATE" == "awaiting_children" ]] || fail "the parent left the join: $STATE"

  # One write, on the repository both lanes' worktrees share.
  git -C "$REPO" config vincent.gate.go 1

  # One retry, on the parent — not on either lane.
  OUT="$(api_status POST "/tasks/$TID/retry" '{}')"
  STATUS="${OUT%%$'\n'*}"; BODY="${OUT#*$'\n'}"
  [[ "$STATUS" == 200 ]] || fail "retry on the parked parent -> HTTP $STATUS: $BODY"
  N="$(jq -r .retried_descendants <<<"$BODY" | tr -d '\r')"
  [[ "$N" == 2 ]] || fail "retried_descendants = $N, want 2 — one per blocked lane"
  # Read from the retry's own response, not from a fresh GET: the parked
  # parent's row is deliberately not written on this path, and by the time a
  # second request lands the re-admitted lanes may already have woken it.
  STATE="$(jq -r .state <<<"$BODY" | tr -d '\r')"
  [[ "$STATE" == "awaiting_children" ]] \
    || fail "the retry moved the parked parent to $STATE; its row must not be written"

  wait_for_state "$TID" done 180
  BRANCH="$(api GET "/tasks/$TID" | jq -r .branch_name | tr -d '\r')"
  # Several lines, so `tr -d '\r'` for the same reason scenario 11 gives, and
  # captured before matching rather than piped into `grep -q` — see m7
  # scenario 4's comment.
  SUBJECTS="$(git -C "$REPO" log --format=%s "$BRANCH" | tr -d '\r')"
  for lane in left right; do
    grep -q "^Merge lane '$lane' of task " <<<"$SUBJECTS" \
      || fail "the join never merged lane $lane: $SUBJECTS"
  done
  daemon_down
  echo "=== scenario 12 PASS"
fi

echo "M6 GATE PASS"
