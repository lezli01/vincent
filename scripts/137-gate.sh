#!/usr/bin/env bash
# Task 137.13 gate (§5.7, §13.2, §16): prove over the wire that handoffs work
# end to end — the claim, the grant, the item verbs and readiness — against
# a real daemon, the fake `gh` and the fake agent.
#
#    1. claim exclusivity: two handoffs racing for one hand_off issue — one
#       201 and one 409 issue_not_eligible, one live item, and no task made
#       by the loser
#    2. the grant: an ungranted handoff task's `vincent issue close` and
#       `vincent github pr merge` are refused forge_write_needs_human with
#       nothing sent to `gh`; granted issue.close and pull.merge, both
#       succeed, the close attributed to the handoff and written back once,
#       and `gh pr merge` called once
#    3. `wait` resolved by a sync close: the item outlives its task with the
#       claim held, and a close imported from GitHub records it delivered
#    4. `return` with a follow-up: the claim released, readiness withdrawn,
#       the issue back in_progress, and the prompt run as a follow-up on the
#       issue's main task
#    5. an aborted handoff releases its items, and a fresh handoff claims them
#    6. `hand_off: explicit`: a done task leaves its issue in_progress, a
#       handoff over it is refused, and `vincent task deliver --ready` or
#       `vincent issue ready` moves it to hand_off
#    7. a task on a claimed issue is 409 issue_in_handoff, and is accepted
#       once the item is returned
#
# Task-numbered rather than `mN` because this is not a §19 milestone, like
# 130-gate.sh. Each scenario runs its own daemon over its own config and data
# dirs, so VINCENT_GATE_SCENARIO=n runs one alone.
#
# An issue reaches the hand_off lane the way a user's does: a task created
# from it runs a command-only workflow to done (`hand_off: auto`, the
# default). The handoff workflows are `role: handoff`; the steps that act
# from inside a handoff's task run `vincent ...` — found on the daemon's PATH,
# and run under pwsh on Windows — so the caller the forge-write guard judges
# is a real step, not a header the gate forged. Every other `run:` body is
# `exit 0`, and a handoff task that must stay live waits on a manual step.
# The GitHub half is cmd/fakegh installed as `gh`, as in 130-gate.sh and
# 068-gate.sh: its issue corpus in FAKEGH_ISSUES_FILE, its pull-request
# writes in FAKEGH_STATE_FILE, and every call it was asked to make in
# FAKEGH_ARGV_FILE — so "nothing was sent" is observed process behaviour.
#
# The gate observes the rules CLAUDE.md records: no pipe into `grep -q` or
# any other early-exiting consumer (capture first, match a here-string),
# `| tr -d '\r'` on multi-line captures, `run:` bodies in the sh∩pwsh
# intersection, waits that are bounded polls on API state, and committed
# executable.
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

echo "== build vincent, the fake agent and the fake gh"
mkdir -p "$BIN"
gate_build "$BIN" vincent fakeagent
# cmd/fakegh is built *as* `gh`, so the daemon's PATH lookup finds it exactly
# the way it would find the real CLI.
gate_build_as "$BIN/gh$EXE" fakegh
# The daemon inherits this PATH, and so does every step it runs: a step's
# `vincent ...` is this build.
PATH="$BIN:$PATH"
export PATH

unset GITHUB_TOKEN GH_TOKEN
# The gate is the human. Run from inside a vincent step — a gate the author's
# own workflow runs — the step's VINCENT_TASK_ID would make every `vincent`
# call here an agent's (§13.4's marker) and refuse the human's grant, and the
# daemon would hand the rest to its steps. Only the gate's own knobs stay.
for v in $(compgen -e); do
  case "$v" in
    VINCENT_GATE_*) ;;
    VINCENT_*) unset "$v" ;;
  esac
done
export FAKEGH_SCENARIO=success
REPO_SLUG=gate/handoffs
export FAKEGH_REPO="$REPO_SLUG"

CONFIG_DIR="" DATA_DIR="" CORPUS="" GH_ARGV=""
scenario_dirs() { # scenario_dirs NAME
  CONFIG_DIR="$TMP/$1/config"
  DATA_DIR="$TMP/$1/data"
  mkdir -p "$CONFIG_DIR/workflows" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
  CORPUS="$TMP/$1/corpus.json"
  GH_ARGV="$TMP/$1/gh-argv.txt"
  : > "$GH_ARGV"
  export FAKEGH_ISSUES_FILE FAKEGH_ARGV_FILE FAKEGH_STATE_FILE
  FAKEGH_ISSUES_FILE="$(hostpath "$CORPUS")"
  FAKEGH_ARGV_FILE="$(hostpath "$GH_ARGV")"
  FAKEGH_STATE_FILE="$(hostpath "$TMP/$1/gh-state.json")"
  cat > "$CONFIG_DIR/config.yaml" <<EOF
github:
  enabled: true
  poll_interval: 1s
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
EOF
  write_workflows
}

# The workflows every scenario shares. `work` and `explicit` are a
# workshop's: one `exit 0` step each, the first leaving its issue ready to
# hand off and the second not. The rest are `role: handoff`.
write_workflows() {
  local d="$CONFIG_DIR/workflows"
  cat > "$d/work.yaml" <<'YAML'
name: work
steps:
  - id: noop
    type: command
    run: exit 0
YAML
  cat > "$d/explicit.yaml" <<'YAML'
name: explicit
hand_off: explicit
steps:
  - id: noop
    type: command
    run: exit 0
YAML
  # hold keeps the handoff's task live until the gate acts on it.
  cat > "$d/hold.yaml" <<'YAML'
name: hold
role: handoff
steps:
  - id: hold
    type: manual
    instructions: The gate acts on this handoff from outside.
YAML
  cat > "$d/close.yaml" <<'YAML'
name: close
role: handoff
steps:
  - id: close
    type: command
    run: vincent issue close {{ (index .Handoff.Items 0).Issue.Number }}
YAML
  # A merge is pinned to the head of fakegh's one pull request, #412.
  cat > "$d/merge.yaml" <<'YAML'
name: merge
role: handoff
steps:
  - id: merge
    type: command
    run: vincent github pr merge --task {{ (index .Handoff.Items 0).TaskID }} --method rebase --head-sha d3adb33fd3adb33fd3adb33fd3adb33fd3adb33f
YAML
  cat > "$d/land.yaml" <<'YAML'
name: land
role: handoff
steps:
  - id: merge
    type: command
    run: vincent github pr merge --task {{ (index .Handoff.Items 0).TaskID }} --method rebase --head-sha d3adb33fd3adb33fd3adb33fd3adb33fd3adb33f
  - id: close
    type: command
    run: vincent issue close {{ (index .Handoff.Items 0).Issue.Number }}
YAML
  cat > "$d/park.yaml" <<'YAML'
name: park
role: handoff
steps:
  - id: wait
    type: command
    run: vincent handoff wait {{ .Handoff.ID }} {{ (index .Handoff.Items 0).Issue.Number }} --note "waiting on the release"
YAML
}

PORT="" TOKEN="" BASE=""
daemon_up() {
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
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

# expect_conflict OUT REASON: a 409 whose details.reason is REASON.
expect_conflict() {
  local status body
  status="$(status_of "$1")"
  body="$(body_of "$1")"
  [[ "$status" == "409" ]] || fail "want 409 $2, got HTTP $status: $body"
  [[ "$(jq -r .error.details.reason <<<"$body")" == "$2" ]] \
    || fail "a 409 not at $2: $body"
}

# wait_for SECS WHAT COMMAND... polls COMMAND every GATE_POLL for SECS seconds.
wait_for() {
  local tries="$1" what="$2"
  shift 2
  for _ in $(seq 1 "$(gate_ticks "$tries")"); do
    if "$@"; then return 0; fi
    sleep "$GATE_POLL"
  done
  fail "timed out after ${tries}s waiting for $what"
}

run_scenario() { [[ -z "$ONLY" || "$ONLY" == "$1" ]]; }

PID=""
github_project() { # github_project NAME: origin is github.com/$REPO_SLUG
  local repo="$TMP/$1/repo"
  git init -q -b main "$repo"
  git -C "$repo" config user.name gate
  git -C "$repo" config user.email gate@example.invalid
  git -C "$repo" config commit.gpgsign false
  printf 'gate repo\n' > "$repo/README.md"
  git -C "$repo" add . && git -C "$repo" commit -qm init
  git -C "$repo" remote add origin "https://github.com/$REPO_SLUG.git"
  PID="$(api POST /projects "$(jq -cn --arg p "$(hostpath "$repo")" '{path: $p}')" | jq -r .id)"
}

# ---- the fake's corpus: open issues #1–#N

seed_corpus() { # seed_corpus N
  jq -n --argjson last "$1" --arg r "$REPO_SLUG" '[range(1; $last + 1) as $n | {
    id: (3000000 + $n), node_id: "I_gate_\($n)", number: $n,
    title: "Gate issue \($n)", body: "", state: "open", state_reason: null, closed_at: null,
    created_at: "2026-09-01T00:00:00Z", updated_at: "2026-09-01T01:00:00Z",
    labels: [], assignees: [], user: {login: "octocat"}, milestone: null, comments: 0,
    url: "https://api.github.com/repos/\($r)/issues/\($n)",
    repository_url: "https://api.github.com/repos/\($r)",
    html_url: "https://github.com/\($r)/issues/\($n)"}]' > "$CORPUS"
}

# corpus_close N: someone closes #N on GitHub. Written through a temp file
# and a rename, retried because on Windows the rename fails while a fakegh
# child holds the file open.
corpus_close() {
  local now
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  jq --argjson n "$1" --arg now "$now" 'map(if .number == $n then
      .state = "closed" | .state_reason = "completed" | .closed_at = $now | .updated_at = $now
    else . end)' "$CORPUS" > "$CORPUS.edit"
  for _ in $(seq 1 "$(gate_ticks 10)"); do
    if mv -f "$CORPUS.edit" "$CORPUS" 2>/dev/null; then return 0; fi
    sleep "$GATE_POLL"
  done
  fail "the corpus could not be rewritten"
}

corpus_state() { jq -r --argjson n "$1" '.[] | select(.number == $n) | .state' "$CORPUS"; }
corpus_is() { [[ "$(corpus_state "$1")" == "$2" ]]; }

gh_calls() { tr -d '\r' < "$GH_ARGV"; }
calls_matching() { # calls_matching ERE -> how many of gh's calls match
  local calls
  calls="$(gh_calls)"
  grep -cE -- "$1" <<<"$calls" || true
}
patches_for() { calls_matching "-X PATCH .*repos/$REPO_SLUG/issues/$1\$"; }

imported_count_is() { [[ "$(api GET "/issues?project_id=$PID&source=github" | jq length)" == "$1" ]]; }
issue_by_number() {
  api GET "/issues?project_id=$PID&source=github" | jq -r --argjson n "$1" '.[] | select(.source.number == $n) | .id'
}

# ---- issues, tasks and handoffs

issue_field() { api GET "/issues/$1" | jq -r "$2"; }
field_is() { [[ "$(issue_field "$1" "$2")" == "$3" ]]; } # field_is ID FILTER WANT

task_field() { api GET "/tasks/$1" | jq -r "$2"; }
task_state_is() { [[ "$(task_field "$1" .state)" == "$2" ]]; }

create_task() { # create_task ISSUE_ID WORKFLOW -> task id
  api POST /tasks "$(jq -cn --argjson p "$PID" --argjson i "$1" --arg w "$2" \
    '{project_id: $p, workflow: $w, issue_id: $i}')" | jq -r .id
}

# deliver ISSUE_ID [WORKFLOW]: a workshop task on the issue runs to done, and
# its id is left in TASK.
TASK=""
deliver() {
  TASK="$(create_task "$1" "${2:-work}")"
  wait_for 60 "task $TASK to finish" task_state_is "$TASK" "done"
}

# make_ready ISSUE_ID: the issue reaches the hand_off lane as a user's does.
make_ready() {
  deliver "$1"
  wait_for 30 "issue $1 to reach hand_off" field_is "$1" .lane hand_off
}

handoff_raw() { # handoff_raw WORKFLOW ISSUE_IDS_JSON [GRANT_JSON] -> api_raw output
  api_raw POST /handoffs "$(jq -cn --argjson p "$PID" --arg w "$1" --argjson i "$2" --argjson g "${3:-[]}" \
    '{project_id: $p, workflow: $w, issue_ids: $i, grant: $g}')"
}

handoff() { # handoff WORKFLOW ISSUE_IDS_JSON [GRANT_JSON] -> the handoff's JSON
  local out
  out="$(handoff_raw "$@")"
  [[ "$(status_of "$out")" == "201" ]] || fail "handoff $1 over $2 -> HTTP $(status_of "$out"): $(body_of "$out")"
  body_of "$out"
}

handoff_get() { api GET "/handoffs/$1"; }
item_of() { handoff_get "$1" | jq -r --argjson i "$2" ".items[] | select(.issue_id == \$i) | $3"; }
item_is() { [[ "$(item_of "$1" "$2" "$3")" == "$4" ]]; } # item_is H ISSUE FILTER WANT
handoff_state_is() { [[ "$(handoff_get "$1" | jq -r .state)" == "$2" ]]; }

# The live handoffs claiming ISSUE_ID.
live_claims() { api GET "/handoffs?issue_id=$1&state=live" | jq length; }

# The issue's state_changed events: `by` and `handoff_id` of each, read from
# the stream from its start for --max-time, as 130-gate.sh reads it.
state_events_for() { # state_events_for ISSUE_ID -> "by handoff_id" lines
  local raw
  raw="$(curl -sS --max-time 3 -N -H "Authorization: Bearer $TOKEN" \
    -H "Last-Event-ID: 1" "$BASE/events?types=issue.state_changed" 2>/dev/null || true)"
  tr -d '\r' <<<"$raw" | sed -n 's/^data: //p' \
    | jq -r --argjson i "$1" 'select(.payload.id == $i) | "\(.payload.by) \(.payload.handoff_id // "")"'
}

# --------------------------------------------------------------- scenario 1
if run_scenario 1; then
  echo "== scenario 1: two handoffs race for one issue"
  scenario_dirs s1
  seed_corpus 1
  daemon_up
  github_project s1
  wait_for 30 "the import of #1" imported_count_is 1
  one="$(issue_by_number 1)"
  make_ready "$one"
  tasks_before="$(api GET "/tasks?project_id=$PID" | jq length)"

  body="$(jq -cn --argjson p "$PID" --argjson i "$one" '{project_id: $p, workflow: "hold", issue_ids: [$i]}')"
  race() { # race N: one contender, its status and body into $TMP/race-N
    curl -sS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
      -d "$body" -w $'\n%{http_code}' "$BASE/handoffs" > "$TMP/race-$1" 2>&1
  }
  race a &
  ra=$!
  race b &
  rb=$!
  wait "$ra" || fail "contender a's curl failed"
  wait "$rb" || fail "contender b's curl failed"
  a="$(tr -d '\r' < "$TMP/race-a")"
  b="$(tr -d '\r' < "$TMP/race-b")"
  statuses="$(printf '%s\n%s\n' "${a##*$'\n'}" "${b##*$'\n'}" | sort | tr '\n' ' ')"
  [[ "$statuses" == "201 409 " ]] || fail "the race answered [$statuses], want one 201 and one 409: $a / $b"
  if [[ "${a##*$'\n'}" == "201" ]]; then won="${a%$'\n'*}" lost="${b%$'\n'*}"; else won="${b%$'\n'*}" lost="${a%$'\n'*}"; fi
  [[ "$(jq -r .error.details.reason <<<"$lost")" == "issue_not_eligible" ]] \
    || fail "the loser was not refused issue_not_eligible: $lost"
  hid="$(jq -r .id <<<"$won")"
  [[ "$(jq -r .error.details.handoff_id <<<"$lost")" == "$hid" ]] \
    || fail "the refusal does not name the winning handoff $hid: $lost"

  [[ "$(live_claims "$one")" == "1" ]] || fail "issue $one has $(live_claims "$one") live handoffs, want 1"
  item_is "$hid" "$one" .outcome "" || fail "the winner's item is $(item_of "$hid" "$one" .outcome), want unresolved"
  tasks_after="$(api GET "/tasks?project_id=$PID" | jq length)"
  [[ "$tasks_after" == "$((tasks_before + 1))" ]] \
    || fail "the race made $((tasks_after - tasks_before)) tasks, want 1 (the loser made one)"

  daemon_down
fi

# --------------------------------------------------------------- scenario 2
if run_scenario 2; then
  echo "== scenario 2: the grant"
  scenario_dirs s2
  seed_corpus 1
  daemon_up
  github_project s2
  wait_for 30 "the import of #1" imported_count_is 1
  one="$(issue_by_number 1)"
  make_ready "$one"
  delivering="$TASK"
  out="$("$VINCENT" github pr link 412 --task "$delivering" 2>&1)" \
    || fail "vincent github pr link exited non-zero: $out"

  # refused RUNNING_WORKFLOW: an ungranted handoff's step fails, refused by
  # the guard; cancelling it releases the claim for the next.
  refused() {
    local h tid step
    h="$(handoff "$1" "[$one]")"
    tid="$(jq -r .task_id <<<"$h")"
    wait_for 60 "handoff task $tid to block" task_state_is "$tid" blocked
    step="$(api GET "/tasks/$tid/steps" | jq -c '[.[] | select(.step_id == $s)] | last' --arg s "$1")"
    [[ "$(jq -r .state <<<"$step")" == "failed" ]] || fail "the ungranted $1 step did not fail: $step"
    api POST "/tasks/$tid/cancel" > /dev/null
    wait_for 30 "handoff $(jq -r .id <<<"$h") to settle" handoff_state_is "$(jq -r .id <<<"$h")" settled
  }
  : > "$GH_ARGV"
  refused close
  refused merge
  field_is "$one" .state open || fail "an ungranted close closed issue $one"
  sleep 3 # a few sync ticks: no write follows a refusal
  [[ "$(patches_for 1)" == "0" ]] || fail "an ungranted close reached GitHub: $(gh_calls)"
  [[ "$(calls_matching '^pr merge ')" == "0" ]] || fail "an ungranted merge reached GitHub: $(gh_calls)"

  # Granted, by a human, through the CLI: the same writes are made.
  out="$("$VINCENT" handoff start "$one" --project "$PID" --workflow land \
    --grant issue.close --grant pull.merge --json)" || fail "vincent handoff start exited non-zero: $out"
  hid="$(jq -r .id <<<"$out")"
  tid="$(jq -r .task_id <<<"$out")"
  [[ "$(jq -r '.grant | sort | join(",")' <<<"$out")" == "issue.close,pull.merge" ]] \
    || fail "the handoff's grant is $(jq -c .grant <<<"$out")"
  wait_for 60 "granted handoff task $tid to finish" task_state_is "$tid" "done"
  field_is "$one" .state closed || fail "the granted close left issue $one $(issue_field "$one" .state)"
  wait_for 30 "#1's write-back" corpus_is 1 closed
  wait_for 30 "issue $one to say synced" field_is "$one" .sync.state synced
  sleep 3 # a few more ticks: the write is not repeated
  [[ "$(patches_for 1)" == "1" ]] || fail "#1 was PATCHed $(patches_for 1) times, want 1: $(gh_calls)"
  [[ "$(calls_matching '^pr merge 412 ')" == "1" ]] || fail "gh pr merge was not called once: $(gh_calls)"
  events="$(state_events_for "$one")"
  [[ "$events" == "handoff $hid" ]] || fail "issue $one's state events say [$events], want [handoff $hid]"

  daemon_down
fi

# --------------------------------------------------------------- scenario 3
if run_scenario 3; then
  echo "== scenario 3: wait, resolved by a close imported from GitHub"
  scenario_dirs s3
  seed_corpus 1
  daemon_up
  github_project s3
  wait_for 30 "the import of #1" imported_count_is 1
  one="$(issue_by_number 1)"
  make_ready "$one"

  h="$(handoff park "[$one]")"
  hid="$(jq -r .id <<<"$h")"
  tid="$(jq -r .task_id <<<"$h")"
  wait_for 60 "handoff task $tid to finish" task_state_is "$tid" "done"
  item_is "$hid" "$one" .outcome waiting || fail "the item is $(item_of "$hid" "$one" .outcome), want waiting"
  item_is "$hid" "$one" .outcome_by handoff || fail "the wait was recorded by $(item_of "$hid" "$one" .outcome_by)"
  handoff_state_is "$hid" waiting || fail "handoff $hid is $(handoff_get "$hid" | jq -r .state), want waiting"
  out="$(handoff_raw hold "[$one]")"
  expect_conflict "$out" issue_not_eligible

  corpus_close 1
  wait_for 30 "the item to be delivered by sync" item_is "$hid" "$one" .outcome delivered
  item_is "$hid" "$one" .outcome_by sync || fail "the delivery was recorded by $(item_of "$hid" "$one" .outcome_by)"
  handoff_state_is "$hid" settled || fail "handoff $hid is $(handoff_get "$hid" | jq -r .state), want settled"
  field_is "$one" .state closed || fail "issue $one is $(issue_field "$one" .state) after GitHub closed it"

  daemon_down
fi

# --------------------------------------------------------------- scenario 4
if run_scenario 4; then
  echo "== scenario 4: return queues a follow-up"
  scenario_dirs s4
  seed_corpus 1
  daemon_up
  github_project s4
  wait_for 30 "the import of #1" imported_count_is 1
  one="$(issue_by_number 1)"
  make_ready "$one"
  main="$TASK"
  field_is "$one" .delivery.ready true || fail "the delivery is not ready: $(issue_field "$one" .delivery)"

  h="$(handoff hold "[$one]")"
  hid="$(jq -r .id <<<"$h")"
  prompt="Address the integrator's review of handoff $hid"
  # --paused holds the round, so the lane is read before it runs: the fake
  # agent finishes a round fast enough to deliver again, ready, and put the
  # issue straight back in hand_off.
  out="$("$VINCENT" handoff return "$hid" "$one" --follow-up "$prompt" --paused --json)" \
    || fail "vincent handoff return exited non-zero: $out"
  item_is "$hid" "$one" .outcome returned || fail "the item is $(item_of "$hid" "$one" .outcome), want returned"
  # The handoff itself is still live — its task waits on the manual step —
  # so the release shows as the item's outcome, and as the follow-up below
  # running on the issue at all: a claimed issue takes no task but the
  # handoff's own (scenario 7 asks POST /v1/tasks the same question).
  handoff_state_is "$hid" live || fail "the return settled handoff $hid: $(handoff_get "$hid" | jq -r .state)"
  field_is "$one" .lane in_progress || fail "issue $one is in $(issue_field "$one" .lane), want in_progress"
  field_is "$one" '.delivery.ready // false' false || fail "the return left the delivery ready: $(issue_field "$one" .delivery)"

  # The follow-up is a round on the issue's main task, and it runs the prompt.
  task_state_is "$main" paused || fail "the follow-up left task $main $(task_field "$main" .state), want paused"
  api POST "/tasks/$main/resume" > /dev/null
  wait_for 60 "the follow-up on task $main to finish" task_state_is "$main" "done"
  ran="$(api GET "/tasks/$main/steps" | jq -r --arg p "$prompt" '[.[] | select((.rendered_prompt // "") | contains($p))] | length')"
  [[ "$ran" -ge 1 ]] || fail "no step of task $main ran the follow-up prompt: $(api GET "/tasks/$main/steps")"

  daemon_down
fi

# --------------------------------------------------------------- scenario 5
if run_scenario 5; then
  echo "== scenario 5: an aborted handoff releases its items"
  scenario_dirs s5
  seed_corpus 2
  daemon_up
  github_project s5
  wait_for 30 "the import of #1 and #2" imported_count_is 2
  one="$(issue_by_number 1)"
  two="$(issue_by_number 2)"
  make_ready "$one"
  make_ready "$two"

  h="$(handoff hold "[$one, $two]")"
  hid="$(jq -r .id <<<"$h")"
  tid="$(jq -r .task_id <<<"$h")"
  wait_for 60 "handoff task $tid to wait on its manual step" task_state_is "$tid" awaiting_gate
  api POST "/tasks/$tid/cancel" > /dev/null
  wait_for 30 "handoff $hid to settle" handoff_state_is "$hid" settled
  for i in "$one" "$two"; do
    item_is "$hid" "$i" .outcome released || fail "issue $i's item is $(item_of "$hid" "$i" .outcome), want released"
    item_is "$hid" "$i" .outcome_by settle || fail "issue $i's item was released by $(item_of "$hid" "$i" .outcome_by)"
    field_is "$i" .lane hand_off || fail "issue $i is in $(issue_field "$i" .lane), want hand_off"
  done
  again="$(handoff hold "[$one, $two]")"
  [[ "$(jq -r '[.items[].issue_id] | sort | join(" ")' <<<"$again")" == "$(printf '%s\n%s\n' "$one" "$two" | sort -n | tr '\n' ' ' | sed 's/ $//')" ]] \
    || fail "a fresh handoff did not claim both: $again"

  daemon_down
fi

# --------------------------------------------------------------- scenario 6
if run_scenario 6; then
  echo "== scenario 6: hand_off: explicit"
  scenario_dirs s6
  seed_corpus 2
  daemon_up
  github_project s6
  wait_for 30 "the import of #1 and #2" imported_count_is 2
  one="$(issue_by_number 1)"
  two="$(issue_by_number 2)"

  deliver "$one" explicit
  first="$TASK"
  deliver "$two" explicit
  for i in "$one" "$two"; do
    field_is "$i" .lane in_progress || fail "an explicit workflow put issue $i in $(issue_field "$i" .lane)"
  done
  out="$(handoff_raw hold "[$one]")"
  expect_conflict "$out" issue_not_eligible

  out="$("$VINCENT" task deliver --task "$first" --ready 2>&1)" \
    || fail "vincent task deliver --ready exited non-zero: $out"
  wait_for 30 "issue $one to reach hand_off" field_is "$one" .lane hand_off
  out="$("$VINCENT" issue ready "$two" 2>&1)" || fail "vincent issue ready exited non-zero: $out"
  wait_for 30 "issue $two to reach hand_off" field_is "$two" .lane hand_off
  handoff hold "[$one, $two]" > /dev/null

  daemon_down
fi

# --------------------------------------------------------------- scenario 7
if run_scenario 7; then
  echo "== scenario 7: a task on a claimed issue"
  scenario_dirs s7
  seed_corpus 1
  daemon_up
  github_project s7
  wait_for 30 "the import of #1" imported_count_is 1
  one="$(issue_by_number 1)"
  make_ready "$one"

  h="$(handoff hold "[$one]")"
  hid="$(jq -r .id <<<"$h")"
  req="$(jq -cn --argjson p "$PID" --argjson i "$one" '{project_id: $p, workflow: "work", issue_id: $i}')"
  out="$(api_raw POST /tasks "$req")"
  expect_conflict "$out" issue_in_handoff

  out="$("$VINCENT" handoff return "$hid" "$one" 2>&1)" || fail "vincent handoff return exited non-zero: $out"
  out="$(api_raw POST /tasks "$req")"
  [[ "$(status_of "$out")" == "201" ]] \
    || fail "a task on a returned issue -> HTTP $(status_of "$out"): $(body_of "$out")"

  daemon_down
fi

echo "GATE PASS"
