#!/usr/bin/env bash
# Task 125 gate (spec §10, §5.3, §18, §13.2): prove over the wire that a task
# can run on a branch that already exists, including one the human has checked
# out in the project itself.
#
#   1. a free existing branch is accepted where it is 400'd without the field,
#      the task runs on it, and its worktree is one of vincent's own
#   2. a branch checked out in the *main checkout* produces a task whose
#      working directory is the project path, and archiving it leaves both the
#      branch and that directory intact
#   3. a diverged branch blocks with adopt_branch_diverged and **no ref moved**
#   4. a second task on a branch another task is working in waits queued
#      rather than blocking, and admits once the first is archived
#   5. a chat runs on an existing branch, and a second one against the same
#      claim is a 409
#
# The agent is cmd/fakeagent unless VINCENT_GATE_AGENT names a real one.
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
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"

AGENT="${VINCENT_GATE_AGENT:-claude}"
# delete_empty_branch_on_archive stays on, at its default: scenario 2 asserts
# an adopted branch survives archive, and it is the *not_ours* rule (task 064
# decision 3, extended by task 125 decision 6) that has to make that true, not
# the setting being off.
{
  if [[ -z "${VINCENT_GATE_AGENT:-}" ]]; then
    echo "agents:"
    echo "  claude:"
    echo "    path: $(hostpath "$FAKEAGENT")"
  fi
} > "$CONFIG_DIR/config.yaml"

REPO="$TMP/repo"
REMOTE="$TMP/remote.git"
mkdir -p "$REPO"
git init -q --bare "$REMOTE"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email gate@example.com
git -C "$REPO" config user.name "125 Gate"
git -C "$REPO" commit -q --allow-empty -m "root"
git -C "$REPO" remote add origin "$(hostpath "$REMOTE")"
git -C "$REPO" push -q origin main

# The branches the gate runs on, all made by hand — which is the whole point:
# vincent cut none of them.
git -C "$REPO" branch shared/free main
git -C "$REPO" branch shared/claimed main
git -C "$REPO" checkout -q -b shared/in-my-checkout main

# A branch that diverges from its own upstream: one commit on the remote copy
# and a different one locally, with the local branch tracking it.
git -C "$REPO" checkout -q -b shared/diverged main
git -C "$REPO" commit -q --allow-empty -m "the local commit nobody pushed"
DIVERGED_LOCAL="$(git -C "$REPO" rev-parse HEAD)"
git -C "$REPO" push -q origin "main:refs/heads/shared/diverged"
git -C "$REPO" config branch.shared/diverged.remote origin
git -C "$REPO" config branch.shared/diverged.merge refs/heads/shared/diverged
CLONE="$TMP/clone"
git clone -q "$(hostpath "$REMOTE")" "$CLONE"
git -C "$CLONE" config user.email gate@example.com
git -C "$CLONE" config user.name "125 Gate"
git -C "$CLONE" checkout -q -B shared/diverged origin/shared/diverged
git -C "$CLONE" commit -q --allow-empty -m "the remote commit nobody has"
git -C "$CLONE" push -q origin shared/diverged
# Back to the branch the human is sitting on for scenario 2.
git -C "$REPO" checkout -q shared/in-my-checkout

# `run:` bodies execute under the daemon's shell — /bin/sh on POSIX, pwsh on
# Windows (§8.3) — so they are spelled in the intersection of the two:
# `git ...`, `sleep N`, and `exit N` as a whole body.
mkdir -p "$CONFIG_DIR/workflows"
cat > "$CONFIG_DIR/workflows/gate-commit.yaml" <<'YAML'
name: gate-commit
description: One step that leaves a commit where the task is working.
steps:
  - id: work
    type: command
    run: git commit --allow-empty -m "work the gate can see"
YAML
cat > "$CONFIG_DIR/workflows/gate-slow.yaml" <<'YAML'
name: gate-slow
description: One step that holds its working directory for a while.
steps:
  - id: work
    type: command
    run: sleep 5
YAML

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

api_status() {
  local method="$1" path="$2"
  shift 2
  curl -sS -o "$TMP/body.json" -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    "$@" "$BASE$path"
}

PROJECT="$(api POST /projects -d "{\"name\":\"gate\",\"path\":\"$(hostpath "$REPO")\"}")" \
  || fail "POST /v1/projects failed"
PROJECT_ID="$(printf '%s' "$PROJECT" | jq -r .id)"

# create_adopted WORKFLOW TITLE BRANCH -> the new task's id
create_adopted() {
  local wf="$1" title="$2" branch="$3" body
  body="$(api POST /tasks -d "{\"project_id\":$PROJECT_ID,\"workflow\":\"$wf\",\"title\":\"$title\",\"agent\":\"$AGENT\",\"branch_name\":\"$branch\",\"existing_branch\":true}")" \
    || fail "POST /v1/tasks failed for $title"
  printf '%s' "$body" | jq -r .id
}

# wait_state ID STATE — poll until the task reaches STATE.
wait_state() {
  local id="$1" want="$2" i state
  for i in $(seq 1 120); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "aborted" ]] && [[ "$want" != "aborted" ]]; then
      fail "task $id went aborted waiting for $want"
    fi
    sleep 1
  done
  fail "task $id never reached $want (stuck in $state)"
}

echo "== 0. the branch listing offers the project's own branches"
BRANCHES="$(api GET "/projects/$PROJECT_ID/branches" | jq -r '.branches[].name' | tr -d '\r')"
grep -qx "shared/free" <<<"$BRANCHES" || fail "shared/free missing from the listing: $BRANCHES"
MAIN_HELD="$(api GET "/projects/$PROJECT_ID/branches" | jq -r '.branches[] | select(.main_checkout) | .name' | tr -d '\r')"
grep -qx "shared/in-my-checkout" <<<"$MAIN_HELD" \
  || fail "the branch the human has checked out is not reported as main_checkout: $MAIN_HELD"

echo "== 1. a free existing branch is refused without the field and accepted with it"
CODE="$(api_status POST /tasks \
  -d "{\"project_id\":$PROJECT_ID,\"workflow\":\"gate-commit\",\"title\":\"no field\",\"agent\":\"$AGENT\",\"branch_name\":\"shared/free\"}")"
[[ "$CODE" == "400" ]] || fail "creating on an existing branch without the field answered $CODE, want 400"
jq -e '.error.message | test("never reuses a branch")' "$TMP/body.json" >/dev/null \
  || fail "the 400 is not task 001's refusal: $(cat "$TMP/body.json")"

FREE_ID="$(create_adopted gate-commit "on a free branch" "shared/free")"
wait_state "$FREE_ID" done
FREE_BODY="$(api GET "/tasks/$FREE_ID")"
[[ "$(printf '%s' "$FREE_BODY" | jq -r .branch_name)" == "shared/free" ]] \
  || fail "the task did not run on shared/free"
[[ "$(printf '%s' "$FREE_BODY" | jq -r .adopted_branch)" == "true" ]] \
  || fail "adopted_branch is not true on the task"
FREE_WT="$(printf '%s' "$FREE_BODY" | jq -r .worktree_path)"
[[ "$FREE_WT" != "$(hostpath "$REPO")" ]] || fail "a free branch ran in the project path"
LOG="$(git -C "$REPO" log --format=%s shared/free)"
grep -qx "work the gate can see" <<<"$LOG" || fail "the commit is not on shared/free: $LOG"

echo "== 2. a branch checked out in the main checkout runs there"
MAIN_ID="$(create_adopted gate-commit "in the human's checkout" "shared/in-my-checkout")"
wait_state "$MAIN_ID" done
MAIN_WT="$(api GET "/tasks/$MAIN_ID" | jq -r .worktree_path)"
[[ -n "$MAIN_WT" ]] || fail "the task recorded no working directory"
# Compared through git, not by string: the daemon records the path git gives
# it, and a repository reached by a symlink spells it differently.
MAIN_TOP="$(git -C "$MAIN_WT" rev-parse --show-toplevel)"
REPO_TOP="$(git -C "$REPO" rev-parse --show-toplevel)"
[[ "$MAIN_TOP" == "$REPO_TOP" ]] \
  || fail "the task ran in $MAIN_TOP, want the project path $REPO_TOP"
api POST "/tasks/$MAIN_ID/archive" >/dev/null || fail "archiving the main-checkout task failed"
wait_state "$MAIN_ID" archived
git -C "$REPO" rev-parse --verify -q refs/heads/shared/in-my-checkout >/dev/null \
  || fail "archive deleted the adopted branch"
git -C "$REPO" rev-parse --show-toplevel >/dev/null \
  || fail "archive removed the project's own checkout"

echo "== 3. a diverged branch blocks with no ref moved"
DIV_ID="$(create_adopted gate-commit "diverged" "shared/diverged")"
wait_state "$DIV_ID" blocked
REASON="$(api GET "/tasks/$DIV_ID" | jq -r .block_reason)"
[[ "$REASON" == "adopt_branch_diverged" ]] || fail "block_reason = $REASON, want adopt_branch_diverged"
NOW="$(git -C "$REPO" rev-parse refs/heads/shared/diverged)"
[[ "$NOW" == "$DIVERGED_LOCAL" ]] \
  || fail "the diverged branch moved from $DIVERGED_LOCAL to $NOW"

echo "== 4. a second task on a claimed branch waits queued, then admits"
HOLD_ID="$(create_adopted gate-slow "holds the branch" "shared/claimed")"
wait_state "$HOLD_ID" running
WAIT_ID="$(create_adopted gate-commit "waits for it" "shared/claimed")"
sleep 2
STATE="$(api GET "/tasks/$WAIT_ID" | jq -r .state)"
[[ "$STATE" == "queued" ]] || fail "the second task is $STATE, want queued (a claim waits, it does not block)"
wait_state "$HOLD_ID" done
api POST "/tasks/$HOLD_ID/archive" >/dev/null || fail "archiving the holder failed"
wait_state "$WAIT_ID" done

echo "== 5. a chat runs on an existing branch, and a second is a 409"
git -C "$REPO" branch shared/chat main
CHAT="$(api POST /chats -d "{\"project_id\":$PROJECT_ID,\"title\":\"on a branch\",\"agent\":\"$AGENT\",\"branch_name\":\"shared/chat\",\"existing_branch\":true}")" \
  || fail "POST /v1/chats failed"
[[ "$(printf '%s' "$CHAT" | jq -r .branch)" == "shared/chat" ]] \
  || fail "the chat did not run on shared/chat"
[[ "$(printf '%s' "$CHAT" | jq -r .adopted_branch)" == "true" ]] \
  || fail "adopted_branch is not true on the chat"
CODE="$(api_status POST /chats \
  -d "{\"project_id\":$PROJECT_ID,\"title\":\"the second one\",\"agent\":\"$AGENT\",\"branch_name\":\"shared/chat\",\"existing_branch\":true}")"
[[ "$CODE" == "409" ]] || fail "a second chat on the claimed branch answered $CODE, want 409"

echo "GATE PASS: task 125 — a task or chat runs on an existing branch"
