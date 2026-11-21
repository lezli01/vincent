#!/usr/bin/env bash
# Task 170 gate (issue #1024; spec §5.5, §13.2): a chat's shell passthrough,
# driven over the wire and through the real binary against the committed
# fakeagent, so CI never calls a real agent CLI.
#
#   1. a command runs in the chat's worktree: POST /v1/chats/{id}/shell
#      answers 202 `running`, the run ends `exited` 0, its output route
#      holds what the command printed (the worktree's own HEAD), and
#      `vincent chat shell` prints the same output and exits 0
#   2. a failing command reports its exit code: the row says `exited` 7, and
#      `vincent chat shell` exits 7 with it
#   3. one command at a time: a second POST while one runs is 409
#      invalid_state, and so is `vincent chat shell` (exit 1); the chat's
#      cancel stops the running one, which ends `canceled`
#   4. an attached run reaches the next turn and only that one: the run is
#      marked (and the mark taken off and put back), a message is sent, the
#      fake agent's recorded prompt for that turn carries the command and its
#      output, the row reads attached_turn 1 and is no longer marked; the
#      following turn's prompt carries neither, and attaching the taken run
#      again is 409
#   5. a terminal chat refuses: an archived chat's POST is 409 invalid_state
#      and `vincent chat shell` exits 1 with the daemon's reason, while its
#      earlier run still lists
#
# Every scenario starts a chat of its own, so VINCENT_GATE_SCENARIO=N runs
# any one of them without the others.
#
# The commands the daemon runs go through its shell — /bin/sh on POSIX, pwsh
# on Windows (§8.3) — so they are spelled in the intersection of the two:
# `git ...`, `exit N` as the whole body, and `sleep N`. This gate's own bash
# obeys the standing rules: `| tr -d '\r'` on any multi-line capture, and
# never `| grep -q`.
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

# The fake CLI's own conversation store, and the file echo-prompt appends
# every prompt it is handed to — one JSON value a line — which is how
# scenario 4 reads what the agent was sent.
export FAKEAGENT_SESSION_DIR
FAKEAGENT_SESSION_DIR="$(hostpath "$TMP/sessions")"
mkdir -p "$TMP/sessions"
PROMPTS="$TMP/prompts.jsonl"
export FAKEAGENT_SCENARIO=echo-prompt
export FAKEAGENT_PROMPT_FILE
FAKEAGENT_PROMPT_FILE="$(hostpath "$PROMPTS")"

ROOT_SUBJECT="170 gate root"

REPO="$TMP/repo"
mkdir -p "$REPO"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email gate@example.com
git -C "$REPO" config user.name "170 Gate"
git -C "$REPO" commit -q --allow-empty -m "$ROOT_SUBJECT"

cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
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

# expect_refusal CODE WANT_STATUS WANT_ERROR WHAT — the last api_status call
# answered WANT_STATUS with WANT_ERROR in the envelope.
expect_refusal() {
  local code="$1" want_status="$2" want_error="$3" what="$4" got
  [[ "$code" == "$want_status" ]] \
    || fail "$what answered $code, want $want_status: $(cat "$TMP/body.json")"
  got="$(jq -r .error.code < "$TMP/body.json")"
  [[ "$got" == "$want_error" ]] || fail "$what was refused as $got, want $want_error"
}

# shell_body COMMAND prints the POST body for a command, quoted by jq.
shell_body() { jq -cn --arg c "$1" '{command: $c}'; }

# start_shell COMMAND — POST it to $CHAT_ID, leaving the run's id in RUN_ID.
start_shell() {
  local code state
  code="$(api_status POST "/chats/$CHAT_ID/shell" -d "$(shell_body "$1")")"
  [[ "$code" == "202" ]] || fail "POST shell [$1] answered $code: $(cat "$TMP/body.json")"
  RUN_ID="$(jq -r .id < "$TMP/body.json")"
  state="$(jq -r .state < "$TMP/body.json")"
  [[ "$state" == "running" ]] || fail "a new run is $state, want running"
}

# wait_run RUN — poll until the run has ended, leaving its row in
# $TMP/run.json.
wait_run() {
  local run="$1" i=0 ticks state
  ticks="$(gate_ticks 150)"
  while (( i < ticks )); do
    api GET "/chats/$CHAT_ID/shell/$run" > "$TMP/run.json"
    state="$(jq -r .state < "$TMP/run.json")"
    if [[ "$state" != "running" ]]; then return 0; fi
    i=$(( i + 1 ))
    sleep "$GATE_POLL"
  done
  fail "shell run $run of chat $CHAT_ID never ended"
}

run_field() { jq -r ".$1" < "$TMP/run.json"; }

# output_of RUN prints the run's stored output, CRs dropped.
output_of() { api GET "/chats/$CHAT_ID/shell/$1/output" | tr -d '\r'; }

# wait_chat STATE — poll until $CHAT_ID reaches a state.
wait_chat() {
  local want="$1" i=0 ticks state
  ticks="$(gate_ticks 300)"
  while (( i < ticks )); do
    state="$(api GET "/chats/$CHAT_ID" | jq -r .chat.state)"
    if [[ "$state" == "$want" ]]; then return 0; fi
    i=$(( i + 1 ))
    sleep "$GATE_POLL"
  done
  fail "chat $CHAT_ID never reached $want (it is $state)"
}

# wait_turn SEQ — poll until that turn of $CHAT_ID is no longer running,
# echoing its state.
wait_turn() {
  local seq="$1" i=0 ticks state
  ticks="$(gate_ticks 300)"
  while (( i < ticks )); do
    state="$(api GET "/chats/$CHAT_ID" | jq -r --argjson s "$seq" \
      '.turns[] | select(.seq == $s) | .state')"
    if [[ -n "$state" && "$state" != "running" ]]; then
      printf '%s\n' "$state"
      return 0
    fi
    i=$(( i + 1 ))
    sleep "$GATE_POLL"
  done
  fail "turn $seq of chat $CHAT_ID never finished"
}

# send MESSAGE SEQ — send a message to $CHAT_ID and wait for that turn to
# end done and the chat to be idle again.
send() {
  local state
  api POST "/chats/$CHAT_ID/send" -d "$(jq -cn --arg m "$1" '{message: $m}')" >/dev/null \
    || fail "the send failed"
  state="$(wait_turn "$2")"
  [[ "$state" == "done" ]] || fail "turn $2 of chat $CHAT_ID is $state, want done"
  wait_chat idle
}

# new_chat TITLE — a fresh chat, its id in CHAT_ID and worktree in WT.
new_chat() {
  local chat
  chat="$(api POST /chats \
    -d "$(jq -cn --argjson p "$PROJECT_ID" --arg t "$1" '{project_id: $p, title: $t, agent: "claude"}')")" \
    || fail "POST /v1/chats failed"
  CHAT_ID="$(printf '%s' "$chat" | jq -r .id)"
  [[ "$CHAT_ID" != "null" && -n "$CHAT_ID" ]] || fail "creating the chat failed: $chat"
  WT="$(printf '%s' "$chat" | jq -r .worktree_path)"
}

# prompt_line N prints the Nth (1-based) prompt the fake agent recorded,
# its text blocks joined, CRs dropped.
prompt_line() {
  jq -rs --argjson n "$1" '.[$n - 1] | if type == "array" then join("\n") else . end' \
    < "$PROMPTS" | tr -d '\r'
}

prompt_count() { jq -s 'length' < "$PROMPTS" | tr -d '\r'; }

a_command_runs() {
  echo "== 1. a command runs in the chat's worktree and reports exit 0 and its output"
  new_chat "run a command"
  local out want code
  start_shell "git rev-parse --abbrev-ref HEAD"
  wait_run "$RUN_ID"
  [[ "$(run_field state)" == "exited" && "$(run_field exit_code)" == "0" ]] \
    || fail "the run ended $(cat "$TMP/run.json")"
  [[ "$(run_field after_turn)" == "0" && "$(run_field attach)" == "false" ]] \
    || fail "a run before any turn reads $(cat "$TMP/run.json")"
  out="$(output_of "$RUN_ID")"
  want="$(git -C "$WT" rev-parse --abbrev-ref HEAD | tr -d '\r')"
  [[ "$out" == "$want" ]] || fail "the output is [$out], want the worktree's branch [$want]"
  [[ "$want" == vincent/* ]] || fail "the chat's worktree is on [$want], not its own branch"

  code=0
  out="$("$VINCENT" chat shell "$CHAT_ID" "git log -1 --format=%s")" || code=$?
  [[ "$code" == "0" ]] || fail "vincent chat shell exited $code"
  out="$(printf '%s' "$out" | tr -d '\r')"
  [[ "$out" == "$ROOT_SUBJECT" ]] || fail "vincent chat shell printed [$out], want [$ROOT_SUBJECT]"
}

a_failure_reports_its_code() {
  echo "== 2. a failing command reports its exit code"
  new_chat "fail a command"
  local code
  start_shell "exit 7"
  wait_run "$RUN_ID"
  [[ "$(run_field state)" == "exited" && "$(run_field exit_code)" == "7" ]] \
    || fail "exit 7 ended $(cat "$TMP/run.json")"
  code=0
  "$VINCENT" chat shell "$CHAT_ID" "exit 7" >/dev/null || code=$?
  [[ "$code" == "7" ]] || fail "vincent chat shell of exit 7 exited $code"
}

one_at_a_time() {
  echo "== 3. a second command while one runs is refused"
  new_chat "one at a time"
  local long code err state
  start_shell "sleep 60"
  long="$RUN_ID"
  code="$(api_status POST "/chats/$CHAT_ID/shell" -d "$(shell_body "git status")")"
  expect_refusal "$code" 409 invalid_state "a second command"
  code=0
  err="$("$VINCENT" chat shell "$CHAT_ID" "git status" 2>&1 >/dev/null)" || code=$?
  [[ "$code" == "1" ]] || fail "vincent chat shell beside a running command exited $code"
  grep -q "already has a shell command running" <<<"$err" \
    || fail "vincent chat shell's refusal reads [$err]"
  # The refusals were answered while the first command still ran.
  state="$(api GET "/chats/$CHAT_ID/shell/$long" | jq -r .state)"
  [[ "$state" == "running" ]] || fail "the long command was $state when the refusals were read"
  api POST "/chats/$CHAT_ID/cancel" >/dev/null || fail "the cancel failed"
  wait_run "$long"
  [[ "$(run_field state)" == "canceled" ]] || fail "the canceled command ended $(cat "$TMP/run.json")"
}

an_attached_run_reaches_one_turn() {
  echo "== 4. an attached run reaches the next turn, and only that one"
  new_chat "attach a run"
  local code before first second
  start_shell "git log -1 --format=%s"
  wait_run "$RUN_ID"
  for attach in true false true; do
    code="$(api_status POST "/chats/$CHAT_ID/shell/$RUN_ID/attach" -d "{\"attach\": $attach}")"
    [[ "$code" == "200" ]] || fail "attach $attach answered $code: $(cat "$TMP/body.json")"
    [[ "$(jq -r .attach < "$TMP/body.json")" == "$attach" ]] \
      || fail "attach $attach reads $(cat "$TMP/body.json")"
  done

  before=0
  if [[ -f "$PROMPTS" ]]; then before="$(prompt_count)"; fi
  send "what did that print?" 1
  first="$(prompt_line $(( before + 1 )))"
  grep -q 'shell-run command="git log -1 --format=%s"' <<<"$first" \
    || fail "turn 1 was not sent the command:"$'\n'"$first"
  grep -q "$ROOT_SUBJECT" <<<"$first" || fail "turn 1 was not sent the output:"$'\n'"$first"
  grep -q "what did that print?" <<<"$first" || fail "turn 1 was not sent the message:"$'\n'"$first"
  api GET "/chats/$CHAT_ID/shell/$RUN_ID" > "$TMP/run.json"
  [[ "$(run_field attached_turn)" == "1" && "$(run_field attach)" == "false" ]] \
    || fail "the taken run reads $(cat "$TMP/run.json")"
  # The turn row keeps the message as typed.
  [[ "$(api GET "/chats/$CHAT_ID" | jq -r '.turns[0].prompt' | tr -d '\r')" == "what did that print?" ]] \
    || fail "turn 1's prompt is not the message as typed"

  send "and now?" 2
  second="$(prompt_line $(( before + 2 )))"
  grep -q "and now?" <<<"$second" || fail "turn 2's recorded prompt is:"$'\n'"$second"
  if grep -q "shell-run" <<<"$second"; then fail "turn 2 was sent the run again:"$'\n'"$second"; fi

  code="$(api_status POST "/chats/$CHAT_ID/shell/$RUN_ID/attach" -d '{"attach": true}')"
  expect_refusal "$code" 409 invalid_state "attaching a taken run"
}

a_terminal_chat_refuses() {
  echo "== 5. a terminal chat runs nothing"
  new_chat "archive me"
  local code err n
  start_shell "git status"
  wait_run "$RUN_ID"
  api POST "/chats/$CHAT_ID/archive" >/dev/null || fail "the archive failed"
  wait_chat archived
  code="$(api_status POST "/chats/$CHAT_ID/shell" -d "$(shell_body "git status")")"
  expect_refusal "$code" 409 invalid_state "a command on an archived chat"
  code=0
  err="$("$VINCENT" chat shell "$CHAT_ID" "git status" 2>&1 >/dev/null)" || code=$?
  [[ "$code" == "1" ]] || fail "vincent chat shell on an archived chat exited $code"
  grep -q "cannot run a shell command" <<<"$err" || fail "vincent chat shell's refusal reads [$err]"
  n="$(api GET "/chats/$CHAT_ID/shell" | jq '.runs | length' | tr -d '\r')"
  [[ "$n" == "1" ]] || fail "the archived chat lists $n runs, want its one"
}

PROJECT="$(api POST /projects -d "$(jq -cn --arg p "$(hostpath "$REPO")" '{path: $p}')")" \
  || fail "registering the project failed"
PROJECT_ID="$(printf '%s' "$PROJECT" | jq -r .id)"
[[ "$PROJECT_ID" != "null" && -n "$PROJECT_ID" ]] || fail "registering the project failed: $PROJECT"

if [[ -z "$ONLY" || "$ONLY" == "1" ]]; then a_command_runs; fi
if [[ -z "$ONLY" || "$ONLY" == "2" ]]; then a_failure_reports_its_code; fi
if [[ -z "$ONLY" || "$ONLY" == "3" ]]; then one_at_a_time; fi
if [[ -z "$ONLY" || "$ONLY" == "4" ]]; then an_attached_run_reaches_one_turn; fi
if [[ -z "$ONLY" || "$ONLY" == "5" ]]; then a_terminal_chat_refuses; fi

echo "GATE PASS: 170 (a chat's shell passthrough)"
