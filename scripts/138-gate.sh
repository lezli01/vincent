#!/usr/bin/env bash
# Task 138 gate (138.9; spec §9.9): prove via curl alone that the copilot
# adapter is registered in a real daemon and runs end to end — a workflow
# whose step names `agent: copilot` reaches done with the edit on its branch
# (scenario 1), copilot's stderr-only refusal stays diagnosable (scenario 2),
# its login state is reported as unknown rather than guessed (scenario 3), a
# `restricted` copilot step runs on every OS with the restricted argv and no
# shell tool (scenario 4, task 138.10), and a copilot step reaches the
# daemon's per-step MCP server through its inline `--additional-mcp-config`
# carrier (scenario 5) — the `cmd /c` quoting of task 138.8 running through a
# daemon on Windows. Chats are #863's.
#
# Runs against the committed fakeagent so CI never calls a real API; run
# manually with VINCENT_GATE_AGENT=copilot to exercise the real `copilot` CLI
# (scenarios 1 and 2 only — 3, 4 and 5 assert vincent's own behavior). That
# manual run is the other half of the gate and is walked through in
# docs/gates/138-copilot-gate.md. VINCENT_GATE_SCENARIO=1|2|3|4|5 runs one
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
# shellcheck source=scripts/lib/gate.sh
source "$ROOT/scripts/lib/gate.sh"
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
[[ "${VINCENT_GATE_AGENT:-fake}" == "copilot" ]] && REAL_AGENT=1

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
# to the agent process, so a hand run is only reproducible if the environment
# it ran under is recorded alongside its result (m5's reasoning). The token
# variables are reported as set or unset, never by value.
show_env() { # show_env NAME
  printf '   %s=%s\n' "$1" "$(printenv "$1" || echo '<not exported>')"
}
show_set() { # show_set NAME — whether it is exported, never its value
  if printenv "$1" >/dev/null; then
    printf '   %s is set\n' "$1"
  else
    printf '   %s is not set\n' "$1"
  fi
}
echo "== launch environment (copy into the docs/gates/138-copilot-gate.md row)"
if (( REAL_AGENT )); then
  # The launcher may otherwise run a newer cached build than the one probed
  # (task 138 decision 5); the adapter passes --no-auto-update as well.
  export COPILOT_AUTO_UPDATE=false
  show_env COPILOT_AUTO_UPDATE
  show_env COPILOT_HOME
  show_set COPILOT_GITHUB_TOKEN
  show_set GH_TOKEN
  show_set GITHUB_TOKEN
  # copilot installs as an npm .cmd shim on Windows. Go's exec.LookPath
  # honors PATHEXT and resolves it; bash does not, so ask cmd.exe there — the
  # doubled slash stops MSYS rewriting /c as a path. Captured, then cut to
  # its first line on a here-string, so nothing is piped into an early exit.
  if (( WINDOWS )); then
    agent_version="$(cmd //c copilot --version 2>/dev/null | tr -d '\r' || true)"
  else
    agent_version="$(copilot --version 2>/dev/null || true)"
  fi
  agent_version="$(sed -n 1p <<<"$agent_version")"
  echo "   copilot ${agent_version:-<could not resolve>}"
fi

echo "== build vincent + fakeagent"
gate_build "$BIN" vincent fakeagent

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

# copilot_config writes a config.yaml pointing the copilot adapter at either
# the fake binary or the real `copilot` on PATH. Against the fake,
# FAKEAGENT_DIALECT=copilot makes the version and help probes — which carry
# no dialect hint in their argv — answer the way copilot 1.0.105 does.
copilot_config() {
  local agent_path=""
  if (( ! REAL_AGENT )); then
    agent_path="$(hostpath "$FAKEAGENT")"
    export FAKEAGENT_DIALECT=copilot
  fi
  cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  copilot:
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

daemon_down() {
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
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

wait_for_state() { # wait_for_state TASK_ID STATE SECS
  local id="$1" want="$2" tries="$3" state=""
  for _ in $(seq 1 "$(gate_ticks "$tries")"); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "blocked" || "$state" == "aborted" ]] && [[ "$want" != "blocked" ]]; then
      api GET "/tasks/$id" | jq . >&2
      api GET "/tasks/$id/steps" | jq . >&2
      fail "task $id reached $state while waiting for $want"
    fi
    sleep "$GATE_POLL"
  done
  api GET "/tasks/$id" | jq . >&2
  fail "task $id never reached $want (still $state)"
}

# ---------------------------------------------------------------------------
# Scenario 1 — the acceptance itself: agent(copilot) edits a tracked file, a
# command step commits it, the task reaches done and the branch carries the
# change. Spec §9.9.
# ---------------------------------------------------------------------------
scenario1() {
  echo "=== scenario 1: a copilot workflow runs unattended to completion"
  scenario_dirs s1

  local prompt effort_line=""
  if (( REAL_AGENT )); then
    # No model line: copilot ships no model catalog (task 138 decision 9),
    # so the account's own default is the only model known to exist.
    prompt='Append one line reading "copilot was here" to README.md in the current directory. Do not do anything else.'
  else
    export FAKEAGENT_SCENARIO=success
    export FAKEAGENT_EDIT_FILE=README.md
    prompt='Task: {{.Task.Title}}'
    # The fake accepts any effort; the real leg leaves it to the CLI.
    effort_line="  effort: high"
  fi
  copilot_config

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/138-flow.yaml" <<EOF
name: 138-flow
description: 138 gate — a copilot agent step, then a commit.
defaults:
  agent: copilot
  max_retries: 0
$effort_line
steps:
  - id: edit
    type: agent
    prompt: |
      $prompt
  - id: commit
    type: command
    run: 'git add -A && git commit -m "138 gate: copilot edit"'
EOF

  daemon_up

  local repo="$TMP/s1/repo"
  make_repo "$repo"

  echo "== assert copilot is registered beside the other three, with its catalog (§9.6, §9.9)"
  local agents names copilot_entry
  agents="$(api GET /agents)"
  names="$(jq -r '[.agents[].name] | join(",")' <<<"$agents" | tr -d '\r')"
  [[ "$names" == "claude,codex,cursor,copilot" ]] \
    || fail "/v1/agents lists $names, want claude,codex,cursor,copilot"
  copilot_entry="$(jq '.agents[] | select(.name == "copilot")' <<<"$agents")"
  jq -e '.available' <<<"$copilot_entry" >/dev/null || fail "copilot unavailable: $copilot_entry"
  # No model catalog and no default model (task 138 decision 9); the
  # reasoning efforts come from --help merged over the curated floor.
  [[ "$(jq '.models | length' <<<"$copilot_entry")" == "0" ]] \
    || fail "copilot reports models; §9.9 says it lists none: $copilot_entry"
  [[ "$(jq -r '.default_model // ""' <<<"$copilot_entry")" == "" ]] \
    || fail "copilot reports a default model: $copilot_entry"
  jq -e '[.efforts[].value] | index("high")' <<<"$copilot_entry" >/dev/null \
    || fail "copilot's effort catalog has no 'high': $copilot_entry"

  local project_id task_id
  project_id="$(register_project "$repo")"
  task_id="$(api POST /tasks "{\"project_id\":$project_id,\"workflow\":\"138-flow\",\"title\":\"138 gate flow\",\"description\":\"Edit and commit.\"}" | jq -r .id)"
  [[ "$task_id" =~ ^[0-9]+$ ]] || fail "task creation returned no id"

  echo "== wait for done"
  wait_for_state "$task_id" done 300

  echo "== assert the step ran as copilot, with tokens and cost as §9.9 states them"
  local steps edit_row
  steps="$(api GET "/tasks/$task_id/steps")"
  edit_row="$(jq '[.[] | select(.step_id == "edit")][-1]' <<<"$steps")"
  [[ "$(jq -r .state <<<"$edit_row")" == "succeeded" ]] || fail "edit step not succeeded: $edit_row"
  [[ "$(jq -r .agent <<<"$edit_row")" == "copilot" ]] || fail "step did not run as copilot: $edit_row"
  # Tokens are the root usage checkpoint's session totals (§9.9, task
  # 138.7) — the fake's checkpoint says 100 in, 42 out, a real run's anything
  # but zero. Premium requests never become a cost: absent is how the API
  # serializes that.
  jq -e '(.cost_usd // 0) == 0' <<<"$edit_row" >/dev/null \
    || fail "copilot reported a cost; §9.9 says it reports none: $edit_row"
  if (( REAL_AGENT )); then
    jq -e '(.input_tokens // 0) > 0 and (.output_tokens // 0) > 0' <<<"$edit_row" >/dev/null \
      || fail "copilot reported no tokens; §9.9 reads them off the usage checkpoint: $edit_row"
  else
    jq -e '.input_tokens == 100 and .output_tokens == 42' <<<"$edit_row" >/dev/null \
      || fail "the fake checkpoint's 100/42 tokens did not reach the step row: $edit_row"
  fi
  if (( ! REAL_AGENT )); then
    [[ "$(jq -r '.effort // ""' <<<"$edit_row")" == "high" ]] \
      || fail "the resolved effort was not recorded: $edit_row"
  fi
  [[ "$(jq -r '[.[] | select(.step_id == "commit")][-1].state' <<<"$steps")" == "succeeded" ]] \
    || fail "commit step not succeeded: $steps"

  echo "== assert the transcript carries copilot-shaped lines"
  local run_id transcript
  run_id="$(jq -r .id <<<"$edit_row")"
  transcript="$(api GET "/tasks/$task_id/steps/$run_id/transcript")"
  grep -q '"type":"result"' <<<"$transcript" || fail "transcript has no copilot result line"
  grep -q '"type":"assistant.message"' <<<"$transcript" \
    || fail "transcript has no assistant.message event; is this really the copilot dialect?"

  echo "== assert the branch carries the edit"
  local branch subject files
  branch="$(api GET "/tasks/$task_id" | jq -r .branch_name)"
  # Captured rather than piped into `grep -q` — see m7 scenario 4's comment.
  subject="$(git -C "$repo" log -1 --format=%s "$branch")"
  grep -q '138 gate: copilot edit' <<<"$subject" \
    || fail "branch $branch tip is not the gate commit"
  files="$(git -C "$repo" show --name-only --format= "$branch")"
  grep -qx 'README.md' <<<"$files" \
    || fail "the copilot step changed no tracked file"

  daemon_down
  unset FAKEAGENT_SCENARIO FAKEAGENT_EDIT_FILE FAKEAGENT_DIALECT
}

# ---------------------------------------------------------------------------
# Scenario 2 — copilot's stderr-only refusal. An unavailable model exits 1
# with the reason on stderr and no result line at all, so the stderr tail is
# the whole diagnosis (§9.9, §18).
# ---------------------------------------------------------------------------
scenario2() {
  echo "=== scenario 2: a stderr-only refusal stays diagnosable"
  scenario_dirs s2

  local model_line=""
  if (( REAL_AGENT )); then
    model_line="    model: fake-nonexistent"
  else
    export FAKEAGENT_SCENARIO=no-result
  fi
  copilot_config

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/138-bad-model.yaml" <<EOF
name: 138-bad-model
description: 138 gate — a copilot step refused before it emits a result.
steps:
  - id: doomed
    type: agent
    agent: copilot
$model_line
    max_retries: 0
    prompt: this run is expected to fail
EOF

  daemon_up

  local repo="$TMP/s2/repo"
  make_repo "$repo"
  local project_id task_id
  project_id="$(register_project "$repo")"
  task_id="$(api POST /tasks "{\"project_id\":$project_id,\"workflow\":\"138-bad-model\",\"title\":\"138 bad model\"}" | jq -r .id)"

  echo "== wait for blocked"
  wait_for_state "$task_id" blocked 300

  echo "== assert the failure carries the stderr text"
  local row summary
  row="$(api GET "/tasks/$task_id/steps" | jq '[.[] | select(.step_id == "doomed")][-1]')"
  [[ "$(jq -r .state <<<"$row")" == "failed" ]] || fail "doomed step is not failed: $row"
  summary="$(jq -r '.result_summary // ""' <<<"$row")$(jq -r '.failure_reason // ""' <<<"$row")"
  grep -qi 'fake-nonexistent' <<<"$summary" \
    || fail "the refused model's stderr did not reach the step record: $row"
  grep -qi 'not available' <<<"$summary" \
    || fail "the stderr tail did not reach the step record: $row"

  daemon_down
  unset FAKEAGENT_SCENARIO FAKEAGENT_DIALECT
}

# ---------------------------------------------------------------------------
# Scenario 3 — copilot has no login probe (task 138 decision 10), so its
# login state is unknown: `null`, never a guessed true or false, on both
# endpoints that serve the §9.5 cache. Fake-only: it asserts vincent.
# ---------------------------------------------------------------------------
scenario3() {
  if (( REAL_AGENT )); then
    echo "=== scenario 3: skipped (asserts vincent's own reporting, not the CLI)"
    return 0
  fi
  echo "=== scenario 3: copilot's login state is reported unknown"
  scenario_dirs s3
  copilot_config
  daemon_up

  local entry
  entry="$(api GET /agents | jq '.agents[] | select(.name == "copilot")')"
  [[ -n "$entry" ]] || fail "no copilot adapter in /v1/agents"
  jq -e '.available' <<<"$entry" >/dev/null || fail "copilot unavailable: $entry"
  jq -e 'has("logged_in") and .logged_in == null' <<<"$entry" >/dev/null \
    || fail "copilot's logged_in is not null on /v1/agents: $entry"

  local info_entry
  info_entry="$(api GET /info | jq '.agents[] | select(.name == "copilot")')"
  [[ -n "$info_entry" ]] || fail "no copilot adapter in /v1/info"
  jq -e '.logged_in == null' <<<"$info_entry" >/dev/null \
    || fail "copilot's logged_in is not null on /v1/info: $info_entry"

  daemon_down
  unset FAKEAGENT_DIALECT
}

# ---------------------------------------------------------------------------
# Scenario 4 — a `restricted` copilot step runs, on every OS (task 138.10,
# §9.9). No OS refuses it, so there is no 400 leg as m5's has for cursor on
# Windows. The fake refuses — exit 2 and a marker on stderr — any argv that
# pairs --available-tools with an --allow-all-* flag, --yolo or
# COPILOT_ALLOW_ALL, or names a shell tool in it, so a step that succeeds ran
# bounded. The §12.3 debug record is what shows the argv itself. Fake-only:
# the real CLI's restricted runs are the task document's captures.
# ---------------------------------------------------------------------------
scenario4() {
  if (( REAL_AGENT )); then
    echo "=== scenario 4: skipped (the fake is what checks the restricted argv)"
    return 0
  fi
  echo "=== scenario 4: a restricted copilot step runs with the restricted argv"
  scenario_dirs s4
  export FAKEAGENT_SCENARIO=success
  export FAKEAGENT_EDIT_FILE=README.md
  copilot_config
  # debug: true writes the step's argv into its transcript (§12.3).
  printf 'debug: true\n' >> "$CONFIG_DIR/config.yaml"

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/138-restricted.yaml" <<EOF
name: 138-restricted
description: 138 gate — a restricted copilot step, then a commit.
steps:
  - id: careful
    type: agent
    agent: copilot
    permission_mode: restricted
    max_retries: 0
    prompt: work carefully
  - id: commit
    type: command
    run: 'git add -A && git commit -m "138 gate: restricted copilot edit"'
EOF

  daemon_up

  local repo="$TMP/s4/repo"
  make_repo "$repo"
  local project_id task_id
  project_id="$(register_project "$repo")"
  task_id="$(api POST /tasks "{\"project_id\":$project_id,\"workflow\":\"138-restricted\",\"title\":\"138 restricted\"}" | jq -r .id)"
  [[ "$task_id" =~ ^[0-9]+$ ]] || fail "creating a restricted copilot task returned no id"

  # Not wait_for_state: a refused argv blocks the task (exit 2,
  # max_retries: 0), and that helper would fail on `blocked` without saying
  # why. Stop on done or blocked, and name the fake's refusal when it is the
  # cause.
  echo "== wait for done"
  local state="" steps row run_id transcript
  for _ in $(seq 1 "$(gate_ticks 300)"); do
    state="$(api GET "/tasks/$task_id" | jq -r .state)"
    [[ "$state" == "done" || "$state" == "blocked" || "$state" == "aborted" ]] && break
    sleep "$GATE_POLL"
  done
  steps="$(api GET "/tasks/$task_id/steps")"
  row="$(jq '[.[] | select(.step_id == "careful")][-1]' <<<"$steps")"
  run_id="$(jq -r '.id // empty' <<<"$row")"
  transcript=""
  [[ -n "$run_id" ]] && transcript="$(api GET "/tasks/$task_id/steps/$run_id/transcript")"
  if grep -q 'restricted argv widened' <<<"$steps$transcript"; then
    local why
    why="$(grep -o 'restricted argv widened[^"\\]*' <<<"$steps$transcript")"
    fail "the fake refused the restricted argv (task $state): ${why%%$'\n'*}"
  fi
  if [[ "$state" != "done" ]]; then
    api GET "/tasks/$task_id" | jq . >&2
    jq . <<<"$steps" >&2
    fail "task $task_id never reached done (still $state)"
  fi

  echo "== assert the step ran restricted, with the tool set and no widening flag"
  [[ "$(jq -r .state <<<"$row")" == "succeeded" ]] || fail "restricted step not succeeded: $row"
  [[ "$(jq -r .agent <<<"$row")" == "copilot" ]] || fail "step did not run as copilot: $row"
  grep -q '"permission_mode":"restricted"' <<<"$transcript" \
    || fail "the debug record does not say restricted: $transcript"
  grep -q -- '--available-tools=view,grep,glob,edit,create,apply_patch' <<<"$transcript" \
    || fail "the argv carries no restricted --available-tools: $transcript"
  if grep -q -- '--allow-all' <<<"$transcript"; then
    fail "the restricted argv carries an --allow-all flag: $transcript"
  fi

  echo "== assert the edit inside the worktree reached the branch"
  local branch files
  branch="$(api GET "/tasks/$task_id" | jq -r .branch_name)"
  files="$(git -C "$repo" show --name-only --format= "$branch")"
  grep -qx 'README.md' <<<"$files" || fail "the restricted step changed no tracked file"

  daemon_down
  unset FAKEAGENT_SCENARIO FAKEAGENT_EDIT_FILE FAKEAGENT_DIALECT
}

# ---------------------------------------------------------------------------
# Scenario 5 — a copilot step calls `step_status` on the daemon's per-step MCP
# server (§13.4), found through the inline `--additional-mcp-config` JSON the
# adapter puts on its argv (§9.9). On Windows that JSON crosses `cmd /c` to
# reach the npm shim, and task 138.8's quoting is what keeps it whole. Fake-
# only: the fake is what makes the call.
# ---------------------------------------------------------------------------
scenario5() {
  if (( REAL_AGENT )); then
    echo "=== scenario 5: skipped (the fake is what makes the MCP call)"
    return 0
  fi
  echo "=== scenario 5: a copilot step reaches step_status over its inline MCP config"
  scenario_dirs s5
  export FAKEAGENT_SCENARIO=mcp-callback
  # Spaces and quotes in the message ride the same JSON-RPC body the carrier
  # set up, so a mangled carrier cannot pass by luck.
  export FAKEAGENT_MCP_STATUS='138 gate: "copilot" called back'
  copilot_config

  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/138-mcp.yaml" <<EOF
name: 138-mcp
description: 138 gate — a copilot step that reports its status over MCP.
steps:
  - id: report
    type: agent
    agent: copilot
    max_retries: 0
    prompt: report a status
EOF

  daemon_up

  local repo="$TMP/s5/repo"
  make_repo "$repo"
  local project_id task_id
  project_id="$(register_project "$repo")"
  task_id="$(api POST /tasks "{\"project_id\":$project_id,\"workflow\":\"138-mcp\",\"title\":\"138 mcp\"}" | jq -r .id)"

  echo "== wait for done"
  wait_for_state "$task_id" done 300

  echo "== assert the status the step set reached the API"
  local row status
  row="$(api GET "/tasks/$task_id/steps" | jq '[.[] | select(.step_id == "report")][-1]')"
  [[ "$(jq -r .agent <<<"$row")" == "copilot" ]] || fail "step did not run as copilot: $row"
  status="$(jq -r '.status_message // ""' <<<"$row" | tr -d '\r')"
  [[ "$status" == "$FAKEAGENT_MCP_STATUS" ]] \
    || fail "the copilot step's status_message is '$status', want '$FAKEAGENT_MCP_STATUS': $row"

  daemon_down
  unset FAKEAGENT_SCENARIO FAKEAGENT_MCP_STATUS FAKEAGENT_DIALECT
}

case "${VINCENT_GATE_SCENARIO:-all}" in
  1) scenario1 ;;
  2) scenario2 ;;
  3) scenario3 ;;
  4) scenario4 ;;
  5) scenario5 ;;
  all)
    scenario1
    scenario2
    scenario3
    scenario4
    scenario5
    ;;
  *) fail "unknown VINCENT_GATE_SCENARIO: ${VINCENT_GATE_SCENARIO:-}" ;;
esac

echo
echo "138 GATE PASS"
