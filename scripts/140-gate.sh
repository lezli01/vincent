#!/usr/bin/env bash
# Task 140.11 gate (§5.5 "On an issue", §5.6, §10, §11, §13.2, §13.3; task 140
# decisions 1–7): prove via curl alone that a chat opened on an issue holds
# the issue's main worktree as a main task does, and hands it on.
#
#   1. open: POST /v1/chats with issue_id on an issue with no main branch
#      cuts the branch the issue's main branch becomes, and the issue reads
#      in_progress with the chat as its main worktree's occupant
#   2. chat: a turn edits a tracked file in that worktree and leaves the
#      change uncommitted; a second chat on the issue is 409
#      issue_has_live_chat naming the first
#   3. queued main task: a main task created behind the chat names the chat
#      as its occupant on the 201, joins the chat's branch, and stays queued
#      while the chat is live
#   4. close: closing the chat stamps its end_sha and frees the worktree
#   5. admission: the queued task is admitted into the chat's own directory,
#      the uncommitted edit included — its only step commits with `git
#      commit -a`, which fails on a clean tree — and while it runs a chat on
#      the issue is 409 issue_worktree_occupied
#   6. handoff: once it is done a new chat takes the same directory and
#      branch, and handing it off makes the issue's next main task in that
#      directory, with no issue_id in the request; the chat is handed_off and
#      the task runs to done on the branch
#
# Task-numbered rather than `mN` because this is not a §19 milestone, like
# 134-gate.sh. The six steps are one chain — each reads what the one before
# it left — so VINCENT_GATE_SCENARIO is not offered: the whole gate is one
# scenario.
#
# The chat's agent is cmd/fakeagent configured as `claude`, told by
# FAKEAGENT_EDIT_FILE which tracked file to append a line to. The workflows'
# `run:` bodies keep to what `/bin/sh` and `pwsh` both accept — `git …` and
# `sleep N` — and concurrency is asserted from the API, never from inside a
# step.
#
# The gate observes the rules CLAUDE.md records: no pipe into `grep -q` or any
# other early-exiting consumer (capture first, match a here-string), `| tr -d
# '\r'` on multi-line captures, waits that are bounded polls on API state, and
# committed executable.
#
# Requirements: bash, go, git, curl, jq.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/gate.sh
source "$ROOT/scripts/lib/gate.sh"
TMP="$(mktemp -d)"
BIN="$TMP/bin"

EXE=""
if [[ "${OS:-}" == "Windows_NT" ]]; then
  EXE=".exe"
fi
VINCENT="$BIN/vincent$EXE"
FAKEAGENT="$BIN/fakeagent$EXE"

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
gate_build "$BIN" vincent fakeagent

CONFIG_DIR="$TMP/config"
DATA_DIR="$TMP/data"
REPO="$TMP/repo"
mkdir -p "$CONFIG_DIR/workflows" "$DATA_DIR" "$TMP/sessions"
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
# The fake CLI's conversation store, and the tracked file its turns append
# to. Exported before the daemon starts: its agents inherit its environment.
export FAKEAGENT_SESSION_DIR
FAKEAGENT_SESSION_DIR="$(hostpath "$TMP/sessions")"
export FAKEAGENT_EDIT_FILE=notes.txt

git init -q -b main "$REPO"
git -C "$REPO" config user.name gate
git -C "$REPO" config user.email gate@example.invalid
git -C "$REPO" config commit.gpgsign false
printf 'the chat appends below\n' > "$REPO/notes.txt"
git -C "$REPO" add . && git -C "$REPO" commit -qm init

cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
EOF

# land commits whatever the worktree holds: `git commit -a` exits 1 on a
# clean tree, so the step passes only on a directory carrying the chat's
# uncommitted edit. The sleep keeps the task running long enough to be seen.
cat > "$CONFIG_DIR/workflows/land.yaml" <<'EOF'
name: land
steps:
  - {id: land, type: command, max_retries: 0, run: 'git commit -a -qm landed'}
  - {id: hold, type: command, max_retries: 0, run: 'sleep 3'}
EOF
cat > "$CONFIG_DIR/workflows/after-chat.yaml" <<'EOF'
name: after-chat
steps:
  - {id: work, type: command, max_retries: 0, run: 'git commit -a -qm after-chat'}
EOF

echo "== start the daemon"
"$VINCENT" daemon start
PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
TOKEN="$(cat "$DATA_DIR/token")"
BASE="http://127.0.0.1:$PORT/v1"

api() { # api METHOD PATH [JSON_BODY] -> body; a non-2xx fails
  local method="$1" path="$2" body="${3:-}" out status
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  [[ "$status" == 2* ]] || fail "$method $path -> HTTP $status: $out"
  printf '%s' "$out"
}

# api_status METHOD PATH [JSON_BODY] -> the status code, with the body in
# $TMP/body.json.
api_status() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-sS -o "$TMP/body.json" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $TOKEN")
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  curl "${args[@]}" "$BASE$path"
}

expect_eq() { # expect_eq GOT WANT WHAT
  [[ "$1" == "$2" ]] || fail "$3: got '$1', want '$2'"
}

task() { api GET "/tasks/$1"; }
tf() { task "$1" | jq -r "$2"; }
issue_f() { api GET "/issues/$ISSUE" | jq -r "$1"; }
chat_f() { api GET "/chats/$1" | jq -r ".chat | $2"; }

wait_task() { # wait_task ID STATE SECS; a block or an abort fails at once
  local id="$1" want="$2" secs="$3" body state=""
  for _ in $(seq 1 "$(gate_ticks "$secs")"); do
    body="$(task "$id")"
    state="$(jq -r .state <<<"$body")"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == aborted || "$state" == blocked ]]; then
      jq . <<<"$body" >&2
      fail "task $id reached $state ($(jq -r '.block_reason // ""' <<<"$body")) while waiting for $want"
    fi
    sleep "$GATE_POLL"
  done
  task "$id" | jq . >&2
  fail "task $id never reached $want (last: $state)"
}

wait_turn() { # wait_turn CHAT SEQ -> the turn's state once it is not running
  local id="$1" seq="$2" state=""
  for _ in $(seq 1 "$(gate_ticks 120)"); do
    state="$(api GET "/chats/$id" | jq -r --argjson s "$seq" '.turns[] | select(.seq == $s) | .state')"
    if [[ -n "$state" && "$state" != running ]]; then
      printf '%s' "$state"
      return 0
    fi
    sleep "$GATE_POLL"
  done
  fail "turn $seq of chat $id never finished"
}

wait_chat_idle() { # wait_chat_idle CHAT
  local state=""
  for _ in $(seq 1 "$(gate_ticks 60)"); do
    state="$(chat_f "$1" .state)"
    [[ "$state" == idle ]] && return 0
    sleep "$GATE_POLL"
  done
  fail "chat $1 never returned to idle (last: $state)"
}

PROJECT="$(api POST /projects "$(jq -cn --arg p "$(hostpath "$REPO")" '{path: $p}')" | jq -r .id)"
ISSUE="$(api POST /issues "$(jq -cn --argjson p "$PROJECT" \
  '{project_id: $p, title: "Talk the gate through", body: "Reach for notes.txt."}')" | jq -r .id)"

echo "== 1. open: a chat on an issue with no main branch cuts it"
expect_eq "$(issue_f '.main_worktree.branch // ""')" "" "issue's main branch before any work"
CHAT="$(api POST /chats "$(jq -cn --argjson p "$PROJECT" --argjson i "$ISSUE" \
  '{project_id: $p, title: "work it", agent: "claude", issue_id: $i}')")"
CHAT_ID="$(jq -r .id <<<"$CHAT")"
CHAT_WT="$(jq -r .worktree_path <<<"$CHAT")"
BRANCH="$(jq -r .branch <<<"$CHAT")"
expect_eq "$(jq -r .issue_id <<<"$CHAT")" "$ISSUE" "chat's issue_id"
[[ -n "$CHAT_WT" && -d "$CHAT_WT" ]] || fail "the chat has no worktree on disk: '$CHAT_WT'"
[[ "$BRANCH" == vincent/* ]] || fail "the chat's branch is '$BRANCH', want a cut vincent/… branch"
expect_eq "$(issue_f .lane)" in_progress "lane with the chat open"
expect_eq "$(issue_f .main_worktree.branch)" "$BRANCH" "issue's main branch"
expect_eq "$(issue_f .main_worktree.occupant_chat_id)" "$CHAT_ID" "main worktree's occupant chat"

echo "== 2. chat: a turn edits the worktree; a second chat is refused"
api POST "/chats/$CHAT_ID/send" '{"message": "add a line to notes.txt"}' >/dev/null
expect_eq "$(wait_turn "$CHAT_ID" 1)" done "first turn"
wait_chat_idle "$CHAT_ID"
DIRTY="$(git -C "$CHAT_WT" status --porcelain | tr -d '\r')"
grep -q 'notes.txt' <<<"$DIRTY" || fail "the turn left no uncommitted edit to notes.txt: $DIRTY"
CODE="$(api_status POST /chats "$(jq -cn --argjson p "$PROJECT" --argjson i "$ISSUE" \
  '{project_id: $p, title: "again", agent: "claude", issue_id: $i}')")"
expect_eq "$CODE" 409 "second chat on the issue"
expect_eq "$(jq -r .error.code "$TMP/body.json")" issue_has_live_chat "second chat's refusal"
expect_eq "$(jq -r .error.details.chat_id "$TMP/body.json")" "$CHAT_ID" "the live chat the refusal names"

echo "== 3. queued main task: it joins the chat's branch and waits"
MAIN="$(api POST /tasks "$(jq -cn --argjson p "$PROJECT" --argjson i "$ISSUE" \
  '{project_id: $p, issue_id: $i, workflow: "land", title: "land the chat"}')")"
MAIN_ID="$(jq -r .id <<<"$MAIN")"
expect_eq "$(jq -r '.main_worktree_occupant_chat_id // ""' <<<"$MAIN")" "$CHAT_ID" "the 201's occupant chat"
expect_eq "$(jq -r .branch_name <<<"$MAIN")" "$BRANCH" "main task's branch"
expect_eq "$(jq -r .issue_worktree <<<"$MAIN")" main "main task's role"
# The scheduler ticks at least every few seconds; a task still queued after
# several of them is waiting for the chat, not for a tick.
for _ in $(seq 1 "$(gate_ticks 6)"); do
  expect_eq "$(tf "$MAIN_ID" .state)" queued "main task while the chat holds the worktree"
  sleep "$GATE_POLL"
done

echo "== 4. close: the chat lets go and stamps its end_sha"
CLOSED="$(api POST "/chats/$CHAT_ID/close")"
expect_eq "$(jq -r .state <<<"$CLOSED")" closed "chat after close"
TIP="$(git -C "$REPO" rev-parse "$BRANCH" | tr -d '\r')"
expect_eq "$(chat_f "$CHAT_ID" '.end_sha // ""')" "$TIP" "closed chat's end_sha"

echo "== 5. admission: the main task takes the chat's directory, edit included"
for _ in $(seq 1 "$(gate_ticks 30)"); do
  [[ "$(tf "$MAIN_ID" .state)" == running ]] && break
  sleep "$GATE_POLL"
done
expect_eq "$(tf "$MAIN_ID" .state)" running "main task after the chat closed"
expect_eq "$(hostpath "$(tf "$MAIN_ID" .worktree_path)")" "$(hostpath "$CHAT_WT")" "main task's worktree"
CODE="$(api_status POST /chats "$(jq -cn --argjson p "$PROJECT" --argjson i "$ISSUE" \
  '{project_id: $p, title: "too soon", agent: "claude", issue_id: $i}')")"
expect_eq "$CODE" 409 "chat beside a running main task"
expect_eq "$(jq -r .error.code "$TMP/body.json")" issue_worktree_occupied "chat beside a running main task"
wait_task "$MAIN_ID" done 60
SUBJECTS="$(git -C "$REPO" log --format=%s "$BRANCH" | tr -d '\r')"
grep -qx landed <<<"$SUBJECTS" || fail "the main branch carries no 'landed' commit: $SUBJECTS"
LANDED="$(git -C "$REPO" show --stat --format= "$BRANCH" | tr -d '\r')"
grep -q notes.txt <<<"$LANDED" || fail "'landed' did not commit the chat's edit to notes.txt: $LANDED"

echo "== 6. handoff: a new chat takes the directory and hands it to the next main task"
SECOND="$(api POST /chats "$(jq -cn --argjson p "$PROJECT" --argjson i "$ISSUE" \
  '{project_id: $p, title: "one more pass", agent: "claude", issue_id: $i}')")"
SECOND_ID="$(jq -r .id <<<"$SECOND")"
expect_eq "$(jq -r .branch <<<"$SECOND")" "$BRANCH" "second chat's branch"
expect_eq "$(hostpath "$(jq -r .worktree_path <<<"$SECOND")")" "$(hostpath "$CHAT_WT")" "second chat's worktree"
expect_eq "$(issue_f .lane)" in_progress "lane with the second chat open"
api POST "/chats/$SECOND_ID/send" '{"message": "one more line"}' >/dev/null
expect_eq "$(wait_turn "$SECOND_ID" 1)" done "second chat's turn"
wait_chat_idle "$SECOND_ID"
HANDED="$(api POST "/chats/$SECOND_ID/handoff" '{"workflow": "after-chat", "title": "after the chat"}')"
NEXT_ID="$(jq -r .task.id <<<"$HANDED")"
[[ "$NEXT_ID" =~ ^[0-9]+$ ]] || fail "handoff answered no task id: $HANDED"
expect_eq "$(tf "$NEXT_ID" .issue.id)" "$ISSUE" "handed-off task's issue"
expect_eq "$(tf "$NEXT_ID" .issue_worktree)" main "handed-off task's role"
expect_eq "$(tf "$NEXT_ID" .branch_name)" "$BRANCH" "handed-off task's branch"
expect_eq "$(chat_f "$SECOND_ID" .state)" handed_off "second chat after the handoff"
wait_task "$NEXT_ID" done 60
expect_eq "$(hostpath "$(tf "$NEXT_ID" .worktree_path)")" "$(hostpath "$CHAT_WT")" "handed-off task's worktree"
SUBJECTS="$(git -C "$REPO" log --format=%s "$BRANCH" | tr -d '\r')"
grep -qx after-chat <<<"$SUBJECTS" || fail "the main branch carries no 'after-chat' commit: $SUBJECTS"
expect_eq "$(issue_f '.main_worktree.occupant_chat_id // ""')" "" "occupant chat once both chats ended"

echo "GATE PASS"
