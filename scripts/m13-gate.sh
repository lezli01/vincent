#!/usr/bin/env bash
# M13 phase gate (task 065; spec §5.2, §8.2, §13.2): prove over the wire that
# the daemon can author a workflow file on a client's behalf without losing
# what a human wrote in it.
#
#   1. GET /v1/workflows/schema serves §8.2 as data — every step type, with
#      the contexts each may appear in
#   2. POST /v1/workflows creates a file in the global scope and the registry
#      lists it before the response lands
#   3. PATCH /v1/workflows changes one field and every other byte of the file
#      is identical, comments and blank lines included
#   4. a fork into a project scope shadows the entry it copied (§5.2), and
#      keeps the source's own name:, which is what makes it shadow
#   5. a stale write answers 409 carrying the current version
#   6. a patch that would not validate answers the §13.1 envelope and leaves
#      the file byte-identical
#   7. the write routes are not MCP tools; the schema route is
#
# No agent CLI: what is under test is three endpoints and a file.
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

REPO="$TMP/repo"
mkdir -p "$REPO"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email gate@example.com
git -C "$REPO" config user.name "M13 Gate"
git -C "$REPO" commit -q --allow-empty -m "root"

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

echo "== 1. GET /v1/workflows/schema serves §8.2 as data"
SCHEMA="$(api GET /workflows/schema)" || fail "GET /v1/workflows/schema failed"
for typ in agent command manual parallel fan_out condition loop break include; do
  printf '%s' "$SCHEMA" | jq -e --arg t "$typ" '[.steps[].type] | index($t)' >/dev/null \
    || fail "the schema does not describe the $typ step type"
done
# The nesting rules travel with the types: a client that had to re-derive
# them is the drift PR L recorded.
printf '%s' "$SCHEMA" | jq -e \
  '[.steps[] | select(.type == "break") | .contexts[]] == ["loop"]' >/dev/null \
  || fail "break is not restricted to a loop body in the served schema"
printf '%s' "$SCHEMA" | jq -e \
  '[.steps[] | select(.type == "manual") | .contexts[]] | index("parallel") | not' >/dev/null \
  || fail "manual is offered inside a parallel group in the served schema"

echo "== 2. POST /v1/workflows creates a file, in force before it answers"
CREATED="$(api POST /workflows -d '{"scope":"global","name":"gate-flow"}')" \
  || fail "POST /v1/workflows failed"
printf '%s' "$CREATED" | jq -e '.name == "gate-flow" and .scope == "global"' >/dev/null \
  || fail "the create response is wrong: $CREATED"
FILE="$(printf '%s' "$CREATED" | jq -r .file)"
[[ -f "$FILE" ]] || fail "no file at $FILE"
# No sleep: the write is put into force before the response is written, the
# same contract PATCH /v1/config has.
api GET /workflows | jq -e '[.workflows[].name] | index("gate-flow")' >/dev/null \
  || fail "the new workflow is not in the registry immediately after the 201"
grep -q '^#' "$FILE" || fail "the skeleton's comments did not reach the disk"

echo "== 3. PATCH changes one field and nothing else"
# A hand-written file with the shapes a marshal round trip destroys.
GLOBAL_DIR="$(dirname "$FILE")"
cat > "$GLOBAL_DIR/hand.yaml" <<'YAML'
# hand — a human wrote this and the comments are the point.
name: hand
description: Do the thing

steps:
  - id: build
    type: command
    run: git --version   # the only command every platform has

  # the second step is deliberately after a blank line and a comment
  - id: check
    type: command
    run: git status
YAML
"$VINCENT" workflow list >/dev/null 2>&1 || true
# The registry watch picks the file up; poll rather than sleep.
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if api GET /workflows | jq -e '[.workflows[].name] | index("hand")' >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
VERSION="$(api "GET" "/workflows/definition?name=hand" | jq -r .version)"
[[ -n "$VERSION" && "$VERSION" != "null" ]] || fail "the definition served no version token"
cp "$GLOBAL_DIR/hand.yaml" "$TMP/hand.before"
api PATCH "/workflows?name=hand" \
  -d "{\"version\":\"$VERSION\",\"ops\":[{\"op\":\"set\",\"path\":\"steps[1].run\",\"value\":\"git log --oneline -1\"}]}" \
  >/dev/null || fail "PATCH /v1/workflows failed"
# Everything outside the one edited line is identical. diff is the assertion:
# one changed line, and no other.
CHANGED="$(diff "$TMP/hand.before" "$GLOBAL_DIR/hand.yaml" | grep -c '^[<>]' || true)"
[[ "$CHANGED" == "2" ]] \
  || fail "the patch touched more than the one line it addressed ($CHANGED changed lines)"
COMMENTS="$(grep -c '#' "$GLOBAL_DIR/hand.yaml")"
[[ "$COMMENTS" == "3" ]] || fail "a comment was lost: $COMMENTS remain, want 3"

echo "== 4. a fork into a project scope shadows what it copied"
PROJECT="$(api POST /projects -d "{\"path\":\"$(hostpath "$REPO")\"}")" \
  || fail "POST /v1/projects failed"
PID="$(printf '%s' "$PROJECT" | jq -r .id)"
FORK="$(api POST /workflows \
  -d "{\"scope\":\"project\",\"project_id\":$PID,\"name\":\"adhoc\",\"from\":\"adhoc\"}")" \
  || fail "the fork failed"
# The copy keeps the source's own name:, because §5.2 shadows by name.
printf '%s' "$FORK" | jq -e '.name == "adhoc" and .scope == "project"' >/dev/null \
  || fail "the fork is not a project-scoped adhoc: $FORK"
SCOPE="$(api "GET" "/workflows?project_id=$PID" \
  | jq -r '.workflows[] | select(.name == "adhoc") | .scope')"
[[ "$SCOPE" == "project" ]] || fail "the fork does not shadow the built-in (scope $SCOPE)"

echo "== 5. a stale write answers 409 with the current version"
STALE="$(api "GET" "/workflows/definition?name=hand" | jq -r .version)"
api PATCH "/workflows?name=hand" \
  -d "{\"version\":\"$STALE\",\"ops\":[{\"op\":\"set\",\"path\":\"description\",\"value\":\"once\"}]}" \
  >/dev/null || fail "the first write failed"
STATUS="$(api_status PATCH "/workflows?name=hand" \
  -d "{\"version\":\"$STALE\",\"ops\":[{\"op\":\"set\",\"path\":\"description\",\"value\":\"twice\"}]}")" \
  || fail "curl PATCH (stale) failed"
[[ "$STATUS" == "409" ]] || fail "a stale write answered $STATUS, want 409"
jq -e '.error.details.version != null' "$TMP/body.json" >/dev/null \
  || fail "the 409 does not carry the current version: $(cat "$TMP/body.json")"

echo "== 6. a refused patch writes nothing"
cp "$GLOBAL_DIR/hand.yaml" "$TMP/hand.refused"
CURRENT="$(api "GET" "/workflows/definition?name=hand" | jq -r .version)"
STATUS="$(api_status PATCH "/workflows?name=hand" \
  -d "{\"version\":\"$CURRENT\",\"ops\":[{\"op\":\"set\",\"path\":\"steps[0].prompt\",\"value\":\"nope\"}]}")" \
  || fail "curl PATCH (invalid) failed"
[[ "$STATUS" == "400" ]] || fail "prompt on a command step answered $STATUS, want 400"
jq -e '.error.code == "validation_failed"' "$TMP/body.json" >/dev/null \
  || fail "the refusal is not the §13.1 envelope: $(cat "$TMP/body.json")"
cmp -s "$TMP/hand.refused" "$GLOBAL_DIR/hand.yaml" \
  || fail "a rejected patch changed the file"

echo "== 7. the write routes are not MCP tools"
INIT="$(curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"m13-gate","version":"1"}}}' \
  -D "$TMP/init-headers.txt" "http://127.0.0.1:$PORT/mcp")" \
  || fail "curl POST /mcp (initialize) failed"
printf '%s' "$INIT" | tr -d '\r' | sed -n 's/^data: //p' | head -n 1 \
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
grep -qx 'workflow_schema' <<<"$NAMES" || fail "workflow_schema is not a tool"
for tool in workflow_create workflow_patch; do
  if grep -qx "$tool" <<<"$NAMES"; then
    fail "$tool is exposed; an agent must not edit the workflows its own daemon runs"
  fi
done

echo "== 8. the daemon is still up"
api GET /health | jq -e '.status == "ok"' >/dev/null \
  || fail "the daemon did not survive the gate"

echo "GATE PASS: M13 (task 065)"
