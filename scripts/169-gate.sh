#!/usr/bin/env bash
# Task 169 gate (issue #1018; spec §5.5, §13.2): a chat's diff and its
# per-hunk review, driven over the wire and through the real binary against
# the committed fakeagent, so CI never calls a real agent CLI.
#
#   1. a chat's turn edits one tracked file in two places and adds a new,
#      untracked file — on disk, in the chat's own worktree, nothing staged
#   2. GET /v1/chats/{id}/diff lists both in `scope=base` and `scope=turn`:
#      the edited file with two hunks, the untracked one as `added`; the stat
#      route, `vincent chat diff` and `vincent chat diff --stat` agree on the
#      file count and the +/-; a scope that is neither is 400
#   3. `vincent chat diff reject` reverts exactly one hunk in the working
#      tree — the other edit stays, nothing is staged, HEAD does not move —
#      and the chat's draft now starts with the note block naming the path;
#      rejecting the new file's only hunk removes the file; the remaining
#      hunk is accepted and reads back `accepted: true`, the mark comes off
#      again and taking it off twice is still 204; a second reject of a hash
#      already rejected is 409 chat_hunk_stale
#   4. while a turn runs, a reject and an accept are 409 chat_turn_live and
#      the file on disk is untouched, though the diff still reads; once the
#      turn has finished the same reject goes through
#
# Every scenario starts a chat of its own, so VINCENT_GATE_SCENARIO=N runs
# any one of them without the others.
#
# The gate is workflow-free: a chat has none, so there is no `run:` body here
# to spell in the sh∩pwsh intersection. Its own bash obeys the standing
# rules: `| tr -d '\r'` on any multi-line capture, file content compared with
# its CRs dropped (on Windows git may check the worktree out CRLF), and never
# `| grep -q`.
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
  "" | [1-4]) ;;
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

# The fake CLI's own conversation store: scenario 4's second turn resumes the
# session its first one wrote, across a daemon restart.
export FAKEAGENT_SESSION_DIR
FAKEAGENT_SESSION_DIR="$(hostpath "$TMP/sessions")"
mkdir -p "$TMP/sessions"

# notes.txt is thirty lines so that an edit to line 3 and one to line 25 are
# two hunks: git joins changes closer than its three lines of context on
# each side, and these are twenty-one unchanged lines apart.
EDIT_LINES="3 25"
EDITED_LINE="fakeagent was here"
NEW_CONTENT="a brand new file"

# notes_with LINE... prints notes.txt as it reads with those lines edited.
notes_with() {
  local i n line
  for i in $(seq 1 30); do
    line="line $i"
    for n in "$@"; do
      if [[ "$n" == "$i" ]]; then line="$EDITED_LINE"; fi
    done
    printf '%s\n' "$line"
  done
}

REPO="$TMP/repo"
mkdir -p "$REPO"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email gate@example.com
git -C "$REPO" config user.name "169 Gate"
notes_with > "$REPO/notes.txt"
git -C "$REPO" add notes.txt
git -C "$REPO" commit -q -m "root"

cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
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

# wait_chat CHAT_ID STATE — poll until the chat reaches a state.
wait_chat() {
  local id="$1" want="$2" i=0 ticks state
  ticks="$(gate_ticks 300)"
  while (( i < ticks )); do
    state="$(api GET "/chats/$id" | jq -r .chat.state)"
    if [[ "$state" == "$want" ]]; then return 0; fi
    i=$(( i + 1 ))
    sleep "$GATE_POLL"
  done
  fail "chat $id never reached $want (it is $state)"
}

# wait_turn CHAT_ID SEQ — poll until that turn is no longer running, echoing
# its state.
wait_turn() {
  local id="$1" seq="$2" i=0 ticks state
  ticks="$(gate_ticks 300)"
  while (( i < ticks )); do
    state="$(api GET "/chats/$id" | jq -r --argjson s "$seq" \
      '.turns[] | select(.seq == $s) | .state')"
    if [[ -n "$state" && "$state" != "running" ]]; then
      printf '%s\n' "$state"
      return 0
    fi
    i=$(( i + 1 ))
    sleep "$GATE_POLL"
  done
  fail "turn $seq of chat $id never finished"
}

# new_chat TITLE — a chat whose first turn has made the edits, leaving its id
# in CHAT_ID and its worktree in WT.
new_chat() {
  local chat state
  chat="$(api POST /chats \
    -d "{\"project_id\": $PROJECT_ID, \"title\": \"$1\", \"agent\": \"claude\"}")" \
    || fail "POST /v1/chats failed"
  CHAT_ID="$(printf '%s' "$chat" | jq -r .id)"
  [[ "$CHAT_ID" != "null" && -n "$CHAT_ID" ]] || fail "creating the chat failed: $chat"
  api POST "/chats/$CHAT_ID/send" -d '{"message": "edit notes.txt twice and add a file"}' >/dev/null \
    || fail "the send failed"
  state="$(wait_turn "$CHAT_ID" 1)"
  [[ "$state" == "done" ]] || fail "turn 1 of chat $CHAT_ID is $state, want done"
  wait_chat "$CHAT_ID" idle
  WT="$(api GET "/chats/$CHAT_ID" | jq -r .chat.worktree_path)"
  [[ -d "$WT" ]] || fail "chat $CHAT_ID has no worktree at $WT"
}

# notes_now prints the worktree's notes.txt with its CRs dropped: on Windows
# git may have checked it out CRLF, and what is asserted is the lines.
notes_now() { tr -d '\r' < "$WT/notes.txt"; }

# expect_notes WHAT LINE... — notes.txt reads as the original with exactly
# those lines edited.
expect_notes() {
  local what="$1" got want
  shift
  got="$(notes_now)"
  want="$(notes_with "$@")"
  [[ "$got" == "$want" ]] || fail "$what: notes.txt is:"$'\n'"$got"
}

# expect_unstaged WHAT HEAD STATUS — nothing is staged, HEAD is where it was,
# and `git status` reads exactly STATUS.
expect_unstaged() {
  local what="$1" head="$2" want="$3" got
  git -C "$WT" diff --cached --quiet || fail "$what staged something: $(git -C "$WT" diff --cached --stat)"
  got="$(git -C "$WT" rev-parse HEAD)"
  [[ "$got" == "$head" ]] || fail "$what moved HEAD from $head to $got"
  got="$(git -C "$WT" status --porcelain | tr -d '\r')"
  [[ "$got" == "$want" ]] || fail "$what left git status as [$got], want [$want]"
}

# diff_of SCOPE prints the chat's diff in that scope.
diff_of() {
  local code
  code="$(api_status GET "/chats/$CHAT_ID/diff?scope=$1")"
  [[ "$code" == "200" ]] || fail "GET diff?scope=$1 answered $code: $(cat "$TMP/body.json")"
  cat "$TMP/body.json"
}

# hunk_field DIFF PATH INDEX FIELD — one field of one hunk of one file.
hunk_field() {
  printf '%s' "$1" | jq -r --arg p "$2" --argjson i "$3" --arg f "$4" \
    '.files[] | select(.path == $p) | .hunks[$i][$f]' | tr -d '\r'
}

HEADING="The user reverted these hunks; do not reapply them:"

# draft_now prints the chat's daemon-held draft.
draft_now() { api GET "/chats/$CHAT_ID/draft" | jq -r .draft | tr -d '\r'; }

the_turn_edits() {
  echo "== 1. a turn edits one tracked file in two places and adds a new one"
  new_chat "two edits and a file"
  # shellcheck disable=SC2086 # EDIT_LINES is a list of line numbers
  expect_notes "the turn's edit" $EDIT_LINES
  [[ -f "$WT/added.txt" ]] || fail "the turn did not add added.txt"
  local added
  added="$(tr -d '\r' < "$WT/added.txt")"
  [[ "$added" == "$NEW_CONTENT" ]] || fail "added.txt holds [$added], want [$NEW_CONTENT]"
  # The file is new to git as well as to the directory: untracked, not added.
  expect_unstaged "the turn" "$(git -C "$REPO" rev-parse main)" $' M notes.txt\n?? added.txt'
}

the_diff_reads() {
  echo "== 2. both scopes list the edited file's two hunks and the untracked file"
  new_chat "read the diff"
  local scope diff code stat cli want body
  for scope in base turn; do
    diff="$(diff_of "$scope")"
    # shellcheck disable=SC2016 # $s is jq's
    printf '%s' "$diff" | jq -e --arg s "$scope" '
      .scope == $s and (.from | test("^[0-9a-f]{40,64}$"))
      and .added == 3 and .removed == 2
      and ([.files[].path] | sort) == ["added.txt", "notes.txt"]
      and (.files[] | select(.path == "notes.txt")
           | .status == "modified" and .old_path == "" and .binary == false
             and .rejectable == true and .added == 2 and .removed == 2
             and (.hunks | length) == 2
             and all(.hunks[]; .added == 1 and .removed == 1 and .accepted == false
                               and (.hash | test("^[0-9a-f]{64}$"))
                               and (.header | startswith("@@ -"))))
      and (.files[] | select(.path == "added.txt")
           | .status == "added" and .binary == false and .added == 1 and .removed == 0
             and (.hunks | length) == 1 and (.hunks[0].hash | test("^[0-9a-f]{64}$")))
    ' >/dev/null || fail "the $scope diff is wrong: $diff"
    # The body is the hunk's own lines: line 3's goes out and the edit comes in.
    body="$(hunk_field "$diff" notes.txt 0 body)"
    grep -Fx -- '-line 3' <<<"$body" >/dev/null || fail "the $scope diff's first hunk does not remove line 3: $body"
    grep -Fx -- "+$EDITED_LINE" <<<"$body" >/dev/null || fail "the $scope diff's first hunk does not add the edit: $body"
    body="$(hunk_field "$diff" notes.txt 1 body)"
    grep -Fx -- '-line 25' <<<"$body" >/dev/null || fail "the $scope diff's second hunk does not remove line 25: $body"
  done
  # Scope omitted is base; anything else is a 400, whatever the chat is.
  diff="$(api GET "/chats/$CHAT_ID/diff")"
  [[ "$(printf '%s' "$diff" | jq -r .scope)" == "base" ]] || fail "a diff with no scope is not base: $diff"
  code="$(api_status GET "/chats/$CHAT_ID/diff?scope=everything")"
  expect_refusal "$code" 400 validation_failed "an unknown scope"

  echo "== 2b. the stat route and the CLI agree"
  stat="$(api GET "/chats/$CHAT_ID/diff/stat")"
  printf '%s' "$stat" | jq -e '.files == 2 and .added == 3 and .removed == 2' >/dev/null \
    || fail "the stat route answered $stat, want 2 files +3 -2"
  want=$'2 files\t+3\t-2'
  cli="$("$VINCENT" chat diff "$CHAT_ID" --stat | tr -d '\r')" || fail "vincent chat diff --stat failed"
  [[ "$cli" == "$want" ]] || fail "vincent chat diff --stat printed [$cli], want [$want]"
  for scope in base turn; do
    cli="$("$VINCENT" chat diff "$CHAT_ID" --scope "$scope" | tr -d '\r')" \
      || fail "vincent chat diff --scope $scope failed"
    grep -Fx -- "$want" <<<"$cli" >/dev/null || fail "vincent chat diff --scope $scope has no totals line [$want]: $cli"
    grep -Fx -- $'modified\tnotes.txt\t+2\t-2' <<<"$cli" >/dev/null || fail "the $scope listing lacks notes.txt: $cli"
    grep -Fx -- $'added\tadded.txt\t+1\t-0' <<<"$cli" >/dev/null || fail "the $scope listing lacks added.txt: $cli"
    # Each hunk is listed by the hash prefix `reject` and `accept` take back.
    diff="$(diff_of "$scope")"
    local hashes hash
    hashes="$(printf '%s' "$diff" | jq -r '.files[].hunks[].hash' | tr -d '\r')"
    [[ "$(wc -l <<<"$hashes" | tr -d ' ')" == "3" ]] || fail "the $scope diff does not carry three hunks: $hashes"
    while IFS= read -r hash; do
      grep -F -- "${hash:0:12}" <<<"$cli" >/dev/null || fail "the $scope listing lacks hunk ${hash:0:12}: $cli"
    done <<<"$hashes"
  done
  cli="$("$VINCENT" chat diff "$CHAT_ID" --json | jq -c '[(.files | length), .added, .removed]' | tr -d '\r')" \
    || fail "vincent chat diff --json failed"
  [[ "$cli" == "[2,3,2]" ]] || fail "vincent chat diff --json totals are $cli, want [2,3,2]"
}

the_hunks_are_reviewed() {
  echo "== 3. reject reverts one hunk in the working tree and notes it in the draft"
  new_chat "review the hunks"
  local diff head h1 h2 hn header1 headern draft want code out left scope
  head="$(git -C "$WT" rev-parse HEAD)"
  diff="$(diff_of base)"
  h1="$(hunk_field "$diff" notes.txt 0 hash)"
  header1="$(hunk_field "$diff" notes.txt 0 header)"
  hn="$(hunk_field "$diff" added.txt 0 hash)"
  headern="$(hunk_field "$diff" added.txt 0 header)"
  [[ "$(draft_now)" == "" ]] || fail "a new chat's draft is not empty: $(draft_now)"

  # By the prefix the listing prints, resolved client-side: the route a user has.
  out="$("$VINCENT" chat diff reject "$CHAT_ID" notes.txt "${h1:0:12}" | tr -d '\r')" \
    || fail "vincent chat diff reject failed"
  [[ "$out" == "rejected notes.txt ${h1:0:12}" ]] || fail "reject printed [$out]"
  # Exactly that hunk: line 3 is back, line 25 still carries the edit.
  expect_notes "rejecting the first hunk" 25
  expect_unstaged "the reject" "$head" $' M notes.txt\n?? added.txt'
  draft="$(draft_now)"
  want="$HEADING"$'\n'"- notes.txt $header1"
  [[ "$draft" == "$want" ]] || fail "the draft after the reject is [$draft], want [$want]"

  echo "== 3b. rejecting an untracked file's hunk removes the file"
  # In the turn scope this time: the same hunk, found from the checkpoint.
  "$VINCENT" chat diff reject "$CHAT_ID" added.txt "$hn" --scope turn >/dev/null \
    || fail "rejecting added.txt's hunk failed"
  [[ ! -e "$WT/added.txt" ]] || fail "added.txt is still there after its hunk was rejected"
  expect_notes "rejecting the new file" 25
  expect_unstaged "the second reject" "$head" ' M notes.txt'
  draft="$(draft_now)"
  want+=$'\n'"- added.txt $headern"
  [[ "$draft" == "$want" ]] || fail "the draft after two rejects is [$draft], want [$want]"

  echo "== 3c. the remaining hunk is accepted, and a rejected hash is stale"
  diff="$(diff_of base)"
  left="$(printf '%s' "$diff" | jq -c '[.files[] | [.path, (.hunks | length)]]' | tr -d '\r')"
  [[ "$left" == '[["notes.txt",1]]' ]] || fail "the diff after two rejects holds $left, want one hunk of notes.txt"
  h2="$(hunk_field "$diff" notes.txt 0 hash)"
  [[ "$h2" != "$h1" ]] || fail "the remaining hunk carries the rejected hunk's hash"
  "$VINCENT" chat diff accept "$CHAT_ID" notes.txt "${h2:0:12}" >/dev/null \
    || fail "vincent chat diff accept failed"
  for scope in base turn; do
    [[ "$(hunk_field "$(diff_of "$scope")" notes.txt 0 accepted)" == "true" ]] \
      || fail "the accepted hunk does not read accepted in the $scope scope"
  done
  # Only a mark: nothing in git or on disk changed.
  expect_notes "accepting a hunk" 25
  expect_unstaged "the accept" "$head" ' M notes.txt'

  code="$(api_status POST "/chats/$CHAT_ID/diff/reject" \
    -d "{\"scope\": \"base\", \"path\": \"notes.txt\", \"hunk\": \"$h1\"}")"
  expect_refusal "$code" 409 chat_hunk_stale "a second reject of a rejected hunk"
  expect_notes "the stale reject" 25
  [[ "$(draft_now)" == "$want" ]] || fail "the stale reject changed the draft: $(draft_now)"

  "$VINCENT" chat diff accept "$CHAT_ID" notes.txt "$h2" --undo >/dev/null \
    || fail "vincent chat diff accept --undo failed"
  [[ "$(hunk_field "$(diff_of base)" notes.txt 0 accepted)" == "false" ]] \
    || fail "the hunk still reads accepted after --undo"
  code="$(api_status POST "/chats/$CHAT_ID/diff/unaccept" \
    -d "{\"path\": \"notes.txt\", \"hunk\": \"$h2\"}")"
  [[ "$code" == "204" ]] || fail "a second unaccept answered $code, want 204: $(cat "$TMP/body.json")"
}

a_live_turn_refuses() {
  echo "== 4. a reject or an accept while a turn runs is 409 chat_turn_live"
  new_chat "a live turn"
  local diff h1 code state before
  diff="$(diff_of base)"
  h1="$(hunk_field "$diff" notes.txt 0 hash)"

  # The second turn is only held open: with the file knobs gone it writes
  # nothing, so "the file is unchanged" is about the refused reject and not
  # about what the agent did meanwhile. Six seconds is the window the two
  # refused calls and the read have to land in.
  unset FAKEAGENT_EDIT_FILE FAKEAGENT_EDIT_LINES FAKEAGENT_WRITE_FILE FAKEAGENT_WRITE_CONTENT
  export FAKEAGENT_DELAY_MS=6000
  start_daemon

  api POST "/chats/$CHAT_ID/send" -d '{"message": "take your time"}' >/dev/null \
    || fail "the second send failed"
  wait_chat "$CHAT_ID" running
  before="$(cksum < "$WT/notes.txt")"
  code="$(api_status POST "/chats/$CHAT_ID/diff/reject" \
    -d "{\"scope\": \"base\", \"path\": \"notes.txt\", \"hunk\": \"$h1\"}")"
  expect_refusal "$code" 409 chat_turn_live "a reject during a live turn"
  code="$(api_status POST "/chats/$CHAT_ID/diff/accept" \
    -d "{\"scope\": \"base\", \"path\": \"notes.txt\", \"hunk\": \"$h1\"}")"
  expect_refusal "$code" 409 chat_turn_live "an accept during a live turn"
  [[ "$(cksum < "$WT/notes.txt")" == "$before" ]] || fail "a refused reject changed notes.txt"
  # shellcheck disable=SC2086 # EDIT_LINES is a list of line numbers
  expect_notes "a refused reject" $EDIT_LINES
  [[ "$(draft_now)" == "" ]] || fail "a refused reject wrote the draft: $(draft_now)"
  # Reading is information: the diff still answers, the hunk unmarked.
  [[ "$(hunk_field "$(diff_of base)" notes.txt 0 accepted)" == "false" ]] \
    || fail "the refused accept left a mark"
  # The refusals were answered while the turn was live, not after it.
  state="$(api GET "/chats/$CHAT_ID" | jq -r .chat.state)"
  [[ "$state" == "running" ]] || fail "the chat was $state, not running, when the refusals were read"

  state="$(wait_turn "$CHAT_ID" 2)"
  [[ "$state" == "done" ]] || fail "turn 2 is $state, want done"
  wait_chat "$CHAT_ID" idle
  unset FAKEAGENT_DELAY_MS

  # The same call, now that nothing is running: it was the turn that refused.
  code="$(api_status POST "/chats/$CHAT_ID/diff/reject" \
    -d "{\"scope\": \"base\", \"path\": \"notes.txt\", \"hunk\": \"$h1\"}")"
  [[ "$code" == "200" ]] || fail "the reject after the turn answered $code: $(cat "$TMP/body.json")"
  jq -e --arg h "$HEADING" '.note_added == true and .warning == "" and (.draft | startswith($h + "\n- notes.txt @@"))' \
    < "$TMP/body.json" >/dev/null || fail "the reject's answer is wrong: $(cat "$TMP/body.json")"
  expect_notes "the reject after the turn" 25
}

echo "== start the daemon"
# FAKEAGENT_EDIT_LINES turns FAKEAGENT_EDIT_FILE's append into edits in
# place, which is the only way one fake turn leaves two hunks in one file;
# FAKEAGENT_WRITE_FILE creates the file git has never seen.
export FAKEAGENT_EDIT_FILE=notes.txt
export FAKEAGENT_EDIT_LINES="${EDIT_LINES// /,}"
export FAKEAGENT_WRITE_FILE=added.txt
export FAKEAGENT_WRITE_CONTENT="$NEW_CONTENT"$'\n'
start_daemon

PROJECT="$(api POST /projects -d "{\"path\": \"$(hostpath "$REPO")\"}")" \
  || fail "registering the project failed"
PROJECT_ID="$(printf '%s' "$PROJECT" | jq -r .id)"
[[ "$PROJECT_ID" != "null" && -n "$PROJECT_ID" ]] || fail "registering the project failed: $PROJECT"

# Scenario 4 goes last: it restarts the daemon without the file knobs.
if [[ -z "$ONLY" || "$ONLY" == "1" ]]; then the_turn_edits; fi
if [[ -z "$ONLY" || "$ONLY" == "2" ]]; then the_diff_reads; fi
if [[ -z "$ONLY" || "$ONLY" == "3" ]]; then the_hunks_are_reviewed; fi
if [[ -z "$ONLY" || "$ONLY" == "4" ]]; then a_live_turn_refuses; fi

echo "GATE PASS: 169 (a chat's diff and its per-hunk review)"
