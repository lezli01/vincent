#!/usr/bin/env bash
# M5 phase gate (T5.7; spec §19 M5): prove via curl alone that a workflow
# whose steps name `agent: cursor` runs unattended to completion (scenario 1),
# that cursor's stderr-only failure mode is diagnosable (scenario 2), that an
# installed-but-unauthenticated CLI is visible *before* a task is created
# (scenario 3), and that a `restricted` step is refused rather than silently
# downgraded where the sandbox is unavailable (scenario 4).
#
# Runs against the committed fakeagent so CI never calls a real API; run
# manually with VINCENT_GATE_AGENT=cursor to exercise the real cursor-agent
# (scenarios 1 and 2 only — 3 and 4 assert vincent's own behavior, and
# scenario 3 would mean signing the operator out). That manual run is the
# other half of the gate and is walked through in
# docs/gates/m5-gate.md. VINCENT_GATE_SCENARIO=1|2|3|4 runs one
# scenario for debugging.
#
# Each scenario gets fresh config/data/repo dirs and its own daemon:
# FAKEAGENT_* is read from the daemon's environment, so it can only change at
# daemon start, and isolation keeps one scenario's leftovers out of another's
# assertions (PR G decision, inherited from m2-gate.sh).
#
# Requirements: bash, go, git, curl, jq (all present on the GitHub runners,
# incl. Git Bash on Windows).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
BIN="$TMP/bin"

VINCENT="$BIN/vincent"
FAKEAGENT="$BIN/fakeagent"
WINDOWS=0
if [[ "${OS:-}" == "Windows_NT" ]]; then
  VINCENT+=".exe"
  FAKEAGENT+=".exe"
  WINDOWS=1
fi

REAL_AGENT=0
[[ "${VINCENT_GATE_AGENT:-fake}" == "cursor" ]] && REAL_AGENT=1

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

# The daemon inherits the environment of the shell that starts it and hands it
# to the agent process unchanged: `RunSpec.Env` is never populated for agent
# steps (internal/taskrun/steps.go), the adapter only overrides a non-nil one
# (internal/agent/cursor/cursor.go), and the detached spawn sets no environment
# of its own (internal/daemon/spawn.go). Everything cursor-agent reads from its
# environment therefore comes from whoever started vincent, which is why a hand
# run is only reproducible if its environment is recorded alongside its result.
# USERPROFILE is on the list because relocating it is what unblocks a Windows
# real-CLI run (see the hook note in docs/gates/m5-gate.md).
#
# Read the *exported* environment rather than shell variables: bash assigns
# SHELL the current user's login shell when it did not inherit one (bash(1)) and
# does not export it, so "$SHELL" reports Git Bash even from a PowerShell parent
# that exported nothing. `printenv` reads what a native child actually inherits.
show_env() { # show_env NAME
  printf '   %s=%s\n' "$1" "$(printenv "$1" || echo '<not exported>')"
}
echo "== launch environment (copy into the docs/gates/m5-gate.md row)"
show_env SHELL
show_env MSYSTEM
show_env USERPROFILE
if (( REAL_AGENT )); then
  # cursor-agent installs as a .cmd shim on Windows. Go's exec.LookPath honors
  # PATHEXT and resolves it; bash does not resolve a bare name to .cmd, so
  # asking bash directly reports "not found" for a binary the daemon runs
  # perfectly well. Ask cmd.exe there instead — the doubled slash stops MSYS
  # rewriting /c as a path.
  if (( WINDOWS )); then
    agent_version="$(cmd //c cursor-agent --version 2>/dev/null | tr -d '\r' | head -1)"
  else
    agent_version="$(cursor-agent --version 2>/dev/null | head -1)"
  fi
  echo "   cursor-agent ${agent_version:-<could not resolve>}"
fi

echo "== build vincent + fakeagent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent ./cmd/fakeagent)

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

# cursor_config writes a config.yaml pointing the cursor adapter at either the
# fake binary or the real cursor-agent on PATH.
cursor_config() {
  local agent_path=""
  (( REAL_AGENT )) || agent_path="$(hostpath "$FAKEAGENT")"
  cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  cursor:
    path: "$agent_path"
EOF
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

register_project() { # register_project REPO_PATH -> id
  api POST /projects "$(jq -cn --arg p "$(hostpath "$1")" '{path: $p}')" | jq -r .id
}

# try_wait_for_state is wait_for_state without the exit: it returns 1 so the
# caller can diagnose *why* before failing. wait_for_state itself cannot be
# used for that — it calls fail, which exits the script.
try_wait_for_state() { # try_wait_for_state TASK_ID STATE TRIES
  local id="$1" want="$2" tries="$3" state=""
  for _ in $(seq 1 "$tries"); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    [[ "$state" == "blocked" || "$state" == "aborted" ]] && return 1
    sleep 1
  done
  return 1
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
# Scenario 1 — the M5 acceptance itself: agent(cursor) edits a tracked file,
# a command step commits it, the task reaches done and the branch carries the
# change. Spec §19 M5, §9.7.
# ---------------------------------------------------------------------------
scenario1() {
  echo "=== scenario 1: a cursor workflow runs unattended to completion"
  scenario_dirs s1

  local prompt model_line=""
  if (( REAL_AGENT )); then
    # Deliberately `auto`: the account's model list is not knowable here, and
    # §9.7 makes auto the adapter default anyway.
    model_line="  model: auto"
    prompt='Append one line reading "cursor was here" to README.md in the current directory. Do not do anything else.'
  else
    export FAKEAGENT_SCENARIO=success
    export FAKEAGENT_EDIT_FILE=README.md
    prompt='Task: {{.Task.Title}}'
  fi
  cursor_config

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/m5-flow.yaml" <<EOF
name: m5-flow
description: M5 gate — a cursor agent step, then a commit.
defaults:
  agent: cursor
  max_retries: 0
$model_line
steps:
  - id: edit
    type: agent
    prompt: |
      $prompt
  - id: commit
    type: command
    run: 'git add -A && git commit -m "m5 gate: cursor edit"'
EOF

  daemon_up

  local repo="$TMP/s1/repo"
  make_repo "$repo"

  echo "== assert the adapter is registered and reports its catalog (§9.6, §9.7)"
  local agents cursor_entry
  agents="$(api GET /agents)"
  cursor_entry="$(jq '.agents[] | select(.name == "cursor")' <<<"$agents")"
  [[ -n "$cursor_entry" ]] || fail "no cursor adapter in /v1/agents: $agents"
  jq -e '.available' <<<"$cursor_entry" >/dev/null || fail "cursor unavailable: $cursor_entry"
  # Efforts are empty by construction: effort lives in the model id (§9.7).
  [[ "$(jq '.efforts | length' <<<"$cursor_entry")" == "0" ]] \
    || fail "cursor reports efforts; §9.7 says it has none: $cursor_entry"
  [[ "$(jq -r .default_model <<<"$cursor_entry")" == "auto" ]] \
    || fail "cursor default_model is not auto: $cursor_entry"

  local project_id task_id
  project_id="$(register_project "$repo")"
  task_id="$(api POST /tasks "{\"project_id\":$project_id,\"workflow\":\"m5-flow\",\"title\":\"M5 gate flow\",\"description\":\"Edit and commit.\"}" | jq -r .id)"
  [[ "$task_id" =~ ^[0-9]+$ ]] || fail "task creation returned no id"

  echo "== wait for done"
  # A cursor-agent whose tool calls are all rejected still exits 0 and still
  # emits a clean result: the *agent* step succeeds and the commit step then
  # fails with "nothing to commit". Diagnose that here rather than leaving the
  # next reader with a bare nonzero_exit — on the machine this gate was
  # written on, two `pretooluse` hooks were registered in PowerShell syntax
  # while cursor-agent runs hooks through bash, so every edit was blocked.
  if ! try_wait_for_state "$task_id" done 300; then
    local commit_row
    commit_row="$(api GET "/tasks/$task_id/steps" | jq '[.[] | select(.step_id == "commit")][-1]')"
    if grep -q 'nothing to commit' <<<"$commit_row"; then
      fail "the agent step reported success but changed no file — cursor's tool
  calls are being rejected by a blocked hook. Confirm it with:
      cursor-agent -p 'create hi.txt' --output-format stream-json --trust --force --model auto
  and look for tool_call results carrying \"rejected\":{\"reason\":\"Hook blocked...\"}.
  On Windows the source is usually not a Cursor hook at all: Cursor imports
  Claude Code's hooks from ~/.claude/settings.json (and Claude plugin hooks),
  wraps each command in a PowerShell preamble, then evaluates that string with
  bash — so every imported hook errors, and an erroring hook blocks the tool.
  The trigger is MSYSTEM, printed at the top of this run: set it and Cursor uses
  bash, unset it and the same hooks are fine. You cannot drop it from inside Git
  Bash (the MSYS runtime re-injects it into every child), so the remedy works on
  the other half instead: run the daemon with USERPROFILE and HOME pointed at a
  scratch dir holding a junction to the real .cursor and no .claude.
  docs/gates/m5-gate.md carries the procedure."
    fi
    api GET "/tasks/$task_id" | jq . >&2
    fail "task $task_id never reached done"
  fi

  echo "== assert the step ran as cursor and recorded usage without cost"
  local steps edit_row
  steps="$(api GET "/tasks/$task_id/steps")"
  edit_row="$(jq '[.[] | select(.step_id == "edit")][-1]' <<<"$steps")"
  [[ "$(jq -r .state <<<"$edit_row")" == "succeeded" ]] || fail "edit step not succeeded: $edit_row"
  [[ "$(jq -r .agent <<<"$edit_row")" == "cursor" ]] || fail "step did not run as cursor: $edit_row"
  # §9.7: cursor reports no cost, and effort never reaches it.
  jq -e '.cost_usd == null' <<<"$edit_row" >/dev/null \
    || fail "cursor reported a cost; §9.7 says it reports none: $edit_row"
  [[ "$(jq -r '.effort // ""' <<<"$edit_row")" == "" ]] \
    || fail "an effort was recorded for a cursor step: $edit_row"
  [[ "$(jq -r '[.[] | select(.step_id == "commit")][-1].state' <<<"$steps")" == "succeeded" ]] \
    || fail "commit step not succeeded: $steps"

  echo "== assert the transcript carries cursor-shaped lines"
  local run_id transcript
  run_id="$(jq -r .id <<<"$edit_row")"
  transcript="$(api GET "/tasks/$task_id/steps/$run_id/transcript")"
  grep -q '"type":"result"' <<<"$transcript" || fail "transcript has no cursor result event"
  grep -q '"inputTokens"' <<<"$transcript" \
    || fail "transcript has no camelCase usage keys; is this really the cursor dialect?"

  echo "== assert the branch carries the edit"
  local branch
  branch="$(api GET "/tasks/$task_id" | jq -r .branch_name)"
  # Captured rather than piped into `grep -q` — see m7 scenario 4's comment.
  local subject files
  subject="$(git -C "$repo" log -1 --format=%s "$branch")"
  grep -q 'm5 gate: cursor edit' <<<"$subject" \
    || fail "branch $branch tip is not the gate commit"
  files="$(git -C "$repo" show --name-only --format= "$branch")"
  grep -qx 'README.md' <<<"$files" \
    || fail "the cursor step changed no tracked file"

  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  unset FAKEAGENT_SCENARIO FAKEAGENT_EDIT_FILE
}

# ---------------------------------------------------------------------------
# Scenario 2 — cursor's stderr-only failure. An invalid model exits nonzero
# with the reason on stderr and *no* result event at all, so the stderr tail
# is the whole diagnosis (§9.7, §18).
# ---------------------------------------------------------------------------
scenario2() {
  echo "=== scenario 2: a stderr-only failure stays diagnosable"
  scenario_dirs s2

  local model_line=""
  if (( REAL_AGENT )); then
    model_line="    model: definitely-not-a-real-model"
  else
    export FAKEAGENT_SCENARIO=no-result
  fi
  cursor_config

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/m5-bad-model.yaml" <<EOF
name: m5-bad-model
description: M5 gate — a cursor step that fails before emitting a result.
steps:
  - id: doomed
    type: agent
    agent: cursor
$model_line
    max_retries: 0
    prompt: this run is expected to fail
EOF

  daemon_up

  local repo="$TMP/s2/repo"
  make_repo "$repo"
  local project_id task_id
  project_id="$(register_project "$repo")"
  task_id="$(api POST /tasks "{\"project_id\":$project_id,\"workflow\":\"m5-bad-model\",\"title\":\"M5 bad model\"}" | jq -r .id)"

  echo "== wait for blocked"
  wait_for_state "$task_id" blocked 300

  echo "== assert the failure names the missing result and carries the stderr tail"
  local row summary
  row="$(api GET "/tasks/$task_id/steps" | jq '[.[] | select(.step_id == "doomed")][-1]')"
  [[ "$(jq -r .state <<<"$row")" == "failed" ]] || fail "doomed step is not failed: $row"
  summary="$(jq -r '.result_summary // ""' <<<"$row")$(jq -r '.failure_reason // ""' <<<"$row")"
  grep -qi 'result' <<<"$summary" \
    || fail "the failure does not mention the missing result event: $row"
  if (( REAL_AGENT )); then
    grep -qi 'model' <<<"$summary" \
      || fail "the real CLI's stderr tail did not reach the step record: $row"
  else
    grep -qi 'Model name is not valid' <<<"$summary" \
      || fail "the stderr tail did not reach the step record: $row"
  fi

  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  unset FAKEAGENT_SCENARIO
}

# ---------------------------------------------------------------------------
# Scenario 3 — an installed-but-unauthenticated CLI is visible before a task
# is created (§9.5). Fake-only: the real leg would mean signing the operator
# out of Cursor.
# ---------------------------------------------------------------------------
scenario3() {
  if (( REAL_AGENT )); then
    echo "=== scenario 3: skipped (would sign the operator out of Cursor)"
    return 0
  fi
  echo "=== scenario 3: logged-out cursor is flagged before a task is created"
  scenario_dirs s3
  export FAKEAGENT_CURSOR_LOGGED_OUT=1
  cursor_config
  daemon_up

  local entry
  entry="$(api GET /agents | jq '.agents[] | select(.name == "cursor")')"
  jq -e '.available' <<<"$entry" >/dev/null \
    || fail "a logged-out CLI is still installed; available must stay true: $entry"
  [[ "$(jq -r .logged_in <<<"$entry")" == "false" ]] \
    || fail "logged_in is not a definite false: $entry"

  # /v1/info serves the same truth from the same cache (§9.5).
  local info_entry
  info_entry="$(api GET /info | jq '.agents[] | select(.name == "cursor")')"
  [[ "$(jq -r .logged_in <<<"$info_entry")" == "false" ]] \
    || fail "/v1/info disagrees with /v1/agents about logged_in: $info_entry"

  # The adapters that cannot tell must report null, never a guessed false.
  local claude_logged_in
  claude_logged_in="$(api GET /agents | jq -r '.agents[] | select(.name == "claude") | .logged_in')"
  [[ "$claude_logged_in" == "null" ]] \
    || fail "claude reports logged_in=$claude_logged_in; it has no probe and must report null"

  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  unset FAKEAGENT_CURSOR_LOGGED_OUT
}

# ---------------------------------------------------------------------------
# Scenario 4 — a `restricted` cursor step where the sandbox is unavailable is
# refused, never downgraded to full-auto (§9.4, §9.7). Windows-only by
# nature: elsewhere the same workflow must simply run.
# ---------------------------------------------------------------------------
scenario4() {
  echo "=== scenario 4: restricted mode is refused, not downgraded"
  scenario_dirs s4
  (( REAL_AGENT )) || export FAKEAGENT_SCENARIO=success
  cursor_config

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/m5-restricted.yaml" <<EOF
name: m5-restricted
description: M5 gate — a restricted cursor step.
steps:
  - id: careful
    type: agent
    agent: cursor
    permission_mode: restricted
    max_retries: 0
    prompt: work carefully
EOF

  daemon_up

  local repo="$TMP/s4/repo"
  make_repo "$repo"
  local project_id task_id
  project_id="$(register_project "$repo")"
  task_id="$(api POST /tasks "{\"project_id\":$project_id,\"workflow\":\"m5-restricted\",\"title\":\"M5 restricted\"}" | jq -r .id)"

  if (( WINDOWS )); then
    echo "== windows: expect a refusal with its own reason"
    wait_for_state "$task_id" blocked 180
    local reason row
    reason="$(api GET "/tasks/$task_id" | jq -r .block_reason)"
    [[ "$reason" == "restricted_unsupported" ]] \
      || fail "block reason = $reason, want restricted_unsupported (not agent_unavailable — the CLI is fine)"
    row="$(api GET "/tasks/$task_id/steps" | jq '[.[] | select(.step_id == "careful")][-1]')"
    [[ "$(jq -r .failure_reason <<<"$row")" == "restricted_unsupported" ]] \
      || fail "step failure reason is not restricted_unsupported: $row"
    # The refusal must be *before* the process: nothing may have run.
    jq -e '.pid == null' <<<"$row" >/dev/null \
      || fail "a process was spawned for a refused restricted step: $row"
  else
    echo "== posix: the sandbox exists, so the same workflow must simply run"
    wait_for_state "$task_id" done 300
  fi

  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  unset FAKEAGENT_SCENARIO
}

case "${VINCENT_GATE_SCENARIO:-all}" in
  1) scenario1 ;;
  2) scenario2 ;;
  3) scenario3 ;;
  4) scenario4 ;;
  all)
    scenario1
    scenario2
    scenario3
    scenario4
    ;;
  *) fail "unknown VINCENT_GATE_SCENARIO: ${VINCENT_GATE_SCENARIO:-}" ;;
esac

echo
echo "M5 GATE PASS"
