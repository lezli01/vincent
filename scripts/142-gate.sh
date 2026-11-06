#!/usr/bin/env bash
# Task 142.18 gate (§5.6, §12.3, §13.2, §13.4): prove via curl alone that
# vincent's Jira issue sync works end to end against a Jira site served over
# HTTP — the import, the poll, write-back through the durable outbox, the
# guards, and the thread.
#
#    1. the import: VIN-1 and VIN-2, open, `source: jira` with their keys;
#       the Done VIN-3 is not imported
#    2. an idle sync over an unchanged corpus records no issue.updated
#    3. closed in Jira: a Done transition POSTed to the site closes the issue
#       here, by sync, the reason mapped from the resolution (Won't Do is
#       not_planned, Done is completed), and nothing is written back
#    4. a human close is written to Jira once: one transition POST, the
#       resolution set, and a second sync writes nothing more
#    5. crash-safe outbox: a close made while the site is unreachable
#       survives `daemon stop --force` and is delivered once after restart
#    6. `jira.enabled: false` set over the config API: a sync makes no request
#    7. a read-only token: the write fails no_write_scope, the local state kept
#    8. a conflict: Jira's state changed first while a local close was
#       pending, and Jira's value wins
#    9. no transition to a done-category status: the close fails
#       no_transition, the local state kept
#   10. moved and deleted: `fakejira move` marks the issue moved under its
#       new key, `fakejira delete` marks it missing — and it stays here
#   11. the MCP guard: an agent's ungranted close is refused, with no outbox
#       row and no request
#   12. the comment mirror: a public comment is mirrored, a restricted one
#       (POSTed with `visibility`) never is
#   13. a granted handoff writes back one ADF comment and one transition
#   14. the daemon restarted without JIRA_API_TOKEN reports no_credential and
#       sends nothing
#   15. a Jira-bound project whose origin is GitHub: pull-request linking
#       still works through `gh`, and `gh` is never asked for its issues
#
# Scenario 12 asserts only that a comment restricted *before* it is read is
# never mirrored. A comment mirrored while public and restricted later is
# task 142's open question 12, which this gate deliberately does not settle.
#
# Task-numbered rather than `mN` because this is not a §19 milestone, like
# 130-gate.sh. Each scenario runs its own daemon and its own fake Jira over
# its own files, so VINCENT_GATE_SCENARIO=n runs one alone.
#
# The Jira half is cmd/fakejira serving a corpus file on a loopback port —
# the first gate with a background HTTP server. It binds :0 and writes the
# port it got to a file, which the gate polls for, so no port is ever chosen
# in bash. Its scenario is a file the gate flips without a restart, and its
# request log is one `METHOD path status` line per request, so "nothing was
# sent" is observed process behaviour. The corpus is never edited with jq
# while the fake serves it: a move or a delete goes through `fakejira move`
# and `fakejira delete`, which take the corpus's OS lock as the server does,
# and a transition or a comment goes over the fake's own REST routes, as a
# Jira user's would. The fake is stopped with POST /_fake/shutdown, and
# waited out, before the directory its binary lives in is removed: Windows
# has no graceful signal for it, and removes no running executable.
#
# No agent CLI is involved and every workflow `run:` body is `exit 0` or a
# `vincent ...` call, portable to the daemon's pwsh on Windows (§8.3).
#
# The gate observes the rules CLAUDE.md records: no pipe into `grep -q` or
# any other early-exiting consumer (capture first, match a here-string),
# `| tr -d '\r'` on multi-line captures, waits that are bounded polls on
# API state, and committed executable.
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
FAKEJIRA="$BIN/fakejira$EXE"

ONLY="${VINCENT_GATE_SCENARIO:-}"

fail() { echo "GATE FAIL: $*" >&2; exit 1; }

JPID="" JBASE=""
jira_down() {
  [[ -n "$JPID" ]] || return 0
  curl -sS -X POST --max-time 5 "$JBASE/_fake/shutdown" >/dev/null 2>&1 || true
  for _ in $(seq 1 "$(gate_ticks 10)"); do
    kill -0 "$JPID" 2>/dev/null || break
    sleep "$GATE_POLL"
  done
  wait "$JPID" 2>/dev/null || true
  JPID=""
}

cleanup() {
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  jira_down
  rm -rf "$TMP"
}
trap cleanup EXIT

hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

echo "== build vincent, the fake Jira and the fake gh"
mkdir -p "$BIN"
gate_build "$BIN" vincent fakejira
# cmd/fakegh is built *as* `gh`, so a `gh` the daemon looks up is the fake,
# never a real CLI on the host's PATH.
gate_build_as "$BIN/gh$EXE" fakegh
PATH="$BIN:$PATH"
export PATH

unset GITHUB_TOKEN GH_TOKEN
# The gate is the human: a VINCENT_TASK_ID inherited from a step running the
# gate would make every `vincent` call here an agent's.
for v in $(compgen -e); do
  case "$v" in
    VINCENT_GATE_*) ;;
    VINCENT_*) unset "$v" ;;
  esac
done
export FAKEGH_SCENARIO=success
export FAKEGH_REPO=gate/jira

# The credentials the fake accepts, and the daemon reads from the variables
# jira.user_env and jira.token_env name (their defaults).
JIRA_GATE_USER="gate@example.com"
JIRA_GATE_TOKEN="gate-jira-token"
export JIRA_USER="$JIRA_GATE_USER" JIRA_API_TOKEN="$JIRA_GATE_TOKEN"

CONFIG_DIR="" DATA_DIR="" CORPUS="" JSCENARIO="" JREQUESTS="" GH_ARGV=""
scenario_dirs() { # scenario_dirs NAME
  CONFIG_DIR="$TMP/$1/config"
  DATA_DIR="$TMP/$1/data"
  mkdir -p "$CONFIG_DIR/workflows" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
  CORPUS="$TMP/$1/corpus.json"
  JSCENARIO="$TMP/$1/jira-scenario.txt"
  JREQUESTS="$TMP/$1/jira-requests.txt"
  GH_ARGV="$TMP/$1/gh-argv.txt"
  : > "$JSCENARIO"
  : > "$GH_ARGV"
  export FAKEGH_ARGV_FILE FAKEGH_STATE_FILE FAKEGH_ISSUES_FILE
  FAKEGH_ARGV_FILE="$(hostpath "$GH_ARGV")"
  FAKEGH_STATE_FILE="$(hostpath "$TMP/$1/gh-state.json")"
  FAKEGH_ISSUES_FILE="$(hostpath "$TMP/$1/gh-issues.json")"
  write_config false
  write_workflows
  "$FAKEJIRA" seed --corpus "$(hostpath "$CORPUS")" || fail "fakejira seed failed"
}

write_config() { # write_config GITHUB_ENABLED
  cat > "$CONFIG_DIR/config.yaml" <<EOF
github:
  enabled: $1
  poll_interval: 1h
jira:
  enabled: true
  poll_interval: 1s
EOF
}

# The workflows: `work` runs one `exit 0` step, leaving its issue ready to
# hand off; `land` is a handoff's, commenting on and then closing its issue
# through the CLI from inside the step, so the guard judges a real step.
write_workflows() {
  local d="$CONFIG_DIR/workflows"
  cat > "$d/work.yaml" <<'YAML'
name: work
steps:
  - id: noop
    type: command
    run: exit 0
YAML
  cat > "$d/land.yaml" <<'YAML'
name: land
role: handoff
steps:
  - id: comment
    type: command
    run: vincent issue comment {{ (index .Handoff.Items 0).Issue.Number }} --body "Landed in main."
  - id: close
    type: command
    run: vincent issue close {{ (index .Handoff.Items 0).Issue.Number }}
YAML
}

# jira_up: the fake Jira on a port of its own choosing, read from its port
# file once it appears.
jira_up() {
  local portfile="$TMP/jira-port.$RANDOM"
  "$FAKEJIRA" serve --addr 127.0.0.1:0 --port-file "$(hostpath "$portfile")" \
    --corpus "$(hostpath "$CORPUS")" --scenario-file "$(hostpath "$JSCENARIO")" \
    --requests-file "$(hostpath "$JREQUESTS")" \
    --user "$JIRA_GATE_USER" --token "$JIRA_GATE_TOKEN" 2> "$TMP/fakejira.err" &
  JPID=$!
  for _ in $(seq 1 "$(gate_ticks 20)"); do
    [[ -s "$portfile" ]] && break
    kill -0 "$JPID" 2>/dev/null || fail "fakejira exited: $(cat "$TMP/fakejira.err")"
    sleep "$GATE_POLL"
  done
  [[ -s "$portfile" ]] || fail "fakejira wrote no port file"
  JPORT="$(tr -d '\r\n' < "$portfile")"
  JBASE="http://127.0.0.1:$JPORT"
}

PORT="" TOKEN="" BASE="" MCP="" SESSION="" RPC_ID=0 PID=""
daemon_up() {
  "$VINCENT" daemon start
  daemon_addr
}
daemon_addr() { # the running daemon's port and token
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
  MCP="http://127.0.0.1:$PORT/mcp"
}
daemon_down() { "$VINCENT" daemon stop --force >/dev/null 2>&1 || true; }

# api_raw METHOD PATH [JSON_BODY] -> the status on the first line, the body
# after it, whatever the status.
api_raw() {
  local method="$1" path="$2" body="${3:-}" out
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  printf '%s\n%s' "${out##*$'\n'}" "${out%$'\n'*}"
}
status_of() { printf '%s' "${1%%$'\n'*}"; }
body_of() { printf '%s' "${1#*$'\n'}"; }

api() { # api METHOD PATH [JSON_BODY] -> body; a non-2xx fails
  local out status
  out="$(api_raw "$@")"
  status="$(status_of "$out")"
  [[ "$status" == 2* ]] || fail "$1 $2 -> HTTP $status: $(body_of "$out")"
  body_of "$out"
}

# jira_rest METHOD PATH JSON_BODY: someone else, on the Jira site, as its
# REST API takes it.
jira_rest() {
  local out
  out="$(curl -sS -X "$1" -u "$JIRA_GATE_USER:$JIRA_GATE_TOKEN" -H "Content-Type: application/json" \
    -d "$3" -w $'\n%{http_code}' "$JBASE/rest/api/3/$2")" || fail "curl $1 Jira $2 failed"
  [[ "${out##*$'\n'}" == 2* ]] || fail "Jira $1 $2 -> ${out##*$'\n'}: ${out%$'\n'*}"
}

jira_transition() { # jira_transition KEY TRANSITION_ID [RESOLUTION]
  local body
  if [[ -n "${3:-}" ]]; then
    body="$(jq -cn --arg t "$2" --arg r "$3" '{transition: {id: $t}, fields: {resolution: {name: $r}}}')"
  else
    body="$(jq -cn --arg t "$2" '{transition: {id: $t}}')"
  fi
  jira_rest POST "issue/$1/transitions" "$body"
}

adf() { jq -cn --arg t "$1" '{type: "doc", version: 1, content: [{type: "paragraph", content: [{type: "text", text: $t}]}]}'; }

# The fake's request log, and how many lines match an extended regex.
jira_requests() { if [[ -f "$JREQUESTS" ]]; then tr -d '\r' < "$JREQUESTS"; fi; }
jira_request_count() { local r; r="$(jira_requests)"; grep -c . <<<"$r" || true; }
jira_matching() { local r; r="$(jira_requests)"; grep -cE -- "$1" <<<"$r" || true; }
# A transition or comment POST the daemon made for Jira issue ID (the
# daemon addresses an issue by its id; the gate's own REST calls use keys).
daemon_posts() { jira_matching "^POST rest/api/3/issue/$1/$2 2[0-9][0-9]\$"; }

corpus_issue() { # corpus_issue ID -> "status/resolution" as Jira holds it
  jq -r --arg i "$1" '.issues[] | select(.id == $i) | "\(.status)/\(.resolution.name // "")"' "$CORPUS"
}
corpus_is() { [[ "$(corpus_issue "$1")" == "$2" ]]; }

make_repo() { # make_repo PATH [ORIGIN]
  git init -q -b main "$1"
  git -C "$1" config user.name gate
  git -C "$1" config user.email gate@example.invalid
  git -C "$1" config commit.gpgsign false
  printf 'gate repo\n' > "$1/README.md"
  git -C "$1" add . && git -C "$1" commit -qm init
  if [[ -n "${2:-}" ]]; then git -C "$1" remote add origin "$2"; fi
}

# jira_project NAME [ORIGIN]: a project bound to the fake's VIN project, in
# PID once VIN-1 and VIN-2 are imported.
jira_project() {
  make_repo "$TMP/$1" "${2:-}"
  PID="$(api POST /projects "$(jq -cn --arg p "$(hostpath "$TMP/$1")" --arg s "$JBASE" \
    '{path: $p, issue_source: {provider: "jira", jira: {site: $s, project_key: "VIN"}}}')" | jq -r .id)"
  sync_now > /dev/null
  wait_for 30 "the import of VIN-1 and VIN-2" imported_count_is 2
}

sync_now() { api POST "/projects/$PID/issues/sync"; }
sync_status() { api GET "/projects/$PID/issues/sync"; }

imported() { api GET "/issues?project_id=$PID&source=jira"; }
imported_count_is() { [[ "$(imported | jq length)" == "$1" ]]; }
issue_by_key() { imported | jq -r --arg k "$1" '.[] | select(.source.key == $k) | .id'; }
issue_field() { api GET "/issues/$1" | jq -r "$2"; }
field_is() { [[ "$(issue_field "$1" "$2")" == "$3" ]]; } # field_is ID FILTER WANT
sync_of() { issue_field "$1" '"\(.sync.state)/\(.sync.reason // "")"'; }

# wait_for SECS WHAT COMMAND... polls COMMAND every GATE_POLL for SECS seconds.
wait_for() {
  local secs="$1" what="$2"
  shift 2
  for _ in $(seq 1 "$(gate_ticks "$secs")"); do
    if "$@"; then return 0; fi
    sleep "$GATE_POLL"
  done
  fail "timed out after ${secs}s waiting for $what"
}

# events TYPES -> the data payloads since the start of the stream, read for
# --max-time as 130-gate.sh reads them.
events() {
  local raw
  raw="$(curl -sS --max-time 3 -N -H "Authorization: Bearer $TOKEN" \
    -H "Last-Event-ID: 1" "$BASE/events?types=$1" 2>/dev/null || true)"
  tr -d '\r' <<<"$raw" | sed -n 's/^data: //p' | jq -sc .
}

# ---- MCP, as 130-gate.sh speaks it

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
  out="$(mcp_post '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"142-gate","version":"1"}}}' \
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

task_state_is() { [[ "$(api GET "/tasks/$1" | jq -r .state)" == "$2" ]]; }

run_scenario() { [[ -z "$ONLY" || "$ONLY" == "$1" ]]; }

# start NAME [ORIGIN]: the scenario's files, the fake, a daemon and the bound
# project, imported.
start() {
  scenario_dirs "$1"
  jira_up
  daemon_up
  jira_project "$1/repo" "${2:-}"
}
finish() {
  daemon_down
  jira_down
}

# ---------------------------------------------------------------- scenario 1
if run_scenario 1; then
  echo "== scenario 1: the import"
  start s1
  rows="$(imported)"
  keys="$(jq -r '[.[].source.key] | sort | join(" ")' <<<"$rows")"
  [[ "$keys" == "VIN-1 VIN-2" ]] || fail "imported [$keys], want VIN-1 VIN-2 (no Done VIN-3)"
  bad="$(jq -r '[.[] | select(.state != "open" or .source.provider != "jira" or .sync.state != "synced")] | length' <<<"$rows")"
  [[ "$bad" == "0" ]] || fail "$bad imported issues are not open, jira and synced: $rows"
  [[ "$(jq -r '.[] | select(.source.key == "VIN-1") | .title' <<<"$rows")" == "Sync Jira issues into vincent" ]] \
    || fail "VIN-1's summary did not become its title: $rows"
  st="$(sync_status)"
  [[ "$(jq -r '"\(.provider)/\(.ok)/\(.import_complete)"' <<<"$st")" == "jira/true/true" ]] \
    || fail "the sync status is $st"
  finish
fi

# ---------------------------------------------------------------- scenario 2
if run_scenario 2; then
  echo "== scenario 2: an idle sync moves nothing"
  start s2
  # Let the import's own events land, then compare across a forced sync.
  sleep 2
  before="$(events issue.updated | jq length)"
  snap() { imported | jq -c '[.[] | {id, updated_at, version}] | sort_by(.id)'; }
  rows_before="$(snap)"
  lines="$(jira_request_count)"
  sync_now > /dev/null
  wait_for 30 "the forced sync to ask Jira" eval '[[ "$(jira_request_count)" -gt "$lines" ]]'
  sleep 2
  after="$(events issue.updated | jq length)"
  [[ "$after" == "$before" ]] || fail "an idle sync recorded $((after - before)) issue.updated events"
  [[ "$(snap)" == "$rows_before" ]] || fail "an idle sync moved issues: $rows_before -> $(snap)"
  finish
fi

# ---------------------------------------------------------------- scenario 3
if run_scenario 3; then
  echo "== scenario 3: closed in Jira"
  start s3
  one="$(issue_by_key VIN-1)"
  two="$(issue_by_key VIN-2)"
  jira_transition VIN-1 31 "Won't Do"
  jira_transition VIN-2 31 Done
  wait_for 30 "VIN-1 to close here" field_is "$one" '"\(.state)/\(.close_reason)"' closed/not_planned
  wait_for 30 "VIN-2 to close here" field_is "$two" '"\(.state)/\(.close_reason)"' closed/completed
  # The closes' events, whatever their type: each says closed, and by sync.
  by="$(events issue.created,issue.updated,issue.state_changed | jq -r --argjson a "$one" --argjson b "$two" \
    '[.[] | .payload | select((.id == $a or .id == $b) and .to == "closed") | "\(.id):\(.by)"] | sort | join(",")')"
  [[ "$by" == "$(jq -nr --argjson a "$one" --argjson b "$two" '["\($a):sync", "\($b):sync"] | sort | join(",")')" ]] \
    || fail "the closes' events say [$by], want both closed by sync"
  sleep 3 # a few ticks: a close read from Jira is not written back
  [[ "$(daemon_posts 10001 transitions)$(daemon_posts 10002 transitions)" == "00" ]] \
    || fail "a close read from Jira was written back: $(jira_requests)"
  finish
fi

# ---------------------------------------------------------------- scenario 4
if run_scenario 4; then
  echo "== scenario 4: a human close, written to Jira once"
  start s4
  one="$(issue_by_key VIN-1)"
  api POST "/issues/$one/close" '{"reason": "completed"}' > /dev/null
  wait_for 30 "VIN-1's write-back" corpus_is 10001 10002/Done
  wait_for 30 "VIN-1 to say synced" field_is "$one" .sync.state synced
  sync_now > /dev/null
  sleep 3 # a second sync and a few ticks: the write is not repeated
  [[ "$(daemon_posts 10001 transitions)" == "1" ]] \
    || fail "VIN-1 was transitioned $(daemon_posts 10001 transitions) times, want 1: $(jira_requests)"
  field_is "$one" '"\(.state)/\(.close_reason)"' closed/completed \
    || fail "VIN-1 is $(issue_field "$one" '"\(.state)/\(.close_reason)"') after the write"
  finish
fi

# ---------------------------------------------------------------- scenario 5
if run_scenario 5; then
  echo "== scenario 5: an offline close drains across a forced stop"
  start s5
  two="$(issue_by_key VIN-2)"
  echo unreachable > "$JSCENARIO"
  api POST "/issues/$two/close" '{"reason": "completed"}' > /dev/null
  wait_for 30 "VIN-2 to say pending" field_is "$two" .sync.state pending
  corpus_is 10002 10001/ || fail "VIN-2 changed in Jira while it was unreachable: $(corpus_issue 10002)"
  # A forced stop is the crash: the write is a row, not a goroutine.
  daemon_down
  : > "$JSCENARIO"
  daemon_up
  # The retry waits out the outbox's backoff after the failed try.
  wait_for 120 "VIN-2 to say synced after the restart" field_is "$two" .sync.state synced
  corpus_is 10002 10002/Done || fail "VIN-2 in Jira is $(corpus_issue 10002), want 10002/Done"
  sleep 3
  [[ "$(daemon_posts 10002 transitions)" == "1" ]] \
    || fail "VIN-2 was transitioned $(daemon_posts 10002 transitions) times, want 1: $(jira_requests)"
  finish
fi

# ---------------------------------------------------------------- scenario 6
if run_scenario 6; then
  echo "== scenario 6: jira.enabled false makes no request"
  start s6
  api PATCH /config '{"jira": {"enabled": false}}' > /dev/null
  wait_for 30 "the sync status to say disabled" \
    eval '[[ "$(sync_status | jq -r .reason)" == "disabled" ]]'
  sleep 2 # a tick already in flight lands before the count
  lines="$(jira_request_count)"
  api_raw POST "/projects/$PID/issues/sync" > /dev/null
  one="$(issue_by_key VIN-1)"
  api POST "/issues/$one/close" '{"reason": "completed"}' > /dev/null
  sleep 4 # several poll intervals: the point is that no request ever comes
  [[ "$(jira_request_count)" == "$lines" ]] \
    || fail "a disabled provider called Jira: $(jira_requests | sed -n "$((lines + 1)),\$p")"
  field_is "$one" .state closed || fail "a local close failed while Jira was disabled"
  finish
fi

# ---------------------------------------------------------------- scenario 7
if run_scenario 7; then
  echo "== scenario 7: a read-only token keeps the local state"
  start s7
  one="$(issue_by_key VIN-1)"
  echo read-only > "$JSCENARIO"
  api POST "/issues/$one/close" '{"reason": "completed"}' > /dev/null
  wait_for 60 "VIN-1's close to fail" field_is "$one" '"\(.sync.state)/\(.sync.reason)"' failed/no_write_scope
  field_is "$one" .state closed || fail "a refused write undid the local close: $(issue_field "$one" .state)"
  corpus_is 10001 10000/ || fail "VIN-1 in Jira is $(corpus_issue 10001), want 10000/"
  finish
fi

# ---------------------------------------------------------------- scenario 8
if run_scenario 8; then
  echo "== scenario 8: a conflict, which Jira wins"
  start s8
  one="$(issue_by_key VIN-1)"
  echo unreachable > "$JSCENARIO"
  api POST "/issues/$one/close" '{"reason": "completed"}' > /dev/null
  wait_for 30 "VIN-1 to say pending" field_is "$one" .sync.state pending
  # Someone else closes it in Jira, for a different reason, meanwhile.
  : > "$JSCENARIO"
  jira_transition VIN-1 31 "Won't Do"
  wait_for 120 "VIN-1 to say conflict" field_is "$one" .sync.state conflict
  field_is "$one" '"\(.state)/\(.close_reason)"' closed/not_planned \
    || fail "VIN-1 did not adopt Jira's value: $(issue_field "$one" '"\(.state)/\(.close_reason)"')"
  [[ "$(daemon_posts 10001 transitions)" == "0" ]] || fail "a conflicted write was sent anyway: $(jira_requests)"
  corpus_is 10001 "10002/Won't Do" || fail "VIN-1 in Jira is $(corpus_issue 10001), want 10002/Won't Do"
  [[ "$(sync_status | jq -r .writes_conflict)" == "1" ]] \
    || fail "the sync status counts $(sync_status | jq -r .writes_conflict) conflicts, want 1"
  finish
fi

# ---------------------------------------------------------------- scenario 9
if run_scenario 9; then
  echo "== scenario 9: no transition to a done status"
  scenario_dirs s9
  # Before the fake serves it: In Progress (10001) loses its edge to Done, so
  # VIN-2 has no way to a done-category status.
  jq '(.projects[] | select(.key == "VIN") | .transitions[] | select(.id == "31") | .from) = ["10000"]' \
    "$CORPUS" > "$CORPUS.edit" && mv -f "$CORPUS.edit" "$CORPUS"
  jira_up
  daemon_up
  jira_project s9/repo
  two="$(issue_by_key VIN-2)"
  api POST "/issues/$two/close" '{"reason": "completed"}' > /dev/null
  wait_for 60 "VIN-2's close to fail" field_is "$two" '"\(.sync.state)/\(.sync.reason)"' failed/no_transition
  field_is "$two" .state closed || fail "a failed write undid the local close: $(issue_field "$two" .state)"
  corpus_is 10002 10001/ || fail "VIN-2 in Jira is $(corpus_issue 10002), want 10001/"
  [[ "$(daemon_posts 10002 transitions)" == "0" ]] || fail "a transition was POSTed anyway: $(jira_requests)"
  finish
fi

# --------------------------------------------------------------- scenario 10
if run_scenario 10; then
  echo "== scenario 10: moved and deleted"
  start s10
  one="$(issue_by_key VIN-1)"
  two="$(issue_by_key VIN-2)"
  "$FAKEJIRA" move --corpus "$(hostpath "$CORPUS")" VIN-1 OPS OPS-7 || fail "fakejira move failed"
  "$FAKEJIRA" delete --corpus "$(hostpath "$CORPUS")" VIN-2 || fail "fakejira delete failed"
  # A sync asked for by a human sweeps the open set at once.
  sync_now > /dev/null
  wait_for 30 "VIN-1 to be marked moved" field_is "$one" .source.status moved
  wait_for 30 "VIN-2 to be marked missing" field_is "$two" .source.status missing
  # The new location is the moved issue's repo, number and URL. (Its display
  # key still reads VIN-1 at the time of writing: recorded on the pull
  # request as a follow-up, not asserted here.)
  got="$(issue_field "$one" '"\(.source.repo)/\(.source.number)/\(.source.url | sub("^.*/browse/"; ""))"')"
  [[ "$got" == "OPS/7/OPS-7" ]] || fail "the moved issue's location is $got, want OPS/7/OPS-7: $(issue_field "$one" .source)"
  out="$(api_raw GET "/issues/$two")"
  [[ "$(status_of "$out")" == "200" ]] || fail "a deleted Jira issue was deleted here: HTTP $(status_of "$out")"
  imported_count_is 2 || fail "the import lists $(imported | jq length) issues, want 2"
  finish
fi

# --------------------------------------------------------------- scenario 11
if run_scenario 11; then
  echo "== scenario 11: the MCP guard"
  start s11
  one="$(issue_by_key VIN-1)"
  mcp_init
  res="$(mcp_call issue_close "$(jq -cn --argjson i "$one" '{id: $i, body: {reason: "completed"}}')")"
  [[ "$(jq -r .isError <<<"$res")" == "true" ]] || fail "an agent closed an imported Jira issue: $res"
  text="$(jq -r '.content[0].text' <<<"$res")"
  [[ "$(jq -r .error.details.reason <<<"$text")" == "forge_write_needs_human" ]] \
    || fail "the refusal is not forge_write_needs_human: $text"
  field_is "$one" .state open || fail "the refused close closed VIN-1 anyway"
  sleep 3 # a few ticks: no write follows a refusal
  [[ "$(sync_of "$one")" == "synced/" ]] || fail "the refusal left a write: $(sync_of "$one")"
  [[ "$(sync_status | jq -r '.writes_pending + .writes_failed')" == "0" ]] \
    || fail "the refusal enqueued a write: $(sync_status)"
  [[ "$(jira_matching '^POST rest/api/3/issue/')" == "0" ]] || fail "a refused agent close reached Jira: $(jira_requests)"
  finish
fi

# --------------------------------------------------------------- scenario 12
if run_scenario 12; then
  echo "== scenario 12: the comment mirror"
  start s12
  one="$(issue_by_key VIN-1)"
  bodies() { api GET "/issues/$1/comments" | jq -r '[.comments[].body] | join("|")'; }
  # The built-in corpus carries a public "First." and an Administrators-only
  # "Admins only." on VIN-1.
  wait_for 30 "VIN-1's public comment" eval '[[ "$(bodies "$one")" == *"First."* ]]'
  jira_rest POST issue/VIN-1/comment "$(jq -cn --argjson b "$(adf "Seen in Jira")" '{body: $b}')"
  jira_rest POST issue/VIN-1/comment "$(jq -cn --argjson b "$(adf "Restricted note")" \
    '{body: $b, visibility: {type: "role", value: "Administrators"}}')"
  wait_for 30 "the new public comment to be mirrored" eval '[[ "$(bodies "$one")" == *"Seen in Jira"* ]]'
  sync_now > /dev/null
  sleep 3
  got="$(bodies "$one")"
  [[ "$got" != *"Admins only."* && "$got" != *"Restricted note"* ]] \
    || fail "a restricted comment was mirrored: $got"
  [[ "$(api GET "/issues/$one/comments" | jq '.comments | length')" == "2" ]] \
    || fail "VIN-1's thread is [$got], want the two public comments"
  finish
fi

# --------------------------------------------------------------- scenario 13
if run_scenario 13; then
  echo "== scenario 13: a granted handoff writes a comment and a close back"
  start s13
  two="$(issue_by_key VIN-2)"
  task="$(api POST /tasks "$(jq -cn --argjson p "$PID" --argjson i "$two" \
    '{project_id: $p, workflow: "work", issue_id: $i}')" | jq -r .id)"
  wait_for 60 "task $task to finish" task_state_is "$task" done
  wait_for 30 "VIN-2 to reach hand_off" field_is "$two" .lane hand_off
  h="$(api POST /handoffs "$(jq -cn --argjson p "$PID" --argjson i "$two" \
    '{project_id: $p, workflow: "land", issue_ids: [$i], grant: ["issue.comment", "issue.close"]}')")"
  tid="$(jq -r .task_id <<<"$h")"
  task_done_or_dump() {
    local st
    st="$(api GET "/tasks/$1" | jq -r .state)"
    [[ "$st" == done ]] && return 0
    [[ "$st" == blocked || "$st" == failed ]] \
      && fail "handoff task $1 is $st: $(api GET "/tasks/$1/steps" | jq -c '[.[] | {step_id, state, failure_reason}]') $(api GET "/tasks/$1" | jq -c '{block_reason, failure_reason}')"
    return 1
  }
  wait_for 60 "handoff task $tid to finish" task_done_or_dump "$tid"
  field_is "$two" .state closed || fail "the granted close left VIN-2 $(issue_field "$two" .state)"
  wait_for 60 "VIN-2's write-back" corpus_is 10002 10002/Done
  wait_for 30 "the handoff comment's write-back" eval '[[ "$(daemon_posts 10002 comment)" == "1" ]]'
  sleep 3 # a few more ticks: neither write is repeated
  [[ "$(daemon_posts 10002 transitions)/$(daemon_posts 10002 comment)" == "1/1" ]] \
    || fail "VIN-2 got $(daemon_posts 10002 transitions) transitions and $(daemon_posts 10002 comment) comments, want 1/1: $(jira_requests)"
  posted="$(jq -c '[.comments[] | select(.issue == "10002")] | last | .body.type' "$CORPUS")"
  [[ "$posted" == '"doc"' ]] || fail "the handoff comment reached Jira as $posted, want an ADF doc"
  finish
fi

# --------------------------------------------------------------- scenario 14
if run_scenario 14; then
  echo "== scenario 14: no credential, no request"
  start s14
  daemon_down
  lines="$(jira_request_count)"
  # The daemon reads the credential from the environment it inherited.
  (
    unset JIRA_API_TOKEN
    "$VINCENT" daemon start
  )
  daemon_addr
  sync_now > /dev/null
  wait_for 30 "the sync status to say no_credential" \
    eval '[[ "$(sync_status | jq -r .reason)" == "no_credential" ]]'
  sleep 2
  [[ "$(jira_request_count)" == "$lines" ]] \
    || fail "a daemon with no credential called Jira: $(jira_requests | sed -n "$((lines + 1)),\$p")"
  finish
fi

# --------------------------------------------------------------- scenario 15
if run_scenario 15; then
  echo "== scenario 15: a Jira-bound project with a GitHub origin"
  scenario_dirs s15
  write_config true
  jira_up
  daemon_up
  jira_project s15/repo "https://github.com/$FAKEGH_REPO.git"
  [[ "$(api GET "/projects/$PID" | jq -r .issue_source.effective)" == "jira" ]] \
    || fail "the project's issue source is $(api GET "/projects/$PID" | jq -c .issue_source)"
  one="$(issue_by_key VIN-1)"
  task="$(api POST /tasks "$(jq -cn --argjson p "$PID" --argjson i "$one" \
    '{project_id: $p, workflow: "work", issue_id: $i}')" | jq -r .id)"
  wait_for 60 "task $task to finish" task_state_is "$task" done
  out="$("$VINCENT" github pr link 412 --task "$task" 2>&1)" \
    || fail "vincent github pr link exited non-zero: $out"
  [[ "$(api GET "/tasks/$task/github/pull" | jq -r .number)" == "412" ]] \
    || fail "the task does not link pull request 412: $(api GET "/tasks/$task/github/pull")"
  sync_now > /dev/null
  sleep 3
  calls="$(tr -d '\r' < "$GH_ARGV")"
  listings="$(grep -cE 'repos/[^ ]+/issues([?/ ]|$)' <<<"$calls" || true)"
  [[ "$listings" == "0" ]] || fail "gh was asked for the project's GitHub issues: $calls"
  imported_count_is 2 || fail "the project lists $(imported | jq length) Jira issues, want 2"
  [[ "$(api GET "/issues?project_id=$PID&source=github" | jq length)" == "0" ]] \
    || fail "GitHub issues were imported into a Jira-bound project"
  finish
fi

echo "GATE PASS"
