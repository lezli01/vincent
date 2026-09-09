#!/usr/bin/env bash
# M15 phase gate (task 092; spec §10, §13.2, §13.3, §17): prove over the wire
# that an archived row can be permanently deleted, that the delete refuses
# rather than cascading, and that §10's branch rule survives being asked to
# break it.
#
#   1. a task runs to done, is archived, and is listed by archived=true —
#      newest-archived first, and absent from the default listing
#   2. archived_before / archived_since bound that listing, and garbage is 400
#   3. DELETE on a *live* task is refused 409 with `reason: not_archived`,
#      and the row is still there afterwards
#   4. DELETE on the archived task removes the row (404 afterwards) and its
#      transcript directory
#   5. delete_branch=true against a branch that carries commits reports
#      has_commits and *keeps* the branch — §10's standing rule, unchanged by
#      the asking
#   6. delete_branch=true against a branch with no commits past its base
#      deletes it, and the delete reports `deleted`
#   7. a task.deleted event reaches GET /v1/events
#   8. DELETE on an unknown id is 404
#   9. neither DELETE is an MCP tool (§13.4)
#
# The agent is cmd/fakeagent unless VINCENT_GATE_AGENT names a real one.
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

echo "== build vincent and the fake agent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent ./cmd/fakeagent)

CONFIG_DIR="$TMP/config"
DATA_DIR="$TMP/data"
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"

AGENT="${VINCENT_GATE_AGENT:-claude}"
# delete_empty_branch_on_archive is off deliberately: it defaults on, and an
# archive that had already deleted the empty branch would leave scenario 6
# asserting nothing. With it off, the branch survives the archive and the
# *delete* is what judges it — which is the thing under test.
{
  echo "delete_empty_branch_on_archive: false"
  if [[ -z "${VINCENT_GATE_AGENT:-}" ]]; then
    echo "agents:"
    echo "  claude:"
    echo "    path: $(hostpath "$FAKEAGENT")"
  fi
} > "$CONFIG_DIR/config.yaml"

REPO="$TMP/repo"
mkdir -p "$REPO"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email gate@example.com
git -C "$REPO" config user.name "M15 Gate"
git -C "$REPO" commit -q --allow-empty -m "root"

# Two workflows: one whose step commits (so its branch carries something past
# its base) and one whose step does not. `run:` bodies execute under the
# daemon's shell — /bin/sh on POSIX, pwsh on Windows (§8.3) — so they are
# spelled in the intersection of the two: `git ...` and nothing else.
mkdir -p "$CONFIG_DIR/workflows"
cat > "$CONFIG_DIR/workflows/gate-commits.yaml" <<'YAML'
name: gate-commits
description: One step that leaves a commit on the task's branch.
steps:
  - id: work
    type: command
    run: git commit --allow-empty -m "work the gate can see"
YAML
cat > "$CONFIG_DIR/workflows/gate-empty.yaml" <<'YAML'
name: gate-empty
description: One step that writes nothing, so the branch stays empty.
steps:
  - id: work
    type: command
    run: git status
YAML

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

api_status() {
  local method="$1" path="$2"
  shift 2
  curl -sS -o "$TMP/body.json" -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    "$@" "$BASE$path"
}

PROJECT="$(api POST /projects -d "{\"name\":\"gate\",\"path\":\"$(hostpath "$REPO")\"}")" \
  || fail "POST /v1/projects failed"
PROJECT_ID="$(printf '%s' "$PROJECT" | jq -r .id)"

# create_task WORKFLOW TITLE -> the new task's id
create_task() {
  local wf="$1" title="$2" body
  body="$(api POST /tasks -d "{\"project_id\":$PROJECT_ID,\"workflow\":\"$wf\",\"title\":\"$title\",\"agent\":\"$AGENT\"}")" \
    || fail "POST /v1/tasks failed for $title"
  printf '%s' "$body" | jq -r .id
}

# wait_state ID STATE — poll until the task reaches STATE.
wait_state() {
  local id="$1" want="$2" i state
  for i in $(seq 1 120); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "aborted" || "$state" == "blocked" ]] && [[ "$want" != "$state" ]]; then
      fail "task $id went $state waiting for $want"
    fi
    sleep 1
  done
  fail "task $id never reached $want (stuck in $state)"
}

echo "== 1. a task runs, archives, and appears only in the archived listing"
COMMITS_ID="$(create_task gate-commits "carries a commit")"
wait_state "$COMMITS_ID" done
BRANCH="$(api GET "/tasks/$COMMITS_ID" | jq -r .branch_name)"
api POST "/tasks/$COMMITS_ID/archive" >/dev/null || fail "archive failed"

LIVE="$(api GET /tasks | jq -r '[.[].id] | join(",")')"
case ",$LIVE," in
  *",$COMMITS_ID,"*) fail "the archived task is still on the default listing" ;;
esac
ARCHIVED="$(api GET "/tasks?archived=true" | jq -r '[.[].id] | join(",")')"
case ",$ARCHIVED," in
  *",$COMMITS_ID,"*) ;;
  *) fail "the archived task is not in archived=true ($ARCHIVED)" ;;
esac

echo "== 2. the date bounds narrow the archive, and garbage is 400"
FUTURE="2099-01-01T00:00:00Z"
PAST="2000-01-01T00:00:00Z"
N_BEFORE_FUTURE="$(api GET "/tasks?archived=true&archived_before=$FUTURE" | jq 'length')"
[[ "$N_BEFORE_FUTURE" -ge 1 ]] || fail "archived_before=$FUTURE returned nothing"
N_BEFORE_PAST="$(api GET "/tasks?archived=true&archived_before=$PAST" | jq 'length')"
[[ "$N_BEFORE_PAST" -eq 0 ]] || fail "archived_before=$PAST returned $N_BEFORE_PAST rows"
N_SINCE_PAST="$(api GET "/tasks?archived=true&archived_since=$PAST" | jq 'length')"
[[ "$N_SINCE_PAST" -ge 1 ]] || fail "archived_since=$PAST returned nothing"
CODE="$(api_status GET "/tasks?archived_before=nonsense")"
[[ "$CODE" == "400" ]] || fail "an unparseable archived_before answered $CODE, want 400"
jq -e '.error.code == "validation_failed"' "$TMP/body.json" >/dev/null \
  || fail "the 400 is not the §13.1 envelope: $(cat "$TMP/body.json")"
# GET /v1/chats takes the same four parameters, spelled the same way.
CODE="$(api_status GET "/chats?archived_since=nonsense")"
[[ "$CODE" == "400" ]] || fail "GET /v1/chats accepted an unparseable archived_since ($CODE)"
api GET "/chats?limit=1&offset=0&archived=true" >/dev/null || fail "GET /v1/chats rejected paging"

echo "== 3. a live task is refused 409, and survives it"
LIVE_ID="$(create_task gate-empty "still alive")"
wait_state "$LIVE_ID" done
CODE="$(api_status DELETE "/tasks/$LIVE_ID")"
[[ "$CODE" == "409" ]] || fail "deleting a done task answered $CODE, want 409"
jq -e '.error.details.reason == "not_archived"' "$TMP/body.json" >/dev/null \
  || fail "the refusal does not name the reason: $(cat "$TMP/body.json")"
api GET "/tasks/$LIVE_ID" >/dev/null || fail "the refused task was deleted anyway"

echo "== 4/5. delete_branch against a branch that carries commits keeps it"
TRANSCRIPTS="$DATA_DIR/transcripts/$COMMITS_ID"
[[ -d "$TRANSCRIPTS" ]] || fail "no transcript directory at $TRANSCRIPTS to reclaim"
OUT="$(api DELETE "/tasks/$COMMITS_ID?delete_branch=true")" || fail "DELETE failed"
printf '%s' "$OUT" | jq -e '.deleted == true' >/dev/null || fail "the delete did not report itself: $OUT"
printf '%s' "$OUT" | jq -e '.branch.result == "has_commits"' >/dev/null \
  || fail "a branch with commits was not reported has_commits: $OUT"
REFS="$(git -C "$REPO" for-each-ref --format='%(refname:short)' refs/heads)"
grep -qx "$BRANCH" <<<"$REFS" || fail "the branch carrying commits was deleted: $REFS"
CODE="$(api_status GET "/tasks/$COMMITS_ID")"
[[ "$CODE" == "404" ]] || fail "the deleted task still answers $CODE"
[[ -d "$TRANSCRIPTS" ]] && fail "the transcript directory survived the delete"

echo "== 6. delete_branch against an empty branch deletes it"
EMPTY_ID="$(create_task gate-empty "writes nothing")"
wait_state "$EMPTY_ID" done
EMPTY_BRANCH="$(api GET "/tasks/$EMPTY_ID" | jq -r .branch_name)"
api POST "/tasks/$EMPTY_ID/archive" >/dev/null || fail "archive failed"
OUT="$(api DELETE "/tasks/$EMPTY_ID?delete_branch=true")" || fail "DELETE failed"
printf '%s' "$OUT" | jq -e '.branch.result == "deleted"' >/dev/null \
  || fail "an empty branch was not deleted: $OUT"
REFS="$(git -C "$REPO" for-each-ref --format='%(refname:short)' refs/heads)"
if grep -qx "$EMPTY_BRANCH" <<<"$REFS"; then
  fail "the empty branch survived: $REFS"
fi

echo "== 7. the delete is announced as a durable event"
# GET /v1/events is a stream that never ends by itself, and it never replays
# history unasked (PR D decision): `Last-Event-ID: 0` is *no cursor* — the
# handler's replay is gated on `cursor > 0` — so a stream opened here, after
# the delete, would sit idle until --max-time and prove nothing. Resume from 1
# instead, which is every event this daemon has ever written but the first,
# and narrow it with ?types= so the replay is the one type under test rather
# than the whole run. `head` caps the follow that comes after the replay; it
# is inside the substitution, so pipefail's SIGPIPE 141 is caught by the
# `|| true` there and not read as a gate failure.
DELETED="$(curl -sS --max-time 5 -H "Authorization: Bearer $TOKEN" \
  -H "Last-Event-ID: 1" -N "$BASE/events?types=task.deleted" 2>/dev/null \
  | tr -d '\r' | head -c 200000 || true)"
grep -q 'event: task.deleted' <<<"$DELETED" \
  || fail "no task.deleted event reached GET /v1/events: $DELETED"
grep -q "\"id\":$COMMITS_ID" <<<"$DELETED" \
  || fail "task.deleted does not name the deleted task $COMMITS_ID: $DELETED"

echo "== 8. an unknown id is 404"
CODE="$(api_status DELETE "/tasks/999999")"
[[ "$CODE" == "404" ]] || fail "deleting an unknown task answered $CODE, want 404"
CODE="$(api_status DELETE "/chats/999999")"
[[ "$CODE" == "404" ]] || fail "deleting an unknown chat answered $CODE, want 404"

echo "== 9. neither DELETE is an MCP tool"
TOOLS="$(curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  "http://127.0.0.1:$PORT/mcp" | tr -d '\r')"
for forbidden in task_delete chat_delete; do
  if grep -q "\"$forbidden\"" <<<"$TOOLS"; then
    fail "$forbidden is an MCP tool; §13.4 excludes both deletes"
  fi
done

echo "GATE PASS: m15 (archived listings and permanent delete)"
