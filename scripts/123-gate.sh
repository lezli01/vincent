#!/usr/bin/env bash
# 123 gate (task 123; spec §5.2, §12.1, §12.2): prove end to end that the real
# update-workflows built-in, with global: true, updates the global workflows
# only through a proposal a person approves — and that a project run is the
# pass it always was.
#
#   1. staged → gate → approve: the global file is rewritten, the staging
#      directory is gone, and the task's worktree is clean
#   2. staged → reject: every global file is byte-identical, and the worktree
#      is clean
#   3. an empty global directory: done, with no agent step run
#   4. a project run ends done, with the new global-only condition stopped
#
# The agent is cmd/fakeagent's stage-workflows scenario, which stages through
# the real `vincent workflow ls --global --json`; every command step runs the
# real `vincent`, found on the daemon's PATH.
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

echo "== build vincent and the fake agent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent ./cmd/fakeagent)

CONFIG_DIR="$TMP/config"
DATA_DIR="$TMP/data"
GLOBAL="$CONFIG_DIR/workflows"
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"

{
  echo "agents:"
  echo "  claude:"
  echo "    path: $(hostpath "$FAKEAGENT")"
} > "$CONFIG_DIR/config.yaml"

# The built-in's command steps run `vincent ...` under the daemon's shell, and
# the agent's own listing runs it by path. Both read the daemon's environment,
# so everything the agent needs is exported before it starts.
export PATH="$BIN:$PATH"
export FAKEAGENT_SCENARIO=stage-workflows
export FAKEAGENT_VINCENT_BIN
FAKEAGENT_VINCENT_BIN="$(hostpath "$VINCENT")"
export FAKEAGENT_PROPOSAL_LINE="# reviewed by the 123 gate"

REPO="$TMP/repo"
mkdir -p "$REPO/.vincent/workflows"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email gate@example.com
git -C "$REPO" config user.name "123 Gate"
# A committed project workflow, so scenario 4's project run gets all the way
# to the trailing condition. run: bodies are in the sh∩pwsh intersection.
cat > "$REPO/.vincent/workflows/local.yaml" <<'YAML'
name: local
steps:
  - id: one
    type: command
    run: git status
YAML
git -C "$REPO" add .vincent
git -C "$REPO" commit -q -m "root"

echo "== start the daemon"
"$VINCENT" daemon start
PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
TOKEN="$(cat "$DATA_DIR/token")"
BASE="http://127.0.0.1:$PORT/v1"

api() {
  local method="$1" path="$2"
  shift 2
  curl -sS -X "$method" -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" "$@" "$BASE$path"
}

PROJECT="$(api POST /projects -d "{\"name\":\"gate\",\"path\":\"$(hostpath "$REPO")\"}")" \
  || fail "POST /v1/projects failed"
PROJECT_ID="$(printf '%s' "$PROJECT" | jq -r .id)"

# create_task TITLE GLOBAL -> the new task's id
create_task() {
  local body
  body="$(api POST /tasks -d "$(jq -cn --argjson p "$PROJECT_ID" --arg t "$1" --arg g "$2" \
    '{project_id: $p, workflow: "update-workflows", title: $t, agent: "claude", fields: {global: $g}}')")" \
    || fail "POST /v1/tasks failed for $1"
  printf '%s' "$body" | jq -r .id
}

# wait_state ID STATE — poll until the task reaches STATE.
wait_state() {
  local id="$1" want="$2" i state=""
  for i in $(seq 1 120); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "aborted" || "$state" == "blocked" || "$state" == "done" ]] && [[ "$want" != "$state" ]]; then
      fail "task $id went $state waiting for $want: $(steps_dump "$id")"
    fi
    sleep 1
  done
  fail "task $id never reached $want (stuck in $state)"
}

steps_dump() { # steps_dump TASK_ID — every step run's id and state, for a failure
  api GET "/tasks/$1/steps" | jq -c '[.[] | {step_id, state}]'
}

step_state() { # step_state TASK_ID STEP_ID — the newest run's state
  api GET "/tasks/$1/steps" | jq -r --arg s "$2" '[.[] | select(.step_id == $s)] | last | .state // "none"'
}

# worktree_clean ID — the task's worktree has no change at all: a global run
# must leave its project alone.
worktree_clean() {
  local wt status
  wt="$(api GET "/tasks/$1" | jq -r .worktree_path)"
  [[ -n "$wt" && "$wt" != "null" ]] || fail "task $1 has no worktree path"
  status="$(git -C "$wt" status --porcelain | tr -d '\r')"
  [[ -z "$status" ]] || fail "task $1's worktree changed: $status"
}

write_global() { # write_global NAME COMMENT
  cat > "$GLOBAL/$1.yaml" <<YAML
# $2
name: $1
steps:
  - id: one
    type: command
    run: git status
YAML
}

echo "== 3. an empty global directory ends done without an agent"
EMPTY_ID="$(create_task "nothing global yet" true)"
wait_state "$EMPTY_ID" done
[[ "$(step_state "$EMPTY_ID" inventory)" == "failed" ]] || fail "the global inventory did not report none"
[[ "$(step_state "$EMPTY_ID" modernize)" == "none" ]] || fail "an agent ran with no global workflows"

mkdir -p "$GLOBAL"
write_global alpha "live alpha"
write_global beta "live beta"

echo "== 1. staged, gated, approved: the global file is rewritten"
APPROVE_ID="$(create_task "update the global workflows" true)"
wait_state "$APPROVE_ID" awaiting_gate
STAGE="$DATA_DIR/workflow-proposals/$APPROVE_ID"
[[ -f "$STAGE/manifest.json" && -f "$STAGE/alpha.yaml" ]] || fail "nothing was staged at $STAGE"
ALPHA="$(cat "$GLOBAL/alpha.yaml")"
[[ "$ALPHA" != *"reviewed by the 123 gate"* ]] || fail "alpha.yaml changed before the gate"
[[ "$(step_state "$APPROVE_ID" relist)" == "succeeded" ]] || fail "apply --check did not pass the proposal"
worktree_clean "$APPROVE_ID"
api POST "/tasks/$APPROVE_ID/approve" -d '{}' >/dev/null || fail "approve failed"
wait_state "$APPROVE_ID" done
ALPHA="$(cat "$GLOBAL/alpha.yaml")"
[[ "$ALPHA" == *"reviewed by the 123 gate"* ]] || fail "the approved proposal did not reach alpha.yaml: $ALPHA"
[[ ! -e "$STAGE" ]] || fail "a successful apply left its staging directory"
worktree_clean "$APPROVE_ID"

echo "== 2. staged, gated, rejected: nothing global changes"
BEFORE_ALPHA="$(cat "$GLOBAL/alpha.yaml")"
BEFORE_BETA="$(cat "$GLOBAL/beta.yaml")"
REJECT_ID="$(create_task "update them again" true)"
wait_state "$REJECT_ID" awaiting_gate
api POST "/tasks/$REJECT_ID/reject" -d '{}' >/dev/null || fail "reject failed"
wait_state "$REJECT_ID" blocked
[[ "$(cat "$GLOBAL/alpha.yaml")" == "$BEFORE_ALPHA" ]] || fail "a rejected proposal changed alpha.yaml"
[[ "$(cat "$GLOBAL/beta.yaml")" == "$BEFORE_BETA" ]] || fail "a rejected proposal changed beta.yaml"
[[ "$(step_state "$REJECT_ID" apply)" == "none" ]] || fail "apply ran after a reject"
worktree_clean "$REJECT_ID"

echo "== 4. a project run ends done, with the global-only condition stopped"
PROJECT_RUN_ID="$(create_task "update this project's workflows" false)"
wait_state "$PROJECT_RUN_ID" done
# A loop records its body's rows, not one of its own.
for step in file rendered; do
  [[ "$(step_state "$PROJECT_RUN_ID" "$step")" == "succeeded" ]] \
    || fail "the project run did not validate its workflows: $(steps_dump "$PROJECT_RUN_ID")"
done
[[ "$(step_state "$PROJECT_RUN_ID" global-only)" == "stopped" ]] \
  || fail "global-only is $(step_state "$PROJECT_RUN_ID" global-only), want stopped"
for step in approve apply result; do
  [[ "$(step_state "$PROJECT_RUN_ID" "$step")" == "none" ]] || fail "a project run reached $step"
done
[[ ! -e "$DATA_DIR/workflow-proposals/$PROJECT_RUN_ID" ]] || fail "a project run staged a proposal"

echo "GATE PASS: 123"
