#!/usr/bin/env bash
# M1 phase gate (T1.9; spec §19 M1): prove via curl alone that vincent can
# register a repo, create a one-step agent task, watch it finish, and show
# the branch and diff. Runs against the committed fakeagent so CI never
# calls a real API; run manually with VINCENT_GATE_AGENT=claude to exercise
# the real CLI.
#
# Requirements: bash, go, git, curl, jq (all present on the GitHub runners,
# incl. Git Bash on Windows).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
BIN="$TMP/bin"
CONFIG_DIR="$TMP/config"
DATA_DIR="$TMP/data"

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

# hostpath converts a bash path for consumption by vincent — Git Bash /tmp
# paths are meaningless to a native Windows binary. -m keeps forward slashes
# so nothing needs YAML/JSON escaping.
hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

# Environment variables are never MSYS-converted, so the overrides must
# carry host paths (the phase-1 env-override knob the gate rides on).
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"

echo "== build vincent + fakeagent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent ./cmd/fakeagent)

echo "== configure (agents.claude.path -> fakeagent)"
AGENT_PATH="$(hostpath "$FAKEAGENT")"
if [[ "${VINCENT_GATE_AGENT:-fake}" == "claude" ]]; then
  AGENT_PATH="" # empty = resolve the real claude from PATH
else
  export FAKEAGENT_EDIT_FILE=README.md # give the diff something to show
fi
cat > "$CONFIG_DIR/config.yaml" <<EOF
agents:
  claude:
    path: "$AGENT_PATH"
EOF

echo "== start daemon"
"$VINCENT" daemon start
PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
TOKEN="$(cat "$DATA_DIR/token")"
BASE="http://127.0.0.1:$PORT/v1"
auth=(-H "Authorization: Bearer $TOKEN")

api() { # api METHOD PATH [JSON_BODY]
  local method="$1" path="$2" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -sS -f -X "$method" "${auth[@]}" -H "Content-Type: application/json" -d "$body" "$BASE$path"
  else
    curl -sS -f -X "$method" "${auth[@]}" "$BASE$path"
  fi
}

echo "== agent availability"
api GET /info | jq -e '.agents[] | select(.name == "claude") | .available == true' >/dev/null \
  || fail "claude agent not available in /v1/info"

echo "== register a repo"
REPO="$TMP/repo"
git init -q -b main "$REPO"
git -C "$REPO" config user.name gate
git -C "$REPO" config user.email gate@example.invalid
git -C "$REPO" config commit.gpgsign false
printf 'gate repo\n' > "$REPO/README.md"
git -C "$REPO" add . && git -C "$REPO" commit -qm init
PROJECT_ID="$(api POST /projects "{\"path\":$(jq -Rn --arg p "$(hostpath "$REPO")" '$p')}" | jq -r .id)"
[[ "$PROJECT_ID" =~ ^[0-9]+$ ]] || fail "project registration returned no id"

echo "== create a one-step agent task"
TASK_ID="$(api POST /tasks "{\"project_id\":$PROJECT_ID,\"title\":\"M1 gate task\",\"description\":\"Append a line to README.md in this worktree.\"}" | jq -r .id)"
[[ "$TASK_ID" =~ ^[0-9]+$ ]] || fail "task creation returned no id"

echo "== watch it finish"
STATE=queued
for _ in $(seq 1 120); do
  STATE="$(api GET "/tasks/$TASK_ID" | jq -r .state)"
  case "$STATE" in
    done) break ;;
    blocked) api GET "/tasks/$TASK_ID" | jq . >&2; fail "task blocked" ;;
  esac
  sleep 1
done
[[ "$STATE" == "done" ]] || fail "task did not finish (state $STATE)"

echo "== assert branch"
BRANCH="$(api GET "/tasks/$TASK_ID" | jq -r .branch_name)"
git -C "$REPO" rev-parse --verify "refs/heads/$BRANCH" >/dev/null || fail "branch $BRANCH missing in repo"

echo "== assert step run + transcript"
RUN="$(api GET "/tasks/$TASK_ID/steps" | jq '.[0]')"
[[ "$(jq -r .state <<<"$RUN")" == "succeeded" ]] || fail "step run not succeeded: $RUN"
RUN_ID="$(jq -r .id <<<"$RUN")"
TRANSCRIPT="$(api GET "/tasks/$TASK_ID/steps/$RUN_ID/transcript")"
grep -q '"type":"result"' <<<"$TRANSCRIPT" || fail "transcript has no result event"
grep -q '"vincent.step_started"' <<<"$TRANSCRIPT" || fail "transcript has no vincent annotation"

echo "== assert diff"
DIFF="$(api GET "/tasks/$TASK_ID/diff")"
grep -q 'README.md' <<<"$DIFF" || fail "diff does not mention README.md:
$DIFF"

# The gc leg rides here because this script already has the two things it
# needs: a registered project and a task with a real worktree that must come
# through untouched (task 005, §10).
echo "== plant an orphaned worktree"
ORPHAN_ID=999999
ORPHAN="$DATA_DIR/worktrees/$ORPHAN_ID"
mkdir -p "$ORPHAN"
printf '0123456789\n' > "$ORPHAN/payload"
LIVE_WORKTREE="$(api GET "/tasks/$TASK_ID" | jq -r .worktree_path)"
[[ -d "$LIVE_WORKTREE" ]] || fail "the task's worktree is not on disk: $LIVE_WORKTREE"

echo "== list orphans"
ORPHANS="$(api GET /maintenance/orphans)"
jq -e --argjson id "$ORPHAN_ID" \
  '[.orphans[] | select(.task_id == $id)] | length == 1' <<<"$ORPHANS" >/dev/null \
  || fail "the planted orphan is not listed:
$ORPHANS"
jq -e --argjson id "$ORPHAN_ID" \
  '.orphans[] | select(.task_id == $id) | .kind == "worktree" and .bytes > 0' <<<"$ORPHANS" >/dev/null \
  || fail "the orphan is listed without a kind or a size:
$ORPHANS"
# The live task's worktree is claimed, so it must never appear.
jq -e --argjson id "$TASK_ID" \
  '[.orphans[] | select(.task_id == $id)] | length == 0' <<<"$ORPHANS" >/dev/null \
  || fail "a live task's worktree was reported as an orphan:
$ORPHANS"
api GET /info | jq -e '.orphans >= 1' >/dev/null || fail "/v1/info does not report the orphan"

echo "== dry run removes nothing"
api POST /maintenance/gc '{"force":true,"dry_run":true}' \
  | jq -e '.dry_run == true and .reclaimed == 0' >/dev/null || fail "dry run reported a removal"
[[ -d "$ORPHAN" ]] || fail "the dry run removed the orphan"

# --force: the planted directory has no repository behind it, so git cannot
# judge it and gc reports dirty_unknown rather than guessing.
echo "== reclaim"
GC="$(api POST /maintenance/gc '{"force":true}')"
jq -e '.reclaimed == 1 and .reclaimed_bytes > 0' <<<"$GC" >/dev/null || fail "gc reclaimed nothing:
$GC"
[[ ! -e "$ORPHAN" ]] || fail "the orphan survived gc"
[[ -d "$LIVE_WORKTREE" ]] || fail "gc removed a live task's worktree: $LIVE_WORKTREE"
git -C "$REPO" rev-parse --verify "refs/heads/$BRANCH" >/dev/null \
  || fail "gc deleted branch $BRANCH — branches are never deleted"

echo "== stop daemon"
"$VINCENT" daemon stop

echo "M1 GATE PASS (task $TASK_ID, branch $BRANCH)"
