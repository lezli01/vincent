#!/usr/bin/env bash
# M7 phase gate (task 015; spec §7.7): prove via curl alone that conditions
# between steps behave as specified.
#
#   1. `if: false` skips a step and the workflow carries on
#   2. a `condition` step ends the run, and the task is `done`
#   3. `allow_failure` advances, and the next step's guard reads the exit code
#      the failure left behind
#   4. a guarded-off fan-out lane is not spawned; the rest still merge
#   5. every lane guarded off is a no-op success that does not park
#   6. a guard that renders no verdict blocks with `condition_error`, once,
#      whatever the retry budget says
#   7. a retry re-evaluates the guard rather than replaying its verdict
#
# Every scenario uses command steps only, so no agent CLI is involved and the
# gate is as fast on CI as it is locally.
#
# The `run:` bodies are restricted to what `/bin/sh` and `pwsh` both accept —
# `exit N`, `git ...`, and nothing else. §8.3 leaves portability to the
# workflow author, and a gate that has to run on all three platforms is that
# author. This is why the lanes commit with `--allow-empty` instead of writing
# a file: `printf > x` and `Set-Content` share no spelling, and the join has
# something to merge either way.
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


# step_state prints one step run's state, selected by step id. A step with
# several attempts prints them all, newest last.
step_state() { # step_state TASK_ID STEP_ID
  api GET "/tasks/$1/steps" | jq -r --arg s "$2" '.[] | select(.step_id == $s) | .state'
}

step_field() { # step_field TASK_ID STEP_ID FIELD
  api GET "/tasks/$1/steps" | jq -r --arg s "$2" --arg f "$3" \
    '[.[] | select(.step_id == $s)] | last | .[$f] // "null"'
}

step_count() { # step_count TASK_ID
  api GET "/tasks/$1/steps" | jq 'length'
}

# --------------------------------------------------------------------------
# Scenario 1: a false `if:` skips its step and the workflow carries on.
# --------------------------------------------------------------------------
if run_scenario 1; then
  echo "=== scenario 1: a guard skips a step"
  scenario_dirs s1
  REPO="$TMP/s1/repo"; make_repo "$REPO"
  write_workflow guard-skip "$(cat <<'YAML'
name: guard-skip
steps:
  - id: first
    type: command
    max_retries: 0
    run: exit 0
  - id: skipped
    type: command
    max_retries: 0
    if: "{{ false }}"
    run: exit 1
  - id: last
    type: command
    max_retries: 0
    run: exit 0
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" guard-skip "guard skips a step")"
  wait_for_state "$TID" done 60

  [[ "$(step_state "$TID" skipped)" == "skipped" ]] \
    || fail "the guarded step is not recorded as skipped"
  [[ "$(step_field "$TID" skipped skip_reason)" == "condition" ]] \
    || fail "skip_reason is not 'condition'; a human skip must stay distinguishable"
  [[ "$(step_field "$TID" skipped transcript_path)" == "null" ]] \
    || fail "a skipped step opened a transcript"
  [[ "$(step_state "$TID" last)" == "succeeded" ]] \
    || fail "the workflow did not carry on past the skip"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 2: a `condition` step ends the run, successfully.
# --------------------------------------------------------------------------
if run_scenario 2; then
  echo "=== scenario 2: a condition step stops the workflow"
  scenario_dirs s2
  REPO="$TMP/s2/repo"; make_repo "$REPO"
  write_workflow early-finish "$(cat <<'YAML'
name: early-finish
steps:
  - id: first
    type: command
    max_retries: 0
    run: exit 0
  - id: gate
    type: condition
    if: "{{ false }}"
  - id: never
    type: command
    max_retries: 0
    run: exit 1
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" early-finish "condition ends the run")"
  wait_for_state "$TID" done 60

  [[ "$(step_state "$TID" gate)" == "stopped" ]] \
    || fail "the condition step is not recorded as stopped"
  [[ "$(step_count "$TID")" == 2 ]] \
    || fail "expected 2 step rows: the steps after a stop are never considered"
  CURRENT="$(api GET "/tasks/$TID" | jq -r .current_step)"
  [[ "$CURRENT" == 3 ]] \
    || fail "current_step = $CURRENT, want 3 (len(steps)) so completion runs the ordinary path"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 3: allow_failure advances and feeds the next step's guard.
#
# This is the pairing the feature stands on: without allow_failure a guard can
# only read what a human typed at creation, never what the run discovered.
# --------------------------------------------------------------------------
if run_scenario 3; then
  echo "=== scenario 3: allow_failure advances and the next guard reads it"
  scenario_dirs s3
  REPO="$TMP/s3/repo"; make_repo "$REPO"
  write_workflow probe "$(cat <<'YAML'
name: probe
steps:
  - id: probe
    type: command
    max_retries: 0
    allow_failure: true
    run: exit 3
  - id: fixup
    type: command
    max_retries: 0
    if: '{{ ne (index .Steps "probe").ExitCode 0 }}'
    run: exit 0
  - id: unreached
    type: command
    max_retries: 0
    if: '{{ eq (index .Steps "probe").ExitCode 0 }}'
    run: exit 1
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" probe "allow_failure feeds a guard")"
  wait_for_state "$TID" done 60

  [[ "$(step_state "$TID" probe)" == "failed" ]] \
    || fail "allow_failure relabelled the row; the failure still happened"
  [[ "$(step_field "$TID" probe failure_reason)" == "nonzero_exit" ]] \
    || fail "the probe's failure reason was not preserved"
  [[ "$(step_state "$TID" fixup)" == "succeeded" ]] \
    || fail "the guard did not see the probe's exit code"
  [[ "$(step_state "$TID" unreached)" == "skipped" ]] \
    || fail "the complementary guard should have skipped its step"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 4: a guarded-off lane is not spawned; the rest still merge.
# --------------------------------------------------------------------------
if run_scenario 4; then
  echo "=== scenario 4: a fan-out lane is guarded off"
  scenario_dirs s4
  REPO="$TMP/s4/repo"; make_repo "$REPO"
  write_workflow lane-guard "$(cat <<'YAML'
name: lane-guard
steps:
  - id: build
    type: fan_out
    lanes:
      - id: api
        steps:
          - id: work
            type: command
            max_retries: 0
            run: git commit --allow-empty -m lane-api
      - id: skipped
        if: "{{ false }}"
        steps:
          - id: work
            type: command
            max_retries: 0
            run: git commit --allow-empty -m lane-skipped
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" lane-guard "one lane guarded off")"
  wait_for_state "$TID" done 240

  KIDS="$(api GET "/tasks?parent_id=$TID" | jq 'length')"
  [[ "$KIDS" == 1 ]] || fail "expected 1 lane to be spawned, got $KIDS"
  BRANCH="$(api GET "/tasks/$TID" | jq -r .branch_name)"
  # Captured, not piped into `grep -q`. `grep -q` exits at its first match,
  # `git log` then dies of SIGPIPE writing the commits after it, and pipefail
  # reports that as a failed assertion over data that was correct all along —
  # 20% of runs here, because `lane-api` is the second subject of three. Same
  # trap release.yml's signature checks already work around.
  SUBJECTS="$(git -C "$REPO" log --format=%s "$BRANCH")"
  grep -qx lane-api <<<"$SUBJECTS" \
    || fail "the selected lane's commit is not on the parent's branch"
  if grep -qx lane-skipped <<<"$SUBJECTS"; then
    fail "the guarded-off lane ran anyway"
  fi
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 5: every lane guarded off is a no-op that does not park.
#
# The regression this guards against is specific: a parent parked in
# `awaiting_children` with no children is re-queued the moment the scheduler
# looks, spawns nothing, and parks again — a loop with no exit. Reaching
# `done` at all is the assertion.
# --------------------------------------------------------------------------
if run_scenario 5; then
  echo "=== scenario 5: every lane guarded off"
  scenario_dirs s5
  REPO="$TMP/s5/repo"; make_repo "$REPO"
  write_workflow no-lanes "$(cat <<'YAML'
name: no-lanes
steps:
  - id: build
    type: fan_out
    lanes:
      - id: none
        if: "{{ false }}"
        steps:
          - id: work
            type: command
            max_retries: 0
            run: git commit --allow-empty -m lane-none
  - id: after
    type: command
    max_retries: 0
    run: exit 0
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" no-lanes "no lane selected")"
  wait_for_state "$TID" done 120

  KIDS="$(api GET "/tasks?parent_id=$TID" | jq 'length')"
  [[ "$KIDS" == 0 ]] || fail "expected no lanes to be spawned, got $KIDS"
  [[ "$(step_state "$TID" build)" == "succeeded" ]] \
    || fail "the no-op fan_out recorded no successful row"
  [[ "$(step_state "$TID" after)" == "succeeded" ]] \
    || fail "the workflow did not carry on past the no-op fan_out"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 6: a guard with no verdict blocks once, whatever the budget says.
# --------------------------------------------------------------------------
if run_scenario 6; then
  echo "=== scenario 6: a guard that renders no verdict"
  scenario_dirs s6
  REPO="$TMP/s6/repo"; make_repo "$REPO"
  write_workflow bad-guard "$(cat <<'YAML'
name: bad-guard
steps:
  - id: step
    type: command
    max_retries: 3
    if: "{{ 7 }}"
    run: exit 0
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" bad-guard "a guard with no verdict")"
  wait_for_state "$TID" blocked 60

  REASON="$(api GET "/tasks/$TID" | jq -r .block_reason)"
  [[ "$REASON" == "condition_error" ]] || fail "block_reason = $REASON, want condition_error"
  [[ "$(step_count "$TID")" == 1 ]] \
    || fail "a guard error was retried; it is the one reason that does not run the §7.2 budget"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 7: a retry re-evaluates the guard rather than replaying its verdict.
#
# `.Step.Attempt` is 1 on the first pass and 2 on the retry, so a cached
# verdict would run the step twice and a re-evaluated one skips it.
# --------------------------------------------------------------------------
if run_scenario 7; then
  echo "=== scenario 7: a retry re-evaluates the guard"
  scenario_dirs s7
  REPO="$TMP/s7/repo"; make_repo "$REPO"
  write_workflow reeval "$(cat <<'YAML'
name: reeval
steps:
  - id: once
    type: command
    max_retries: 0
    if: "{{ eq .Step.Attempt 1 }}"
    run: exit 4
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" reeval "guards are never sticky")"
  wait_for_state "$TID" blocked 60

  api POST "/tasks/$TID/retry" '{}' >/dev/null
  wait_for_state "$TID" done 60

  LAST="$(step_field "$TID" once state)"
  [[ "$LAST" == "skipped" ]] \
    || fail "the retry's row is $LAST, want skipped — the guard was not re-evaluated"
  [[ "$(step_field "$TID" once skip_reason)" == "condition" ]] \
    || fail "the retry's skip is not attributed to the guard"
  daemon_down
fi

echo
echo "M7 GATE PASSED"
