#!/usr/bin/env bash
# Task 068 gate (§13.2, §13.4): prove over the wire that vincent's pull-request
# writes — merge, close, reopen, comment and re-run — send exactly what a
# human asked for, and that every refusal the write vocabulary names is
# decided where task 068.4 says it is: before GitHub is asked, or by GitHub
# after it was.
#
#   1. reads, and nothing written without a route: the linked row and its
#      check rollup answer, `vincent github pr checks` agrees, and after two
#      reconciler ticks the fake `gh` has been asked for no write at all
#   2. refused before GitHub is asked: `validation_failed`, `pull_not_linked`
#      on a task never linked and on one whose link was removed, and
#      `disabled` with no `gh` process run at all
#   3. each write through the API: one argv line of its own verb per call,
#      the comment's bytes arriving on `gh`'s stdin unchanged, a re-run of a
#      run that did not fail refused `bad_request`, and a double merge
#      refused by the preflight
#   4. each write through the CLI, the surface a script without a terminal
#      has: the comment through two stdin hops, a merge, and two refusals
#   5. the merge preflight: `branch_behind`, `checks_running`,
#      `not_mergeable` (blocked and dirty) and `head_changed`, each with no
#      `pr merge` sent
#   6. the pin fires at send: the preflight passes, `pr merge` is sent once,
#      pinned to the confirmed head, and GitHub's refusal is `head_changed`
#   7. no write scope: the reads still answer, and every write is attempted
#      once and refused `no_write_scope`
#
# Task-numbered rather than `mN` because this is not a §19 milestone; 017,
# 032, 052 and 069 set that precedent.
#
# No agent CLI is involved: every task here is created and left where it is,
# and linked **by hand** (`POST /v1/tasks/{id}/github/pull`), so no scenario
# waits on a reconciler tick to create a link, and a human link is one the
# reconciler never overwrites. The write routes carry no task-state guard, so
# what a task is doing is irrelevant to them. What is exercised is the GitHub
# half, against cmd/fakegh installed as `gh` on the daemon's PATH — there is
# no `gh_path` config key, and the daemon resolves `gh` from PATH when GHPath
# is empty.
#
# The gate observes the rules CLAUDE.md records: `run:` bodies in the sh∩pwsh
# intersection, no `grep -q` on a pipe (capture first, match a here-string),
# `| tr -d '\r'` on multi-line captures, and committed executable.
#
# Requirements: bash, go, git, curl, jq.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
BIN="$TMP/bin"

EXE=""
if [[ "${OS:-}" == "Windows_NT" ]]; then
  EXE=".exe"
fi
VINCENT="$BIN/vincent$EXE"

ONLY="${VINCENT_GATE_SCENARIO:-}"

fail() { echo "GATE FAIL: $*" >&2; exit 1; }

cleanup() {
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

echo "== build vincent and the fake gh"
mkdir -p "$BIN"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent)
# cmd/fakegh is built *as* `gh`, so the daemon's PATH lookup finds it exactly
# the way it would find the real CLI.
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/gh$EXE" ./cmd/fakegh)
PATH="$BIN:$PATH"
export PATH

# Credential selection prefers an authenticated `gh` and falls back to these.
# With them unset, a `gh` that failed to resolve is a `no_credential` failure
# of this gate — never a request to api.github.com.
unset GITHUB_TOKEN GH_TOKEN
export FAKEGH_SCENARIO=success

# #412's head in the fake's corpus, and a commit that is not it.
HEAD_SHA=d3adb33fd3adb33fd3adb33fd3adb33fd3adb33f
OTHER_SHA=c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff

CONFIG_DIR="" DATA_DIR="" REPO="" GH_ARGV="" GH_STDIN="" COMMENT_BYTES=""
scenario_dirs() { # scenario_dirs NAME
  CONFIG_DIR="$TMP/$1/config"
  DATA_DIR="$TMP/$1/data"
  REPO="$TMP/$1/repo"
  mkdir -p "$CONFIG_DIR" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
  # GH_ARGV is every call the fake was asked to make, so "nothing was sent"
  # is observed process behaviour rather than a reading of the daemon's
  # source. The state file is what lets a `pr view` after a write read back
  # the state the write left, and the stdin file is the comment body exactly
  # as `gh` received it.
  GH_ARGV="$TMP/$1/gh-argv.txt"
  : > "$GH_ARGV"
  export FAKEGH_ARGV_FILE
  FAKEGH_ARGV_FILE="$(hostpath "$GH_ARGV")"
  export FAKEGH_STATE_FILE
  FAKEGH_STATE_FILE="$(hostpath "$TMP/$1/gh-state.json")"
  GH_STDIN="$TMP/$1/gh-stdin.txt"
  export FAKEGH_STDIN_FILE
  FAKEGH_STDIN_FILE="$(hostpath "$GH_STDIN")"
  COMMENT_BYTES="$TMP/$1/comment.txt"
}

PORT="" TOKEN="" BASE=""
daemon_up() {
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
}
daemon_down() { "$VINCENT" daemon stop --force >/dev/null 2>&1 || true; }

api() { # api METHOD PATH [JSON_BODY] — fails the gate on a non-2xx answer
  local method="$1" path="$2" body="${3:-}" out status
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  [[ "$status" == 2* ]] || fail "$method $path -> HTTP $status: $out"
  printf '%s' "$out"
}

# api_status METHOD PATH [JSON_BODY] sets STATUS and BODY and never fails, so
# a refusal can be asserted. Called directly, never inside $(...), or the two
# globals would be set in a subshell and lost.
STATUS="" BODY=""
api_status() {
  local method="$1" path="$2" body="${3:-}" out
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  STATUS="${out##*$'\n'}"
  BODY="${out%$'\n'*}"
}

# expect JSON WHAT FILTER [JQ_ARGS...] fails the gate unless FILTER is true of
# JSON, and prints the JSON when it is not.
expect() {
  local json="$1" what="$2" filter="$3"
  shift 3
  jq -e "$@" "$filter" <<<"$json" >/dev/null || fail "$what: $json"
}

# expect_ok WHAT: the last api_status answered 200.
expect_ok() {
  [[ "$STATUS" == "200" ]] || fail "$1 answered $STATUS, want 200: $BODY"
}

# expect_invalid WHAT: the last api_status was refused 400 validation_failed.
expect_invalid() {
  [[ "$STATUS" == "400" ]] || fail "$1 answered $STATUS, want 400: $BODY"
  expect "$BODY" "$1 was not refused validation_failed" '.error.code == "validation_failed"'
}

# expect_refused REASON WHAT: the last api_status was refused 409 naming
# REASON in details.
expect_refused() {
  [[ "$STATUS" == "409" ]] || fail "$2 answered $STATUS, want 409 $1: $BODY"
  expect "$BODY" "$2 was not refused $1" '.error.details.reason == $r' --arg r "$1"
}

# The project needs a github.com origin: that is the whole of what makes it a
# GitHub project (§13.2). Nothing is ever pushed.
make_repo() {
  git init -q -b main "$REPO"
  git -C "$REPO" config user.name gate
  git -C "$REPO" config user.email gate@example.invalid
  git -C "$REPO" config commit.gpgsign false
  printf 'gate repo\n' > "$REPO/README.md"
  git -C "$REPO" add . && git -C "$REPO" commit -qm init
  git -C "$REPO" remote add origin https://github.com/octo/repo.git
}

# The gate's one workflow. A single command step, so no agent CLI is involved
# and a task settles in seconds. `exit 0` is the whole body, which is what
# makes it portable to the daemon's pwsh on Windows (§8.3).
write_workflow() {
  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/gate.yaml" <<'YAML'
name: gate
description: One step that does nothing.
steps:
  - id: noop
    type: command
    run: exit 0
YAML
}

PROJECT_ID=""
register_project() {
  PROJECT_ID="$(api POST /projects \
    "$(jq -cn --arg p "$(hostpath "$REPO")" '{path: $p}')" | jq -r .id)"
}

# Titles are chosen so no task's branch is one of the fake's corpus heads
# (vincent/1-add-a-thing, vincent/3-ship-the-thing,
# vincent/9-rework-the-board-header): the reconciler would otherwise auto-link
# a task the gate means to leave unlinked.
create_task() { # create_task TITLE -> id
  api POST /tasks "$(jq -cn --argjson p "$PROJECT_ID" --arg t "$1" \
    '{project_id: $p, workflow: "gate", title: $t, description: "gate task"}')" | jq -r .id
}

link() { # link TASK_ID NUMBER
  local row
  api POST "/tasks/$1/github/pull" "{\"number\": $2}" > /dev/null
  row="$(api GET "/tasks/$1/github/pull")"
  expect "$row" "task $1 did not take a human link to #$2" \
    '.linked == true and .number == $n and .source == "human"' --argjson n "$2"
}

wait_for_branch() { # wait_for_branch TASK_ID
  local branch=""
  for _ in $(seq 1 30); do
    branch="$(api GET "/tasks/$1" | jq -r .branch_name)"
    [[ -n "$branch" && "$branch" != "null" ]] && return 0
    sleep 1
  done
  fail "task $1 never got a branch"
}

# setup NAME [CONFIG_YAML]: a fresh installation with its own daemon and
# project, and TASK_ID a task linked by hand to #412.
TASK_ID=""
setup() {
  scenario_dirs "$1"
  make_repo
  write_workflow
  [[ -n "${2:-}" ]] && printf '%s' "$2" > "$CONFIG_DIR/config.yaml"
  daemon_up
  register_project
  TASK_ID="$(create_task "Act on the linked pull request")"
  link "$TASK_ID" 412
}

# The five write routes, each with a body that passes validation, so what
# refuses them is the gate or the link rather than the request.
merge_ok() { printf '{"method": "squash", "head_sha": "%s"}' "$HEAD_SHA"; }
COMMENT_OK='{"body": "A comment the gate never expects to be posted."}'
RERUN_OK='{"run_id": 5150}'

write_all() { # write_all TASK_ID REASON WHAT: all five routes refused 409 REASON
  local id="$1" reason="$2" what="$3"
  api_status POST "/tasks/$id/github/pull/merge" "$(merge_ok)"
  expect_refused "$reason" "merge $what"
  api_status POST "/tasks/$id/github/pull/close"
  expect_refused "$reason" "close $what"
  api_status POST "/tasks/$id/github/pull/reopen"
  expect_refused "$reason" "reopen $what"
  api_status POST "/tasks/$id/github/pull/comment" "$COMMENT_OK"
  expect_refused "$reason" "comment $what"
  api_status POST "/tasks/$id/github/pull/checks/rerun" "$RERUN_OK"
  expect_refused "$reason" "rerun $what"
}

# The comment the write scenarios post, spelled twice: once as the JSON the
# API is sent, and once as the bytes `gh` must receive on stdin. It is
# multi-line, indented, carries quotes, a `$`, a backslash and one CRLF, and
# ends in a newline — everything a hop that re-quotes, trims or translates
# line endings would change. Neither spelling goes through argv or jq, so the
# only hops it takes are the ones under test.
COMMENT_JSON='{"body": "Posted by the 068 gate.\n\n  Indented, with \"quotes\", a $dollar and a back\\slash.\r\nThe line above ends in CRLF; this one in LF.\n"}'
write_comment_bytes() {
  printf 'Posted by the 068 gate.\n\n  Indented, with "quotes", a $dollar and a back\\slash.\r\nThe line above ends in CRLF; this one in LF.\n' \
    > "$COMMENT_BYTES"
}

# expect_comment_arrived WHAT: `gh` read exactly COMMENT_BYTES on stdin. The
# bytes are compared, never a CR-stripped copy: the CRLF in the body is part
# of what has to survive. git is the one byte-exact comparison the gate's
# requirements already guarantee on every platform.
expect_comment_arrived() {
  [[ -f "$GH_STDIN" ]] || fail "$1: gh never received a comment body on stdin"
  local want got
  want="$(git hash-object --no-filters -- "$COMMENT_BYTES")"
  got="$(git hash-object --no-filters -- "$GH_STDIN")"
  [[ "$got" == "$want" ]] || fail "$1: the body gh read on stdin is not the body sent: $(od -c "$GH_STDIN")"
}

# gh_calls is every argv the fake recorded. The file is written by a Go
# program, with the platform's own line endings, so CRs are stripped.
gh_calls() { tr -d '\r' < "$GH_ARGV"; }

# calls_matching REGEX prints the recorded argv lines matching REGEX.
calls_matching() {
  local calls
  calls="$(gh_calls)"
  grep -E "$1" <<<"$calls" || true
}

# count_calls REGEX prints how many recorded argv lines match REGEX.
count_calls() {
  local calls
  calls="$(gh_calls)"
  grep -cE "$1" <<<"$calls" || true
}

# expect_writes MERGE CLOSE REOPEN COMMENT RERUN: the fake was asked for
# exactly that many invocations of each write verb.
WRITE_VERBS=("pr merge" "pr close" "pr reopen" "pr comment" "run rerun")
expect_writes() {
  local want=("$@") i got
  for i in "${!WRITE_VERBS[@]}"; do
    got="$(count_calls "^${WRITE_VERBS[$i]} ")"
    [[ "$got" == "${want[$i]}" ]] \
      || fail "gh was asked for $got '${WRITE_VERBS[$i]}' calls, want ${want[$i]}: $(gh_calls)"
  done
}

expect_pull_state() { # expect_pull_state TASK_ID STATE MERGED WHAT
  local row
  row="$(api GET "/tasks/$1/github/pull")"
  expect "$row" "$4: the pull request does not read $2 (merged $3)" \
    '.linked == true and .pull.state == $s and .pull.merged == $m' --arg s "$2" --argjson m "$3"
}

run_scenario() { # run_scenario N
  [[ -z "$ONLY" || "$ONLY" == "$1" ]]
}

# ---------------------------------------------------------------- scenario 1
if run_scenario 1; then
  echo "== scenario 1: reads, and nothing written without a route"
  # A short poll interval, so the reconciler ticks inside the gate's patience.
  setup s1 $'github:\n  enabled: true\n  poll_interval: 1s\n'
  # The reconciler lists pull requests only while some task could still gain
  # an auto link, and a human-linked task cannot. This one keeps it listing.
  bystander="$(create_task "Stand by while the reconciler ticks")"
  wait_for_branch "$bystander"

  row="$(api GET "/tasks/$TASK_ID/github/pull")"
  expect "$row" "the linked row is not an open #412 on the corpus head" \
    '.linked == true and .number == 412 and .source == "human" and .pull.state == "open"
     and .pull.merged == false and .pull.head_sha == $h' --arg h "$HEAD_SHA"

  checks="$(api GET "/tasks/$TASK_ID/github/pull/checks")"
  expect "$checks" "the rollup is not about #412's head" '.ref == $h and (.runs | length) == 4' --arg h "$HEAD_SHA"
  expect "$checks" "build is not a failed check of run 5150" \
    '.runs[] | select(.name == "build") | .state == "failure" and .run_id == 5150'
  expect "$checks" "test is not a running check of run 5150" \
    '.runs[] | select(.name == "test") | .state == "in_progress" and .run_id == 5150'
  expect "$checks" "license/cla or the legacy status carries a run id" \
    '[.runs[] | select(.name == "license/cla" or .name == "ci/legacy-builder") | select(has("run_id") | not)] | length == 2'
  expect "$checks" "the rollup is not failure, which beats running" '.state == "failure"'

  cli="$("$VINCENT" github pr checks --task "$TASK_ID" --json)" \
    || fail "vincent github pr checks exited non-zero: $cli"
  want="$(jq -cS '{ref, state, runs}' <<<"$checks" | tr -d '\r')"
  got="$(jq -cS '{ref, state, runs}' <<<"$cli" | tr -d '\r')"
  [[ "$got" == "$want" ]] || fail "vincent github pr checks disagrees with the route: $got, want $want"

  # Two ticks after the reads: new `pr list` lines are the reconciler's.
  listed="$(count_calls '^pr list ')"
  ticked=""
  for _ in $(seq 1 30); do
    ticked="$(count_calls '^pr list ')"
    (( ticked >= listed + 2 )) && break
    sleep 1
  done
  (( ticked >= listed + 2 )) \
    || fail "the reconciler did not tick twice in 30s ($listed pr list calls, then $ticked)"
  expect_writes 0 0 0 0 0

  daemon_down
fi

# ---------------------------------------------------------------- scenario 2
if run_scenario 2; then
  echo "== scenario 2: refused before GitHub is asked"
  setup s2

  api_status POST "/tasks/$TASK_ID/github/pull/merge" "{\"head_sha\": \"$HEAD_SHA\"}"
  expect_invalid "a merge naming no method"
  api_status POST "/tasks/$TASK_ID/github/pull/merge" "{\"method\": \"fast-forward\", \"head_sha\": \"$HEAD_SHA\"}"
  expect_invalid "a fast-forward merge"
  api_status POST "/tasks/$TASK_ID/github/pull/merge" '{"method": "squash"}'
  expect_invalid "a merge naming no head"
  api_status POST "/tasks/$TASK_ID/github/pull/comment" '{"body": "  \n\t "}'
  expect_invalid "a blank comment"
  api_status POST "/tasks/$TASK_ID/github/pull/checks/rerun" '{"run_id": 0}'
  expect_invalid "a re-run of run 0"
  expect_writes 0 0 0 0 0

  unlinked="$(create_task "Stay unlinked")"
  write_all "$unlinked" pull_not_linked "on a task never linked"
  expect_writes 0 0 0 0 0

  # Off in config.yaml: the gate answers before the link is looked at, and no
  # `gh` process runs at all — not even the credential probe.
  daemon_down
  printf 'github:\n  enabled: false\n' > "$CONFIG_DIR/config.yaml"
  : > "$GH_ARGV"
  daemon_up
  write_all "$TASK_ID" disabled "with the integration off"
  calls="$(gh_calls)"
  [[ -z "$calls" ]] || fail "gh ran with the integration off: $calls"

  daemon_down
  rm "$CONFIG_DIR/config.yaml"
  daemon_up
  # A removed link is suppressed, and suppressed does not count as linked.
  api DELETE "/tasks/$TASK_ID/github/pull" > /dev/null
  row="$(api GET "/tasks/$TASK_ID/github/pull")"
  expect "$row" "the unlink did not leave a suppressed, unlinked task" '.linked == false and .suppressed == true'
  write_all "$TASK_ID" pull_not_linked "after the link was removed"
  expect_writes 0 0 0 0 0

  daemon_down
fi

# ---------------------------------------------------------------- scenario 3
if run_scenario 3; then
  echo "== scenario 3: each write through the API"
  setup s3
  write_comment_bytes

  api_status POST "/tasks/$TASK_ID/github/pull/comment" "$COMMENT_JSON"
  expect_ok "the comment"
  expect "$BODY" "the comment answered no comment URL" \
    '.url == "https://github.com/octo/repo/pull/412#issuecomment-1"'
  expect_writes 0 0 0 1 0
  line="$(calls_matching '^pr comment ')"
  [[ "$line" == "pr comment 412 -R octo/repo --body-file -" ]] || fail "unexpected comment argv: $line"
  expect_comment_arrived "the API's comment"

  api_status POST "/tasks/$TASK_ID/github/pull/checks/rerun" "$RERUN_OK"
  expect_ok "the re-run of 5150"
  expect "$BODY" "the re-run did not answer its run" '.run_id == 5150'
  expect_writes 0 0 0 1 1
  line="$(calls_matching '^run rerun ')"
  [[ "$line" == "run rerun 5150 --failed -R octo/repo" ]] || fail "unexpected re-run argv: $line"
  # 9999 backs no failed Actions row on the head, so it is refused on the
  # rollup, before anything is sent.
  api_status POST "/tasks/$TASK_ID/github/pull/checks/rerun" '{"run_id": 9999}'
  expect_refused bad_request "a re-run of a run that is not a failed Actions run"
  expect_writes 0 0 0 1 1

  api_status POST "/tasks/$TASK_ID/github/pull/close"
  expect_ok "the close"
  expect "$BODY" "the close did not answer a closed pull request" '.state == "closed" and .merged == false'
  expect_writes 0 1 0 1 1
  expect_pull_state "$TASK_ID" closed false "after the close"

  api_status POST "/tasks/$TASK_ID/github/pull/reopen"
  expect_ok "the reopen"
  expect "$BODY" "the reopen did not answer an open pull request" '.state == "open" and .merged == false'
  expect_writes 0 1 1 1 1
  expect_pull_state "$TASK_ID" open false "after the reopen"

  api_status POST "/tasks/$TASK_ID/github/pull/merge" "$(merge_ok)"
  expect_ok "the squash merge"
  expect "$BODY" "the merge did not answer a merged pull request" '.merged == true'
  expect_writes 1 1 1 1 1
  expect_pull_state "$TASK_ID" closed true "after the merge"
  line="$(calls_matching '^pr merge ')"
  [[ " $line " == *" --squash "* ]] || fail "the merge was not sent --squash: $line"
  [[ " $line " == *" --match-head-commit $HEAD_SHA "* ]] || fail "the merge was not pinned to $HEAD_SHA: $line"
  for flag in --delete-branch --auto --admin; do
    [[ " $line " != *" $flag "* ]] || fail "the merge was sent $flag: $line"
  done

  api_status POST "/tasks/$TASK_ID/github/pull/merge" "$(merge_ok)"
  expect_refused not_mergeable "a second merge"
  expect_writes 1 1 1 1 1

  daemon_down
fi

# ---------------------------------------------------------------- scenario 4
if run_scenario 4; then
  echo "== scenario 4: each write through the CLI"
  setup s4
  write_comment_bytes

  # Two stdin hops: the CLI reads the body, and the daemon hands it to gh.
  out="$("$VINCENT" github pr comment --task "$TASK_ID" --body-file - --json < "$COMMENT_BYTES")" \
    || fail "vincent github pr comment exited non-zero: $out"
  expect "$out" "the CLI's comment printed no comment URL" \
    '.url == "https://github.com/octo/repo/pull/412#issuecomment-1"'
  expect_writes 0 0 0 1 0
  expect_comment_arrived "the CLI's comment"

  out="$("$VINCENT" github pr merge --task "$TASK_ID" --method rebase --head-sha "$HEAD_SHA" --json)" \
    || fail "vincent github pr merge exited non-zero: $out"
  expect "$out" "the CLI's merge did not print a merged pull request" '.number == 412 and .merged == true'
  expect_writes 1 0 0 1 0
  line="$(calls_matching '^pr merge ')"
  [[ " $line " == *" --rebase "* ]] || fail "the CLI's merge was not sent --rebase: $line"
  expect_pull_state "$TASK_ID" closed true "after the CLI's merge"

  code=0
  out="$("$VINCENT" github pr merge --task "$TASK_ID" --method rebase --head-sha "$HEAD_SHA" 2>&1)" || code=$?
  [[ "$code" == "1" ]] || fail "a second merge through the CLI exited $code, want 1: $out"
  expect_writes 1 0 0 1 0

  merged="$(create_task "Point at a merged pull request")"
  link "$merged" 377
  code=0
  out="$("$VINCENT" github pr merge --task "$merged" --method merge --head-sha "$HEAD_SHA" 2>&1)" || code=$?
  [[ "$code" == "1" ]] || fail "merging an already merged pull request exited $code, want 1: $out"
  expect_writes 1 0 0 1 0

  daemon_down
fi

# ---------------------------------------------------------------- scenario 5
if run_scenario 5; then
  echo "== scenario 5: merge refusals from the preflight send nothing"
  setup s5

  # fakegh reads its scenario from the environment of the daemon that spawns
  # it, so each merge state needs a daemon started with it.
  for refusal in "behind $HEAD_SHA branch_behind" \
    "blocked-running $HEAD_SHA checks_running" \
    "blocked $HEAD_SHA not_mergeable" \
    "dirty $HEAD_SHA not_mergeable" \
    "success $OTHER_SHA head_changed"; do
    read -r fake sha reason <<<"$refusal"
    daemon_down
    FAKEGH_SCENARIO="$fake"
    daemon_up

    api_status POST "/tasks/$TASK_ID/github/pull/merge" "{\"method\": \"merge\", \"head_sha\": \"$sha\"}"
    expect_refused "$reason" "a merge under $fake"
    expect_writes 0 0 0 0 0
    expect_pull_state "$TASK_ID" open false "after the refusal under $fake"
  done

  FAKEGH_SCENARIO=success
  daemon_down
fi

# ---------------------------------------------------------------- scenario 6
if run_scenario 6; then
  echo "== scenario 6: the pin fires at send"
  FAKEGH_SCENARIO=head-moved
  # setup starts the daemon, which is what hands the scenario to fakegh.
  setup s6

  api_status POST "/tasks/$TASK_ID/github/pull/merge" "$(merge_ok)"
  expect_refused head_changed "a merge whose head moved after the preflight"
  # Scenario 5's head_changed sent nothing. This one was sent, once, pinned —
  # and GitHub refused it.
  expect_writes 1 0 0 0 0
  line="$(calls_matching '^pr merge ')"
  [[ " $line " == *" --match-head-commit $HEAD_SHA "* ]] || fail "the merge was not pinned to $HEAD_SHA: $line"
  expect_pull_state "$TASK_ID" open false "after the pin fired"

  FAKEGH_SCENARIO=success
  daemon_down
fi

# ---------------------------------------------------------------- scenario 7
if run_scenario 7; then
  echo "== scenario 7: no write scope"
  FAKEGH_SCENARIO=read-only
  setup s7

  row="$(api GET "/tasks/$TASK_ID/github/pull")"
  expect "$row" "a read-only credential did not read the pull request" \
    '.linked == true and .pull.number == 412 and (has("reason") | not)'
  checks="$(api GET "/tasks/$TASK_ID/github/pull/checks")"
  expect "$checks" "a read-only credential did not read the checks" \
    '(.runs | length) == 4 and (has("reason") | not)'

  # Every write is attempted once — the merge's preflight read passes — and
  # GitHub's 403 is the refusal.
  api_status POST "/tasks/$TASK_ID/github/pull/merge" "$(merge_ok)"
  expect_refused no_write_scope "a merge without write scope"
  expect_writes 1 0 0 0 0
  api_status POST "/tasks/$TASK_ID/github/pull/close"
  expect_refused no_write_scope "a close without write scope"
  expect_writes 1 1 0 0 0
  api_status POST "/tasks/$TASK_ID/github/pull/reopen"
  expect_refused no_write_scope "a reopen without write scope"
  expect_writes 1 1 1 0 0
  api_status POST "/tasks/$TASK_ID/github/pull/comment" "$COMMENT_OK"
  expect_refused no_write_scope "a comment without write scope"
  expect_writes 1 1 1 1 0
  api_status POST "/tasks/$TASK_ID/github/pull/checks/rerun" "$RERUN_OK"
  expect_refused no_write_scope "a re-run without write scope"
  expect_writes 1 1 1 1 1

  expect_pull_state "$TASK_ID" open false "after the refused writes"

  FAKEGH_SCENARIO=success
  daemon_down
fi

echo "GATE PASS: 068 (pull request writes)"
