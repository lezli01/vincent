#!/usr/bin/env bash
# M10 phase gate (task 057; spec §13.4): prove over the wire that the daemon
# serves MCP and that an MCP client can do the whole job through it.
#
#   1. the tool list is served, carries the route table's tools plus
#      `task_wait`, and carries none of the five destructive-admin routes
#   2. an MCP client creates a project and a task, waits on that task through
#      one blocking call, and gets back its human-blocking state
#   3. it answers the gate with a tool, waits again, and gets `done`
#   4. it reads the task's transcript and its diff
#   5. an invalid action reaches the client as a tool error still carrying
#      the §13.1 envelope's `code` and `details.state`
#   6. an agent step calls back over its own per-step endpoint and keeps it
#      across `awaiting_input`: it reports a status through `step_status`,
#      parks on a question, is answered through `task_answer`, and reports
#      again on the same session
#   7. the daemon is still up at the end — nothing in the tool surface could
#      have stopped it
#
# Scenarios 1-5 run command steps and one `manual` gate, so no agent CLI is
# involved in them. Scenario 6 is the leg 057.8 deferred to 057.9: the claude
# CLI is cmd/fakeagent's `mcp-callback` scenario, which reads the endpoint and
# secret out of the `--mcp-config` the adapter rendered — an MCP client the
# daemon wired, independent of curl. config.yaml carries no `mcp:` block, so
# what it proves is `mcp.wire_steps`' default. Every `run:` body stays inside
# the sh∩pwsh intersection the other gates are held to (§8.3): `git ...` only.
#
# The MCP client is curl. Streamable HTTP is a POST per JSON-RPC message whose
# response is an SSE frame, so `mcp_rpc` posts, keeps the `Mcp-Session-Id` the
# initialize response minted, and lifts the payload off the `data:` line.
#
# Requirements: bash, go, git, curl, jq.
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

fail() { echo "GATE FAIL: $*" >&2; exit 1; }

cleanup() {
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

echo "== build vincent + fakeagent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent ./cmd/fakeagent)

CONFIG_DIR="$TMP/config"
DATA_DIR="$TMP/data"
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"

PORT="" TOKEN="" BASE="" MCP="" SESSION="" RPC_ID=0

daemon_up() {
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
  MCP="http://127.0.0.1:$PORT/mcp"
}

# mcp_rpc METHOD PARAMS_JSON -> the JSON-RPC `result` object.
#
# The response is one SSE frame, so the payload is the `data:` line. `tr -d
# '\r'` is not optional: SSE frames are CRLF-delimited by the spec, and the CR
# would ride into jq.
mcp_rpc() {
  local method="$1" params="$2" out payload headers=()
  RPC_ID=$((RPC_ID + 1))
  headers=(-H "Authorization: Bearer $TOKEN"
           -H "Content-Type: application/json"
           -H "Accept: application/json, text/event-stream")
  [[ -n "$SESSION" ]] && headers+=(-H "Mcp-Session-Id: $SESSION")
  out="$(curl -sS -X POST "${headers[@]}" \
    -d "$(jq -cn --arg m "$method" --argjson p "$params" --argjson i "$RPC_ID" \
      '{jsonrpc:"2.0", id:$i, method:$m, params:$p}')" "$MCP")" \
    || fail "curl POST /mcp ($method) failed"
  payload="$(printf '%s' "$out" | tr -d '\r' | sed -n 's/^data: //p' | head -n 1)"
  [[ -n "$payload" ]] || fail "$method returned no SSE data frame: $out"
  if printf '%s' "$payload" | jq -e 'has("error")' >/dev/null; then
    fail "$method -> JSON-RPC error: $(printf '%s' "$payload" | jq -c .error)"
  fi
  printf '%s' "$payload" | jq -c .result
}

# mcp_notify sends a notification (no id, no response body to parse).
mcp_notify() {
  local method="$1" headers=()
  headers=(-H "Authorization: Bearer $TOKEN"
           -H "Content-Type: application/json"
           -H "Accept: application/json, text/event-stream")
  [[ -n "$SESSION" ]] && headers+=(-H "Mcp-Session-Id: $SESSION")
  curl -sS -o /dev/null -X POST "${headers[@]}" \
    -d "$(jq -cn --arg m "$method" '{jsonrpc:"2.0", method:$m, params:{}}')" "$MCP" \
    || fail "curl POST /mcp ($method) failed"
}

mcp_init() {
  local out
  SESSION=""
  out="$(curl -sS -D "$TMP/init-headers.txt" -X POST \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -H "Accept: application/json, text/event-stream" \
    -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"m10-gate","version":"1"}}}' \
    "$MCP")" || fail "curl POST /mcp (initialize) failed"
  printf '%s' "$out" | tr -d '\r' | sed -n 's/^data: //p' | head -n 1 \
    | jq -e '.result.serverInfo.name == "vincent"' >/dev/null \
    || fail "initialize did not identify the vincent server: $out"
  SESSION="$(tr -d '\r' < "$TMP/init-headers.txt" \
    | sed -n 's/^[Mm]cp-[Ss]ession-[Ii]d: //p' | head -n 1)"
  [[ -n "$SESSION" ]] || fail "initialize minted no Mcp-Session-Id"
  mcp_notify notifications/initialized
}

# mcp_tool NAME ARGS_JSON -> the tool result's first text block.
# A tool that reports IsError fails the gate; call_tool_error is for the
# scenario that wants the failure.
mcp_tool() {
  local name="$1" args="$2" res
  res="$(mcp_rpc tools/call "$(jq -cn --arg n "$name" --argjson a "$args" \
    '{name:$n, arguments:$a}')")"
  if printf '%s' "$res" | jq -e '.isError == true' >/dev/null; then
    fail "tool $name reported an error: $(printf '%s' "$res" | jq -r '.content[0].text')"
  fi
  printf '%s' "$res" | jq -r '.content[0].text'
}

# mcp_tool_error is mcp_tool's counterpart for the refusals.
mcp_tool_error() {
  local name="$1" args="$2" res
  res="$(mcp_rpc tools/call "$(jq -cn --arg n "$name" --argjson a "$args" \
    '{name:$n, arguments:$a}')")"
  printf '%s' "$res" | jq -e '.isError == true' >/dev/null \
    || fail "tool $name was expected to fail but did not: $res"
  printf '%s' "$res" | jq -r '.content[0].text'
}

make_repo() {
  git init -q -b main "$1"
  git -C "$1" config user.name gate
  git -C "$1" config user.email gate@example.invalid
  git -C "$1" config commit.gpgsign false
  printf 'gate repo\n' > "$1/README.md"
  git -C "$1" add . && git -C "$1" commit -qm init
}

# write_workflow takes the YAML as a single-quoted bash string, so the YAML may
# contain **no single quote of its own** — one would close the string and the
# rest of the workflow would be lost as stray argv. That is why the `run:`
# bodies below are plain YAML scalars with hyphenated commit messages rather
# than quoted ones: it costs nothing, and a truncated `run: git` fails as a
# git usage error twenty seconds into the run rather than as a parse error.
write_workflow() {
  mkdir -p "$CONFIG_DIR/workflows"
  printf '%s' "$2" > "$CONFIG_DIR/workflows/$1.yaml"
}

# wait_via_mcp TASK_ID WANT TRIES: task_wait plus a retry loop. One call is
# enough for a task that reaches a wake state, but a task still `queued` when
# the first call opens can wake and move on before the next transition, so the
# loop is what makes the assertion about the *state* rather than about timing.
wait_via_mcp() {
  local id="$1" want="$2" tries="$3" out state=""
  local i
  for ((i = 0; i < tries; i++)); do
    out="$(mcp_tool task_wait "$(jq -cn --argjson t "$id" '{task_id:$t, timeout_seconds:60}')")"
    state="$(printf '%s' "$out" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == aborted && "$want" != aborted ]]; then
      fail "task $id reached aborted while waiting for $want"
    fi
  done
  mcp_tool task_get "$(jq -cn --argjson t "$id" '{id:$t}')" | jq . >&2
  fail "task $id never reached $want (last: $state)"
}

echo "== scenario 1: the tool list is the route table minus destructive admin"
REPO="$TMP/repo"
make_repo "$REPO"
write_workflow gate10 'name: gate10
steps:
  - id: touch
    type: command
    run: git commit --allow-empty -m m10-gate-step-ran
  - id: review
    type: manual
    instructions: |
      Inspect task #{{.Task.ID}} before it is published.
  - id: publish
    type: command
    run: git commit --allow-empty -m m10-gate-published
'
write_workflow gate10-agent 'name: gate10-agent
steps:
  - id: ask
    type: agent
    agent: claude
    max_retries: 0
    prompt: |
      Report a status through the step_status tool, ask one question, and once
      it is answered report again and append a line to README.md.
  - id: commit
    type: command
    run: git commit -am m10-gate-agent-answered
'
# Scenario 6's agent, written before the daemon starts. There is no `mcp:`
# block on purpose: the leg must prove wiring with no user configuration.
cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
EOF
# The step inherits the daemon's environment, so these must be exported before
# `daemon_up`. No other scenario runs an agent step, so none of them reads them.
export FAKEAGENT_SCENARIO=mcp-callback
export FAKEAGENT_MCP_ASK=1
export FAKEAGENT_MCP_STATUS="m10 gate: called back while running"
export FAKEAGENT_MCP_STATUS_FINAL="m10 gate: called back after the answer"
export FAKEAGENT_EDIT_FILE=README.md
daemon_up
mcp_init

TOOLS="$(mcp_rpc tools/list '{}' | jq -r '.tools[].name' | tr -d '\r' | sort)"
for want in health info task_create task_list task_get task_wait task_approve \
            task_transcript task_diff project_create workflow_list; do
  grep -qx "$want" <<<"$TOOLS" || fail "tool $want is not served"
done
# The five exclusions are a design line (§13.4). Nothing named after any of
# them may appear, and the assertion is on the *served list* rather than on a
# route table the wire never shows.
for banned in stop backup delete gc fix; do
  if grep -q "$banned" <<<"$TOOLS"; then
    fail "the tool list mentions '$banned'; destructive admin must not be a tool: $TOOLS"
  fi
done
echo "   tools: $(wc -l <<<"$TOOLS" | tr -d ' ') served, none of the five exclusions"

echo "== scenario 2: create a project and a task through tools, then wait"
PROJECT_ID="$(mcp_tool project_create \
  "$(jq -cn --arg p "$(hostpath "$REPO")" '{body:{path:$p}}')" | jq -r .id)"
[[ "$PROJECT_ID" =~ ^[0-9]+$ ]] || fail "project_create returned no id"

TASK_ID="$(mcp_tool task_create "$(jq -cn --argjson p "$PROJECT_ID" \
  '{body:{project_id:$p, workflow:"gate10", title:"m10 gate"}}')" | jq -r .id)"
[[ "$TASK_ID" =~ ^[0-9]+$ ]] || fail "task_create returned no id"

wait_via_mcp "$TASK_ID" awaiting_gate 5
echo "   task $TASK_ID reached awaiting_gate through one blocking call"

echo "== scenario 3: an invalid action is a typed error carrying details.state"
# `resume` is only defined from `paused` (§6), so this is a 409 from the FSM
# rather than a body the route rejected — which is the one that carries
# details.state, and the whole reason a tool replays instead of reimplementing.
ERR="$(mcp_tool_error task_resume "$(jq -cn --argjson t "$TASK_ID" '{id:$t, body:{}}')")"
printf '%s' "$ERR" | jq -e '.error.code != null and .error.code != ""' >/dev/null \
  || fail "the tool error carries no §13.1 code: $ERR"
printf '%s' "$ERR" | jq -e '.error.details.state == "awaiting_gate"' >/dev/null \
  || fail "the tool error lost details.state: $ERR"
echo "   409 reached the client as $(printf '%s' "$ERR" | jq -r .error.code) with details.state"

echo "== scenario 4: approve through a tool and wait for done"
mcp_tool task_approve "$(jq -cn --argjson t "$TASK_ID" '{id:$t, body:{}}')" >/dev/null
wait_via_mcp "$TASK_ID" done 5
echo "   task $TASK_ID reached done"

echo "== scenario 5: read the transcript and the diff through tools"
STEPS="$(mcp_tool task_steps "$(jq -cn --argjson t "$TASK_ID" '{id:$t}')")"
RUN_ID="$(printf '%s' "$STEPS" | jq -r '[.. | objects | select(has("run_id")) | .run_id] | first // empty')"
if [[ -z "$RUN_ID" || "$RUN_ID" == null ]]; then
  RUN_ID="$(printf '%s' "$STEPS" | jq -r '[.. | objects | select(.id? and .step_id?) | .id] | first // empty')"
fi
[[ -n "$RUN_ID" ]] || fail "task_steps carried no step run to read a transcript from: $STEPS"
TRANSCRIPT="$(mcp_tool task_transcript "$(jq -cn --argjson t "$TASK_ID" --arg r "$RUN_ID" \
  '{id:$t, run_id:$r, query:{format:"raw"}}')")"
[[ -n "$TRANSCRIPT" ]] || fail "task_transcript returned nothing for run $RUN_ID"

DIFF="$(mcp_tool task_diff "$(jq -cn --argjson t "$TASK_ID" '{id:$t}')")"
[[ -n "$DIFF" ]] || fail "task_diff returned nothing"
echo "   transcript and diff read for run $RUN_ID"

echo "== scenario 6: an agent step calls back over its per-step endpoint, across awaiting_input"
AGENT_TASK_ID="$(mcp_tool task_create "$(jq -cn --argjson p "$PROJECT_ID" \
  '{body:{project_id:$p, workflow:"gate10-agent", title:"m10 gate agent"}}')" | jq -r .id)"
[[ "$AGENT_TASK_ID" =~ ^[0-9]+$ ]] || fail "task_create returned no id for the agent task"
wait_via_mcp "$AGENT_TASK_ID" awaiting_input 5

# ask_status -> the newest `ask` step run's status_message, read through a
# tool. The step can only have set it one way: over /mcp/step/{run_id}, with
# the secret the daemon minted for that run and handed to claude's argv.
ask_status() {
  mcp_tool task_steps "$(jq -cn --argjson t "$AGENT_TASK_ID" '{id:$t}')" \
    | jq -r '[.[] | select(.step_id == "ask")][-1].status_message // empty'
}
STATUS="$(ask_status)"
[[ "$STATUS" == "$FAKEAGENT_MCP_STATUS" ]] \
  || fail "the parked agent step's status_message is '$STATUS', want '$FAKEAGENT_MCP_STATUS'"

AGENT_TASK="$(mcp_tool task_get "$(jq -cn --argjson t "$AGENT_TASK_ID" '{id:$t}')")"
PENDING="$(jq -c .pending_input <<<"$AGENT_TASK")"
[[ "$(jq -r .kind <<<"$PENDING")" == "question" ]] || fail "pending_input is not a question: $PENDING"
# First option per question, built from pending_input the way m2 answers.
ANSWER="$(jq -c '{answers: [.questions[]
  | (.options[0] // "Blue") as $pick
  | {key: .text, value: (if .multi_select then [$pick] else $pick end)}]
  | from_entries}' <<<"$PENDING")"
mcp_tool task_answer "$(jq -cn --argjson t "$AGENT_TASK_ID" --argjson b "$ANSWER" \
  '{id:$t, body:$b}')" >/dev/null
wait_via_mcp "$AGENT_TASK_ID" done 5

# The second report went over the same session after the task sat in
# awaiting_input, so the per-step secret outlived the park. It also reached
# the daemon inside §13.3's one-second floor between two status writes of one
# step run (task 036), so it was coalesced rather than written on arrival: the
# engine persists it when the floor expires, by row id, even once the step has
# finished. The task can reach `done` before that, so this waits for the value
# rather than for a length of time. A report that had failed would have failed
# the step instead — the scenario exits nonzero on any tool error.
STATUS=""
for ((i = 0; i < 10; i++)); do
  STATUS="$(ask_status)"
  [[ "$STATUS" == "$FAKEAGENT_MCP_STATUS_FINAL" ]] && break
  sleep 1
done
[[ "$STATUS" == "$FAKEAGENT_MCP_STATUS_FINAL" ]] \
  || fail "the finished agent step's status_message is '$STATUS', want '$FAKEAGENT_MCP_STATUS_FINAL'"
AGENT_DIFF="$(mcp_tool task_diff "$(jq -cn --argjson t "$AGENT_TASK_ID" '{id:$t}')")"
[[ -n "$AGENT_DIFF" ]] || fail "task_diff returned nothing for the agent task"
echo "   task $AGENT_TASK_ID called back before and after its answer, and reached done"

echo "== scenario 7: the daemon the agent was talking to is still up"
HEALTH="$(mcp_tool health '{}')"
printf '%s' "$HEALTH" | jq -e '.status == "ok"' >/dev/null \
  || fail "the daemon did not answer health: $HEALTH"
curl -sS -H "Authorization: Bearer $TOKEN" "$BASE/health" | jq -e '.status == "ok"' >/dev/null \
  || fail "the REST API stopped answering"
echo "   daemon healthy over both protocols"

echo
echo "M10 GATE PASS"
