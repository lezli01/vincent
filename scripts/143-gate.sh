#!/usr/bin/env bash
# Task 143.5 gate (§5.6, §6, §10, §12.3, §12.4; task 143 decisions 1–16):
# prove via curl alone that closing an issue archives the settled tasks it
# started, by every route the worker reads, and that what it must not
# archive it leaves alone.
#
#   1. close by API: a done main task of a closed issue reaches `archived`;
#      its `task.state_changed → archived` event carries
#      `archived_by: "issue_closed"` (decision 9); its empty branch is gone
#      locally and still listed on `origin`, though
#      delete_remote_branch_on_archive is on — the unattended archive never
#      runs the remote leg (decision 6)
#   2. close while running: a task held at a manual gate is not archived by
#      the close; approved, it reaches `done` and then `archived`
#   3. a dirty worktree: an untracked file in a done task's worktree makes
#      the archive refuse; the task stays `done`, `vincent doctor --json`
#      lists it under `issue_archive.held` with `dirty` among its reasons
#      and still exits 0 (decision 16), and the file is still there
#   4. a sync close: an issue imported from cmd/fakegh is closed on
#      "GitHub"; after a sync it reads `closed` and its done task is
#      archived — the `issue.updated` route through store.IssueStateMove —
#      and the fake was asked for no write
#   5. reopen before settle: an issue closed and reopened while its task
#      waits at a manual gate does not archive the task once it is done
#   6. the key off: with `archive_tasks_on_issue_close: false` a closed
#      issue's done task stays `done`, and doctor's `issue_archive` is
#      disabled and empty
#
# Scenarios 3 and 5 assert that something did *not* happen. They do it with
# an ordering barrier rather than a sleep: after the asserted event the gate
# closes a second issue of the same project whose done task must archive,
# and waits for that. The worker walks the event table in order from one
# cursor, so once the barrier is archived every earlier event has been
# processed, and "still done" is proven, not guessed. Scenario 6 cannot use
# one — with the key off the worker reads nothing, a barrier included — so
# it is the gate's one bounded-window negative assertion.
#
# Task-numbered rather than `mN` because this is not a §19 milestone, like
# 130-gate.sh and 134-gate.sh. Each scenario runs its own daemon over its
# own config, data and repository, so VINCENT_GATE_SCENARIO=n runs one
# alone.
#
# No agent CLI is involved. Every step is a command step whose whole body is
# `exit 0`, or a manual gate; the `run:` bodies keep to what `/bin/sh` and
# `pwsh` both accept. The GitHub half is cmd/fakegh installed as `gh` on the
# daemon's PATH, its corpus in a file the gate edits, and every call it was
# asked to make in FAKEGH_ARGV_FILE — 130-gate.sh scenario 4's pattern.
#
# The gate observes the rules CLAUDE.md records: no pipe into `grep -q` or
# any other early-exiting consumer (capture first, match a here-string),
# `| tr -d '\r'` on multi-line captures, waits that are bounded polls on API
# state, and committed executable.
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
gate_build "$BIN" vincent
# cmd/fakegh is built *as* `gh`, so the daemon's PATH lookup finds it exactly
# the way it would find the real CLI.
gate_build_as "$BIN/gh$EXE" fakegh
PATH="$BIN:$PATH"
export PATH

# With these unset, a `gh` that failed to resolve is a failure of this gate —
# never a request to api.github.com.
unset GITHUB_TOKEN GH_TOKEN
export FAKEGH_SCENARIO=success
REPO_SLUG=gate/archive
export FAKEGH_REPO="$REPO_SLUG"

CONFIG_DIR="" DATA_DIR="" REPO="" CORPUS="" GH_ARGV=""
scenario_dirs() { # scenario_dirs NAME
  CONFIG_DIR="$TMP/$1/config"
  DATA_DIR="$TMP/$1/data"
  REPO="$TMP/$1/repo"
  mkdir -p "$CONFIG_DIR/workflows" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
  CORPUS="$TMP/$1/corpus.json"
  GH_ARGV="$TMP/$1/gh-argv.txt"
  printf '[]\n' > "$CORPUS"
  : > "$TMP/$1/gh-scenario.txt"
  : > "$GH_ARGV"
  export FAKEGH_ISSUES_FILE FAKEGH_SCENARIO_FILE FAKEGH_ARGV_FILE
  FAKEGH_ISSUES_FILE="$(hostpath "$CORPUS")"
  FAKEGH_SCENARIO_FILE="$(hostpath "$TMP/$1/gh-scenario.txt")"
  FAKEGH_ARGV_FILE="$(hostpath "$GH_ARGV")"
  write_workflows
}

PORT="" TOKEN="" BASE=""
daemon_up() {
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
}
daemon_down() { "$VINCENT" daemon stop --force >/dev/null 2>&1 || true; }

api() { # api METHOD PATH [JSON_BODY] -> body; a non-2xx fails
  local method="$1" path="$2" body="${3:-}" out status
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  [[ "$status" == 2* ]] || fail "$method $path -> HTTP $status: $out"
  printf '%s' "$out"
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

# The gate's two workflows: one command step, and one held at a manual gate
# so the gate — not a timer — decides when the task settles.
write_workflows() {
  printf '%s' 'name: quick
steps:
  - {id: work, type: command, max_retries: 0, run: exit 0}
' > "$CONFIG_DIR/workflows/quick.yaml"
  printf '%s' 'name: gated
steps:
  - {id: work, type: command, max_retries: 0, run: exit 0}
  - {id: gate, type: manual, instructions: approve to finish}
' > "$CONFIG_DIR/workflows/gated.yaml"
}

write_config() { printf '%s' "$1" > "$CONFIG_DIR/config.yaml"; }

register_project() { # register_project PATH -> id
  api POST /projects "$(jq -cn --arg p "$(hostpath "$1")" '{path: $p}')" | jq -r .id
}

create_issue() { # create_issue PROJECT TITLE -> id
  api POST /issues "$(jq -cn --argjson p "$1" --arg t "$2" \
    '{project_id: $p, title: $t, body: ""}')" | jq -r .id
}

create_task() { # create_task PROJECT ISSUE WORKFLOW TITLE -> id
  api POST /tasks "$(jq -cn --argjson p "$1" --argjson i "$2" --arg w "$3" --arg t "$4" \
    '{project_id: $p, issue_id: $i, workflow: $w, title: $t}')" | jq -r .id
}

close_issue() { api POST "/issues/$1/close" '{"reason": "completed"}' >/dev/null; }

task() { api GET "/tasks/$1"; }
tf() { task "$1" | jq -r "$2"; } # tf ID FILTER
dump() { task "$1" | jq . >&2; }

expect_eq() { # expect_eq GOT WANT WHAT
  [[ "$1" == "$2" ]] || fail "$3: got '$1', want '$2'"
}

wait_state() { # wait_state ID STATE SECS; an unwanted block or abort fails at once
  local id="$1" want="$2" secs="$3" body state=""
  for _ in $(seq 1 "$(gate_ticks "$secs")"); do
    body="$(task "$id")"
    state="$(jq -r .state <<<"$body")"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == aborted || "$state" == blocked ]]; then
      jq . <<<"$body" >&2
      fail "task $id reached $state ($(jq -r '.block_reason // ""' <<<"$body")) while waiting for $want"
    fi
    sleep "$GATE_POLL"
  done
  dump "$id"
  fail "task $id never reached $want (last: $state)"
}

# done_task PROJECT TITLE sets DI and DT to an issue and its one quick main
# task, once the task is done. Globals, not output: a failure inside a
# command substitution would end only the subshell.
DI="" DT=""
done_task() {
  DI="$(create_issue "$1" "$2")"
  DT="$(create_task "$1" "$DI" quick "$2")"
  wait_state "$DT" done 60
}

# barrier ISSUE TASK: close ISSUE and wait for TASK to be archived. The
# worker reads events in order from one cursor, so every event before this
# close has been processed once the barrier task is archived.
barrier() {
  close_issue "$1"
  wait_state "$2" archived 60
}

# archive_event TASK -> the payload of TASK's transition into `archived`, as
# the durable event stream replays it. The stream never ends by itself, so
# it is read from 1 for --max-time; event 1 is never a task's.
archive_event() {
  local raw
  raw="$(curl -sS --max-time 3 -N -H "Authorization: Bearer $TOKEN" \
    -H "Last-Event-ID: 1" "$BASE/events?types=task.state_changed" 2>/dev/null || true)"
  tr -d '\r' <<<"$raw" | sed -n 's/^data: //p' \
    | jq -c --argjson t "$1" 'select(.task_id == $t and .payload.to == "archived") | .payload' \
    | tr -d '\r'
}

# doctor_json -> `vincent doctor --json`, which must exit 0.
doctor_json() {
  local out rc=0
  out="$("$VINCENT" doctor --json)" || rc=$?
  [[ "$rc" == 0 ]] || fail "vincent doctor exited $rc: $(jq -c .problems <<<"$out" 2>/dev/null || printf '%s' "$out")"
  printf '%s' "$out"
}

branch_exists() { git -C "$REPO" rev-parse --verify --quiet "refs/heads/$1" >/dev/null; }

run_scenario() { [[ -z "$ONLY" || "$ONLY" == "$1" ]]; }

# --------------------------------------------------------------------------
# Scenario 1: closed through the API.
# --------------------------------------------------------------------------
if run_scenario 1; then
  echo "=== scenario 1: closed through the API"
  scenario_dirs s1
  git init -q --bare "$TMP/s1/origin.git"
  make_repo "$REPO" "$(hostpath "$TMP/s1/origin.git")"
  git -C "$REPO" push -q origin main
  write_config 'delete_empty_branch_on_archive: true
delete_remote_branch_on_archive: true
'
  daemon_up
  P="$(register_project "$REPO")"
  done_task "$P" "closed by API"; I="$DI" T="$DT"
  BR="$(tf "$T" .branch_name)"
  [[ -n "$BR" && "$BR" != null ]] || fail "task $T has no branch"
  branch_exists "$BR" || fail "branch $BR is not in the repository"
  git -C "$REPO" push -q origin "$BR"

  close_issue "$I"
  wait_state "$T" archived 60
  PAYLOAD="$(archive_event "$T")"
  [[ -n "$PAYLOAD" ]] || fail "no task.state_changed -> archived event for task $T"
  expect_eq "$(jq -r .archived_by <<<"$PAYLOAD")" issue_closed "the archive event's archived_by"
  expect_eq "$(jq -r .issue_id <<<"$PAYLOAD")" "$I" "the archive event's issue_id"
  if branch_exists "$BR"; then fail "the empty branch $BR survived the archive"; fi
  REMOTE="$(git -C "$REPO" ls-remote origin "refs/heads/$BR" | tr -d '\r')"
  [[ -n "$REMOTE" ]] || fail "the unattended archive deleted $BR on origin"
  daemon_down
  echo "=== scenario 1 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 2: closed while the task waits at a manual gate.
# --------------------------------------------------------------------------
if run_scenario 2; then
  echo "=== scenario 2: closed while the task is unsettled"
  scenario_dirs s2
  make_repo "$REPO"
  daemon_up
  P="$(register_project "$REPO")"
  done_task "$P" "barrier"; BI="$DI" BT="$DT"
  I="$(create_issue "$P" "closed while running")"
  T="$(create_task "$P" "$I" gated "held at a gate")"
  wait_state "$T" awaiting_gate 60
  close_issue "$I"
  barrier "$BI" "$BT"
  expect_eq "$(tf "$T" .state)" awaiting_gate "the gated task after its issue closed"
  api POST "/tasks/$T/approve" '{}' >/dev/null
  wait_state "$T" archived 60
  # It got there through done: archive is only open to a settled task.
  PAYLOAD="$(archive_event "$T")"
  expect_eq "$(jq -r .from <<<"$PAYLOAD")" done "the archive event's from"
  expect_eq "$(jq -r .archived_by <<<"$PAYLOAD")" issue_closed "the archive event's archived_by"
  daemon_down
  echo "=== scenario 2 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 3: a dirty worktree is never forced.
# --------------------------------------------------------------------------
if run_scenario 3; then
  echo "=== scenario 3: a dirty worktree"
  scenario_dirs s3
  make_repo "$REPO"
  daemon_up
  P="$(register_project "$REPO")"
  done_task "$P" "barrier"; BI="$DI" BT="$DT"
  done_task "$P" "dirty"; I="$DI" T="$DT"
  WT="$(tf "$T" '.worktree_path // ""')"
  [[ -n "$WT" ]] || fail "done task $T has no worktree_path"
  WT="$(_gate_shellpath "$WT")"
  [[ -d "$WT" ]] || fail "task $T's worktree $WT does not exist"
  printf 'uncommitted\n' > "$WT/untracked.txt"

  close_issue "$I"
  barrier "$BI" "$BT"
  expect_eq "$(tf "$T" .state)" done "the dirty task after its issue closed"
  DOC="$(doctor_json)"
  expect_eq "$(jq -r .issue_archive.enabled <<<"$DOC")" true "doctor's issue_archive.enabled"
  HELD="$(jq -c --argjson t "$T" '.issue_archive.held[] | select(.task_id == $t)' <<<"$DOC")"
  [[ -n "$HELD" ]] || fail "doctor does not hold task $T: $(jq -c .issue_archive <<<"$DOC")"
  REASONS="$(jq -r '.reasons[]' <<<"$HELD" | tr -d '\r')"
  grep -qx dirty <<<"$REASONS" || fail "task $T is held for $REASONS, want dirty among them"
  expect_eq "$(jq -r '.problems | length' <<<"$DOC")" 0 "doctor's problems"
  [[ -f "$WT/untracked.txt" ]] || fail "the untracked file in $WT is gone"
  daemon_down
  echo "=== scenario 3 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 4: closed on GitHub, read in by a sync.
# --------------------------------------------------------------------------
if run_scenario 4; then
  echo "=== scenario 4: closed on GitHub"
  scenario_dirs s4
  make_repo "$REPO" "https://github.com/$REPO_SLUG.git"
  jq -n --arg r "$REPO_SLUG" '[{
    id: 3000001, node_id: "I_gate_1", number: 1,
    title: "Gate issue 1", body: "", state: "open", state_reason: null, closed_at: null,
    created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T01:00:00Z",
    labels: [], assignees: [], user: {login: "octocat"}, milestone: null, comments: 0,
    url: "https://api.github.com/repos/\($r)/issues/1",
    repository_url: "https://api.github.com/repos/\($r)",
    html_url: "https://github.com/\($r)/issues/1"
  }]' > "$CORPUS"
  write_config 'github:
  enabled: true
  poll_interval: 1s
'
  daemon_up
  P="$(register_project "$REPO")"
  I=""
  for _ in $(seq 1 "$(gate_ticks 30)"); do
    I="$(api GET "/issues?project_id=$P&source=github" | jq -r '.[] | select(.source.number == 1) | .id')"
    [[ -n "$I" ]] && break
    sleep "$GATE_POLL"
  done
  [[ -n "$I" ]] || fail "GitHub issue #1 was never imported"
  T="$(create_task "$P" "$I" quick "from GitHub")"
  wait_state "$T" done 60

  : > "$GH_ARGV"
  NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  jq --arg now "$NOW" 'map(.state = "closed" | .state_reason = "completed"
    | .closed_at = $now | .updated_at = $now)' "$CORPUS" > "$CORPUS.edit"
  # The rename retries: on Windows it fails while a fakegh child holds the
  # corpus open for reading.
  for _ in $(seq 1 "$(gate_ticks 10)"); do
    mv -f "$CORPUS.edit" "$CORPUS" 2>/dev/null && break
    sleep "$GATE_POLL"
  done
  [[ ! -e "$CORPUS.edit" ]] || fail "the corpus could not be rewritten"
  api POST "/projects/$P/issues/sync" >/dev/null

  STATE=""
  for _ in $(seq 1 "$(gate_ticks 30)"); do
    STATE="$(api GET "/issues/$I" | jq -r .state)"
    [[ "$STATE" == closed ]] && break
    sleep "$GATE_POLL"
  done
  expect_eq "$STATE" closed "the imported issue after the sync"
  wait_state "$T" archived 60
  expect_eq "$(archive_event "$T" | jq -r .archived_by)" issue_closed "the archive event's archived_by"
  CALLS="$(tr -d '\r' < "$GH_ARGV")"
  WRITES="$(grep -cE -- '-X (PATCH|POST|PUT|DELETE)' <<<"$CALLS" || true)"
  expect_eq "$WRITES" 0 "writes the fake was asked for after the GitHub close ($CALLS)"
  daemon_down
  echo "=== scenario 4 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 5: reopened before the task settled.
# --------------------------------------------------------------------------
if run_scenario 5; then
  echo "=== scenario 5: reopened before the task settled"
  scenario_dirs s5
  make_repo "$REPO"
  daemon_up
  P="$(register_project "$REPO")"
  done_task "$P" "barrier"; BI="$DI" BT="$DT"
  I="$(create_issue "$P" "reopened")"
  T="$(create_task "$P" "$I" gated "held at a gate")"
  wait_state "$T" awaiting_gate 60
  close_issue "$I"
  api POST "/issues/$I/reopen" >/dev/null
  expect_eq "$(api GET "/issues/$I" | jq -r .state)" open "the issue after its reopen"
  api POST "/tasks/$T/approve" '{}' >/dev/null
  wait_state "$T" done 60
  barrier "$BI" "$BT"
  expect_eq "$(tf "$T" .state)" done "the task of the reopened issue"
  daemon_down
  echo "=== scenario 5 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 6: the key off.
# --------------------------------------------------------------------------
if run_scenario 6; then
  echo "=== scenario 6: archive_tasks_on_issue_close off"
  scenario_dirs s6
  make_repo "$REPO"
  write_config 'archive_tasks_on_issue_close: false
'
  daemon_up
  P="$(register_project "$REPO")"
  done_task "$P" "key off"; I="$DI" T="$DT"
  close_issue "$I"
  # The one fixed window in this gate: with the key off the worker reads
  # nothing, so no barrier can order after the close (see the header). Ten
  # seconds is many worker pages; scenario 1 archives in well under one.
  for _ in $(seq 1 "$(gate_ticks 10)"); do
    expect_eq "$(tf "$T" .state)" done "the task of a closed issue with the key off"
    sleep "$GATE_POLL"
  done
  DOC="$(doctor_json)"
  expect_eq "$(jq -c '.issue_archive | [.enabled, (.held | length), (.chat_dirs | length)]' <<<"$DOC")" \
    '[false,0,0]' "doctor's issue_archive with the key off"
  daemon_down
  echo "=== scenario 6 PASS"
fi

echo
echo "143 GATE PASSED"
