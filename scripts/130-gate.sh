#!/usr/bin/env bash
# Task 130.17 gate (§5.6, §13.2, §13.4): prove via curl alone that vincent's
# issues work end to end — local CRUD, a task from an issue, the GitHub
# import, state write-back through the durable outbox, the MCP guard on a
# forge write, and the discussion thread.
#
#    1. local CRUD: an idempotent create (replay, and a reused key refused),
#       the list filters, a stale-version PATCH refused with the current
#       issue, the three close reasons, a double close refused by the FSM,
#       reopen, delete — and the issue.* events on GET /v1/events, replayed
#       from a mid-run Last-Event-ID
#    2. a task from a local issue: the prefill, an explicit title winning
#       over it, the task's link, ?issue_id= and the issue's task rollup
#    3. the import: open issues only (no closed history, no pull request),
#       and an idle tick asking with If-None-Match and moving nothing
#    4. an issue closed on GitHub closes here, with no write back
#    5. an issue closed here is written to GitHub exactly once
#    6. a close made while GitHub is unreachable survives a forced stop and
#       drains after the restart (crash-first)
#    7. `github.enabled: false`: local issues work and no `gh` call is made
#    8. a read-only token: the write fails no_write_scope and the local
#       state is kept
#    9. a conflict: GitHub changed the state first, and GitHub's value wins
#   10. moved and gone: a transferred and a deleted issue are marked, never
#       deleted
#   11. the MCP guard: an agent may not close an imported issue, and may
#       close a local one
#   12. the thread (task 130.16): a comment on GitHub is mirrored onto the
#       imported issue with one issue.comment_added; a local comment on a
#       local issue carries the daemon's author; a local comment on the
#       imported issue is refused issue_mirrored; nothing is posted to GitHub
#
# There is no `github_issue: N` shorthand scenario: #676 made it conditional
# on task 130.11 (#670), which removed `github_issue` from task create
# instead of keeping it as a shorthand (task 130 decision 22.1).
#
# Task-numbered rather than `mN` because this is not a §19 milestone, like
# 123-gate.sh and 125-gate.sh. Each scenario runs its own daemon over its own
# config and data dirs, so VINCENT_GATE_SCENARIO=n runs one alone.
#
# No agent CLI is involved and no workflow step runs anything but `exit 0`.
# The GitHub half is cmd/fakegh installed as `gh` on the daemon's PATH, with
# its issue corpus in a file (FAKEGH_ISSUES_FILE) the gate edits between two
# polls, its scenario in a file (FAKEGH_SCENARIO_FILE) the gate flips without
# a restart, and every call it was asked to make in FAKEGH_ARGV_FILE — so
# "no write was made" is observed process behaviour, not a reading of the
# daemon's source.
#
# The gate observes the rules CLAUDE.md records: no pipe into `grep -q` or
# any other early-exiting consumer (capture first, match a here-string),
# `| tr -d '\r'` on multi-line `jq` captures, waits that are bounded polls on
# API state, and committed executable.
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
# The GitHub project's origin, and the repository the fake answers for.
REPO_SLUG=gate/issues
export FAKEGH_REPO="$REPO_SLUG"

CONFIG_DIR="" DATA_DIR="" CORPUS="" SCENARIO_FILE="" GH_ARGV=""
scenario_dirs() { # scenario_dirs NAME
  CONFIG_DIR="$TMP/$1/config"
  DATA_DIR="$TMP/$1/data"
  mkdir -p "$CONFIG_DIR" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
  # The fake's three files are the scenario's own, and reach the daemon
  # through its environment at start.
  CORPUS="$TMP/$1/corpus.json"
  SCENARIO_FILE="$TMP/$1/gh-scenario.txt"
  GH_ARGV="$TMP/$1/gh-argv.txt"
  : > "$SCENARIO_FILE"
  : > "$GH_ARGV"
  export FAKEGH_ISSUES_FILE FAKEGH_SCENARIO_FILE FAKEGH_ARGV_FILE
  FAKEGH_ISSUES_FILE="$(hostpath "$CORPUS")"
  FAKEGH_SCENARIO_FILE="$(hostpath "$SCENARIO_FILE")"
  FAKEGH_ARGV_FILE="$(hostpath "$GH_ARGV")"
}

write_config() { # write_config ENABLED POLL_INTERVAL
  printf 'github:\n  enabled: %s\n  poll_interval: %s\n' "$1" "$2" > "$CONFIG_DIR/config.yaml"
}

PORT="" TOKEN="" BASE="" MCP="" SESSION="" RPC_ID=0
daemon_up() {
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
  MCP="http://127.0.0.1:$PORT/mcp"
}
daemon_down() { "$VINCENT" daemon stop --force >/dev/null 2>&1 || true; }

# api_raw METHOD PATH [JSON_BODY [HEADER]] -> the status on the first line,
# the body after it, whatever the status.
api_raw() {
  local method="$1" path="$2" body="${3:-}" header="${4:-}" out
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  [[ -n "$header" ]] && args+=(-H "$header")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  printf '%s\n%s' "${out##*$'\n'}" "${out%$'\n'*}"
}

status_of() { printf '%s' "${1%%$'\n'*}"; }
body_of() { printf '%s' "${1#*$'\n'}"; }

api() { # api METHOD PATH [JSON_BODY [HEADER]] -> body; a non-2xx fails
  local out status
  out="$(api_raw "$@")"
  status="$(status_of "$out")"
  [[ "$status" == 2* ]] || fail "$1 $2 -> HTTP $status: $(body_of "$out")"
  body_of "$out"
}

# expect_conflict OUT REASON_FILTER WANT: a 409 at invalid_state whose
# details say WANT under the jq filter.
expect_conflict() {
  local out="$1" filter="$2" want="$3" status body
  status="$(status_of "$out")"
  body="$(body_of "$out")"
  [[ "$status" == "409" ]] || fail "want 409, got HTTP $status: $body"
  [[ "$(jq -r .error.code <<<"$body")" == "invalid_state" ]] \
    || fail "a 409 not at invalid_state: $body"
  [[ "$(jq -r "$filter" <<<"$body")" == "$want" ]] \
    || fail "409 says $(jq -r "$filter" <<<"$body") at $filter, want $want: $body"
}

make_repo() { # make_repo PATH [ORIGIN]
  git init -q -b main "$1"
  git -C "$1" config user.name gate
  git -C "$1" config user.email gate@example.invalid
  git -C "$1" config commit.gpgsign false
  printf 'gate repo\n' > "$1/README.md"
  git -C "$1" add . && git -C "$1" commit -qm init
  if [[ -n "${2:-}" ]]; then git -C "$1" remote add origin "$2"; fi
}

register_project() { api POST /projects \
  "$(jq -cn --arg p "$(hostpath "$1")" '{path: $p}')" | jq -r .id; }

local_project() { # local_project NAME -> id: a plain repository, no remote
  make_repo "$TMP/$1"
  register_project "$TMP/$1"
}

github_project() { # github_project NAME -> id: origin is github.com/gate/issues
  make_repo "$TMP/$1" "https://github.com/$REPO_SLUG.git"
  register_project "$TMP/$1"
}

create_issue() { # create_issue PROJECT_ID TITLE [BODY] -> id
  api POST /issues "$(jq -cn --argjson p "$1" --arg t "$2" --arg b "${3:-}" \
    '{project_id: $p, title: $t, body: $b}')" | jq -r .id
}

issue_field() { api GET "/issues/$1" | jq -r "$2"; }
sync_of() { issue_field "$1" '"\(.sync.state)/\(.sync.reason // "")"'; }
field_is() { [[ "$(issue_field "$1" "$2")" == "$3" ]]; } # field_is ID FILTER WANT

# wait_for TRIES WHAT COMMAND... polls COMMAND once a second.
wait_for() {
  local tries="$1" what="$2"
  shift 2
  for _ in $(seq 1 "$tries"); do
    if "$@"; then return 0; fi
    sleep 1
  done
  fail "timed out after ${tries}s waiting for $what"
}

# ---- the fake's corpus

issue_row() { # issue_row N STATE UPDATED_AT [pr]
  jq -cn --argjson n "$1" --arg s "$2" --arg u "$3" --arg r "$REPO_SLUG" --arg pr "${4:-}" '{
    id: (3000000 + $n), node_id: "I_gate_\($n)", number: $n,
    title: "Gate issue \($n)", body: "", state: $s,
    state_reason: (if $s == "closed" then "completed" else null end),
    closed_at: (if $s == "closed" then $u else null end),
    created_at: "2026-09-01T00:00:00Z", updated_at: $u,
    labels: [], assignees: [], user: {login: "octocat"}, milestone: null, comments: 0,
    url: "https://api.github.com/repos/\($r)/issues/\($n)",
    repository_url: "https://api.github.com/repos/\($r)",
    html_url: "https://github.com/\($r)/issues/\($n)"
  } + (if $pr == "pr" then {pull_request: {url: "https://api.github.com/repos/\($r)/pulls/\($n)"}} else {} end)'
}

# seed_corpus: open issues #1–#4, closed #5 (history the import skips) and
# an open pull request #6 (the issues listing carries pull requests, as
# GitHub's does, and the import must not).
seed_corpus() {
  {
    issue_row 1 open 2026-09-01T01:00:00Z
    issue_row 2 open 2026-09-01T02:00:00Z
    issue_row 3 open 2026-09-01T03:00:00Z
    issue_row 4 open 2026-09-01T04:00:00Z
    issue_row 5 closed 2026-09-01T05:00:00Z
    issue_row 6 open 2026-09-01T06:00:00Z pr
  } | jq -s . > "$CORPUS"
}

now_utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# corpus_edit N JQ_UPDATE: "someone else" editing GitHub. Written through a
# temp file and a rename, as the fake writes it; the rename retries because
# on Windows it fails while a fakegh child holds the file open for reading.
corpus_edit() {
  jq --argjson n "$1" --arg now "$(now_utc)" "map(if .number == \$n then $2 else . end)" \
    "$CORPUS" > "$CORPUS.edit"
  corpus_commit
}

corpus_commit() { # moves $CORPUS.edit over the corpus
  for _ in $(seq 1 10); do
    if mv -f "$CORPUS.edit" "$CORPUS" 2>/dev/null; then return 0; fi
    sleep 1
  done
  fail "the corpus could not be rewritten"
}

# corpus_add_comment N ID BODY: hubot comments on #N on GitHub, now. A row
# with an issue_url is a comment to the fake, which recomputes #N's count.
corpus_add_comment() {
  jq --argjson n "$1" --argjson id "$2" --arg b "$3" --arg now "$(now_utc)" --arg r "$REPO_SLUG" '. + [{
    id: $id, node_id: "IC_gate_\($id)", body: $b, user: {login: "hubot"},
    created_at: $now, updated_at: $now,
    url: "https://api.github.com/repos/\($r)/issues/comments/\($id)",
    html_url: "https://github.com/\($r)/issues/\($n)#issuecomment-\($id)",
    issue_url: "https://api.github.com/repos/\($r)/issues/\($n)"
  }]' "$CORPUS" > "$CORPUS.edit"
  corpus_commit
}

corpus_state() { # corpus_state N -> state/state_reason as GitHub has it
  jq -r --argjson n "$1" '.[] | select(.number == $n) | "\(.state)/\(.state_reason // "")"' "$CORPUS"
}
corpus_is() { [[ "$(corpus_state "$1")" == "$2" ]]; }

gh_calls() { tr -d '\r' < "$GH_ARGV"; }

patches_for() { # patches_for N -> how many PATCHes the fake was asked for on #N
  local calls
  calls="$(gh_calls)"
  grep -cE -- "-X PATCH .*repos/$REPO_SLUG/issues/$1\$" <<<"$calls" || true
}

# ---- imported issues

imported() { api GET "/issues?project_id=$1&source=github"; }
imported_count_is() { [[ "$(imported "$1" | jq length)" == "$2" ]]; }
issue_by_number() { # issue_by_number PROJECT_ID N -> the vincent issue id
  imported "$1" | jq -r --argjson n "$2" '.[] | select(.source.number == $n) | .id'
}

# import_github NAME -> project id, once its four open issues are imported.
import_github() {
  local pid
  pid="$(github_project "$1")"
  wait_for 30 "the import of #1–#4" imported_count_is "$pid" 4
  printf '%s' "$pid"
}

# ---- MCP, as m10-gate.sh speaks it

# mcp_payload OUT -> the first SSE `data:` line, read without an early-exiting
# consumer on a pipe.
mcp_payload() {
  local data
  data="$(tr -d '\r' <<<"$1" | sed -n 's/^data: //p')"
  printf '%s' "${data%%$'\n'*}"
}

mcp_post() { # mcp_post JSON [HEADERS_FILE]
  local headers=(-H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json"
    -H "Accept: application/json, text/event-stream")
  [[ -n "$SESSION" ]] && headers+=(-H "Mcp-Session-Id: $SESSION")
  [[ -n "${2:-}" ]] && headers+=(-D "$2")
  curl -sS -X POST "${headers[@]}" -d "$1" "$MCP" || fail "curl POST /mcp failed"
}

mcp_init() {
  local out hdrs
  SESSION=""
  out="$(mcp_post '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"130-gate","version":"1"}}}' \
    "$TMP/init-headers.txt")"
  [[ "$(mcp_payload "$out" | jq -r .result.serverInfo.name)" == "vincent" ]] \
    || fail "initialize did not identify the vincent server: $out"
  hdrs="$(tr -d '\r' < "$TMP/init-headers.txt" | sed -n 's/^[Mm]cp-[Ss]ession-[Ii]d: //p')"
  SESSION="${hdrs%%$'\n'*}"
  [[ -n "$SESSION" ]] || fail "initialize minted no Mcp-Session-Id"
  mcp_post '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' > /dev/null
}

mcp_call() { # mcp_call TOOL ARGS_JSON -> the tool result object
  local out payload
  RPC_ID=$((RPC_ID + 1))
  out="$(mcp_post "$(jq -cn --arg n "$1" --argjson a "$2" --argjson i "$RPC_ID" \
    '{jsonrpc: "2.0", id: $i, method: "tools/call", params: {name: $n, arguments: $a}}')")"
  payload="$(mcp_payload "$out")"
  [[ -n "$payload" ]] || fail "tools/call $1 returned no SSE data frame: $out"
  [[ "$(jq -r 'has("error")' <<<"$payload")" == "false" ]] \
    || fail "tools/call $1 -> JSON-RPC error: $payload"
  jq -c .result <<<"$payload"
}

run_scenario() { # run_scenario N
  [[ -z "$ONLY" || "$ONLY" == "$1" ]]
}

# The gate's one workflow: a single command step whose whole body is
# `exit 0`, portable to the daemon's pwsh on Windows (§8.3). Scenario 2 needs
# a workflow to create a task with; nothing about it is under test.
GATE_WORKFLOW='name: gate
steps:
  - id: noop
    type: command
    run: exit 0
'

# ---------------------------------------------------------------- scenario 1
if run_scenario 1; then
  echo "== scenario 1: local CRUD, and the issue.* events"
  scenario_dirs s1
  daemon_up
  pid="$(local_project s1/repo)"

  # An idempotent create: the replay is the same issue, not a second row.
  body="$(jq -cn --argjson p "$pid" '{project_id: $p, title: "First", labels: ["bug"]}')"
  out="$(api_raw POST /issues "$body" "Idempotency-Key: gate-130-one")"
  [[ "$(status_of "$out")" == "201" ]] || fail "create -> HTTP $(status_of "$out"): $(body_of "$out")"
  one="$(body_of "$out" | jq -r .id)"
  v1="$(body_of "$out" | jq -r .version)"
  again="$(api POST /issues "$body" "Idempotency-Key: gate-130-one" | jq -r .id)"
  [[ "$again" == "$one" ]] || fail "a replayed create made issue $again, want $one"
  [[ "$(api GET "/issues?project_id=$pid" | jq length)" == "1" ]] \
    || fail "a replayed create wrote a second row"
  out="$(api_raw POST /issues "$(jq -cn --argjson p "$pid" '{project_id: $p, title: "Other"}')" \
    "Idempotency-Key: gate-130-one")"
  expect_conflict "$out" .error.details.reason idempotency_key_reused

  two="$(api POST /issues "$(jq -cn --argjson p "$pid" '{project_id: $p, title: "Second", labels: ["docs"]}')" | jq -r .id)"
  three="$(create_issue "$pid" "Third")"

  # A PATCH at the version read, then one at the stale version: refused,
  # carrying the issue as it is now.
  v2="$(api PATCH "/issues/$one" "$(jq -cn --argjson v "$v1" '{version: $v, title: "First, renamed"}')" | jq -r .version)"
  [[ "$v2" -gt "$v1" ]] || fail "a PATCH left the version at $v2"
  out="$(api_raw PATCH "/issues/$one" "$(jq -cn --argjson v "$v1" '{version: $v, title: "Lost"}')")"
  expect_conflict "$out" .error.details.reason issue_changed
  expect_conflict "$out" .error.details.issue.version "$v2"

  # The three close reasons. duplicate_of names an issue of the same project.
  api POST "/issues/$three/close" "$(jq -cn --argjson d "$one" '{reason: "duplicate", duplicate_of: $d}')" > /dev/null
  api POST "/issues/$one/close" '{"reason": "completed"}' > /dev/null
  api POST "/issues/$two/close" '{"reason": "not_planned"}' > /dev/null
  got="$(api GET "/issues?project_id=$pid" | jq -r '[.[] | "\(.id):\(.close_reason):\(.duplicate_of // "")"] | sort | join(" ")')"
  want="$(jq -nr --arg a "$one:completed:" --arg b "$two:not_planned:" --arg c "$three:duplicate:$one" \
    '[$a, $b, $c] | sort | join(" ")')"
  [[ "$got" == "$want" ]] || fail "close reasons are [$got], want [$want]"
  out="$(api_raw POST "/issues/$one/close" '{"reason": "completed"}')"
  expect_conflict "$out" .error.details.state closed

  # The filters, each to exactly the expected ids.
  ids() { api GET "/issues?$1" | jq -r '[.[].id] | sort | join(" ")'; }
  want="$(jq -nr --argjson a "$one" --argjson b "$two" --argjson c "$three" '[$a, $b, $c] | sort | join(" ")')"
  [[ "$(ids "project_id=$pid&state=closed")" == "$want" ]] \
    || fail "state=closed returned $(ids "project_id=$pid&state=closed")"
  [[ "$(ids "project_id=$pid&label=bug")" == "$one" ]] \
    || fail "label=bug returned $(ids "project_id=$pid&label=bug"), want $one"
  [[ "$(ids "project_id=$pid&label=docs")" == "$two" ]] \
    || fail "label=docs returned $(ids "project_id=$pid&label=docs"), want $two"

  api POST "/issues/$two/reopen" > /dev/null
  field_is "$two" '"\(.state)/\(.close_reason // "")"' "open/" \
    || fail "reopen left issue $two at $(issue_field "$two" '"\(.state)/\(.close_reason)"')"
  [[ "$(ids "project_id=$pid&state=open")" == "$two" ]] \
    || fail "state=open returned $(ids "project_id=$pid&state=open"), want $two"

  api DELETE "/issues/$two" > /dev/null
  out="$(api_raw GET "/issues/$two")"
  [[ "$(status_of "$out")" == "404" ]] || fail "a deleted issue answers HTTP $(status_of "$out")"

  # The events. The stream never ends by itself and replays nothing unasked
  # (`Last-Event-ID: 0` is no cursor), so read from 1 for --max-time and
  # keep the issue.* frames: the id and event lines of each. The capture
  # drops the last frame's closing blank line, hence awk's END.
  ISSUE_TYPES="issue.created,issue.updated,issue.state_changed,issue.labels_changed,issue.deleted"
  sse_issue_events() { # sse_issue_events LAST_EVENT_ID -> "id type" lines
    local raw
    raw="$(curl -sS --max-time 3 -N -H "Authorization: Bearer $TOKEN" \
      -H "Last-Event-ID: $1" "$BASE/events?types=$ISSUE_TYPES" 2>/dev/null || true)"
    tr -d '\r' <<<"$raw" | awk '/^id: /{id=$2} /^event: /{ev=$2} /^$/{if (id != "") print id " " ev; id=""; ev=""}
      END{if (id != "") print id " " ev}'
  }
  all="$(sse_issue_events 1)"
  for t in issue.created issue.updated issue.state_changed issue.deleted; do
    grep -q " $t\$" <<<"$all" || fail "no $t event reached GET /v1/events: $all"
  done
  count="$(grep -c . <<<"$all")"
  mid="$(sed -n "$((count / 2))p" <<<"$all" | cut -d' ' -f1)"
  want="$(awk -v m="$mid" '$1 > m' <<<"$all")"
  got="$(sse_issue_events "$mid")"
  [[ -n "$want" && "$got" == "$want" ]] \
    || fail "resuming from event $mid replayed [$got], want [$want]"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 2
if run_scenario 2; then
  echo "== scenario 2: a task from a local issue"
  scenario_dirs s2
  mkdir -p "$CONFIG_DIR/workflows"
  printf '%s' "$GATE_WORKFLOW" > "$CONFIG_DIR/workflows/gate.yaml"
  daemon_up
  pid="$(local_project s2/repo)"
  iid="$(create_issue "$pid" "Rename the flag" "The flag is misnamed.")"

  task="$(api POST /tasks "$(jq -cn --argjson p "$pid" --argjson i "$iid" \
    '{project_id: $p, workflow: "gate", issue_id: $i}')")"
  tid="$(jq -r .id <<<"$task")"
  [[ "$(jq -r .title <<<"$task")" == "Rename the flag" ]] \
    || fail "the task's title was not prefilled from the issue: $task"
  [[ "$(jq -r .description <<<"$task")" == *"The flag is misnamed."* ]] \
    || fail "the task's description was not prefilled from the issue: $task"
  [[ "$(jq -r .issue.id <<<"$task")" == "$iid" ]] \
    || fail "the task does not link issue $iid: $task"

  # An explicit title wins over the prefill.
  other="$(create_issue "$pid" "Another issue" "Body.")"
  title="$(api POST /tasks "$(jq -cn --argjson p "$pid" --argjson i "$other" \
    '{project_id: $p, workflow: "gate", issue_id: $i, title: "Explicit title"}')" | jq -r .title)"
  [[ "$title" == "Explicit title" ]] || fail "an explicit title lost to the prefill: $title"

  got="$(api GET "/tasks?issue_id=$iid" | jq -r '[.[].id] | join(" ")')"
  [[ "$got" == "$tid" ]] || fail "?issue_id=$iid returned [$got], want [$tid]"
  field_is "$iid" .tasks.count 1 || fail "the issue's rollup says $(issue_field "$iid" .tasks)"
  field_is "$iid" .task_count 1 || fail "the issue's task_count is $(issue_field "$iid" .task_count)"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 3
if run_scenario 3; then
  echo "== scenario 3: the import, and the idle tick"
  scenario_dirs s3
  seed_corpus
  write_config true 1s
  daemon_up
  pid="$(import_github s3/repo)"

  rows="$(imported "$pid")"
  numbers="$(jq -r '[.[].source.number] | sort | join(" ")' <<<"$rows")"
  [[ "$numbers" == "1 2 3 4" ]] || fail "imported #$numbers, want #1–#4 (no closed #5, no pull request #6)"
  bad="$(jq -r '[.[] | select(.state != "open" or .source.provider != "github" or .sync.state != "synced")] | length' <<<"$rows")"
  [[ "$bad" == "0" ]] || fail "$bad imported issues are not open, github and synced: $rows"

  # The idle tick: once a listing carries If-None-Match, the repository is
  # being asked the same question, and the next one moving no row is the 304.
  inm_after() { # inm_after LINE: a listing with If-None-Match after line LINE
    local calls
    calls="$(gh_calls | sed -n "$(($1 + 1)),\$p")"
    grep -q 'If-None-Match: .*issues?' <<<"$calls"
  }
  wait_for 30 "a listing with If-None-Match" inm_after 0
  before="$(imported "$pid" | jq -c '[.[] | {id, updated_at, version}] | sort_by(.id)')"
  lines="$(gh_calls | grep -c .)"
  wait_for 30 "a second listing with If-None-Match" inm_after "$lines"
  after="$(imported "$pid" | jq -c '[.[] | {id, updated_at, version}] | sort_by(.id)')"
  [[ "$after" == "$before" ]] || fail "an idle tick moved issues: $before -> $after"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 4
if run_scenario 4; then
  echo "== scenario 4: closed on GitHub"
  scenario_dirs s4
  seed_corpus
  write_config true 1s
  daemon_up
  pid="$(import_github s4/repo)"
  one="$(issue_by_number "$pid" 1)"

  : > "$GH_ARGV"
  corpus_edit 1 '.state = "closed" | .state_reason = "not_planned" | .closed_at = $now | .updated_at = $now'
  wait_for 30 "#1 to close here" field_is "$one" '"\(.state)/\(.close_reason)"' closed/not_planned
  [[ "$(patches_for 1)" == "0" ]] || fail "a close read from GitHub was written back: $(gh_calls)"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 5
if run_scenario 5; then
  echo "== scenario 5: closed through the API, written to GitHub once"
  scenario_dirs s5
  seed_corpus
  write_config true 1s
  daemon_up
  pid="$(import_github s5/repo)"
  two="$(issue_by_number "$pid" 2)"

  : > "$GH_ARGV"
  api POST "/issues/$two/close" '{"reason": "completed"}' > /dev/null
  wait_for 30 "#2's write-back" corpus_is 2 closed/completed
  wait_for 30 "#2 to say synced" field_is "$two" .sync.state synced
  # A few more ticks: the write is not repeated.
  sleep 3
  [[ "$(patches_for 2)" == "1" ]] || fail "#2 was PATCHed $(patches_for 2) times, want 1: $(gh_calls)"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 6
if run_scenario 6; then
  echo "== scenario 6: an offline close drains across a forced stop"
  scenario_dirs s6
  seed_corpus
  write_config true 1s
  daemon_up
  pid="$(import_github s6/repo)"
  three="$(issue_by_number "$pid" 3)"

  echo unreachable > "$SCENARIO_FILE"
  api POST "/issues/$three/close" '{"reason": "completed"}' > /dev/null
  wait_for 30 "#3 to say pending" field_is "$three" .sync.state pending
  corpus_is 3 open/ || fail "#3 changed on GitHub while it was unreachable: $(corpus_state 3)"

  # A forced stop is the crash: the write is a row, not a goroutine.
  daemon_down
  : > "$SCENARIO_FILE"
  daemon_up
  # The retry waits out the outbox's backoff (30 s after the failed try).
  wait_for 120 "#3 to say synced after the restart" field_is "$three" .sync.state synced
  corpus_is 3 closed/completed || fail "#3 on GitHub is $(corpus_state 3), want closed/completed"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 7
if run_scenario 7; then
  echo "== scenario 7: github.enabled false makes no gh call"
  scenario_dirs s7
  seed_corpus
  write_config false 1s
  daemon_up
  pid="$(local_project s7/repo)"
  github_project s7/gh-repo > /dev/null

  iid="$(create_issue "$pid" "Offline")"
  v="$(issue_field "$iid" .version)"
  api PATCH "/issues/$iid" "$(jq -cn --argjson v "$v" '{version: $v, labels: ["local"]}')" > /dev/null
  api POST "/issues/$iid/close" > /dev/null
  api POST "/issues/$iid/reopen" > /dev/null
  field_is "$iid" .state open || fail "issue $iid is $(issue_field "$iid" .state) after reopen"
  # Several poll intervals' worth: the point is that no call ever comes.
  sleep 4
  [[ ! -s "$GH_ARGV" ]] || fail "a disabled integration called gh: $(gh_calls)"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 8
if run_scenario 8; then
  echo "== scenario 8: a read-only token keeps the local state"
  scenario_dirs s8
  seed_corpus
  write_config true 1s
  daemon_up
  pid="$(import_github s8/repo)"
  two="$(issue_by_number "$pid" 2)"

  api POST "/issues/$two/close" '{"reason": "completed"}' > /dev/null
  wait_for 30 "#2's close to sync" field_is "$two" .sync.state synced

  echo read-only > "$SCENARIO_FILE"
  api POST "/issues/$two/reopen" > /dev/null
  wait_for 30 "#2's reopen to fail" field_is "$two" '"\(.sync.state)/\(.sync.reason)"' failed/no_write_scope
  field_is "$two" .state open || fail "a refused write undid the local reopen: $(issue_field "$two" .state)"
  corpus_is 2 closed/completed || fail "#2 on GitHub is $(corpus_state 2), want closed/completed"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 9
if run_scenario 9; then
  echo "== scenario 9: a conflict, which GitHub wins"
  scenario_dirs s9
  seed_corpus
  write_config true 1s
  daemon_up
  pid="$(import_github s9/repo)"
  four="$(issue_by_number "$pid" 4)"

  : > "$GH_ARGV"
  echo unreachable > "$SCENARIO_FILE"
  api POST "/issues/$four/close" '{"reason": "completed"}' > /dev/null
  wait_for 30 "#4 to say pending" field_is "$four" .sync.state pending
  # Someone else closes it on GitHub, for a different reason, meanwhile.
  corpus_edit 4 '.state = "closed" | .state_reason = "not_planned" | .closed_at = $now | .updated_at = $now'
  : > "$SCENARIO_FILE"
  wait_for 120 "#4 to say conflict" field_is "$four" .sync.state conflict
  field_is "$four" '"\(.state)/\(.close_reason)"' closed/not_planned \
    || fail "#4 did not adopt GitHub's value: $(issue_field "$four" '"\(.state)/\(.close_reason)"')"
  [[ "$(patches_for 4)" == "0" ]] || fail "a conflicted write was sent anyway: $(gh_calls)"
  corpus_is 4 closed/not_planned || fail "#4 on GitHub is $(corpus_state 4), want closed/not_planned"

  daemon_down
fi

# --------------------------------------------------------------- scenario 10
if run_scenario 10; then
  echo "== scenario 10: moved and gone are marked, never deleted"
  # The sweep that finds a transferred or deleted issue runs once a day, and
  # first on the tick the initial import completes — which, for a corpus the
  # gate seeds whole, is the tick that imported the issues to be marked. So
  # this repository is larger than one import pass (500 open issues a tick):
  # the first pass imports the 500 oldest, #7 and #8 among them, and stops
  # short; the gate marks the two; a second pass is the one that completes
  # the import and sweeps. A poll interval of an hour keeps the timer out of
  # it: both passes are "sync now" requests.
  scenario_dirs s10
  jq -n --arg r "$REPO_SLUG" '[range(1; 521) as $n | {
    id: (3000000 + $n), node_id: "I_gate_\($n)", number: $n,
    title: "Gate issue \($n)", body: "", state: "open", state_reason: null, closed_at: null,
    created_at: "2026-09-01T00:00:00Z",
    updated_at: (1788220800 + $n * 60 | todateiso8601),
    labels: [], assignees: [], user: {login: "octocat"}, milestone: null, comments: 0,
    url: "https://api.github.com/repos/\($r)/issues/\($n)",
    repository_url: "https://api.github.com/repos/\($r)",
    html_url: "https://github.com/\($r)/issues/\($n)"}]' > "$CORPUS"
  write_config true 1h
  daemon_up
  pid="$(github_project s10/repo)"
  # The daemon's first tick ran before the project existed.
  api POST "/projects/$pid/issues/sync" > /dev/null
  wait_for 60 "the first import pass" imported_count_is "$pid" 500
  seven="$(issue_by_number "$pid" 7)"
  eight="$(issue_by_number "$pid" 8)"
  [[ -n "$seven" && -n "$eight" ]] || fail "#7 and #8 were not in the first import pass"

  corpus_edit 7 '._fake = {transferred_to: "gate/other#12"}'
  corpus_edit 8 '._fake = {deleted: true}'
  api POST "/projects/$pid/issues/sync" > /dev/null
  wait_for 60 "#7 to be marked moved" field_is "$seven" .source.status moved
  wait_for 30 "#8 to be marked missing" field_is "$eight" .source.status missing
  [[ "$(sync_of "$seven")" == "failed/moved" ]] || fail "#7's sync is $(sync_of "$seven"), want failed/moved"
  [[ "$(sync_of "$eight")" == "failed/gone" ]] || fail "#8's sync is $(sync_of "$eight"), want failed/gone"
  imported_count_is "$pid" 520 || fail "the import lists $(imported "$pid" | jq length) issues, want all 520"
  listed="$(imported "$pid" | jq -r --argjson a "$seven" --argjson b "$eight" \
    '[.[] | select(.id == $a or .id == $b)] | length')"
  [[ "$listed" == "2" ]] || fail "a moved or gone issue left GET /v1/issues"

  daemon_down
fi

# --------------------------------------------------------------- scenario 11
if run_scenario 11; then
  echo "== scenario 11: the MCP guard on a forge write"
  scenario_dirs s11
  seed_corpus
  write_config true 1s
  daemon_up
  pid="$(import_github s11/repo)"
  lpid="$(local_project s11/local)"
  one="$(issue_by_number "$pid" 1)"
  mine="$(create_issue "$lpid" "A local issue")"

  : > "$GH_ARGV"
  mcp_init
  res="$(mcp_call issue_close "$(jq -cn --argjson i "$one" '{id: $i, body: {reason: "completed"}}')")"
  [[ "$(jq -r .isError <<<"$res")" == "true" ]] || fail "an agent closed an imported issue: $res"
  text="$(jq -r '.content[0].text' <<<"$res")"
  [[ "$(jq -r .error.code <<<"$text")" == "invalid_state" ]] || fail "the refusal is not invalid_state: $text"
  [[ "$(jq -r .error.details.reason <<<"$text")" == "forge_write_needs_human" ]] \
    || fail "the refusal is not forge_write_needs_human: $text"
  field_is "$one" .state open || fail "the refused close closed #1 anyway"
  # A tick or two: no write follows a refusal.
  sleep 3
  [[ "$(patches_for 1)" == "0" ]] || fail "a refused agent close reached GitHub: $(gh_calls)"

  res="$(mcp_call issue_close "$(jq -cn --argjson i "$mine" '{id: $i, body: {reason: "completed"}}')")"
  [[ "$(jq -r .isError <<<"$res")" != "true" ]] || fail "an agent may not close a local issue: $res"
  field_is "$mine" .state closed || fail "the agent's close of local issue $mine did not take"

  daemon_down
fi

# --------------------------------------------------------------- scenario 12
if run_scenario 12; then
  echo "== scenario 12: the discussion thread"
  scenario_dirs s12
  seed_corpus
  write_config true 1s
  daemon_up
  pid="$(import_github s12/repo)"
  one="$(issue_by_number "$pid" 1)"
  lpid="$(local_project s12/local)"
  mine="$(create_issue "$lpid" "A local issue")"

  thread() { api GET "/issues/$1/comments"; }
  thread_length_is() { [[ "$(thread "$1" | jq '.comments | length')" == "$2" ]]; }

  # A comment made on GitHub reaches the imported issue on a tick.
  corpus_add_comment 1 4000001 "Seen on GitHub"
  wait_for 30 "the comment on #1 to be mirrored" thread_length_is "$one" 1
  got="$(thread "$one" | jq -c '.comments[0] | [.author, .body, .remote, .remote_key]')"
  [[ "$got" == '["hubot","Seen on GitHub",true,"4000001"]' ]] \
    || fail "the mirrored comment is $got"
  cid="$(thread "$one" | jq -r '.comments[0].id')"

  # Exactly one issue.comment_added, by sync, naming the comment and
  # carrying no text. As in scenario 1, the stream is read from 1 for
  # --max-time.
  raw="$(curl -sS --max-time 3 -N -H "Authorization: Bearer $TOKEN" \
    -H "Last-Event-ID: 1" "$BASE/events?types=issue.comment_added" 2>/dev/null || true)"
  frames="$(tr -d '\r' <<<"$raw" | sed -n 's/^data: //p' | jq -sc .)"
  [[ "$(jq length <<<"$frames")" == "1" ]] \
    || fail "want one issue.comment_added on GET /v1/events, got $frames"
  got="$(jq -c '.[0].payload | [.id, .comment_id, .by, has("body")]' <<<"$frames")"
  [[ "$got" == "[$one,$cid,\"sync\",false]" ]] \
    || fail "issue.comment_added says $got, want [$one,$cid,\"sync\",false]"

  # A local comment on a local issue: its author is the daemon's, by the
  # rule an issue create follows — and an issue created over HTTP shows it.
  author="$(issue_field "$mine" .author)"
  [[ -n "$author" ]] || fail "local issue $mine has no author"
  out="$(api_raw POST "/issues/$mine/comments" '{"body": "A local note"}')"
  [[ "$(status_of "$out")" == "201" ]] || fail "a local comment -> HTTP $(status_of "$out"): $(body_of "$out")"
  got="$(thread "$mine" | jq -c '[.comments[] | [.author, .body, .remote]]')"
  want="$(jq -cn --arg a "$author" '[[$a, "A local note", false]]')"
  [[ "$got" == "$want" ]] || fail "local issue $mine's thread is $got, want $want"

  # A local comment on the imported issue is refused before any I/O.
  out="$(api_raw POST "/issues/$one/comments" '{"body": "Not here"}')"
  expect_conflict "$out" .error.details.reason issue_mirrored
  thread_length_is "$one" 1 || fail "a refused comment reached #1's thread: $(thread "$one")"

  # Nothing was ever posted to GitHub.
  calls="$(gh_calls)"
  posts="$(grep -c -- "-X POST" <<<"$calls" || true)"
  [[ "$posts" == "0" ]] || fail "a comment was posted to GitHub: $calls"

  daemon_down
fi

echo "GATE PASS"
