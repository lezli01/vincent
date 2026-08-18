#!/usr/bin/env bash
# M8 phase gate (task 016; spec §7.8): prove via curl alone that loops behave
# as specified.
#
#   1. `count:` runs its body that many times and the workflow carries on
#   2. converge — an `allow_failure` probe, a `break` reading it, a repair
#   3. a `for_each` list longer than max_iterations blocks with `loop_limit`
#   4. `for_each` over a static list, one iteration per item, in order
#   5. `for_each` over a list a step discovered at run time
#   6. an empty `for_each` list succeeds having run nothing
#   7. a `condition` inside a body ends that iteration, not the loop
#   8. a hard-killed daemon resumes the loop mid-iteration, not at iteration 1
#
# Every scenario uses command steps only, so no agent CLI is involved and the
# gate is as fast on CI as it is locally.
#
# The `run:` bodies are restricted to `exit N` and `git ...`, which is what
# makes this portable to pwsh the way m7 is and m6 is not (§8.3 leaves
# portability to the workflow author, and a gate running on three platforms is
# that author). It is also why the loops count themselves in empty commits:
# `printf > x` and `Set-Content` share no spelling, and `git log` can be asked
# afterwards what happened.
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

run_scenario() { # run_scenario N — honours VINCENT_GATE_SCENARIO
  [[ -z "$ONLY" || "$ONLY" == "$1" ]]
}

# --- loop-shaped assertions -----------------------------------------------
# A loop writes no row of its own; everything below reads the body's rows,
# which is the same derivation the engine makes (§7.8 decision 7).

steps_json() { api GET "/tasks/$1/steps"; }

# `tr -d '\r'` on every helper below that prints more than one line: jq writes
# CRLF on Windows, and while `$(...)` in MSYS bash drops the trailing one, the
# interior ones survive and make an exact multi-line comparison fail against a
# `$'a\nb'` literal. Single-line captures are unaffected, which is why every
# other gate has always passed there and only these assertions did not.
rows_for() { # rows_for TASK_ID STEP_ID
  steps_json "$1" | jq -r --arg s "$2" '.[] | select(.step_id == $s) | "\(.iteration) \(.state)"' \
    | tr -d '\r'
}

# iterations_ran is how many distinct iterations the loop produced rows for.
iterations_ran() { # iterations_ran TASK_ID
  steps_json "$1" | jq '[.[] | select(.iteration > 0) | .iteration] | unique | length'
}

# items_ran prints the for_each item of each iteration, in iteration order.
items_ran() { # items_ran TASK_ID
  steps_json "$1" | jq -r '[.[] | select(.loop_item != null)]
    | group_by(.iteration) | sort_by(.[0].iteration) | .[] | .[0].loop_item' \
    | tr -d '\r'
}

step_field() { # step_field TASK_ID STEP_ID FIELD
  steps_json "$1" | jq -r --arg s "$2" --arg f "$3" \
    '[.[] | select(.step_id == $s)] | last | .[$f] // "null"'
}

# subject_count is how many commits on the task's branch carry a subject.
subject_count() { # subject_count REPO TASK_ID SUBJECT
  local branch
  branch="$(api GET "/tasks/$2" | jq -r .branch_name)"
  git -C "$1" log --format=%s "$branch" | grep -cx -- "$3" || true
}

# --------------------------------------------------------------------------
# Scenario 1: `count:` runs its body that many times, then the workflow
# carries on. The loop writes no row of its own, and each body row carries a
# 1-based iteration.
# --------------------------------------------------------------------------
if run_scenario 1; then
  echo "=== scenario 1: count runs its body three times"
  scenario_dirs s1
  REPO="$TMP/s1/repo"; make_repo "$REPO"
  write_workflow count-to-three "$(cat <<'YAML'
name: count-to-three
steps:
  - id: spin
    type: loop
    count: 3
    steps:
      - id: tick
        type: command
        max_retries: 0
        run: git commit --allow-empty -m tick
  - id: after
    type: command
    max_retries: 0
    run: git commit --allow-empty -m after
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" count-to-three "count to three")"
  wait_for_state "$TID" done 60

  [[ "$(iterations_ran "$TID")" == 3 ]] \
    || fail "the body ran $(iterations_ran "$TID") iterations, want 3"
  [[ "$(subject_count "$REPO" "$TID" tick)" == 3 ]] \
    || fail "the body did its work $(subject_count "$REPO" "$TID" tick) times, want 3"
  [[ "$(subject_count "$REPO" "$TID" after)" == 1 ]] \
    || fail "the step after the loop did not run exactly once"
  [[ "$(rows_for "$TID" spin)" == "" ]] \
    || fail "the loop wrote a step_runs row of its own; its outcome is derived"
  [[ "$(rows_for "$TID" tick)" == $'1 succeeded\n2 succeeded\n3 succeeded' ]] \
    || fail "body rows are not one per iteration, 1-based: $(rows_for "$TID" tick)"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 2: converge. The probe is `allow_failure`, so its red row is data
# rather than a block (§7.2); the `break` two lines below reads that row,
# which is the whole of decision 9's positional visibility rule — under an
# index comparison a body step cannot see its own body and this never breaks.
#
# `git rev-parse HEAD~2` succeeds once the branch has three commits, so the
# probe goes green on the third pass.
# --------------------------------------------------------------------------
if run_scenario 2; then
  echo "=== scenario 2: converge and break"
  scenario_dirs s2
  REPO="$TMP/s2/repo"; make_repo "$REPO"
  write_workflow converge "$(cat <<'YAML'
name: converge
steps:
  - id: green
    type: loop
    count: 8
    steps:
      - id: suite
        type: command
        max_retries: 0
        allow_failure: true
        run: git rev-parse --verify HEAD~2
      - id: passed
        type: break
        if: '{{ eq (index .Steps "suite").ExitCode 0 }}'
      - id: repair
        type: command
        max_retries: 0
        run: git commit --allow-empty -m repair
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" converge "fix until green")"
  wait_for_state "$TID" done 60

  [[ "$(iterations_ran "$TID")" == 3 ]] \
    || fail "converged in $(iterations_ran "$TID") iterations, want 3"
  [[ "$(subject_count "$REPO" "$TID" repair)" == 2 ]] \
    || fail "repaired $(subject_count "$REPO" "$TID" repair) times, want 2 — the third pass must break"
  # A taken break is `stopped`: the loop ended there, on purpose.
  [[ "$(step_field "$TID" passed state)" == "stopped" ]] \
    || fail "the break's last row is $(step_field "$TID" passed state), want stopped"
  [[ "$(rows_for "$TID" passed)" == $'1 succeeded\n2 succeeded\n3 stopped' ]] \
    || fail "the break did not evaluate once per iteration: $(rows_for "$TID" passed)"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 3: a `for_each` list longer than max_iterations blocks with
# `loop_limit`, before iteration 1. Running out of tries is not a decision the
# workflow made, so it must not succeed and must not silently advance
# (decision 5).
# --------------------------------------------------------------------------
if run_scenario 3; then
  echo "=== scenario 3: over the ceiling blocks with loop_limit"
  scenario_dirs s3
  REPO="$TMP/s3/repo"; make_repo "$REPO"
  cat > "$CONFIG_DIR/config.yaml" <<'YAML'
loop:
  max_iterations: 2
YAML
  write_workflow too-many "$(cat <<'YAML'
name: too-many
steps:
  - id: visit
    type: loop
    for_each: [alpha, beta, gamma]
    steps:
      - id: touch
        type: command
        max_retries: 0
        run: git commit --allow-empty -m touch
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" too-many "three items, room for two")"
  wait_for_state "$TID" blocked 60

  [[ "$(api GET "/tasks/$TID" | jq -r .block_reason)" == "loop_limit" ]] \
    || fail "block_reason is not loop_limit"
  [[ "$(iterations_ran "$TID")" == 0 ]] \
    || fail "the loop ran before blocking; the ceiling is checked before iteration 1"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 4: `for_each` over a static list — one iteration per item, in list
# order, with the item recorded on the row rather than re-derived (decision 8).
# --------------------------------------------------------------------------
if run_scenario 4; then
  echo "=== scenario 4: for_each over a static list"
  scenario_dirs s4
  REPO="$TMP/s4/repo"; make_repo "$REPO"
  write_workflow each-static "$(cat <<'YAML'
name: each-static
steps:
  - id: visit
    type: loop
    for_each: [alpha, beta, gamma]
    steps:
      - id: touch
        type: command
        max_retries: 0
        run: git commit --allow-empty -m {{ .Loop.Item }}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" each-static "once per item")"
  wait_for_state "$TID" done 60

  [[ "$(items_ran "$TID")" == $'alpha\nbeta\ngamma' ]] \
    || fail "loop_item column = $(items_ran "$TID"), want alpha/beta/gamma in list order"
  for item in alpha beta gamma; do
    [[ "$(subject_count "$REPO" "$TID" "$item")" == 1 ]] \
      || fail "item $item did not get exactly one iteration's work"
  done
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 5: `for_each` over a list a step discovered at run time. This is
# the case `fan_out` cannot serve — its lane list has to be static in the
# snapshot, which is exactly what makes its creation-time checks possible
# (decision 4).
#
# `git tag -l` prints one tag per line, which is a list produced by a command
# without leaving the `exit N` / `git ...` vocabulary.
# --------------------------------------------------------------------------
if run_scenario 5; then
  echo "=== scenario 5: for_each over a list a step found"
  scenario_dirs s5
  REPO="$TMP/s5/repo"; make_repo "$REPO"
  for tag in one two; do git -C "$REPO" tag "mod-$tag"; done
  write_workflow each-found "$(cat <<'YAML'
name: each-found
steps:
  - id: discover
    type: command
    max_retries: 0
    run: git tag -l mod-*
  - id: visit
    type: loop
    for_each: '{{ .Steps.discover.Result }}'
    steps:
      - id: touch
        type: command
        max_retries: 0
        run: git commit --allow-empty -m {{ .Loop.Item }}
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" each-found "once per module")"
  wait_for_state "$TID" done 60

  [[ "$(items_ran "$TID")" == $'mod-one\nmod-two' ]] \
    || fail "loop_item column = $(items_ran "$TID"), want the two tags the step printed"
  [[ "$(subject_count "$REPO" "$TID" mod-one)" == 1 ]] \
    || fail "the discovered item mod-one did not get an iteration"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 6: an empty `for_each` list succeeds having run nothing. The loop
# had nothing to iterate, which is a correct answer rather than a failure —
# the same posture §7.5 takes for a group whose every sub-step was guarded off.
# --------------------------------------------------------------------------
if run_scenario 6; then
  echo "=== scenario 6: an empty list is a no-op success"
  scenario_dirs s6
  REPO="$TMP/s6/repo"; make_repo "$REPO"
  write_workflow each-none "$(cat <<'YAML'
name: each-none
steps:
  - id: discover
    type: command
    max_retries: 0
    run: git tag -l absent-*
  - id: visit
    type: loop
    for_each: '{{ .Steps.discover.Result }}'
    steps:
      - id: touch
        type: command
        max_retries: 0
        run: exit 9
  - id: after
    type: command
    max_retries: 0
    run: git commit --allow-empty -m after
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" each-none "nothing to do")"
  wait_for_state "$TID" done 60

  [[ "$(iterations_ran "$TID")" == 0 ]] \
    || fail "an empty list still ran $(iterations_ran "$TID") iterations"
  [[ "$(subject_count "$REPO" "$TID" after)" == 1 ]] \
    || fail "the workflow did not carry on past an empty loop"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 7: a `condition` inside a body ends *that iteration* and the loop
# carries on — which is `continue`, spelled with the meaning §7.7 already gave
# the word and no third step type (decision 3).
# --------------------------------------------------------------------------
if run_scenario 7; then
  echo "=== scenario 7: a condition ends the iteration, not the loop"
  scenario_dirs s7
  REPO="$TMP/s7/repo"; make_repo "$REPO"
  write_workflow skip-tails "$(cat <<'YAML'
name: skip-tails
steps:
  - id: spin
    type: loop
    count: 3
    steps:
      - id: head
        type: command
        max_retries: 0
        run: git commit --allow-empty -m head
      - id: onward
        type: condition
        if: '{{ .Loop.IsLast }}'
      - id: tail
        type: command
        max_retries: 0
        run: git commit --allow-empty -m tail
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" skip-tails "continue, spelled condition")"
  wait_for_state "$TID" done 60

  [[ "$(subject_count "$REPO" "$TID" head)" == 3 ]] \
    || fail "head ran $(subject_count "$REPO" "$TID" head) times, want 3 — the loop must carry on"
  [[ "$(subject_count "$REPO" "$TID" tail)" == 1 ]] \
    || fail "tail ran $(subject_count "$REPO" "$TID" tail) times, want 1 — only the last iteration continues"
  [[ "$(rows_for "$TID" onward)" == $'1 stopped\n2 stopped\n3 succeeded' ]] \
    || fail "the condition's rows are $(rows_for "$TID" onward), want two stops then a pass"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 8: a hard-killed daemon resumes the loop mid-iteration. Position is
# derived from the rows, never persisted (decision 7), so the restarted daemon
# must skip the body steps whose latest attempt succeeded and carry on from
# the failed one — in the iteration it was in, not at iteration 1.
#
# The failure that holds the task still is portable (`exit 7`); the kill is
# real (SIGKILL, then §12.4 recovery on restart). What the two together prove
# is that nothing outside the rows was carrying the loop's position across the
# process boundary.
# --------------------------------------------------------------------------
if run_scenario 8; then
  echo "=== scenario 8: crash and resume mid-iteration"
  scenario_dirs s8
  REPO="$TMP/s8/repo"; make_repo "$REPO"
  # `boom` fails until the marker tag exists, which the gate creates while the
  # daemon is dead — standing in for the human who fixed whatever was wrong.
  write_workflow resume "$(cat <<'YAML'
name: resume
steps:
  - id: spin
    type: loop
    count: 2
    steps:
      - id: tick
        type: command
        max_retries: 0
        run: git commit --allow-empty -m tick
      - id: boom
        type: command
        max_retries: 0
        run: git rev-parse --verify refs/tags/fixed
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" resume "resume where it stopped")"
  wait_for_state "$TID" blocked 60

  [[ "$(subject_count "$REPO" "$TID" tick)" == 1 ]] \
    || fail "the loop ran past its failed body step"

  echo "== hard-kill the daemon (pid from daemon.json)"
  DAEMON_PID="$(jq -r .pid "$DATA_DIR/daemon.json")"
  if [[ "${OS:-}" == "Windows_NT" ]]; then
    # Git Bash kill -9 can't reliably kill a native process; // stops MSYS
    # from mangling the flags into paths (phase 2 decision).
    taskkill //F //PID "$DAEMON_PID" >/dev/null
  else
    kill -9 "$DAEMON_PID"
  fi
  for _ in $(seq 1 20); do
    kill -0 "$DAEMON_PID" 2>/dev/null || break
    sleep 0.5
  done

  git -C "$REPO" tag fixed
  daemon_up
  api POST "/tasks/$TID/retry" '{}' >/dev/null
  wait_for_state "$TID" done 60

  # Two ticks, not three: iteration 1's `tick` had already succeeded, so the
  # resumed loop went straight to `boom`.
  [[ "$(subject_count "$REPO" "$TID" tick)" == 2 ]] \
    || fail "tick ran $(subject_count "$REPO" "$TID" tick) times, want 2 — a resumed loop must not re-run a succeeded body step"
  [[ "$(iterations_ran "$TID")" == 2 ]] \
    || fail "the resumed loop ran $(iterations_ran "$TID") iterations, want 2"
  [[ "$(rows_for "$TID" boom)" == $'1 failed\n1 succeeded\n2 succeeded' ]] \
    || fail "boom's rows are $(rows_for "$TID" boom), want a failed then a fresh attempt in iteration 1"
  daemon_down
fi

echo
echo "M8 GATE PASSED"
