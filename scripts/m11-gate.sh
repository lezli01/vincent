#!/usr/bin/env bash
# M11 phase gate (task 060; spec §12.3, §13.2): prove over the wire that the
# daemon serves its whole configuration and can be reconfigured through the
# API without a restart, and that a refused edit leaves the file alone.
#
#   1. GET /v1/config serves every key config.yaml carries — including the
#      nine that used to be invisible from every client
#   2. PATCH /v1/config changes a hot-reloadable key, and a GET issued
#      immediately afterwards reads the new value with no sleep
#   3. the comments and the key order in config.yaml survive the write, and
#      a documented block that was commented out is uncommented in place
#      rather than appended a second time
#   4. an invalid patch answers the §13.1 envelope and leaves the file
#      byte-identical
#   5. the file is still mode 0600 afterwards (POSIX only — Windows has no
#      such mode, and the daemon's ACL story is CheckPermissions')
#   6. `vincent config get|set` does the same job from the command line
#   7. PATCH /v1/config is not an MCP tool: an agent must not be able to
#      reconfigure the daemon supervising it (task 057 decision 4)
#
# No workflow and no agent CLI: what is under test is an endpoint and a file,
# so the gate needs neither, which also keeps it fast on all three platforms.
#
# Requirements: bash, go, git, curl, jq.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
BIN="$TMP/bin"

VINCENT="$BIN/vincent"
if [[ "${OS:-}" == "Windows_NT" ]]; then
  VINCENT+=".exe"
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

echo "== build vincent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent)

CONFIG_DIR="$TMP/config"
DATA_DIR="$TMP/data"
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"

CONFIG_FILE="$CONFIG_DIR/config.yaml"
PORT="" TOKEN="" BASE=""

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

# api_status METHOD PATH [curl args...] -> "<status> <body>" on one line, so a
# scenario can assert both without a second request.
api_status() {
  local method="$1" path="$2"
  shift 2
  curl -sS -o "$TMP/body.json" -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    "$@" "$BASE$path"
}

echo "== 1. GET /v1/config serves every key"
CFG="$(api GET /config)" || fail "GET /v1/config failed"
# The nine keys the endpoint used to omit, plus a representative of each
# section that was already there. A key missing here is a key no client can
# see, which is the defect this task closed.
for key in listen max_parallel_tasks branch_template defaults \
           delete_empty_branch_on_archive delete_remote_branch_on_archive \
           fetch_base_branch transcript_retention_days transcript_max_bytes \
           max_task_cost_usd usage_limit_recheck_interval log_level debug \
           environment agents parallel fan_out loop include mcp github \
           update notify tui; do
  printf '%s' "$CFG" | jq -e --arg k "$key" 'has($k)' >/dev/null \
    || fail "GET /v1/config does not serve $key"
done

echo "== 2. PATCH applies before it answers"
BEFORE="$(printf '%s' "$CFG" | jq -r .max_parallel_tasks)"
[[ "$BEFORE" == "3" ]] || fail "max_parallel_tasks started at $BEFORE, want the default 3"
PATCHED="$(api PATCH /config -d '{"max_parallel_tasks":7,"log_level":"debug"}')" \
  || fail "PATCH /v1/config failed"
printf '%s' "$PATCHED" | jq -e '.max_parallel_tasks == 7 and .log_level == "debug"' >/dev/null \
  || fail "the patch response does not carry the new values: $PATCHED"
# No sleep and no retry loop: decision 5 says the applier runs before the
# response is written, so the very next GET has to agree.
AFTER="$(api GET /config | jq -r .max_parallel_tasks)" || fail "GET after PATCH failed"
[[ "$AFTER" == "7" ]] || fail "GET right after the 200 still reads $AFTER"

echo "== 3. comments and key order survive; a commented block is uncommented in place"
# The file is documentation as much as it is settings. A single-line capture
# needs no CR treatment; the multi-line ones below do (jq writes CRLF on
# Windows and $(...) strips only the trailing one).
COMMENTS_BEFORE="$(grep -c '^#' "$CONFIG_FILE")"
api PATCH /config -d '{"notify":{"on":["blocked"],"command":["git","--version"]}}' >/dev/null \
  || fail "PATCH notify failed"
COMMENTS_AFTER="$(grep -c '^#' "$CONFIG_FILE")"
[[ "$COMMENTS_AFTER" -ge $((COMMENTS_BEFORE - 3)) ]] \
  || fail "the write flattened the file's comments ($COMMENTS_BEFORE -> $COMMENTS_AFTER)"
NOTIFY_LINES="$(grep -c '^notify:' "$CONFIG_FILE")"
[[ "$NOTIFY_LINES" == "1" ]] \
  || fail "notify: appears $NOTIFY_LINES times; the block was appended rather than uncommented"
# The key that was edited first is still where it was, above the one edited
# second: the editor never reorders.
ORDER="$(grep -n '^log_level:\|^notify:' "$CONFIG_FILE" | cut -d: -f2 | tr -d '\r')"
EXPECTED_ORDER="$(printf 'log_level\nnotify')"
[[ "$ORDER" == "$EXPECTED_ORDER" ]] || fail "key order moved: $ORDER"
# And the warning the notify block carries is still there to read.
WARNING="$(grep -c 'WARNING: this is arbitrary code' "$CONFIG_FILE")"
[[ "$WARNING" == "1" ]] || fail "the notify block's documentation was flattened"

echo "== 4. an invalid patch writes nothing"
cp "$CONFIG_FILE" "$TMP/before.yaml"
STATUS="$(api_status PATCH /config -d '{"log_level":"chatty"}')" \
  || fail "curl PATCH (invalid) failed"
[[ "$STATUS" == "400" ]] || fail "an invalid log_level answered $STATUS, want 400"
jq -e '.error.code == "validation_failed"' "$TMP/body.json" >/dev/null \
  || fail "the refusal is not the §13.1 envelope: $(cat "$TMP/body.json")"
cmp -s "$TMP/before.yaml" "$CONFIG_FILE" \
  || fail "a rejected patch changed config.yaml"
# A branch template that does not compile is refused by the same route, on
# the check that lives in internal/worktree.
STATUS="$(api_status PATCH /config -d '{"branch_template":"vincent/{{.ID"}')" \
  || fail "curl PATCH (bad template) failed"
[[ "$STATUS" == "400" ]] || fail "an uncompilable branch_template answered $STATUS, want 400"
cmp -s "$TMP/before.yaml" "$CONFIG_FILE" \
  || fail "a rejected branch_template changed config.yaml"

echo "== 5. the file is still owner-only"
if [[ "${OS:-}" == "Windows_NT" ]]; then
  echo "   (skipped: Windows has no POSIX mode; see config.CheckPermissions)"
else
  MODE="$(ls -l "$CONFIG_FILE" | cut -c1-10)"
  [[ "$MODE" == "-rw-------" ]] || fail "config.yaml is $MODE after a patch, want -rw-------"
fi

echo "== 6. vincent config get|set"
OUT="$("$VINCENT" config get log_level | tr -d '\r')"
[[ "$OUT" == "debug" ]] || fail "vincent config get log_level = $OUT, want debug"
"$VINCENT" config set log_level info >/dev/null || fail "vincent config set failed"
OUT="$(api GET /config | jq -r .log_level)"
[[ "$OUT" == "info" ]] || fail "vincent config set did not reach the daemon: $OUT"
# The whole file, one key per line, and every key the endpoint serves.
LISTED="$("$VINCENT" config get | tr -d '\r')"
grep -q '^branch_template = ' <<<"$LISTED" \
  || fail "vincent config get does not list branch_template"
grep -q '^notify.command = git --version$' <<<"$LISTED" \
  || fail "vincent config get does not read notify.command back: $LISTED"
# A key that does not exist is an error, not a silent success.
if "$VINCENT" config set no_such_key 1 >/dev/null 2>&1; then
  fail "vincent config set accepted a key that does not exist"
fi

echo "== 7. PATCH /v1/config is not an MCP tool"
TOOLS="$(curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"m11-gate","version":"1"}}}' \
  -D "$TMP/init-headers.txt" "http://127.0.0.1:$PORT/mcp")" \
  || fail "curl POST /mcp (initialize) failed"
printf '%s' "$TOOLS" | tr -d '\r' | sed -n 's/^data: //p' | head -n 1 \
  | jq -e '.result.serverInfo.name == "vincent"' >/dev/null \
  || fail "initialize did not identify the vincent server"
SESSION="$(tr -d '\r' < "$TMP/init-headers.txt" \
  | sed -n 's/^[Mm]cp-[Ss]ession-[Ii]d: //p' | head -n 1)"
[[ -n "$SESSION" ]] || fail "initialize minted no Mcp-Session-Id"
curl -sS -o /dev/null -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
  -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' \
  "http://127.0.0.1:$PORT/mcp" || fail "curl POST /mcp (initialized) failed"
LIST="$(curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
  -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  "http://127.0.0.1:$PORT/mcp")" || fail "curl POST /mcp (tools/list) failed"
NAMES="$(printf '%s' "$LIST" | tr -d '\r' | sed -n 's/^data: //p' | head -n 1 \
  | jq -r '.result.tools[].name')"
grep -qx 'config_get' <<<"$NAMES" || fail "config_get is not a tool"
if grep -qx 'config_patch' <<<"$NAMES"; then
  fail "config_patch is exposed; an agent must not be able to reconfigure its own daemon"
fi
if grep -qx 'config_set' <<<"$NAMES"; then
  fail "config_set is exposed; an agent must not be able to reconfigure its own daemon"
fi

echo "== 8. the daemon is still up"
api GET /health | jq -e '.status == "ok"' >/dev/null \
  || fail "the daemon did not survive the gate"

echo "GATE PASS: M11 (task 060)"
