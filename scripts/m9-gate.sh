#!/usr/bin/env bash
# M9 phase gate (task 019; spec §7.9): prove via curl alone that a workflow
# can include another workflow.
#
#   1. an included fragment's steps run in the caller's own task and worktree,
#      in place, and a later caller step reads their `.Steps` entries
#   2. the expansion is frozen at creation: editing the fragment afterwards
#      reaches the next task and not the one already created
#   3. what the *caller's* defaults do, so scenario 4 has a baseline
#   4. the fragment's own `defaults:` travel with its steps
#   5. an include inside a loop body runs once per iteration
#   6. the refusals, each a 400 in front of the person creating the task:
#      unknown workflow, cycle, colliding step id, past include.max_depth,
#      and an expansion that breaks a nesting rule
#
# Every scenario uses command steps only, so no agent CLI is involved and the
# gate is as fast on CI as it is locally.
#
# The `run:` bodies are restricted to `exit N` and `git ...`, which is what
# makes this portable to pwsh the way m7 and m8 are (§8.3 leaves portability
# to the workflow author, and a gate running on three platforms is that
# author). It is also why the fragments record themselves as empty commits and
# git config keys: `printf > x` and `Set-Content` share no spelling, and both
# `git log` and `git config --get` can be asked afterwards what happened.
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

# api_status is api's counterpart for the refusals: it wants the failure, so it
# returns the status and body instead of treating a non-2xx as a gate failure.
api_status() { # api_status METHOD PATH [JSON_BODY] -> "STATUS<TAB>BODY"
  local method="$1" path="$2" body="${3:-}" out status
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  printf '%s\t%s' "$status" "$(printf '%s' "$out" | tr -d '\r\n')"
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

# refuse asserts that creating a task on WORKFLOW is a 400 whose message
# contains WANT. Every include failure is decided in the insert path, which is
# the property this proves: nothing gets a worktree and nothing fails halfway.
refuse() { # refuse PROJECT_ID WORKFLOW WANT
  local got status body
  got="$(api_status POST /tasks \
    "$(jq -cn --argjson p "$1" --arg w "$2" '{project_id: $p, workflow: $w, title: "refused"}')")"
  status="${got%%$'\t'*}"
  body="${got#*$'\t'}"
  [[ "$status" == 400 ]] || fail "creating a task on $2 returned $status, want 400: $body"
  [[ "$body" == *"$3"* ]] || fail "the refusal of $2 does not mention '$3': $body"
}

run_scenario() { # run_scenario N — honours VINCENT_GATE_SCENARIO
  [[ -z "$ONLY" || "$ONLY" == "$1" ]]
}

# --- include-shaped assertions --------------------------------------------
# An include writes no row of its own — it is gone before anything runs — so
# every assertion below reads the steps the expansion produced.

steps_json() { api GET "/tasks/$1/steps"; }

# `tr -d '\r'` on every helper that prints more than one line: jq writes CRLF
# on Windows, and while `$(...)` in MSYS bash drops the trailing one, the
# interior ones survive and make an exact multi-line comparison fail against a
# `$'a\nb'` literal (the m8 finding).
ran_steps() { # ran_steps TASK_ID -> the step ids that produced rows, in order
  steps_json "$1" | jq -r '[.[] | .step_id]
    | reduce .[] as $s ([]; if index($s) then . else . + [$s] end) | .[]' \
    | tr -d '\r'
}

# snapshot_steps prints each step of the task's own snapshot with the workflow
# it was spliced through, which is the §7.9 provenance the TUI badges.
snapshot_steps() { # snapshot_steps TASK_ID
  api GET "/tasks/$1" \
    | jq -r '.workflow_steps[] | "\(.id) \((.resolved_from // []) | join(">"))"' \
    | tr -d '\r'
}

attempts_for() { # attempts_for TASK_ID STEP_ID
  steps_json "$1" | jq --arg s "$2" '[.[] | select(.step_id == $s)] | length'
}

# subject_count is how many commits on the task's branch carry a subject.
subject_count() { # subject_count REPO TASK_ID SUBJECT
  local branch
  branch="$(api GET "/tasks/$2" | jq -r .branch_name)"
  git -C "$1" log --format=%s "$branch" | grep -cx -- "$3" || true
}

# --------------------------------------------------------------------------
# Scenario 1: the whole feature. `checks` is included into `feature`, and the
# task runs four steps in one worktree on one branch — the include is not one
# of them. `review` guards on `.Steps.build`, a step the *fragment* owns:
# templates render with missingkey=error, so a guard naming a step that never
# reached `.Steps` would be a condition error rather than a pass.
# --------------------------------------------------------------------------
if run_scenario 1; then
  echo "=== scenario 1: an included fragment's steps run in place"
  scenario_dirs s1
  REPO="$TMP/s1/repo"; make_repo "$REPO"
  write_workflow checks "$(cat <<'YAML'
name: checks
steps:
  - id: build
    type: command
    max_retries: 0
    run: git commit --allow-empty -m build
  - id: verify
    type: command
    max_retries: 0
    run: git config -f gate.ini checks.ran yes
YAML
)"
  write_workflow feature "$(cat <<'YAML'
name: feature
steps:
  - id: implement
    type: command
    max_retries: 0
    run: git commit --allow-empty -m implement
  - id: shared
    type: include
    workflow: checks
  - id: review
    type: command
    max_retries: 0
    if: '{{ eq .Steps.build.Status "succeeded" }}'
    run: git config --get -f gate.ini checks.ran
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" feature "include a fragment")"
  wait_for_state "$TID" done 60

  [[ "$(ran_steps "$TID")" == $'implement\nbuild\nverify\nreview' ]] \
    || fail "the run was $(ran_steps "$TID"), want the fragment's steps spliced in place"
  [[ "$(snapshot_steps "$TID")" == $'implement \nbuild checks\nverify checks\nreview ' ]] \
    || fail "the snapshot is $(snapshot_steps "$TID"), want the fragment's two steps attributed to it"
  # One branch, one worktree: `review` read a file `verify` wrote, and both
  # commits are on the caller's own branch.
  [[ "$(subject_count "$REPO" "$TID" build)" == 1 ]] \
    || fail "the fragment's commit is not on the caller's branch — it did not run in the caller's worktree"
  [[ "$(subject_count "$REPO" "$TID" implement)" == 1 ]] \
    || fail "the caller's own commit is missing"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 2: the expansion is frozen at creation (§5.3). The fragment is
# rewritten *after* the first task exists; the running task keeps what the
# registry said when it was created, and the next task picks the edit up.
# --------------------------------------------------------------------------
if run_scenario 2; then
  echo "=== scenario 2: an edit to the fragment does not reach an existing task"
  scenario_dirs s2
  REPO="$TMP/s2/repo"; make_repo "$REPO"
  write_workflow frag "$(cat <<'YAML'
name: frag
steps:
  - id: mark
    type: command
    max_retries: 0
    run: git commit --allow-empty -m original
YAML
)"
  write_workflow caller "$(cat <<'YAML'
name: caller
steps:
  - id: pull
    type: include
    workflow: frag
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  FIRST="$(create_task "$PID" caller "before the edit")"
  wait_for_state "$FIRST" done 60

  write_workflow frag "$(cat <<'YAML'
name: frag
steps:
  - id: mark
    type: command
    max_retries: 0
    run: git commit --allow-empty -m edited
YAML
)"
  # The registry watches the directory (§12.3); give the reload a moment to
  # land before the second task reads it.
  sleep 2
  SECOND="$(create_task "$PID" caller "after the edit")"
  wait_for_state "$SECOND" done 60

  [[ "$(subject_count "$REPO" "$FIRST" original)" == 1 ]] \
    || fail "the first task did not run the fragment as it stood at creation"
  [[ "$(subject_count "$REPO" "$FIRST" edited)" == 0 ]] \
    || fail "an edit to the fragment reached a task that already existed"
  [[ "$(subject_count "$REPO" "$SECOND" edited)" == 1 ]] \
    || fail "the second task did not pick up the edited fragment"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 3: the baseline for scenario 4. `caller` says `max_retries: 2`, so
# a failing step of its own gets three attempts. Pinning that here is what
# makes scenario 4's single attempt mean something: without it, "one attempt"
# could just as well be a retry budget that never worked.
# --------------------------------------------------------------------------
if run_scenario 3; then
  echo "=== scenario 3: what the caller's own defaults do"
  scenario_dirs s3
  REPO="$TMP/s3/repo"; make_repo "$REPO"
  write_workflow frag "$(cat <<'YAML'
name: frag
defaults:
  max_retries: 0
steps:
  - id: boom
    type: command
    run: exit 1
YAML
)"
  write_workflow caller "$(cat <<'YAML'
name: caller
defaults:
  max_retries: 2
steps:
  - id: own
    type: command
    run: exit 1
  - id: pull
    type: include
    workflow: frag
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"

  OWN="$(create_task "$PID" caller "the caller's own step")"
  wait_for_state "$OWN" blocked 60
  [[ "$(attempts_for "$OWN" own)" == 3 ]] \
    || fail "the caller's own step got $(attempts_for "$OWN" own) attempts, want its defaults' 3"

  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 4: the fragment's own `defaults:` travel with its steps. The same
# `max_retries: 2` caller as scenario 3, but the failing step comes from a
# fragment that says 0 — so it must get one attempt, not three. Otherwise a
# fragment behaves differently depending on who included it, which is the
# whole failure decision 7 exists to prevent.
# --------------------------------------------------------------------------
if run_scenario 4; then
  echo "=== scenario 4: the included step spends the fragment's retry budget"
  scenario_dirs s4
  REPO="$TMP/s4/repo"; make_repo "$REPO"
  write_workflow frag "$(cat <<'YAML'
name: frag
defaults:
  max_retries: 0
steps:
  - id: boom
    type: command
    run: exit 1
YAML
)"
  write_workflow caller "$(cat <<'YAML'
name: caller
defaults:
  max_retries: 2
steps:
  - id: pull
    type: include
    workflow: frag
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" caller "the fragment's budget")"
  wait_for_state "$TID" blocked 60
  [[ "$(attempts_for "$TID" boom)" == 1 ]] \
    || fail "the fragment's step got $(attempts_for "$TID" boom) attempts, want the fragment's own budget of 1"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 5: an include inside a loop body. This is the payoff of splicing
# rather than nesting — a fragment is reusable in the one place a `fan_out`
# lane could never be — and the spliced step has to run once per iteration.
# --------------------------------------------------------------------------
if run_scenario 5; then
  echo "=== scenario 5: an include inside a loop body"
  scenario_dirs s5
  REPO="$TMP/s5/repo"; make_repo "$REPO"
  write_workflow frag "$(cat <<'YAML'
name: frag
steps:
  - id: tick
    type: command
    max_retries: 0
    run: git commit --allow-empty -m tick
YAML
)"
  write_workflow spin "$(cat <<'YAML'
name: spin
steps:
  - id: repeat
    type: loop
    count: 3
    steps:
      - id: pull
        type: include
        workflow: frag
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"
  TID="$(create_task "$PID" spin "include in a loop")"
  wait_for_state "$TID" done 60

  [[ "$(subject_count "$REPO" "$TID" tick)" == 3 ]] \
    || fail "the included step ran $(subject_count "$REPO" "$TID" tick) times, want once per iteration"
  [[ "$(attempts_for "$TID" pull)" == 0 ]] \
    || fail "the include wrote rows of its own; it is resolved away before anything runs"
  daemon_down
fi

# --------------------------------------------------------------------------
# Scenario 6: the refusals. Every one is decided in the insert path, so the
# person creating the task is told before a worktree exists — which is the
# whole reason includes are resolved at creation rather than mid-run.
#
# `deep` needs a lowered ceiling to cross, so this scenario pins
# include.max_depth to 1 and chains two levels.
# --------------------------------------------------------------------------
if run_scenario 6; then
  echo "=== scenario 6: every include failure is a 400 at creation"
  scenario_dirs s6
  REPO="$TMP/s6/repo"; make_repo "$REPO"
  cat > "$CONFIG_DIR/config.yaml" <<EOF
include:
  max_depth: 1
EOF
  write_workflow ghost "$(cat <<'YAML'
name: ghost
steps:
  - id: pull
    type: include
    workflow: nowhere
YAML
)"
  write_workflow alpha "$(cat <<'YAML'
name: alpha
steps:
  - id: pull
    type: include
    workflow: beta
YAML
)"
  write_workflow beta "$(cat <<'YAML'
name: beta
steps:
  - id: back
    type: include
    workflow: alpha
YAML
)"
  write_workflow shared "$(cat <<'YAML'
name: shared
steps:
  - id: clash
    type: command
    max_retries: 0
    run: exit 0
YAML
)"
  write_workflow collide "$(cat <<'YAML'
name: collide
steps:
  - id: clash
    type: command
    max_retries: 0
    run: exit 0
  - id: pull
    type: include
    workflow: shared
YAML
)"
  write_workflow mid "$(cat <<'YAML'
name: mid
steps:
  - id: pull
    type: include
    workflow: shared
YAML
)"
  write_workflow deep "$(cat <<'YAML'
name: deep
steps:
  - id: pull
    type: include
    workflow: mid
YAML
)"
  write_workflow innerloop "$(cat <<'YAML'
name: innerloop
steps:
  - id: spin
    type: loop
    count: 2
    steps:
      - id: work
        type: command
        max_retries: 0
        run: exit 0
YAML
)"
  write_workflow nests "$(cat <<'YAML'
name: nests
steps:
  - id: outer
    type: loop
    count: 2
    steps:
      - id: pull
        type: include
        workflow: innerloop
YAML
)"
  daemon_up
  PID="$(register_project "$REPO")"

  refuse "$PID" ghost   'not found'
  refuse "$PID" alpha   'workflow cycle'
  refuse "$PID" collide 'twice'
  refuse "$PID" deep    'include.max_depth'
  refuse "$PID" nests   'expanded'
  daemon_down
fi

echo
echo "M9 GATE PASSED"
