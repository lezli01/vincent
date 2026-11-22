#!/usr/bin/env bash
# Task 171 gate (issue #1020; spec §5.5, §13.2): asking another agent — a chat
# forked onto a different agent with an editable turn-0 context — and
# comparing two agents' answers, driven over the wire and through the CLI
# against the committed fakeagent, so CI never calls a real agent CLI.
#
#   1. a claude chat forked onto codex (`vincent chat fork --agent codex`) is
#      seeded, names its source's agent in forked_from, carries the seeded
#      context as pending_preamble — the kept turn's prompt, the source agent
#      named — and `chat show` prints it under "turn 0"; an unknown agent is
#      400, and a model on a same-agent fork is 400
#   2. turn 0 is editable until the first turn: `vincent chat preamble
#      --file` replaces it, the codex CLI is handed exactly the edited text,
#      and afterwards the edit is refused (409 chat_preamble_not_pending) while
#      the context lives on — at the head of `chat transcript --turn 1` and in
#      the export — and `--turn 0` says nothing is pending; `--clear` on
#      another fork drops it
#   3. `vincent chat compare --agents claude,codex` from before turn 2 forks
#      twice and starts both on turn 2's own prompt: claude restricted, codex
#      full-auto with a mode_note; the pair is read from either side and
#      printed by `chat show`; with max_parallel_chats leaving room for one,
#      a compare is 409 chat_cap_reached and creates nothing
#   4. keeping one side (`vincent chat keep`) archives the other, deletes its
#      branch and its checkpoint refs, and leaves the kept side idle; a second
#      keep is 409 compare_kept
#   5. while a side's turn is live, keeping is 409 chat_turn_live
#
# Every scenario starts chats of its own, so VINCENT_GATE_SCENARIO=N runs any
# one of them alone — except 4, which keeps scenario 3's compare and so runs
# 3 first. Scenario 5 goes last: it restarts the daemon with a hanging agent.
#
# The gate is workflow-free: a chat has none, so there is no `run:` body here
# to spell in the sh∩pwsh intersection. Its own bash keeps the standing
# rules: `| tr -d '\r'` on multi-line captures, and never `| grep -q` —
# capture first, then match a here-string.
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
  "" | [1-5]) ;;
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
# Every prompt the fake CLI is handed, one JSON string a line: what scenario
# 2 reads to prove the codex side was given the edited context.
PROMPTS="$TMP/prompts.jsonl"
export FAKEAGENT_SCENARIO=echo-prompt
export FAKEAGENT_PROMPT_FILE
FAKEAGENT_PROMPT_FILE="$(hostpath "$PROMPTS")"

REPO="$TMP/repo"
mkdir -p "$REPO"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email gate@example.com
git -C "$REPO" config user.name "171 Gate"
git -C "$REPO" config core.autocrlf false
printf 'original\n' > "$REPO/tracked.txt"
git -C "$REPO" add tracked.txt
git -C "$REPO" commit -q -m "root"

cat > "$CONFIG_DIR/config.yaml" <<EOF
max_parallel_chats: 4
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

# contains HAYSTACK NEEDLE WHAT — a fixed-string match on a captured value.
contains() {
  grep -qF -- "$2" <<<"$1" || fail "$3: no [$2] in:"$'\n'"$1"
}

lacks() {
  if grep -qF -- "$2" <<<"$1"; then fail "$3: [$2] should not be in:"$'\n'"$1"; fi
}

# chat_field ID JQ — one value off GET /v1/chats/ID.
chat_field() {
  api GET "/chats/$1" | jq -r "$2" | tr -d '\r'
}

# wait_idle ID [TURNS] — poll until the chat is idle (with TURNS turns, when
# given) and its last turn has ended.
wait_idle() {
  local id="$1" n="${2:-}" i=0 ticks state
  ticks="$(gate_ticks 300)"
  while (( i < ticks )); do
    state="$(api GET "/chats/$id" | jq -r '[.chat.state, (.turns | length), (.turns[-1].state // "none")] | join(" ")' | tr -d '\r')"
    read -r st count last <<<"$state"
    if [[ "$st" == "idle" && "$last" != "running" && ( -z "$n" || "$count" == "$n" ) ]]; then
      return 0
    fi
    i=$(( i + 1 ))
    sleep "$GATE_POLL"
  done
  fail "chat $id never came back to idle${n:+ with $n turns} (it is $state)"
}

# new_chat AGENT TITLE — a fresh chat, its id in CHAT_ID.
new_chat() {
  local chat
  chat="$(api POST /chats -d "{\"project_id\": $PROJECT_ID, \"title\": \"$2\", \"agent\": \"$1\"}")" \
    || fail "POST /v1/chats failed"
  CHAT_ID="$(printf '%s' "$chat" | jq -r .id)"
  [[ "$CHAT_ID" != "null" && -n "$CHAT_ID" ]] || fail "creating the chat failed: $chat"
}

# turn ID MESSAGE N — send one turn and wait for the chat to hold N turns.
turn() {
  local code state
  code="$(api_status POST "/chats/$1/send" -d "{\"message\": \"$2\"}")"
  [[ "$code" == "202" ]] || fail "send to chat $1 answered $code: $(body)"
  wait_idle "$1" "$3"
  state="$(chat_field "$1" '.turns[-1].state')"
  [[ "$state" == "done" ]] || fail "the turn '$2' of chat $1 ended $state"
}

# source_chat — a claude chat with two done turns, its id in SRC.
source_chat() {
  new_chat claude "$1"
  SRC="$CHAT_ID"
  turn "$SRC" "first question" 1
  turn "$SRC" "second question" 2
}

# chat_refs ID — chat ID's checkpoint refs in REPO, one per line (they live
# under refs/vincent/<install id>/chats/ID/, and the gate does not know its
# installation's id).
chat_refs() {
  local all line out=""
  all="$(git -C "$REPO" for-each-ref --format='%(refname)' refs/vincent/ | tr -d '\r')"
  while IFS= read -r line; do
    if [[ "$line" == refs/vincent/*/chats/"$1"/* ]]; then out+="$line"$'\n'; fi
  done <<<"$all"
  printf '%s' "$out"
}

chat_count() {
  api GET "/chats?project_id=$PROJECT_ID" | jq -r '.chats | length' | tr -d '\r'
}

cross_agent_fork() {
  echo "== 1. a fork onto another agent is seeded, names its source, shows turn 0"
  source_chat "fork source"
  local out code got
  code="$(api_status POST "/chats/$SRC/fork" -d '{"turn": 2, "agent": "nope"}')"
  expect_refusal "$code" 400 validation_failed "a fork onto an unknown agent"
  code="$(api_status POST "/chats/$SRC/fork" -d '{"turn": 2, "model": "x"}')"
  expect_refusal "$code" 400 validation_failed "a same-agent fork with a model"

  out="$("$VINCENT" chat fork "$SRC" --turn 2 --agent codex --json)" || fail "chat fork --agent codex failed: $out"
  FORK="$(jq -r .chat.id <<<"$out")"
  got="$(jq -r '[.seeded, .chat.agent, .chat.forked_from.chat_id, .chat.forked_from.agent, .chat.resume_pending] | map(tostring) | join(" ")' <<<"$out" | tr -d '\r')"
  [[ "$got" == "true codex $SRC claude seeded" ]] || fail "the fork reads [$got], want [true codex $SRC claude seeded]"
  got="$(chat_field "$FORK" '.chat.pending_preamble')"
  contains "$got" "first question" "the fork's pending_preamble"
  contains "$got" "held with claude" "the fork's pending_preamble"
  lacks "$got" "second question" "the fork's pending_preamble (turn 2 was not kept)"

  out="$("$VINCENT" chat show "$FORK" | tr -d '\r')"
  contains "$out" "forked from chat $SRC (claude), before turn 2" "chat show on the fork"
  contains "$out" "turn 0 · seeded context (summary)" "chat show on the fork"
  contains "$out" "first question" "chat show's turn 0"
}

turn_zero() {
  echo "== 2. turn 0 is edited before the first turn, handed to the agent, and kept after"
  if [[ -z "${FORK:-}" ]]; then cross_agent_fork; fi
  local edit="$TMP/edited.txt" out code got
  printf 'EDITED-CONTEXT: the human asked a first question, and you answered it.\n' > "$edit"
  out="$("$VINCENT" chat preamble "$FORK" --file "$(hostpath "$edit")")" || fail "chat preamble --file failed: $out"
  got="$(chat_field "$FORK" '.chat.pending_preamble')"
  contains "$got" "EDITED-CONTEXT" "pending_preamble after the edit"
  lacks "$got" "held with claude" "pending_preamble after the edit"

  turn "$FORK" "third question" 1
  got="$(tail -n 1 "$PROMPTS" | tr -d '\r')"
  contains "$got" "EDITED-CONTEXT" "the codex CLI's prompt"
  contains "$got" "third question" "the codex CLI's prompt"
  lacks "$got" "held with claude" "the codex CLI's prompt"
  got="$(chat_field "$FORK" '[.turns[0].seeded, .chat.pending_preamble // ""] | map(tostring) | join(" ")')"
  [[ "$got" == "true " ]] || fail "after the first turn the fork reads [$got], want a seeded turn and no pending context"

  code="$(api_status PATCH "/chats/$FORK" -d '{"pending_preamble": "late"}')"
  expect_refusal "$code" 409 chat_preamble_not_pending "a turn-0 edit after the first turn"
  out="$("$VINCENT" chat transcript "$FORK" --turn 1 | tr -d '\r')"
  contains "$out" "── turn 0 · seeded context (summary) ──" "chat transcript --turn 1"
  contains "$out" "EDITED-CONTEXT" "chat transcript --turn 1"
  if out="$("$VINCENT" chat transcript "$FORK" --turn 0 2>&1)"; then
    fail "chat transcript --turn 0 with nothing pending exited 0: $out"
  fi
  contains "$out" "no pending turn 0" "chat transcript --turn 0"
  out="$(api GET "/chats/$FORK/export?format=md" | tr -d '\r')"
  contains "$out" "### Turn 0 · seeded context (summary)" "the export"
  contains "$out" "EDITED-CONTEXT" "the export"
  contains "$out" "**forked from:** #$SRC (claude), before turn 2" "the export"

  # --clear on a fresh fork drops the context entirely.
  out="$("$VINCENT" chat fork "$SRC" --turn 2 --agent codex --json)" || fail "a second fork failed: $out"
  local second
  second="$(jq -r .chat.id <<<"$out")"
  "$VINCENT" chat preamble "$second" --clear >/dev/null || fail "chat preamble --clear failed"
  got="$(chat_field "$second" '[.chat.pending_preamble // "", .chat.resume_pending // ""] | join("|")')"
  [[ "$got" == "|" ]] || fail "after --clear the fork reads [$got], want no context and nothing pending"
}

compare() {
  echo "== 3. a compare forks twice, runs each side in the mode it can, and is all or nothing"
  source_chat "compare source"
  CMP_SRC="$SRC"
  local before out code got

  "$VINCENT" config set max_parallel_chats 1 >/dev/null || fail "lowering max_parallel_chats failed"
  before="$(chat_count)"
  code="$(api_status POST "/chats/$CMP_SRC/compare" -d '{"turn": 2, "agents": ["claude", "codex"]}')"
  expect_refusal "$code" 409 chat_cap_reached "a compare with room for one"
  got="$(chat_count)"
  [[ "$got" == "$before" ]] || fail "a refused compare left chats behind: $before before, $got after"
  "$VINCENT" config set max_parallel_chats 4 >/dev/null || fail "restoring max_parallel_chats failed"

  out="$("$VINCENT" chat compare "$CMP_SRC" --turn 2 --agents claude,codex --json)" || fail "chat compare failed: $out"
  LEFT="$(jq -r .left.chat.id <<<"$out")"
  RIGHT="$(jq -r .right.chat.id <<<"$out")"
  got="$(jq -r '[.compare.prompt, .compare.left.permission_mode, .compare.right.permission_mode] | join("|")' <<<"$out" | tr -d '\r')"
  [[ "$got" == "second question|restricted|full-auto" ]] \
    || fail "the compare reads [$got], want turn 2's prompt, claude restricted and codex full-auto"
  got="$(jq -r .compare.right.mode_note <<<"$out")"
  contains "$got" "codex" "the codex side's mode_note"
  RIGHT_BRANCH="$(jq -r .right.chat.branch <<<"$out" | tr -d '\r')"

  wait_idle "$LEFT" 1
  wait_idle "$RIGHT" 1
  local id
  for id in "$LEFT" "$RIGHT"; do
    got="$(chat_field "$id" '[.turns[0].prompt, .turns[0].state, .chat.forked_from.chat_id] | map(tostring) | join("|")')"
    [[ "$got" == "second question|done|$CMP_SRC" ]] || fail "side $id reads [$got]"
    got="$(api GET "/chats/$id/compare" | jq -r '[.compare.left.chat_id, .compare.right.chat_id] | map(tostring) | join(" ")' | tr -d '\r')"
    [[ "$got" == "$LEFT $RIGHT" ]] || fail "GET compare from side $id reads [$got]"
  done
  got="$(chat_field "$LEFT" '.turns[0].permission_mode')"
  [[ "$got" == "restricted" ]] || fail "the claude side's turn ran $got, want restricted"
  code="$(api_status GET "/chats/$CMP_SRC/compare")"
  [[ "$code" == "404" ]] || fail "GET compare on the source answered $code, want 404"
  out="$("$VINCENT" chat show "$LEFT" | tr -d '\r')"
  contains "$out" "left chat $LEFT (this one), claude, restricted; right chat $RIGHT, codex, full-auto" "chat show on a side"
}

keep() {
  echo "== 4. keeping a side archives the other with its branch and refs"
  if [[ -z "${LEFT:-}" ]]; then compare; fi
  local out code got
  [[ -n "$(chat_refs "$RIGHT")" ]] || fail "the right side has no checkpoint refs before the keep"
  out="$("$VINCENT" chat keep "$LEFT" | tr -d '\r')" || fail "chat keep failed: $out"
  contains "$out" "kept chat $LEFT (claude)" "chat keep"
  contains "$out" "archived chat $RIGHT (codex); branch $RIGHT_BRANCH deleted" "chat keep"
  got="$(chat_field "$RIGHT" .chat.state)"
  [[ "$got" == "archived" ]] || fail "the discarded side is $got, want archived"
  got="$(git -C "$REPO" branch --list "$RIGHT_BRANCH" | tr -d '\r')"
  [[ -z "$got" ]] || fail "the discarded side's branch $RIGHT_BRANCH is still there"
  got="$(chat_refs "$RIGHT")"
  [[ -z "$got" ]] || fail "the discarded side's refs are still there: $got"
  got="$(chat_field "$LEFT" .chat.state)"
  [[ "$got" == "idle" ]] || fail "the kept side is $got, want idle"
  got="$(api GET "/chats/$LEFT/compare" | jq -r .compare.kept_chat_id | tr -d '\r')"
  [[ "$got" == "$LEFT" ]] || fail "kept_chat_id is $got, want $LEFT"
  code="$(api_status POST "/chats/$LEFT/compare/keep" -d '{}')"
  expect_refusal "$code" 409 compare_kept "a second keep"
}

live_keep() {
  echo "== 5. a live side refuses a keep"
  source_chat "live source"
  export FAKEAGENT_SCENARIO=hang
  start_daemon
  local code left right
  code="$(api_status POST "/chats/$SRC/compare" -d '{"turn": 2, "agents": ["claude", "codex"]}')"
  [[ "$code" == "201" ]] || fail "the hanging compare answered $code: $(body)"
  left="$(jq -r .left.chat.id < "$TMP/body.json")"
  right="$(jq -r .right.chat.id < "$TMP/body.json")"
  code="$(api_status POST "/chats/$left/compare/keep" -d '{}')"
  expect_refusal "$code" 409 chat_turn_live "a keep while both sides run"
  api POST "/chats/$left/cancel" >/dev/null || true
  api POST "/chats/$right/cancel" >/dev/null || true
}

start_daemon
PROJECT="$(api POST /projects -d "{\"path\": \"$(hostpath "$REPO")\"}")" \
  || fail "registering the project failed"
PROJECT_ID="$(printf '%s' "$PROJECT" | jq -r .id)"
[[ "$PROJECT_ID" != "null" && -n "$PROJECT_ID" ]] || fail "registering the project failed: $PROJECT"

if want 1; then cross_agent_fork; fi
if want 2; then turn_zero; fi
if want 3; then compare; fi
if want 4; then keep; fi
if want 5; then live_keep; fi

echo "GATE PASS: 171 (ask another agent and compare two answers)"
