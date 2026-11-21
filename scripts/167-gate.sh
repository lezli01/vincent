#!/usr/bin/env bash
# Task 167 gate (issue #1041; spec §5.5, §13.2): rewinding and forking a chat
# from a per-turn checkpoint, driven over the wire against the committed
# fakeagent, so CI never calls a real agent CLI.
#
#   1. every turn leaves a checkpoint: checkpoint_sha on the turn and
#      refs/vincent/chats/<id>/turn-<seq> pointing at it; a code rewind to
#      turn 1 restores the modified, deleted and untracked files it started
#      with exactly, removes what came later, and saves the replaced state as
#      rewound-1, which a forward rewind (`rewound: 1`) puts back
#   2. a commit no turn made refuses a code rewind with 409
#      chat_rewind_foreign_commits, and a fork from the same chat still works
#   3. a conversation rewind on a claude-shaped chat is resumed: the rewound
#      turn is marked, its prompt is the draft, and the next turn runs in a
#      different (forked) session with a resume anchor and seeded=false
#   4. the same on a codex-shaped chat is seeded: the rewind says seeded and
#      the next turn carries seeded=true
#   5. a fork copies the source's settings and base, is idle with no turn,
#      and its worktree holds the checkpoint's dirty state, unstaged
#   6. a permanent delete prunes the chat's refs; an archive keeps them
#   7. while a turn is live, rewind and fork are 409 chat_turn_live
#
# Every scenario starts a chat of its own, so VINCENT_GATE_SCENARIO=N runs any
# one of them without the others. Scenario 7 goes last: it restarts the
# daemon with a hanging agent.
#
# The gate is workflow-free: a chat has none, so there is no `run:` body here
# to spell in the sh∩pwsh intersection. Its own bash keeps the standing
# rules: `| tr -d '\r'` on multi-line captures, file content compared with
# its CRs dropped, and never `| grep -q`.
#
# Requirements: bash, go, git, curl, jq.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/gate.sh
source "$ROOT/scripts/lib/gate.sh"
TMP="$(mktemp -d)"
BIN="$TMP/bin"

VINCENT="$BIN/vincent$(gate_exe)"
FAKEAGENT="$BIN/fakeagent$(gate_exe)"

fail() { echo "GATE FAIL: $*" >&2; exit 1; }

cleanup() {
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

ONLY="${VINCENT_GATE_SCENARIO:-}"
case "$ONLY" in
  "" | [1-7]) ;;
  *) fail "unknown VINCENT_GATE_SCENARIO: $ONLY" ;;
esac
want() { [[ -z "$ONLY" || "$ONLY" == "$1" ]]; }

hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

echo "== build vincent + fakeagent"
gate_build "$BIN" vincent fakeagent

CONFIG_DIR="$TMP/config"
DATA_DIR="$TMP/data"
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"

export FAKEAGENT_SESSION_DIR
FAKEAGENT_SESSION_DIR="$(hostpath "$TMP/sessions")"
mkdir -p "$TMP/sessions"

REPO="$TMP/repo"
mkdir -p "$REPO"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email gate@example.com
git -C "$REPO" config user.name "167 Gate"
git -C "$REPO" config core.autocrlf false
printf 'original\n' > "$REPO/tracked.txt"
printf 'doomed\n' > "$REPO/gone.txt"
git -C "$REPO" add tracked.txt gone.txt
git -C "$REPO" commit -q -m "root"

cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
  codex:
    path: "$(hostpath "$FAKEAGENT")"
EOF

# start_daemon (re)starts the daemon on the environment exported at the call:
# the agent child inherits the daemon's, so a FAKEAGENT_* knob only reaches a
# turn through a start.
start_daemon() {
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
}

api() {
  local method="$1" path="$2"
  shift 2
  curl -sS -X "$method" -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" "$@" "$BASE$path"
}

# api_status METHOD PATH [curl args...] -> the status code, the body in
# $TMP/body.json.
api_status() {
  local method="$1" path="$2"
  shift 2
  curl -sS -o "$TMP/body.json" -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    "$@" "$BASE$path"
}

body() { cat "$TMP/body.json"; }

expect_refusal() {
  local code="$1" want_status="$2" want_error="$3" what="$4" got
  [[ "$code" == "$want_status" ]] || fail "$what answered $code, want $want_status: $(body)"
  got="$(jq -r .error.code < "$TMP/body.json")"
  [[ "$got" == "$want_error" ]] || fail "$what was refused as $got, want $want_error"
}

# wait_idle CHAT_ID — poll until the chat is idle and its last turn's rewind
# options no longer say a turn is live (the queue has drained).
wait_idle() {
  local id="$1" i=0 ticks state live
  ticks="$(gate_ticks 300)"
  while (( i < ticks )); do
    state="$(api GET "/chats/$id" | jq -r '[.chat.state, (.turns[-1].rewind.code // "none")] | join(" ")')"
    live="${state#* }"
    if [[ "${state%% *}" == "idle" && "$live" != "chat_turn_live" ]]; then return 0; fi
    i=$(( i + 1 ))
    sleep "$GATE_POLL"
  done
  fail "chat $id never came back to idle (it is $state)"
}

# new_chat AGENT TITLE — a fresh chat, its id in CHAT_ID and worktree in WT.
new_chat() {
  local chat
  chat="$(api POST /chats -d "{\"project_id\": $PROJECT_ID, \"title\": \"$2\", \"agent\": \"$1\", \"model\": \"gate-model\"}")" \
    || fail "POST /v1/chats failed"
  CHAT_ID="$(printf '%s' "$chat" | jq -r .id)"
  [[ "$CHAT_ID" != "null" && -n "$CHAT_ID" ]] || fail "creating the chat failed: $chat"
  WT="$(printf '%s' "$chat" | jq -r .worktree_path)"
  [[ -d "$WT" ]] || fail "chat $CHAT_ID has no worktree at $WT"
}

# turn MESSAGE — send one turn to CHAT_ID and wait for it to end done.
turn() {
  local code state
  code="$(api_status POST "/chats/$CHAT_ID/send" -d "{\"message\": \"$1\"}")"
  [[ "$code" == "202" ]] || fail "send to chat $CHAT_ID answered $code: $(body)"
  wait_idle "$CHAT_ID"
  state="$(api GET "/chats/$CHAT_ID" | jq -r '.turns[-1].state')"
  [[ "$state" == "done" ]] || fail "the turn '$1' of chat $CHAT_ID ended $state"
}

# turn_field SEQ FIELD — one field of one turn of CHAT_ID.
turn_field() {
  api GET "/chats/$CHAT_ID" | jq -r --argjson s "$1" --arg f "$2" '.turns[] | select(.seq == $s) | .[$f] | if . == null then "" else tostring end' | tr -d '\r'
}

# content PATH — a worktree file with its CRs dropped, or <absent>.
content() {
  if [[ -e "$WT/$1" ]]; then tr -d '\r' < "$WT/$1"; else printf '<absent>'; fi
}

expect_content() {
  local path="$1" want="$2" what="$3" got
  got="$(content "$path")"
  [[ "$got" == "$want" ]] || fail "$what: $path is [$got], want [$want]"
}

rewind() { api_status POST "/chats/$CHAT_ID/rewind" -d "$1"; }

# pre_turn_one_state dirties WT the way scenarios 1 and 5 start turn 1:
# tracked.txt modified, gone.txt deleted, untracked.txt new.
pre_turn_one_state() {
  printf 'edited\n' > "$WT/tracked.txt"
  rm -f "$WT/gone.txt"
  printf 'untracked\n' > "$WT/untracked.txt"
}

checkpoints_and_code_rewind() {
  echo "== 1. a checkpoint per turn; a code rewind restores exactly; a forward rewind"
  new_chat claude "code rewind"
  pre_turn_one_state
  turn "one"
  local sha ref
  sha="$(turn_field 1 checkpoint_sha)"
  [[ -n "$sha" ]] || fail "turn 1 carries no checkpoint_sha"
  ref="$(git -C "$REPO" rev-parse --verify --quiet "refs/vincent/chats/$CHAT_ID/turn-1" || true)"
  [[ "$ref" == "$sha" ]] || fail "refs/vincent/chats/$CHAT_ID/turn-1 is [$ref], want turn 1's checkpoint $sha"

  # What turn 2 starts from: every one of turn 1's changes undone or moved.
  printf 'again\n' > "$WT/tracked.txt"
  git -C "$WT" checkout -q -- gone.txt
  rm -f "$WT/untracked.txt"
  printf 'later\n' > "$WT/later.txt"
  turn "two"
  [[ -n "$(turn_field 2 checkpoint_sha)" ]] || fail "turn 2 carries no checkpoint_sha"

  local code got
  code="$(rewind '{"turn": 1, "mode": "code"}')"
  [[ "$code" == "200" ]] || fail "code rewind to turn 1 answered $code: $(body)"
  got="$(jq -r .ref < "$TMP/body.json")"
  [[ "$got" == "refs/vincent/chats/$CHAT_ID/rewound-1" ]] || fail "the rewind wrote ref [$got]"
  git -C "$REPO" rev-parse --verify --quiet "$got" >/dev/null || fail "$got does not resolve"
  expect_content tracked.txt edited "after the code rewind"
  expect_content gone.txt "<absent>" "after the code rewind"
  expect_content untracked.txt untracked "after the code rewind"
  expect_content later.txt "<absent>" "after the code rewind"
  git -C "$WT" diff --cached --quiet || fail "the code rewind staged something"

  got="$(api GET "/chats/$CHAT_ID" | jq -r '.chat.rewound_states | length')"
  [[ "$got" == "1" ]] || fail "rewound_states has $got entries, want 1"
  code="$(rewind '{"rewound": 1}')"
  [[ "$code" == "200" ]] || fail "forward rewind answered $code: $(body)"
  expect_content tracked.txt again "after the forward rewind"
  expect_content gone.txt doomed "after the forward rewind"
  expect_content untracked.txt "<absent>" "after the forward rewind"
  expect_content later.txt later "after the forward rewind"

  code="$(rewind '{"turn": 1, "rewound": 1}')"
  expect_refusal "$code" 400 validation_failed "a rewind naming both targets"
}

foreign_commits() {
  echo "== 2. a commit no turn made refuses a code rewind; a fork still works"
  new_chat claude "foreign"
  turn "one"
  git -C "$WT" -c user.name=x -c user.email=x@example.com commit -q --allow-empty -m "not a turn"
  local code
  code="$(rewind '{"turn": 1, "mode": "code"}')"
  expect_refusal "$code" 409 chat_rewind_foreign_commits "a code rewind over a foreign commit"
  code="$(api_status POST "/chats/$CHAT_ID/fork" -d '{"turn": 1}')"
  [[ "$code" == "201" ]] || fail "a fork over a foreign commit answered $code: $(body)"
}

conversation_resumed() {
  echo "== 3. a conversation rewind on claude is resumed"
  new_chat claude "resumed"
  turn "one"
  turn "two"
  local session1 code got
  session1="$(turn_field 1 session_id)"
  code="$(rewind '{"turn": 2, "mode": "conversation"}')"
  [[ "$code" == "200" ]] || fail "conversation rewind answered $code: $(body)"
  got="$(jq -r '[.seeded, (.rewound_turns | map(tostring) | join(","))] | join(" ")' < "$TMP/body.json")"
  [[ "$got" == "false 2" ]] || fail "conversation rewind said [$got], want [false 2]"
  [[ -n "$(turn_field 2 rewound_at)" ]] || fail "turn 2 is not marked rewound"
  got="$(api GET "/chats/$CHAT_ID" | jq -r '[.chat.draft, .chat.resume_pending] | join("|")')"
  [[ "$got" == "two|resumed" ]] || fail "after the rewind the chat reads [$got], want [two|resumed]"
  turn "three"
  got="$(turn_field 3 session_id)"
  [[ -n "$got" && "$got" != "$session1" ]] || fail "turn 3 ran in session [$got], want a fork of [$session1]"
  [[ -n "$(turn_field 3 resume_anchor)" ]] || fail "turn 3 records no resume anchor"
  [[ "$(turn_field 3 seeded)" == "false" ]] || fail "turn 3 says it was seeded"
}

conversation_seeded() {
  echo "== 4. a conversation rewind on codex is seeded"
  new_chat codex "seeded"
  turn "one"
  turn "two"
  local code got
  code="$(rewind '{"turn": 2}')"
  [[ "$code" == "200" ]] || fail "codex rewind answered $code: $(body)"
  got="$(jq -r .seeded < "$TMP/body.json")"
  [[ "$got" == "true" ]] || fail "a codex rewind said seeded=$got"
  turn "three"
  [[ "$(turn_field 3 seeded)" == "true" ]] || fail "turn 3 of the codex chat is not seeded"
}

fork_copies() {
  echo "== 5. a fork copies settings and dirty state"
  new_chat claude "fork source"
  pre_turn_one_state
  turn "one"
  local src code fork got
  src="$(api GET "/chats/$CHAT_ID" | jq -c '.chat | {agent, model, effort, permission_mode, base_branch}')"
  code="$(api_status POST "/chats/$CHAT_ID/fork" -d '{"turn": 1, "title": "the fork"}')"
  [[ "$code" == "201" ]] || fail "fork answered $code: $(body)"
  fork="$(jq -r .chat.id < "$TMP/body.json")"
  got="$(api GET "/chats/$fork" | jq -c '.chat | {agent, model, effort, permission_mode, base_branch}')"
  [[ "$got" == "$src" ]] || fail "the fork's settings $got differ from the source's $src"
  got="$(api GET "/chats/$fork" | jq -r '[.chat.state, (.turns | length), .chat.forked_from.chat_id, .chat.forked_from.seq, (.chat.linked_task_id // "none"), (.chat.issue_id // "none")] | map(tostring) | join(" ")')"
  [[ "$got" == "idle 0 $CHAT_ID 1 none none" ]] || fail "the fork reads [$got]"
  WT="$(api GET "/chats/$fork" | jq -r .chat.worktree_path)"
  expect_content tracked.txt edited "in the fork"
  expect_content gone.txt "<absent>" "in the fork"
  expect_content untracked.txt untracked "in the fork"
  git -C "$WT" diff --cached --quiet || fail "the fork's dirty state is staged"
}

pruning() {
  echo "== 6. a permanent delete prunes the refs; an archive keeps them"
  new_chat claude "pruned"
  turn "one"
  local code refs
  code="$(api_status POST "/chats/$CHAT_ID/archive?force=true")"
  [[ "$code" == "200" ]] || fail "archive answered $code: $(body)"
  refs="$(git -C "$REPO" for-each-ref --format='%(refname)' "refs/vincent/chats/$CHAT_ID/" | tr -d '\r')"
  [[ -n "$refs" ]] || fail "the archive pruned chat $CHAT_ID's refs"
  code="$(api_status DELETE "/chats/$CHAT_ID")"
  [[ "$code" == "200" ]] || fail "delete answered $code: $(body)"
  refs="$(git -C "$REPO" for-each-ref --format='%(refname)' "refs/vincent/chats/$CHAT_ID/" | tr -d '\r')"
  [[ -z "$refs" ]] || fail "the delete left [$refs]"
}

live_turn() {
  echo "== 7. a live turn refuses rewind and fork"
  new_chat claude "live"
  turn "one"
  export FAKEAGENT_SCENARIO=hang
  start_daemon
  local code
  code="$(api_status POST "/chats/$CHAT_ID/send" -d '{"message": "hold"}')"
  [[ "$code" == "202" ]] || fail "send answered $code: $(body)"
  code="$(rewind '{"turn": 1}')"
  expect_refusal "$code" 409 chat_turn_live "a rewind during a live turn"
  code="$(api_status POST "/chats/$CHAT_ID/fork" -d '{"turn": 1}')"
  expect_refusal "$code" 409 chat_turn_live "a fork during a live turn"
  api POST "/chats/$CHAT_ID/cancel" >/dev/null || true
}

start_daemon
PROJECT="$(api POST /projects -d "{\"path\": \"$(hostpath "$REPO")\"}")" \
  || fail "registering the project failed"
PROJECT_ID="$(printf '%s' "$PROJECT" | jq -r .id)"
[[ "$PROJECT_ID" != "null" && -n "$PROJECT_ID" ]] || fail "registering the project failed: $PROJECT"

if want 1; then checkpoints_and_code_rewind; fi
if want 2; then foreign_commits; fi
if want 3; then conversation_resumed; fi
if want 4; then conversation_seeded; fi
if want 5; then fork_copies; fi
if want 6; then pruning; fi
if want 7; then live_turn; fi

echo "GATE PASS: 167 (rewind and fork a chat from a turn)"
