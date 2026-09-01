#!/usr/bin/env bash
# M14 phase gate (task 067, closing 063.2; spec §5.5, §11, §13.2, §13.3):
# drive a chat end to end over the wire, against the committed fakeagent so CI
# never calls a real agent CLI.
#
#   1. POST /v1/chats creates a chat with a worktree and a branch
#   2. two sends in a row: turn 2 answers with what only turn 1 supplied, so
#      continuity came from the agent resuming its own session — walked on
#      every shipped adapter, since each spells resume differently (task 070)
#   3. GET /v1/chats/{id}/turns/{seq}/transcript serves the turn, and a
#      resume from its X-Next-Offset returns only what was appended
#   4. GET /v1/chats/{id}/events streams this chat's events and nobody else's
#   5. an awaiting_input chat is answered over /answer and the turn finishes
#   6. the max_parallel_chats+1-th send is 409 chat_cap_reached — refused,
#      never queued — and leaves no turn row behind
#   7. cancel stops a live turn; archive removes the worktree
#   8. chats never appear in GET /v1/tasks
#
# The `agent_cannot_resume` refusal is deliberately *not* here. Since task 070
# no shipped adapter is refused, so a real daemon has no subject to reach it
# with; it stays proven in Go against agenttest.StubNonResuming, in
# internal/api and internal/chatrun. Registering a fake adapter in the
# daemon's own registry to keep the leg would put a test double in the
# production registry, which is the §9.1 property the refusal exists to guard.
#
# There are no workflow `run:` bodies to spell in the sh∩pwsh intersection —
# a chat has no workflow — but this script's own bash obeys the two standing
# rules: `| tr -d '\r'` on any multi-line jq capture, and never `| grep -q`.
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

# The fake CLI's own conversation store, deliberately outside the data dir:
# continuity has to come from the agent remembering, not from anything vincent
# kept.
export FAKEAGENT_SESSION_DIR
FAKEAGENT_SESSION_DIR="$(hostpath "$TMP/sessions")"
mkdir -p "$TMP/sessions"

REPO="$TMP/repo"
mkdir -p "$REPO"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email gate@example.com
git -C "$REPO" config user.name "M14 Gate"
git -C "$REPO" commit -q --allow-empty -m "root"

cat > "$CONFIG_DIR/config.yaml" <<EOF
max_parallel_chats: 1
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
  codex:
    path: "$(hostpath "$FAKEAGENT")"
  cursor:
    path: "$(hostpath "$FAKEAGENT")"
EOF

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

# api_status METHOD PATH [curl args...] -> the status code, with the body in
# $TMP/body.json so a scenario can assert both without a second request.
api_status() {
  local method="$1" path="$2"
  shift 2
  curl -sS -o "$TMP/body.json" -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    "$@" "$BASE$path"
}

# wait_chat CHAT_ID STATE — poll until the chat reaches a state.
wait_chat() {
  local id="$1" want="$2" i=0 state
  while (( i < 300 )); do
    state="$(api GET "/chats/$id" | jq -r .chat.state)"
    if [[ "$state" == "$want" ]]; then return 0; fi
    i=$(( i + 1 ))
    sleep 1
  done
  fail "chat $id never reached $want (it is $state)"
}

# wait_turn CHAT_ID SEQ — poll until that turn is no longer running, echoing
# its state.
wait_turn() {
  local id="$1" seq="$2" i=0 state
  while (( i < 300 )); do
    state="$(api GET "/chats/$id" | jq -r --argjson s "$seq" \
      '.turns[] | select(.seq == $s) | .state')"
    if [[ -n "$state" && "$state" != "running" ]]; then
      printf '%s\n' "$state"
      return 0
    fi
    i=$(( i + 1 ))
    sleep 1
  done
  fail "turn $seq of chat $id never finished"
}

echo "== register the project"
PROJECT="$(api POST /projects -d "{\"path\": \"$(hostpath "$REPO")\"}")" \
  || fail "POST /v1/projects failed"
PROJECT_ID="$(printf '%s' "$PROJECT" | jq -r .id)"

echo "== 1. create a chat"
CHAT="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"gate talk\", \"agent\": \"claude\"}")" \
  || fail "POST /v1/chats failed"
CHAT_ID="$(printf '%s' "$CHAT" | jq -r .id)"
printf '%s' "$CHAT" | jq -e '.state == "idle" and (.branch | startswith("vincent/"))' >/dev/null \
  || fail "the created chat is wrong: $CHAT"
WORKTREE="$(printf '%s' "$CHAT" | jq -r .worktree_path)"
[[ -d "$WORKTREE" ]] || fail "the chat has no worktree at $WORKTREE"

echo "== 2. two turns, and the second remembers the first"
api POST "/chats/$CHAT_ID/send" \
  -d '{"message": "my favourite colour is heliotrope"}' >/dev/null \
  || fail "the first send failed"
STATE="$(wait_turn "$CHAT_ID" 1)"
[[ "$STATE" == "done" ]] || fail "turn 1 is $STATE, want done"
SESSION="$(api GET "/chats/$CHAT_ID" | jq -r .chat.session_id)"
[[ -n "$SESSION" && "$SESSION" != "null" ]] \
  || fail "the chat recorded no session id, so there is nothing to resume"

api POST "/chats/$CHAT_ID/send" -d '{"message": "what is my favourite colour?"}' >/dev/null \
  || fail "the second send failed"
STATE="$(wait_turn "$CHAT_ID" 2)"
[[ "$STATE" == "done" ]] || fail "turn 2 is $STATE, want done"
# The recall line is in the turn's durable record, which is what the new
# transcript route serves — so this assertion proves continuity and the route
# at once.
RECALL="$(curl -sS -H "Authorization: Bearer $TOKEN" \
  "$BASE/chats/$CHAT_ID/turns/2/transcript" | tr -d '\r')"
case "$RECALL" in
  *heliotrope*) ;;
  *) fail "turn 2 did not see turn 1; transcript: $RECALL" ;;
esac
case "$RECALL" in
  *recalled:*) ;;
  *) fail "turn 2 ran a fresh session rather than resuming one" ;;
esac

echo "== 2b. the same two turns on codex and cursor"
# Each adapter spells resume differently — claude and cursor take
# `--resume <id>`, codex an `exec --json resume <id>` subcommand — and each
# reports its id off a different stream shape. A daemon that dropped the id for
# one of them fails here by omission: a fresh session emits no recall line.
for AGENT in codex cursor; do
  OTHER="$(api POST /chats \
    -d "{\"project_id\": $PROJECT_ID, \"title\": \"$AGENT talk\", \"agent\": \"$AGENT\"}")" \
    || fail "POST /v1/chats on $AGENT failed"
  OTHER_ID="$(printf '%s' "$OTHER" | jq -r .id)"
  api POST "/chats/$OTHER_ID/send" \
    -d '{"message": "my favourite colour is heliotrope"}' >/dev/null \
    || fail "the first send on $AGENT failed"
  STATE="$(wait_turn "$OTHER_ID" 1)"
  [[ "$STATE" == "done" ]] || fail "$AGENT turn 1 is $STATE, want done"
  SESSION="$(api GET "/chats/$OTHER_ID" | jq -r .chat.session_id)"
  [[ -n "$SESSION" && "$SESSION" != "null" ]] \
    || fail "the $AGENT chat recorded no session id, so there is nothing to resume"

  api POST "/chats/$OTHER_ID/send" -d '{"message": "what is my favourite colour?"}' >/dev/null \
    || fail "the second send on $AGENT failed"
  STATE="$(wait_turn "$OTHER_ID" 2)"
  [[ "$STATE" == "done" ]] || fail "$AGENT turn 2 is $STATE, want done"
  RECALL="$(curl -sS -H "Authorization: Bearer $TOKEN" \
    "$BASE/chats/$OTHER_ID/turns/2/transcript" | tr -d '\r')"
  case "$RECALL" in
    *heliotrope*) ;;
    *) fail "$AGENT turn 2 did not see turn 1; transcript: $RECALL" ;;
  esac
  case "$RECALL" in
    *recalled:*) ;;
    *) fail "$AGENT turn 2 ran a fresh session rather than resuming one" ;;
  esac
  api POST "/chats/$OTHER_ID/archive" >/dev/null || fail "archiving the $AGENT chat failed"
done

echo "== 3. the per-turn transcript, and its offset seam"
HEADERS="$TMP/transcript.headers"
BODY1="$TMP/transcript1.ndjson"
curl -sS -D "$HEADERS" -o "$BODY1" -H "Authorization: Bearer $TOKEN" \
  "$BASE/chats/$CHAT_ID/turns/1/transcript" || fail "the transcript route failed"
[[ -s "$BODY1" ]] || fail "the turn 1 transcript is empty"
NEXT="$(tr -d '\r' < "$HEADERS" | awk 'tolower($1) == "x-next-offset:" { print $2 }')"
[[ "$NEXT" =~ ^[0-9]+$ ]] || fail "no X-Next-Offset on the transcript response"
BODY2="$TMP/transcript2.ndjson"
curl -sS -o "$BODY2" -H "Authorization: Bearer $TOKEN" \
  "$BASE/chats/$CHAT_ID/turns/1/transcript?offset=$NEXT" \
  || fail "the transcript resume failed"
# Nothing was appended after the turn ended, so resuming from the reported
# offset must return nothing at all. A non-empty body here is a seam that
# would double-print in a client.
[[ ! -s "$BODY2" ]] || fail "resuming at X-Next-Offset re-served $(wc -c < "$BODY2") bytes"
CODE="$(api_status GET "/chats/$CHAT_ID/turns/1/transcript?offset=1&tail=1")"
[[ "$CODE" == "400" ]] || fail "offset with tail answered $CODE, want 400"
CODE="$(api_status GET "/chats/$CHAT_ID/turns/99/transcript")"
[[ "$CODE" == "404" ]] || fail "an unknown turn answered $CODE, want 404"

echo "== 4. the per-chat stream carries this chat and no other"
OTHER="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"someone else\", \"agent\": \"claude\"}")" \
  || fail "creating the second chat failed"
OTHER_ID="$(printf '%s' "$OTHER" | jq -r .id)"
STREAM="$TMP/stream.sse"
curl -sN -H "Authorization: Bearer $TOKEN" "$BASE/chats/$CHAT_ID/events" > "$STREAM" &
STREAM_PID=$!
sleep 2
# Both of these happen; only one belongs on the stream above.
api POST "/chats/$OTHER_ID/archive" >/dev/null || fail "archiving the other chat failed"
api POST "/chats/$CHAT_ID/send" -d '{"message": "still there?"}' >/dev/null \
  || fail "the third send failed"
wait_turn "$CHAT_ID" 3 >/dev/null
sleep 2
kill "$STREAM_PID" 2>/dev/null || true
wait "$STREAM_PID" 2>/dev/null || true
# A multi-line jq capture, so `tr -d '\r'`: jq writes CRLF on Windows, $( )
# strips only the trailing one, and the interior CRs would fail the match below.
IDS="$(sed -n 's/^data: //p' "$STREAM" | jq -r 'select(.payload) | .payload.id' | sort -u | tr -d '\r')"
case "$IDS" in
  *"$OTHER_ID"*) fail "another chat's events arrived on chat $CHAT_ID's stream" ;;
esac
printf '%s\n' "$IDS" | grep -x "$CHAT_ID" >/dev/null \
  || fail "this chat's own events never arrived on its stream: $IDS"

# Task 071 (issue #282): a chat's live-output chunks carry §13.3's typed names
# and normalized fields, with the agent's verbatim line kept beside them. Both
# halves are asserted here because both are the contract: a client renders the
# normalized fields, and `raw` stayed so nothing that read it reads less.
# Multi-line captures, so `tr -d '\r'` — jq writes CRLF on Windows.
NAMES="$(sed -n 's/^event: //p' "$STREAM" | tr -d '\r')"
CHUNK_TYPES="$(grep '^agent\.' <<<"$NAMES" | sort -u || true)"
[ -n "$CHUNK_TYPES" ] \
  || fail "the chat stream carried no §13.3 chunk types at all: $NAMES"
grep -x 'agent.output' <<<"$CHUNK_TYPES" >/dev/null \
  || fail "no agent.output chunk on the chat stream; got: $CHUNK_TYPES"
if grep -x 'output' <<<"$NAMES" >/dev/null; then
  fail "the pre-071 catch-all 'output' chunk type is still published"
fi
# One chunk carrying both a normalized field and the raw line is the whole
# claim; the transcript route is what a client refetches it from.
BOTH="$(sed -n 's/^data: //p' "$STREAM" \
  | jq -r 'select(.turn_id != null and .text != null and .text != "" and .raw != null and .raw != "") | .turn_id' \
  | sort -u | tr -d '\r')"
[ -n "$BOTH" ] \
  || fail "no chat chunk carried both a normalized field and its raw line"

echo "== 5. an awaiting_input chat is answered over the API"
"$VINCENT" daemon stop --force >/dev/null 2>&1 || true
export FAKEAGENT_SCENARIO=ask-question
"$VINCENT" daemon start
PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
TOKEN="$(cat "$DATA_DIR/token")"
BASE="http://127.0.0.1:$PORT/v1"

ASK="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"a question\", \"agent\": \"claude\"}")" \
  || fail "creating the question chat failed"
ASK_ID="$(printf '%s' "$ASK" | jq -r .id)"
api POST "/chats/$ASK_ID/send" -d '{"message": "ask me something"}' >/dev/null \
  || fail "the send failed"
wait_chat "$ASK_ID" awaiting_input
QUESTION="$(api GET "/chats/$ASK_ID" | jq -r '.chat.pending_input.Questions[0].Text // .chat.pending_input.questions[0].text')"
[[ -n "$QUESTION" ]] || fail "the parked chat carries no question"

echo "== 6. the cap refuses rather than queues"
CAP="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"over the cap\", \"agent\": \"claude\"}")" \
  || fail "creating the cap chat failed"
CAP_ID="$(printf '%s' "$CAP" | jq -r .id)"
CODE="$(api_status POST "/chats/$CAP_ID/send" -d '{"message": "me too"}')"
[[ "$CODE" == "409" ]] || fail "a send over max_parallel_chats answered $CODE, want 409"
REASON="$(jq -r .error.code < "$TMP/body.json")"
[[ "$REASON" == "chat_cap_reached" ]] || fail "the refusal is $REASON, want chat_cap_reached"
TURNS="$(api GET "/chats/$CAP_ID" | jq '.turns | length')"
[[ "$TURNS" == "0" ]] || fail "a refused send left $TURNS turn rows behind"

api POST "/chats/$ASK_ID/answer" \
  -d "{\"answers\": {\"$QUESTION\": [\"blue\"]}}" >/dev/null \
  || fail "POST /v1/chats/{id}/answer failed"
wait_turn "$ASK_ID" 1 >/dev/null
wait_chat "$ASK_ID" idle

echo "== 7. cancel stops a live turn; archive removes the worktree"
unset FAKEAGENT_SCENARIO
export FAKEAGENT_SCENARIO=hang
"$VINCENT" daemon stop --force >/dev/null 2>&1 || true
"$VINCENT" daemon start
PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
TOKEN="$(cat "$DATA_DIR/token")"
BASE="http://127.0.0.1:$PORT/v1"

api POST "/chats/$CAP_ID/send" -d '{"message": "run forever"}' >/dev/null \
  || fail "the hanging send failed"
wait_chat "$CAP_ID" running
api POST "/chats/$CAP_ID/cancel" >/dev/null || fail "POST /v1/chats/{id}/cancel failed"
STATE="$(wait_turn "$CAP_ID" 1)"
[[ "$STATE" == "failed" ]] || fail "a cancelled turn is $STATE, want failed"
wait_chat "$CAP_ID" idle

CAP_WORKTREE="$(api GET "/chats/$CAP_ID" | jq -r .chat.worktree_path)"
api POST "/chats/$CAP_ID/archive" >/dev/null || fail "POST /v1/chats/{id}/archive failed"
wait_chat "$CAP_ID" archived
[[ ! -d "$CAP_WORKTREE" ]] || fail "the archived chat's worktree is still at $CAP_WORKTREE"

echo "== 8. chats are not tasks"
TASKS="$(api GET /tasks | jq 'if type == "array" then length else (.tasks | length) end')"
[[ "$TASKS" == "0" ]] || fail "GET /v1/tasks lists $TASKS rows on a daemon with only chats"

echo "== 9. handoff: a task adopts the chat's worktree and branch (task 074)"
unset FAKEAGENT_SCENARIO
HAND="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"an exploration\", \"agent\": \"claude\"}")" \
  || fail "creating the handoff chat failed"
HAND_ID="$(printf '%s' "$HAND" | jq -r .id)"
HAND_WORKTREE="$(printf '%s' "$HAND" | jq -r .worktree_path)"
HAND_BRANCH="$(printf '%s' "$HAND" | jq -r .branch)"
HAND_BASE="$(printf '%s' "$HAND" | jq -r .base_branch)"
[[ -d "$HAND_WORKTREE" ]] || fail "the handoff chat has no worktree at $HAND_WORKTREE"

# Uncommitted work, written the way every gate writes a file: `git config -f`
# is in the sh∩pwsh intersection, and this is the change the handoff must
# preserve without committing it.
git -C "$HAND_WORKTREE" config -f explored.ini gate.explored yes \
  || fail "seeding the dirty file failed"
git -C "$HAND_WORKTREE" commit --allow-empty -m "chat work" >/dev/null 2>&1 \
  || fail "committing on the chat branch failed"
HAND_HEAD="$(git -C "$HAND_WORKTREE" rev-parse HEAD)"

HANDOFF="$(api POST "/chats/$HAND_ID/handoff" \
  -d '{"title": "finish the exploration"}')" || fail "POST /v1/chats/{id}/handoff failed"
TASK_ID="$(printf '%s' "$HANDOFF" | jq -r .task.id)"
[[ "$TASK_ID" != "null" && -n "$TASK_ID" ]] || fail "the handoff returned no task: $HANDOFF"

# The inheritance, field by field, off the task the daemon actually stored.
TASK="$(api GET "/tasks/$TASK_ID")"
for pair in "worktree_path:$HAND_WORKTREE" "branch_name:$HAND_BRANCH" "base_branch:$HAND_BASE"; do
  FIELD="${pair%%:*}"
  WANT="${pair#*:}"
  GOT="$(printf '%s' "$TASK" | jq -r ".$FIELD")"
  [[ "$GOT" == "$WANT" ]] || fail "the task's $FIELD is $GOT, want the chat's $WANT"
done
[[ "$(printf '%s' "$TASK" | jq -r .source_chat_id)" == "$HAND_ID" ]] \
  || fail "the task does not link back to chat $HAND_ID"

# The workspace is untouched: no new commit, and the uncommitted file is still
# uncommitted and still there.
[[ "$(git -C "$HAND_WORKTREE" rev-parse HEAD)" == "$HAND_HEAD" ]] \
  || fail "the handoff moved HEAD in $HAND_WORKTREE"
[[ "$(git -C "$HAND_WORKTREE" config -f explored.ini --get gate.explored)" == "yes" ]] \
  || fail "the uncommitted file did not survive the handoff"
STATUS="$(git -C "$HAND_WORKTREE" status --porcelain)"
[[ -n "$STATUS" ]] || fail "the handoff committed the dirty worktree"

# The chat is terminal, links the task, and has released its claim.
wait_chat "$HAND_ID" handed_off
HAND_AFTER="$(api GET "/chats/$HAND_ID" | jq .chat)"
[[ "$(printf '%s' "$HAND_AFTER" | jq -r .handoff_task_id)" == "$TASK_ID" ]] \
  || fail "the chat does not link task $TASK_ID"
[[ "$(printf '%s' "$HAND_AFTER" | jq -r '.worktree_path // ""')" == "" ]] \
  || fail "the handed-off chat still claims a worktree"

# Terminal means terminal: every chat action is a 409, archive included, so
# chat cleanup can never reach the task's worktree.
for ROUTE in handoff send cancel archive; do
  BODY='{}'
  if [[ "$ROUTE" == "send" ]]; then BODY='{"message": "still there?"}'; fi
  if [[ "$ROUTE" == "handoff" ]]; then BODY='{"title": "again"}'; fi
  CODE="$(api_status POST "/chats/$HAND_ID/$ROUTE" -d "$BODY")"
  [[ "$CODE" == "409" ]] || fail "$ROUTE on a handed-off chat is $CODE, want 409"
done
[[ -d "$HAND_WORKTREE" ]] || fail "a refused archive removed the task's worktree"

# And the ownership claim gc sees: the inherited directory is not an orphan.
ORPHANS="$(api GET /info | jq -r '.orphans // 0')"
[[ "$ORPHANS" == "0" ]] || fail "the handoff left $ORPHANS orphan(s) behind"

echo "GATE PASS: m14"
