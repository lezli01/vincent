#!/usr/bin/env bash
# Task 134.17 gate (§5.6, §6, §11, §12.4, §13.2; task 134 decisions 7–13,
# 15): prove via curl alone that an issue's main worktree has one occupant at
# a time, that successive main tasks share its directory and branch, and that
# a side task is merged back into it in both modes.
#
#   1. single occupant: a main task created while another holds the issue's
#      main worktree is told so on its 201 and stays queued while the
#      occupant runs, is blocked, and waits at a manual gate; then it is
#      admitted into the same directory and branch, cut from the tip its
#      predecessor handed over, and the two tasks' /commits split at that
#      tip (the predecessor's end_sha)
#   2. one walk: two main tasks created back to back never run at once, and
#      the later one starts no earlier than the earlier one finished
#   3. a crash: the daemon is killed while the occupant runs; after the
#      restart the occupant runs again and the queued main task still waits
#      for it, then takes its directory
#   4. manual merge-back, clean: a side task forks from the main tip into a
#      directory of its own; its merge-back waits for the occupant, then
#      merges into the main worktree with a `Merge task …` commit
#   5. manual merge-back, conflict: the merge-back blocks merge_conflict in
#      the main worktree; resolved by hand and retried, the resolution lands
#   6. agentic merge-back: the same conflict with `on_conflict: agent` is
#      resolved by the resolver (the fake agent) with no human action
#   7. FIFO: two side tasks' merge-backs merge in the order the side tasks
#      finished
#   8. a crash with a merge in progress: the daemon is killed while a
#      merge-back is blocked merge_conflict with a main task queued behind
#      it; after the restart both are where they were, and a hand resolution
#      plus retry merges exactly once before the queued task is admitted
#   9. lanes, over scenario 4's run: `in_progress` while the occupant, the
#      side task and the merge-back are live, `hand_off` once they are done
#
# Scenario 8 kills the daemon while a conflicted merge is *left* in the main
# worktree, the shape m6 scenario 6 uses for fan_out's join. A daemon killed
# while `git merge` itself is running is not driven from here: that merge
# takes well under a second, nothing holds it open, and a git hook that did
# would leave an orphaned git process racing the restart on all three
# platforms. The abort-and-re-merge path for that case is proven by
# TestMergeBackInterruptedMidMergeAbortsAndReMerges
# (internal/taskrun/mergeback_test.go) — task 134.17 decision 1.
#
# Task-numbered rather than `mN` because this is not a §19 milestone, like
# 123-gate.sh, 125-gate.sh and 130-gate.sh. Each scenario runs its own daemon
# over its own config, data and repository, so VINCENT_GATE_SCENARIO=n runs
# one alone.
#
# Every step is a command step except scenario 6's resolver, which is
# cmd/fakeagent configured as `claude` and told by FAKEAGENT_WRITE_FILE what
# to write over the conflicted file. The `run:` bodies keep to what
# `/bin/sh` and `pwsh` both accept — `exit N` as a whole body, `sleep N` and
# `git …` — so a file is written with `git config -f`. Concurrency and order
# are asserted from the API and from git, never from inside a step.
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
FAKEAGENT="$BIN/fakeagent$EXE"

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

echo "== build vincent and the fake agent"
gate_build "$BIN" vincent fakeagent

CONFIG_DIR="" DATA_DIR="" REPO=""
scenario_dirs() { # scenario_dirs NAME
  CONFIG_DIR="$TMP/$1/config"
  DATA_DIR="$TMP/$1/data"
  REPO="$TMP/$1/repo"
  mkdir -p "$CONFIG_DIR/workflows" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
  make_repo "$REPO"
}

PORT="" TOKEN="" BASE=""
daemon_up() {
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
}
daemon_down() { "$VINCENT" daemon stop --force >/dev/null 2>&1 || true; }

# daemon_kill is a crash, not a stop: no shutdown path runs.
daemon_kill() {
  local pid
  pid="$(jq -r .pid "$DATA_DIR/daemon.json")"
  if [[ "${OS:-}" == "Windows_NT" ]]; then
    # Git Bash kill -9 can't reliably kill a native process; // stops MSYS
    # from mangling the flags into paths (phase 2 decision).
    taskkill //F //PID "$pid" >/dev/null
  else
    kill -9 "$pid"
  fi
  for _ in $(seq 1 "$(gate_ticks 10)"); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep "$GATE_POLL"
  done
}

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

make_repo() { # make_repo PATH
  git init -q -b main "$1"
  git -C "$1" config user.name gate
  git -C "$1" config user.email gate@example.invalid
  git -C "$1" config commit.gpgsign false
  printf 'gate repo\n' > "$1/README.md"
  git -C "$1" add . && git -C "$1" commit -qm init
}

write_workflow() { # write_workflow NAME YAML
  printf '%s' "$2" > "$CONFIG_DIR/workflows/$1.yaml"
}

# write_hold NAME SECS MAIN_BODY: an occupant that holds the main worktree
# for SECS, then moves the main line with MAIN_BODY.
write_hold() {
  write_workflow "$1" "name: $1
steps:
  - {id: hold, type: command, max_retries: 0, run: 'sleep $2'}
  - {id: main, type: command, max_retries: 0, run: '$3'}
"
}

# The side tasks' workflows: one file per task, or the line that conflicts
# with write_hold's MAIN_SHARED.
MAIN_SHARED='git config -f shared.txt gate.side main && git add -A && git commit -qm main-shared'
write_sides() {
  write_workflow side-file "name: side-file
steps:
  - {id: work, type: command, max_retries: 0, run: 'git config -f side-{{.Task.ID}}.txt gate.side s && git add -A && git commit -qm side-{{.Task.ID}}'}
"
  write_workflow side-shared "name: side-shared
steps:
  - {id: work, type: command, max_retries: 0, run: 'git config -f shared.txt gate.side side && git add -A && git commit -qm side-shared'}
"
  write_workflow commit-b "name: commit-b
steps:
  - {id: work, type: command, max_retries: 0, run: 'git commit --allow-empty -qm b-work'}
"
}

PROJECT="" ISSUE=""
project_and_issue() {
  PROJECT="$(api POST /projects "$(jq -cn --arg p "$(hostpath "$REPO")" '{path: $p}')" | jq -r .id)"
  ISSUE="$(api POST /issues "$(jq -cn --argjson p "$PROJECT" \
    '{project_id: $p, title: "gate issue", body: ""}')" | jq -r .id)"
}

# create_main WORKFLOW TITLE -> the 201 body of a main task on ISSUE.
create_main() {
  api POST /tasks "$(jq -cn --argjson p "$PROJECT" --argjson i "$ISSUE" --arg w "$1" --arg t "$2" \
    '{project_id: $p, issue_id: $i, workflow: $w, title: $t}')"
}

# create_side WORKFLOW TITLE ON_CONFLICT [AGENT] -> the 201 body of a side task.
create_side() {
  api POST /tasks "$(jq -cn --argjson p "$PROJECT" --argjson i "$ISSUE" --arg w "$1" --arg t "$2" \
    --arg c "$3" --arg a "${4:-}" \
    '{project_id: $p, issue_id: $i, workflow: $w, title: $t, merge_back: {on_conflict: $c}}
      + (if $a == "" then {} else {agent: $a} end)')"
}

task() { api GET "/tasks/$1"; }
tf() { task "$1" | jq -r "$2"; } # tf ID FILTER
issue_f() { api GET "/issues/$ISSUE" | jq -r "$1"; }
commit_subjects() { api GET "/tasks/$1/commits" | jq -r '.[].subject' | tr -d '\r'; }
branch_subjects() { git -C "$REPO" log --first-parent --format=%s "$1" | tr -d '\r'; }

dump() { task "$1" | jq . >&2; }

wait_state() { # wait_state ID STATE SECS; an unwanted block or abort fails at once
  local id="$1" want="$2" secs="$3" body state=""
  for _ in $(seq 1 "$(gate_ticks "$secs")"); do
    body="$(task "$id")"
    state="$(jq -r .state <<<"$body")"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == aborted || ("$state" == blocked && "$want" != blocked) ]]; then
      jq . <<<"$body" >&2
      fail "task $id reached $state ($(jq -r '.block_reason // ""' <<<"$body")) while waiting for $want"
    fi
    sleep "$GATE_POLL"
  done
  dump "$id"
  fail "task $id never reached $want (last: $state)"
}

# worktree_of ID -> the task's worktree_path, once its admission has made
# one: a task is `running` a moment before its directory exists.
worktree_of() {
  local wt=""
  for _ in $(seq 1 "$(gate_ticks 30)"); do
    wt="$(tf "$1" '.worktree_path // ""')"
    if [[ -n "$wt" ]]; then printf '%s' "$wt"; return 0; fi
    sleep "$GATE_POLL"
  done
  dump "$1"
  fail "task $1 never had a worktree"
}

# hold_queued WAITER OCCUPANT UNTIL SECS polls until OCCUPANT is UNTIL, and
# fails if WAITER is seen anything but queued before OCCUPANT is done. The
# waiter is read first: seeing it admitted while the occupant, read after
# it, is still not done is a violation however the two reads interleave.
hold_queued() {
  local waiter="$1" occ="$2" until="$3" secs="$4" w o=""
  for _ in $(seq 1 "$(gate_ticks "$secs")"); do
    w="$(tf "$waiter" .state)"
    o="$(tf "$occ" .state)"
    if [[ "$w" != queued && "$o" != done ]]; then
      dump "$waiter"
      fail "task $waiter is $w while task $occ is $o, want it queued behind $occ"
    fi
    [[ "$o" == "$until" ]] && return 0
    sleep "$GATE_POLL"
  done
  dump "$occ"
  fail "task $occ never reached $until (last: $o)"
}

# merge_back_of SIDE -> the id of SIDE's merge-back, once it exists. It has
# no source id on the DTO: it is found by its reserved workflow name and its
# title, which is also its merge commit's message.
merge_back_of() {
  local want="Merge task $1 into issue #$ISSUE" id=""
  for _ in $(seq 1 "$(gate_ticks 30)"); do
    id="$(api GET "/tasks?project_id=$PROJECT" | jq -r --arg t "$want" \
      '[.[] | select(.workflow == "__merge_back" and .title == $t)] | .[0].id // empty')"
    if [[ -n "$id" ]]; then printf '%s' "$id"; return 0; fi
    sleep "$GATE_POLL"
  done
  fail "no merge-back titled '$want' appeared"
}

# Ordering comes from the durable event stream, not from the task DTO: its
# started_at and finished_at are whole seconds, so any overlap inside one
# second would compare equal and pass. Event ids are assigned in commit
# order by the daemon's single writer, so they order two transitions however
# close together they landed.
#
# state_events -> "EVENT_ID TASK_ID FROM TO" lines, oldest first. The stream
# never ends by itself and replays nothing unasked (`Last-Event-ID: 0` is no
# cursor), so it is read from 1 for --max-time; event 1 is the project's,
# never a task's state change.
state_events() {
  local raw
  raw="$(curl -sS --max-time 3 -N -H "Authorization: Bearer $TOKEN" \
    -H "Last-Event-ID: 1" "$BASE/events?types=task.state_changed" 2>/dev/null || true)"
  tr -d '\r' <<<"$raw" | sed -n 's/^data: //p' \
    | jq -r '"\(.id) \(.task_id) \(.payload.from) \(.payload.to)"' | tr -d '\r'
}

# entered EVENTS TASK STATE -> the id of TASK's first transition into STATE.
entered() {
  local id
  id="$(awk -v t="$2" -v s="$3" '$2 == t && $4 == s { print $1; exit }' <<<"$1")"
  [[ -n "$id" ]] || fail "task $2 never entered $3 on the event stream: $1"
  printf '%s' "$id"
}

# ran_apart EARLY LATE: EARLY reached done before LATE was first admitted.
ran_apart() {
  local ev d r
  ev="$(state_events)"
  d="$(entered "$ev" "$1" done)" r="$(entered "$ev" "$2" running)"
  (( d < r )) || fail "task $2 was admitted (event $r) before task $1 was done (event $d)"
}

expect_eq() { # expect_eq GOT WANT WHAT
  [[ "$1" == "$2" ]] || fail "$3: got '$1', want '$2'"
}

# conflicted WT: the worktree holds an unmerged path. Both sides *create*
# shared.txt, so git reports add/add (AA); any unmerged status will do.
conflicted() {
  local porcelain
  porcelain="$(git -C "$1" status --porcelain | tr -d '\r')"
  grep -qE '^(DD|AU|UD|UA|DU|AA|UU)' <<<"$porcelain"
}

resolve_by_hand() { # resolve_by_hand WT VALUE
  printf '[gate]\n\tside = %s\n' "$2" > "$1/shared.txt"
  git -C "$1" add shared.txt
}

shared_on() { # shared_on BRANCH -> gate.side as the branch's shared.txt says
  git -C "$REPO" show "$1:shared.txt" > "$TMP/shared.out"
  git config -f "$TMP/shared.out" gate.side | tr -d '\r'
}

merge_count() { # merge_count BRANCH -> how many `Merge task …` commits it has
  local subjects
  subjects="$(branch_subjects "$1")"
  grep -c '^Merge task ' <<<"$subjects" || true
}

branch_has() { [[ -n "$(git -C "$REPO" ls-tree --name-only "$1" "$2")" ]]; }

run_scenario() { [[ -z "$ONLY" || "$ONLY" == "$1" ]]; }

# --------------------------------------------------------------------------
# Scenario 1: one occupant, held through running, blocked and awaiting_gate.
# --------------------------------------------------------------------------
if run_scenario 1; then
  echo "=== scenario 1: a single occupant, then the hand-over"
  scenario_dirs s1
  write_sides
  write_workflow occupant-gated "$(cat <<'YAML'
name: occupant-gated
steps:
  - {id: settle, type: command, max_retries: 0, run: 'sleep 3'}
  - {id: work, type: command, max_retries: 0, run: 'git commit --allow-empty -qm a-work'}
  - {id: trip, type: command, max_retries: 0, run: 'exit 1'}
  - {id: gate, type: manual, instructions: 'approve to finish'}
  - {id: tail, type: command, max_retries: 0, run: 'sleep 1'}
YAML
)"
  daemon_up
  project_and_issue
  A="$(create_main occupant-gated "occupant A" | jq -r .id)"
  wait_state "$A" running 30
  # Read while A holds it: a predecessor's worktree_path is cleared when it
  # hands the directory on.
  WT="$(worktree_of "$A")"

  CREATED="$(create_main commit-b "waiter B")"
  B="$(jq -r .id <<<"$CREATED")"
  expect_eq "$(jq -r '.main_worktree_occupant_task_id // ""' <<<"$CREATED")" "$A" "B's 201 occupant hint"
  expect_eq "$(jq -r .issue_worktree <<<"$CREATED")" main "B's role"
  expect_eq "$(issue_f '.main_worktree.occupant_task_id')" "$A" "the issue's occupant while A runs"

  hold_queued "$B" "$A" blocked 60
  expect_eq "$(issue_f '.main_worktree.occupant_task_id')" "$A" "the issue's occupant while A is blocked"
  sleep 2 # a deliberate quiet spell: nothing admits B while A is blocked
  expect_eq "$(tf "$B" .state)" queued "B while A is blocked"
  api POST "/tasks/$A/skip" '{}' >/dev/null

  hold_queued "$B" "$A" awaiting_gate 60
  expect_eq "$(issue_f '.main_worktree.occupant_task_id')" "$A" "the issue's occupant while A awaits its gate"
  sleep 2 # likewise, at the gate
  expect_eq "$(tf "$B" .state)" queued "B while A awaits its gate"
  api POST "/tasks/$A/approve" '{}' >/dev/null

  hold_queued "$B" "$A" done 60
  wait_state "$B" done 60

  AT="$(task "$A")" BT="$(task "$B")"
  expect_eq "$(jq -r .worktree_path <<<"$BT")" "$WT" "B's worktree"
  BR="$(jq -r .branch_name <<<"$AT")"
  expect_eq "$(jq -r .branch_name <<<"$BT")" "$BR" "B's branch"
  # A's last commit is the tip it settled on and handed over: B is cut from
  # it, B's own commit sits on it, and /commits splits the branch there.
  A_TIP="$(api GET "/tasks/$A/commits" | jq -r '.[-1].sha')"
  expect_eq "$(jq -r .base_sha <<<"$BT")" "$A_TIP" "B's base_sha"
  expect_eq "$(git -C "$REPO" rev-parse "$BR~1" | tr -d '\r')" "$A_TIP" "the parent of B's commit"
  expect_eq "$(commit_subjects "$A")" a-work "A's commits after B committed"
  expect_eq "$(commit_subjects "$B")" b-work "B's commits"
  daemon_down
  echo "=== scenario 1 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 2: two main tasks queued in one go are admitted one at a time.
# The event order makes it a proof rather than a sample: a poll can miss a
# short overlap, but the later admission cannot precede the earlier finish.
# --------------------------------------------------------------------------
if run_scenario 2; then
  echo "=== scenario 2: two main tasks, one walk"
  scenario_dirs s2
  write_hold walk 2 'git commit --allow-empty -qm walk-{{.Task.ID}}'
  daemon_up
  project_and_issue
  T1="$(create_main walk "first" | jq -r .id)"
  T2="$(create_main walk "second" | jq -r .id)"

  # Each task's directory is read while it runs: a predecessor's
  # worktree_path is cleared when it hands the directory on. Both tasks
  # come from one list response, which is one read of the store: two
  # GETs would let T1 finish and T2 be admitted between them, and a stale
  # `running` beside a fresh one is not an overlap. Unlike hold_queued's
  # waiter and occupant, these two are symmetric, so no read order is safe.
  WT1="" WT2=""
  for _ in $(seq 1 "$(gate_ticks 60)"); do
    BOTH="$(api GET "/tasks?project_id=$PROJECT")"
    J1="$(jq -c --argjson id "$T1" '.[] | select(.id == $id)' <<<"$BOTH")"
    J2="$(jq -c --argjson id "$T2" '.[] | select(.id == $id)' <<<"$BOTH")"
    S1="$(jq -r .state <<<"$J1")" S2="$(jq -r .state <<<"$J2")"
    [[ "$S1" == running && "$S2" == running ]] && fail "tasks $T1 and $T2 ran at once"
    [[ "$S1" == running && -z "$WT1" ]] && WT1="$(jq -r '.worktree_path // ""' <<<"$J1")"
    [[ "$S2" == running && -z "$WT2" ]] && WT2="$(jq -r '.worktree_path // ""' <<<"$J2")"
    [[ "$S1" == done && "$S2" == done ]] && break
    sleep "$GATE_POLL"
  done
  [[ "$S1" == done && "$S2" == done ]] || fail "the two main tasks never both finished ($S1, $S2)"

  EV="$(state_events)"
  if (( $(entered "$EV" "$T1" running) < $(entered "$EV" "$T2" running) )); then
    ran_apart "$T1" "$T2"
  else
    ran_apart "$T2" "$T1"
  fi
  [[ -n "$WT1" ]] || fail "task $T1 was never seen running with a worktree"
  expect_eq "$WT2" "$WT1" "the two tasks' worktree"
  daemon_down
  echo "=== scenario 2 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 3: a crash while the occupant runs does not free its directory.
# --------------------------------------------------------------------------
if run_scenario 3; then
  echo "=== scenario 3: a crash under the occupant"
  scenario_dirs s3
  write_sides
  write_hold held 6 'git commit --allow-empty -qm held'
  daemon_up
  project_and_issue
  A="$(create_main held "occupant A" | jq -r .id)"
  wait_state "$A" running 30
  WT="$(worktree_of "$A")"
  B="$(create_main commit-b "waiter B" | jq -r .id)"
  expect_eq "$(tf "$B" .state)" queued "B before the crash"

  echo "== hard-kill the daemon (pid from daemon.json)"
  daemon_kill
  daemon_up

  hold_queued "$B" "$A" done 90
  wait_state "$B" done 60
  expect_eq "$(tf "$B" .worktree_path)" "$WT" "B's worktree after the crash"
  # The interrupted hold re-ran; the commit after it ran once.
  expect_eq "$(commit_subjects "$A")" held "A's commits"
  daemon_down
  echo "=== scenario 3 PASS"
fi

# --------------------------------------------------------------------------
# Scenarios 4 and 9: a clean manual merge-back, and the issue's lane over it.
# --------------------------------------------------------------------------
if run_scenario 4 || run_scenario 9; then
  echo "=== scenarios 4 and 9: a clean merge-back, and the lanes it passes through"
  scenario_dirs s4
  write_sides
  write_hold hold-main 8 'git commit --allow-empty -qm main-line'
  daemon_up
  project_and_issue
  A="$(create_main hold-main "occupant A" | jq -r .id)"
  wait_state "$A" running 30
  expect_eq "$(issue_f .lane)" in_progress "the lane while A runs"
  WT="$(worktree_of "$A")" BR="$(tf "$A" .branch_name)"
  TIP="$(git -C "$REPO" rev-parse "$BR" | tr -d '\r')"

  CREATED="$(create_side side-file "side S" block)"
  S="$(jq -r .id <<<"$CREATED")"
  expect_eq "$(jq -r .issue_worktree <<<"$CREATED")" side "S's role"
  expect_eq "$(jq -r .merge_back.on_conflict <<<"$CREATED")" block "S's merge_back"
  wait_state "$S" done 60
  expect_eq "$(tf "$S" .base_sha)" "$TIP" "S's base_sha"
  [[ "$(tf "$S" .worktree_path)" != "$WT" ]] || fail "the side task ran in the main worktree"

  M="$(merge_back_of "$S")"
  MT="$(task "$M")"
  expect_eq "$(jq -r .issue_worktree <<<"$MT")" main "the merge-back's role"
  expect_eq "$(tf "$A" .state)" running "A when the merge-back appeared"
  expect_eq "$(jq -r .state <<<"$MT")" queued "the merge-back while A runs"
  expect_eq "$(issue_f .lane)" in_progress "the lane with a merge-back pending"
  hold_queued "$M" "$A" done 60
  wait_state "$M" done 60
  expect_eq "$(tf "$M" .worktree_path)" "$WT" "the merge-back's worktree"

  SUBJECTS="$(branch_subjects "$BR")"
  grep -qx "Merge task $S into issue #$ISSUE" <<<"$SUBJECTS" \
    || fail "no merge commit for task $S on $BR: $SUBJECTS"
  branch_has "$BR" "side-$S.txt" || fail "side-$S.txt never reached $BR"
  expect_eq "$(issue_f .lane)" hand_off "the lane once every task is done"
  daemon_down
  echo "=== scenarios 4 and 9 PASS"
fi

# conflict_setup NAME ON_CONFLICT [AGENT]: an occupant A that moves
# shared.txt after a side task S, cut before it, moved it too; leaves A, S,
# M (S's merge-back), WT and BR set once S is done.
A="" S="" M="" WT="" BR=""
conflict_setup() {
  write_sides
  write_hold hold-shared 6 "$MAIN_SHARED"
  daemon_up
  project_and_issue
  A="$(create_main hold-shared "occupant A" | jq -r .id)"
  wait_state "$A" running 30
  WT="$(worktree_of "$A")" BR="$(tf "$A" .branch_name)"
  S="$(create_side side-shared "side S" "$1" "${2:-}" | jq -r .id)"
  wait_state "$S" done 60
  M="$(merge_back_of "$S")"
}

# --------------------------------------------------------------------------
# Scenario 5: a manual merge-back's conflict, resolved by hand and retried.
# --------------------------------------------------------------------------
if run_scenario 5; then
  echo "=== scenario 5: a conflicted merge-back, resolved by hand"
  scenario_dirs s5
  conflict_setup block
  wait_state "$M" blocked 90
  MT="$(task "$M")"
  expect_eq "$(jq -r .block_reason <<<"$MT")" merge_conflict "the merge-back's block_reason"
  expect_eq "$(jq -r .worktree_path <<<"$MT")" "$WT" "the merge-back's worktree"
  conflicted "$WT" || fail "no unmerged path in the main worktree"
  resolve_by_hand "$WT" resolved
  api POST "/tasks/$M/retry" '{}' >/dev/null
  wait_state "$M" done 60
  expect_eq "$(shared_on "$BR")" resolved "shared.txt on $BR"
  expect_eq "$(merge_count "$BR")" 1 "merge commits on $BR"
  daemon_down
  echo "=== scenario 5 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 6: an agentic merge-back resolves the same conflict on its own.
# --------------------------------------------------------------------------
if run_scenario 6; then
  echo "=== scenario 6: an agentic merge-back"
  scenario_dirs s6
  printf 'agents:\n  claude:\n    path: "%s"\n' "$(hostpath "$FAKEAGENT")" > "$CONFIG_DIR/config.yaml"
  # The resolver overwrites the conflicted file, markers and all, with a
  # resolution — what a real agent asked to resolve it does.
  export FAKEAGENT_WRITE_FILE=shared.txt
  export FAKEAGENT_WRITE_CONTENT=$'[gate]\n\tside = agent\n'
  # The side task's agent override is the resolver's agent.
  conflict_setup agent claude
  expect_eq "$(tf "$S" .merge_back.on_conflict)" agent "S's merge_back"
  wait_state "$M" done 90 # fails at once on a block: no human acts here
  expect_eq "$(shared_on "$BR")" agent "shared.txt on $BR"
  expect_eq "$(merge_count "$BR")" 1 "merge commits on $BR"
  daemon_down
  unset FAKEAGENT_WRITE_FILE FAKEAGENT_WRITE_CONTENT
  echo "=== scenario 6 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 7: merge-backs merge in the order their side tasks finished.
# --------------------------------------------------------------------------
if run_scenario 7; then
  echo "=== scenario 7: merge-backs in FIFO order"
  scenario_dirs s7
  write_sides
  write_hold hold-long 12 'git commit --allow-empty -qm main-line'
  daemon_up
  project_and_issue
  A="$(create_main hold-long "occupant A" | jq -r .id)"
  wait_state "$A" running 30
  # A side task needs the main branch in git, which A's admission cuts.
  worktree_of "$A" >/dev/null
  BR="$(tf "$A" .branch_name)"
  # S2 is created only once S1 is done, so the finishing order is forced.
  S1="$(create_side side-file "side S1" block | jq -r .id)"
  wait_state "$S1" done 60
  S2="$(create_side side-file "side S2" block | jq -r .id)"
  wait_state "$S2" done 60
  M1="$(merge_back_of "$S1")" M2="$(merge_back_of "$S2")"
  hold_queued "$M1" "$A" done 60
  wait_state "$M1" done 60
  wait_state "$M2" done 60
  ran_apart "$M1" "$M2"
  # First-parent, newest first: S2's merge sits above S1's.
  SUBJECTS="$(branch_subjects "$BR")"
  L1="$(grep -nx "Merge task $S1 into issue #$ISSUE" <<<"$SUBJECTS" | cut -d: -f1)"
  L2="$(grep -nx "Merge task $S2 into issue #$ISSUE" <<<"$SUBJECTS" | cut -d: -f1)"
  [[ -n "$L1" && -n "$L2" && "$L2" -lt "$L1" ]] \
    || fail "the merges are not in finishing order on $BR: $SUBJECTS"
  daemon_down
  echo "=== scenario 7 PASS"
fi

# --------------------------------------------------------------------------
# Scenario 8: a crash while a merge-back is blocked with its merge in
# progress. m6 scenario 6's shape; see the header for the case not driven.
# --------------------------------------------------------------------------
if run_scenario 8; then
  echo "=== scenario 8: a crash with a merge left in progress"
  scenario_dirs s8
  conflict_setup block
  wait_state "$M" blocked 90
  CREATED="$(create_main commit-b "waiter B")"
  B="$(jq -r .id <<<"$CREATED")"
  expect_eq "$(jq -r '.main_worktree_occupant_task_id // ""' <<<"$CREATED")" "$M" "B's 201 occupant hint"

  echo "== hard-kill the daemon (pid from daemon.json)"
  daemon_kill
  daemon_up
  sleep 2 # a deliberate quiet spell: recovery and a walk have had their turn
  MT="$(task "$M")"
  expect_eq "$(jq -r .state <<<"$MT")/$(jq -r .block_reason <<<"$MT")" blocked/merge_conflict "the merge-back after the restart"
  expect_eq "$(tf "$B" .state)" queued "B after the restart"
  conflicted "$WT" || fail "the merge in progress did not survive the restart"

  resolve_by_hand "$WT" resolved
  api POST "/tasks/$M/retry" '{}' >/dev/null
  hold_queued "$B" "$M" done 60
  wait_state "$B" done 60
  expect_eq "$(shared_on "$BR")" resolved "shared.txt on $BR"
  expect_eq "$(merge_count "$BR")" 1 "merge commits on $BR"
  expect_eq "$(tf "$B" .worktree_path)" "$WT" "B's worktree"
  daemon_down
  echo "=== scenario 8 PASS"
fi

echo
echo "134 GATE PASSED"
