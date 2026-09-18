#!/usr/bin/env bash
# M16 phase gate (task 096; spec §12.3, §13.2, §13.3, §16): prove over the wire
# that event triggers start work from a system rather than a person — and only
# when, and only as often as, they were told to.
#
#   1. a `type: command` trigger created over the API is born disabled; once
#      enabled it seeds (a `seeded` row, no task), and a later event fires a
#      *paused*, restricted task that the fake agent runs on resume, announced
#      as trigger.fired
#   2. a second event rendering the same dedupe_key is deduped, not a second
#      task
#   3. `seeded` rows block a cursor-less backlog: a source that re-prints the
#      events it had before arming fires none of them on any later poll
#   4. limits.max_per_hour: the event past the cap is `rate_limited`, and stays
#      so on the next poll
#   5. triggers.enabled off disarms and drops the cursor; an event that arrived
#      while off is `seeded`, never fired, when it comes back on
#   6. DELETE drops the cursor (the re-created trigger's first poll is handed
#      an empty one) and keeps the ledger; a stale version is 409
#   7. both dry runs — POST …/poll and POST …/test — judge for real and write
#      nothing: no ledger row, no cursor or poll-health change, no task — and
#      still answer while triggers.enabled is off
#   8. follow_up and retry reactions resolve their task by branch and land it
#      `paused`; a branch no task is on is `refused`, naming the branch
#   9. `type: http` ingress: a valid HMAC fires, a bad or missing one is 401,
#      no bearer is 401 — neither writes a ledger row — the id may come from
#      X-GitHub-Delivery, and a repeated delivery is deduped
#  10. a staged trigger proposal (task 098): `vincent trigger apply` refuses
#      one that enables its trigger and writes nothing, then installs a
#      disarmed one, removes the staging directory, and the registry lists
#      the new trigger disabled
#  11. a `type: schedule` trigger (task 121): born disabled, arming anchors
#      its clock and fires nothing, the next tick fires one paused task
#      announced as trigger.fired, a daemon stop past several occurrences
#      fires exactly once on restart, and the cron grammar is asserted
#      through POST …/validate rather than waited for
#  12. overrun: skip holds its concurrency group while an unadmitted proposal
#      sits in `paused`, another group still fires, the dry run reports the
#      decision without writing, and settling the task releases the group
#  13. overrun: cancel_previous aborts the group and records the supersede
#      link, the new task taking a branch of its own
#  14. overrun: queue_coalesce holds three events and fires the newest when
#      the group empties, recording the other two `superseded`
#  15. overrun: queue_serial drains oldest first, one at a time, and its
#      backlog survives a daemon restart
#
# Each scenario gets fresh config/data/repo dirs and its own daemon (PR G
# decision, as m7): scenario 5 flips the global switch, which would disarm
# every other scenario's trigger. VINCENT_GATE_SCENARIO=N runs one scenario.
#
# The poll command is cmd/fakeagent's `trigger-poll` mode, because a trigger's
# argv runs directly and never through a shell (appendix A): a bash script
# cannot be one on Windows. It prints an events file the gate appends to, so
# what the next poll sees is the gate's to change. The pushed-event signature
# is fakeagent's `hmac` mode, so no runner needs openssl.
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

ONLY="${VINCENT_GATE_SCENARIO:-}"

fail() { echo "GATE FAIL: $*" >&2; exit 1; }

hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

# Isolate from the invoking installation before anything can stop a daemon.
# The EXIT trap and setup's daemon_down both run `vincent daemon stop`, and
# the first of them runs before setup exports a scenario's dirs: without
# these it resolves the real ones, and when the gate runs inside a vincent
# step that is the daemon supervising it — which then interrupts the step,
# re-queues it, and re-runs the gate into the same stop.
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$TMP/config")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$TMP/data")"

cleanup() {
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

run_scenario() { # run_scenario N — honours VINCENT_GATE_SCENARIO
  [[ -z "$ONLY" || "$ONLY" == "$1" ]]
}

echo "== build vincent and the fake agent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent ./cmd/fakeagent)

AGENT="${VINCENT_GATE_AGENT:-}"
FAKEAGENT_HOST="$(hostpath "$FAKEAGENT")"

# A `type: http` source reads its secret from the *daemon's* environment at
# the moment of use (§2), so it is exported before any daemon starts. The
# poll children inherit the daemon's environment the same way (§12.3's
# default `inherit: all`), which is how FAKEAGENT_TRIGGER_* reach them.
export M16_GATE_SECRET="m16-gate-shared-secret"

CONFIG_DIR="" DATA_DIR="" REPO="" SEEN="" PROJECT_ID="" PORT="" TOKEN="" BASE=""

daemon_down() { "$VINCENT" daemon stop --force >/dev/null 2>&1 || true; }

# daemon_up starts a daemon on the current dirs and re-reads its listener. The
# port is chosen per run, so a scenario that restarts the daemon must call this
# rather than keep the address it had.
daemon_up() {
  "$VINCENT" daemon start >/dev/null
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
}

# setup NAME — fresh dirs, a repo, two workflows, a daemon with
# triggers.enabled on, and a registered project.
setup() {
  local name="$1"
  daemon_down
  CONFIG_DIR="$TMP/$name/config"
  DATA_DIR="$TMP/$name/data"
  REPO="$TMP/$name/repo"
  SEEN="$TMP/$name/seen.log"
  mkdir -p "$CONFIG_DIR/workflows" "$CONFIG_DIR/triggers" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
  export FAKEAGENT_TRIGGER_SEEN
  FAKEAGENT_TRIGGER_SEEN="$(hostpath "$SEEN")"

  {
    echo "triggers:"
    echo "  enabled: true"
    if [[ -z "$AGENT" ]]; then
      echo "agents:"
      echo "  claude:"
      echo "    path: \"$FAKEAGENT_HOST\""
    fi
  } > "$CONFIG_DIR/config.yaml"

  # A trigger's create_task sends no agent (it is POST /v1/tasks' body), so
  # the workflow is where a manual run names the real CLI.
  {
    echo "name: gate-agent"
    echo "description: One agent step, so a trigger's task is run by the agent."
    echo "steps:"
    echo "  - id: work"
    echo "    type: agent"
    echo "    prompt: \"Do the work for {{.Task.Title}}\""
    if [[ -n "$AGENT" ]]; then
      echo "    agent: $AGENT"
    fi
  } > "$CONFIG_DIR/workflows/gate-agent.yaml"
  # `run:` bodies execute under the daemon's shell — /bin/sh on POSIX, pwsh on
  # Windows (§8.3) — so this one is `exit N` and nothing else.
  cat > "$CONFIG_DIR/workflows/gate-fail.yaml" <<'YAML'
name: gate-fail
description: One command step that fails, so its task blocks and a retry has a target.
steps:
  - id: work
    type: command
    run: exit 1
YAML

  git init -q -b main "$REPO"
  git -C "$REPO" config user.email gate@example.invalid
  git -C "$REPO" config user.name "M16 Gate"
  git -C "$REPO" config commit.gpgsign false
  printf 'gate repo\n' > "$REPO/README.md"
  git -C "$REPO" add . && git -C "$REPO" commit -qm init

  daemon_up
  PROJECT_ID="$(api POST /projects "$(jq -cn --arg p "$(hostpath "$REPO")" '{path: $p}')" | jq -r .id)"
  [[ "$PROJECT_ID" =~ ^[0-9]+$ ]] || fail "project registration returned no id"
}

# api_code METHOD PATH [BODY] -> the HTTP status; the body is left in
# $TMP/body.json for the caller to judge.
api_code() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-sS -o "$TMP/body.json" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $TOKEN")
  if [[ -n "$body" ]]; then
    args+=(-H "Content-Type: application/json" --data-binary "$body")
  fi
  curl "${args[@]}" "$BASE$path" || fail "curl $method $path failed"
}

# api METHOD PATH [BODY] -> the body of a 2xx; anything else fails the gate.
api() {
  local code
  code="$(api_code "$@")"
  [[ "$code" == 2* ]] || fail "$1 $2 -> HTTP $code: $(cat "$TMP/body.json")"
  cat "$TMP/body.json"
}

# wait_for WHAT TRIES COMMAND... — retry COMMAND every quarter second until it
# succeeds, failing the gate after TRIES attempts. A condition, never a sleep.
wait_for() {
  local what="$1" tries="$2" i
  shift 2
  for ((i = 0; i < tries; i++)); do
    if "$@"; then return 0; fi
    sleep 0.25
  done
  fail "timed out waiting for $what"
}

# trigger_is ID FILTER — the trigger exists and the jq filter holds on it.
trigger_is() {
  [[ "$(api_code GET "/triggers/$1")" == "200" ]] && jq -e "$2" "$TMP/body.json" >/dev/null
}

# outcomes ID OUTCOME [EVENT_ID] -> how many ledger rows have that outcome.
outcomes() {
  local body
  body="$(api GET "/triggers/$1/deliveries?limit=1000")"
  jq --arg o "$2" --arg e "${3:-}" \
    '[.deliveries[] | select(.outcome == $o and ($e == "" or .event_id == $e))] | length' <<<"$body"
}

# ledger_size ID -> every ledger row, whatever its outcome.
ledger_size() { api GET "/triggers/$1/deliveries?limit=1000" | jq '.deliveries | length'; }

# has_outcome ID OUTCOME EVENT_ID [MIN] — at least MIN (default 1) such rows.
has_outcome() { [[ "$(outcomes "$1" "$2" "$3")" -ge "${4:-1}" ]]; }

# fired_at_least ID N — a condition for wait_for, which re-runs its argv: a
# command substitution in the argv would be expanded once, at the call.
fired_at_least() { [[ "$(outcomes "$1" fired)" -ge "$2" ]]; }

# delivery_field ID OUTCOME EVENT_ID FIELD -> FIELD of the first such row.
delivery_field() {
  local body
  body="$(api GET "/triggers/$1/deliveries?limit=1000")"
  jq -r --arg o "$2" --arg e "$3" --arg f "$4" \
    '[.deliveries[] | select(.outcome == $o and .event_id == $e)][0][$f] // "null"' <<<"$body"
}

task_count() { api GET /tasks | jq 'length'; }

# tasks_at_least N — re-read on every wait_for attempt, unlike a substitution.
tasks_at_least() { [[ "$(task_count)" -ge "$1" ]]; }

task_field() { api GET "/tasks/$1" | jq -r --arg f "$2" '.[$f]'; }

# wait_state ID STATE — poll until the task reaches STATE.
wait_state() {
  local id="$1" want="$2" i state=""
  for ((i = 0; i < 240; i++)); do
    state="$(task_field "$id" state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "aborted" || "$state" == "blocked" ]] && [[ "$want" != "$state" ]]; then
      api GET "/tasks/$id" | jq . >&2
      fail "task $id went $state waiting for $want"
    fi
    sleep 0.5
  done
  fail "task $id never reached $want (stuck in $state)"
}

create_task() { # create_task WORKFLOW TITLE -> id
  api POST /tasks "$(jq -cn --argjson p "$PROJECT_ID" --arg w "$1" --arg t "$2" \
    '{project_id: $p, workflow: $w, title: $t}')" | jq -r .id
}

# emit FILE JSON... — append events to a poll command's events file, one line
# each. Append-only on purpose: a poll reading the file mid-rewrite could see
# a torn document, while a torn *append* is at worst a partial last line the
# daemon refuses as not-an-object and reads whole on the next poll.
emit() {
  local file="$1"
  shift
  printf '%s\n' "$@" >> "$file"
}

# write_trigger ID DOCUMENT — the file the registry's live reload picks up.
# The document is JSON, which is YAML, so jq does the quoting of Windows paths
# and template braces rather than a heredoc.
write_trigger() {
  printf '%s\n' "$2" > "$CONFIG_DIR/triggers/$1.yaml"
  if [[ "${OS:-}" != "Windows_NT" ]]; then chmod 600 "$CONFIG_DIR/triggers/$1.yaml"; fi
}

# command_trigger ID EVENTS_FILE INTERVAL ACTION_JSON [EXTRA_JSON] -> an
# enabled `type: command` trigger polling fakeagent over EVENTS_FILE.
command_trigger() {
  local extra="${5:-}"
  [[ -n "$extra" ]] || extra='{}'
  jq -cn --arg id "$1" --argjson p "$PROJECT_ID" --arg fa "$FAKEAGENT_HOST" \
    --arg ev "$(hostpath "$2")" --arg iv "$3" --argjson action "$4" --argjson extra "$extra" \
    '{id: $id, enabled: true,
      source: {type: "command", project: $p, poll_interval: $iv, command: [$fa, "trigger-poll", $ev]},
      action: $action} + $extra'
}

CREATE_ACTION='{"type":"create_task","workflow":"gate-agent","title":"from {{ .Event.id }}"}'

# seen_has LINE — a poll was handed this cursor (one JSON string per line).
seen_has() {
  local seen=""
  [[ -f "$SEEN" ]] && seen="$(tr -d '\r' < "$SEEN")"
  grep -qx -- "$1" <<<"$seen"
}

seen_count() {
  if [[ -f "$SEEN" ]]; then wc -l < "$SEEN" | tr -d ' \r'; else echo 0; fi
}

# ---------------------------------------------------------------------------
if run_scenario 1; then
  echo "== 1. an armed command trigger seeds, then fires a paused task the agent runs"
  setup s1
  EVENTS="$TMP/s1/events.ndjson"
  emit "$EVENTS" '{"id":"e1"}'

  BODY="$(jq -cn --argjson p "$PROJECT_ID" --arg fa "$FAKEAGENT_HOST" --arg ev "$(hostpath "$EVENTS")" \
    '{id: "inbox", project_id: $p, poll_interval: "1s", command: [$fa, "trigger-poll", $ev],
      workflow: "gate-agent", title: "from {{ .Event.id }}"}')"
  CREATED="$(api POST /triggers "$BODY")"
  FILE="$CONFIG_DIR/triggers/inbox.yaml"
  [[ -f "$FILE" ]] || fail "POST /v1/triggers wrote no file at $FILE"
  if [[ "${OS:-}" != "Windows_NT" ]]; then
    MODE="$(ls -l "$FILE" | cut -c1-10)"
    [[ "$MODE" == "-rw-------" ]] || fail "a created trigger file is $MODE, want -rw-------"
  fi
  trigger_is inbox '.valid and (.enabled | not) and (.armed | not) and .poll.last_poll_at == null' \
    || fail "a created trigger is not born valid, disabled and unpolled: $(cat "$TMP/body.json")"
  [[ "$(seen_count)" == "0" ]] || fail "a disabled trigger ran its poll command"

  VERSION="$(jq -r .version <<<"$CREATED")"
  CODE="$(api_code PATCH /triggers/inbox '{"version":"0-stale","ops":[{"op":"set","path":"enabled","value":"true"}]}')"
  [[ "$CODE" == "409" ]] || fail "a stale version enabled the trigger (HTTP $CODE)"
  api PATCH /triggers/inbox "$(jq -cn --arg v "$VERSION" \
    '{version: $v, ops: [{op: "set", path: "enabled", value: "true"}]}')" >/dev/null

  wait_for "inbox to seed" 80 trigger_is inbox '.armed and .poll.seeded and .poll.ok'
  [[ "$(outcomes inbox seeded e1)" == "1" ]] || fail "the seed poll recorded no seeded row for e1"
  [[ "$(outcomes inbox fired)" == "0" ]] || fail "the seed poll fired"
  [[ "$(task_count)" == "0" ]] || fail "the seed poll created a task"

  emit "$EVENTS" '{"id":"e2"}'
  wait_for "e2 to fire" 80 has_outcome inbox fired e2
  TASK_ID="$(delivery_field inbox fired e2 task_id)"
  [[ "$TASK_ID" =~ ^[0-9]+$ ]] || fail "the fired delivery names no task: $TASK_ID"
  TASK="$(api GET "/tasks/$TASK_ID")"
  jq -e '.state == "paused" and .restricted == true and .title == "from e2"' <<<"$TASK" >/dev/null \
    || fail "the fired task is not a paused, restricted proposal titled from the event: $TASK"
  [[ "$(outcomes inbox fired e1)" == "0" ]] || fail "e1, shown before arming, fired"
  [[ "$(task_count)" == "1" ]] || fail "one new event created $(task_count) tasks"

  # GET /v1/events never replays unasked (PR D decision): resume from 1, as
  # m15 does, narrowed to the one type. The follow is capped by --max-time,
  # whose exit 28 the `|| true` inside the substitution absorbs.
  STREAM="$(curl -sS --max-time 3 -H "Authorization: Bearer $TOKEN" \
    -H "Last-Event-ID: 1" -N "$BASE/events?types=trigger.fired" 2>/dev/null \
    | tr -d '\r' | head -c 200000 || true)"
  grep -q 'event: trigger.fired' <<<"$STREAM" || fail "no trigger.fired reached GET /v1/events: $STREAM"
  grep -q '"trigger_id":"inbox"' <<<"$STREAM" || fail "trigger.fired does not name the trigger: $STREAM"

  api POST "/tasks/$TASK_ID/resume" >/dev/null
  wait_state "$TASK_ID" done
fi

# ---------------------------------------------------------------------------
if run_scenario 2; then
  echo "== 2. an event with an already-delivered dedupe_key is deduped"
  setup s2
  EVENTS="$TMP/s2/events.ndjson"
  : > "$EVENTS"
  write_trigger dedupe "$(command_trigger dedupe "$EVENTS" 1s "$CREATE_ACTION" '{"dedupe_key":"{{ .Event.key }}"}')"
  wait_for "dedupe to seed" 80 trigger_is dedupe '.armed and .poll.seeded'

  emit "$EVENTS" '{"id":"d1","key":"same-thing"}'
  wait_for "d1 to fire" 80 has_outcome dedupe fired d1
  emit "$EVENTS" '{"id":"d2","key":"same-thing"}'
  wait_for "d2 to be deduped" 80 has_outcome dedupe deduped d2
  [[ "$(outcomes dedupe fired)" == "1" ]] || fail "a repeated dedupe_key fired again"
  [[ "$(task_count)" == "1" ]] || fail "a repeated dedupe_key created a second task"
  # A cursor-less source re-prints d1 on every poll: deduped, never re-fired.
  wait_for "d1 re-shown to be deduped" 80 has_outcome dedupe deduped d1
  [[ "$(outcomes dedupe fired)" == "1" ]] || fail "a re-shown event fired again"
fi

# ---------------------------------------------------------------------------
if run_scenario 3; then
  echo "== 3. seeded rows block a cursor-less backlog"
  setup s3
  EVENTS="$TMP/s3/events.ndjson"
  emit "$EVENTS" '{"id":"b1"}' '{"id":"b2"}' '{"id":"b3"}'
  write_trigger backlog "$(command_trigger backlog "$EVENTS" 1s "$CREATE_ACTION")"
  wait_for "backlog to seed" 80 trigger_is backlog '.armed and .poll.seeded'
  for ev in b1 b2 b3; do
    [[ "$(outcomes backlog seeded "$ev")" == "1" ]] || fail "the seed did not record $ev"
  done
  # Every later poll re-prints all three, in order; b3 deduped twice means two
  # whole polls past the seed were judged and recorded.
  wait_for "two polls past the seed" 80 has_outcome backlog deduped b3 2
  [[ "$(outcomes backlog fired)" == "0" ]] || fail "the backlog flooded on a poll after the seed"
  [[ "$(task_count)" == "0" ]] || fail "the backlog created tasks"
fi

# ---------------------------------------------------------------------------
if run_scenario 4; then
  echo "== 4. max_per_hour rate-limits the event past the cap"
  setup s4
  EVENTS="$TMP/s4/events.ndjson"
  : > "$EVENTS"
  write_trigger capped "$(command_trigger capped "$EVENTS" 1s "$CREATE_ACTION" '{"limits":{"max_per_hour":1}}')"
  wait_for "capped to seed" 80 trigger_is capped '.armed and .poll.seeded'

  emit "$EVENTS" '{"id":"r1"}' '{"id":"r2"}'
  wait_for "r2 to be rate limited" 80 has_outcome capped rate_limited r2
  [[ "$(outcomes capped fired r1)" == "1" ]] || fail "r1, inside the cap, did not fire"
  [[ "$(outcomes capped fired)" == "1" ]] || fail "the cap of one let $(outcomes capped fired) through"
  [[ "$(task_count)" == "1" ]] || fail "the cap of one created $(task_count) tasks"
  # Limited is not delivered: r2 is judged again next poll, and limited again.
  wait_for "r2 to be limited again" 80 has_outcome capped rate_limited r2 2
  [[ "$(outcomes capped fired)" == "1" ]] || fail "a rate-limited event fired on a later poll"
fi

# ---------------------------------------------------------------------------
if run_scenario 5; then
  echo "== 5. triggers.enabled off disarms; turning it back on seeds again"
  setup s5
  EVENTS="$TMP/s5/events.ndjson"
  emit "$EVENTS" '{"id":"g1"}'
  write_trigger global "$(command_trigger global "$EVENTS" 1s "$CREATE_ACTION")"
  wait_for "global to seed" 80 trigger_is global '.armed and .poll.seeded'

  api PATCH /config '{"triggers":{"enabled":false}}' >/dev/null
  api GET /triggers | jq -e '.enabled == false' >/dev/null || fail "GET /v1/triggers does not report the switch off"
  wait_for "global off to disarm and drop the cursor" 80 trigger_is global \
    '(.armed | not) and (.disarmed_reason | test("triggers.enabled")) and (.poll.seeded | not)'

  # Arrives during the off period. Were the poller still running, it would
  # fire here; the ledger below says it did not.
  emit "$EVENTS" '{"id":"g2"}'
  api PATCH /config '{"triggers":{"enabled":true}}' >/dev/null
  wait_for "re-arming to seed g2" 80 has_outcome global seeded g2
  [[ "$(outcomes global seeded g1)" == "1" ]] || fail "re-arming seeded g1 a second time"
  wait_for "a poll past the re-seed" 80 has_outcome global deduped g2
  [[ "$(outcomes global fired)" == "0" ]] || fail "an event from the off period fired"
  [[ "$(task_count)" == "0" ]] || fail "the off period created a task"
fi

# ---------------------------------------------------------------------------
if run_scenario 6; then
  echo "== 6. DELETE drops the cursor and keeps the ledger"
  # The daemon inherits the cursor line at start; the gate's own shell does
  # not need it afterwards.
  export FAKEAGENT_TRIGGER_CURSOR="m16-watermark"
  setup s6
  unset FAKEAGENT_TRIGGER_CURSOR
  EVENTS="$TMP/s6/events.ndjson"
  emit "$EVENTS" '{"id":"x1"}'
  DOC="$(command_trigger doomed "$EVENTS" 1s "$CREATE_ACTION")"
  write_trigger doomed "$DOC"
  wait_for "doomed to seed" 80 trigger_is doomed '.armed and .poll.seeded'
  # The watermark the seed stored is what the next poll is handed.
  wait_for "the cursor to be handed back" 80 seen_has '"m16-watermark"'

  VERSION="$(api GET /triggers/doomed | jq -r .version)"
  CODE="$(api_code DELETE "/triggers/doomed?version=0-stale")"
  [[ "$CODE" == "409" ]] || fail "a stale version deleted the trigger (HTTP $CODE)"
  CODE="$(api_code DELETE "/triggers/doomed?version=$VERSION")"
  [[ "$CODE" == "204" ]] || fail "DELETE answered $CODE: $(cat "$TMP/body.json")"
  [[ -e "$CONFIG_DIR/triggers/doomed.yaml" ]] && fail "the trigger file survived its DELETE"
  CODE="$(api_code GET /triggers/doomed)"
  [[ "$CODE" == "404" ]] || fail "a deleted trigger still answers $CODE"
  [[ "$(outcomes doomed seeded x1)" == "1" ]] || fail "the delete took the ledger with it"

  # The same file again. The poller is gone (the DELETE's reload stopped it
  # before answering), so the next line in the log is the new trigger's.
  : > "$SEEN"
  write_trigger doomed "$DOC"
  wait_for "the re-created trigger to seed" 80 trigger_is doomed '.armed and .poll.seeded'
  FIRST=""
  IFS= read -r FIRST < "$SEEN" || true
  FIRST="${FIRST%$'\r'}"
  [[ "$FIRST" == '""' ]] \
    || fail "the re-created trigger's first poll was handed $FIRST, not an empty cursor: the delete kept it"
  [[ "$(outcomes doomed seeded x1)" == "1" ]] || fail "the re-seed duplicated a kept ledger row"
  [[ "$(outcomes doomed fired)" == "0" ]] || fail "re-creating a trigger fired its old events"
fi

# ---------------------------------------------------------------------------
if run_scenario 7; then
  echo "== 7. both dry runs judge for real and write nothing"
  setup s7
  EVENTS="$TMP/s7/events.ndjson"
  emit "$EVENTS" '{"id":"p1"}'
  # An hour between polls: after the seed nothing but the dry runs runs the
  # command, so any change below is theirs.
  write_trigger dry "$(command_trigger dry "$EVENTS" 1h "$CREATE_ACTION")"
  wait_for "dry to seed" 80 trigger_is dry '.armed and .poll.seeded and .poll.last_poll_at != null'
  emit "$EVENTS" '{"id":"p2"}'

  POLL_BEFORE="$(api GET /triggers/dry | jq -c .poll)"
  LEDGER_BEFORE="$(api GET /triggers/dry/deliveries | jq -c .deliveries)"
  SEEN_BEFORE="$(seen_count)"

  DRY="$(api POST /triggers/dry/poll)"
  jq -e '(.seed | not)
    and ([.events[] | select(.event_id == "p1")][0] | .outcome == "deduped" and .would_dedupe)
    and ([.events[] | select(.event_id == "p2")][0] | .outcome == "fired" and .action.path == "/v1/tasks")' \
    <<<"$DRY" >/dev/null || fail "the live-poll dry run did not judge p1 deduped and p2 fired: $DRY"
  [[ "$(seen_count)" == "$((SEEN_BEFORE + 1))" ]] || fail "the live-poll dry run did not run the command"

  TEST="$(api POST /triggers/dry/test '{"event":{"id":"t1"}}')"
  jq -e '.event_id == "t1" and .outcome == "fired"
    and .action.body.title == "from t1" and .action.body.paused == true' <<<"$TEST" >/dev/null \
    || fail "the test dry run did not render the proposal: $TEST"

  [[ "$(api GET /triggers/dry | jq -c .poll)" == "$POLL_BEFORE" ]] \
    || fail "a dry run changed the cursor or poll health"
  [[ "$(api GET /triggers/dry/deliveries | jq -c .deliveries)" == "$LEDGER_BEFORE" ]] \
    || fail "a dry run wrote the ledger"
  [[ "$(task_count)" == "0" ]] || fail "a dry run created a task"

  # They fire nothing, so they answer while nothing is armed.
  api PATCH /config '{"triggers":{"enabled":false}}' >/dev/null
  wait_for "dry to disarm" 80 trigger_is dry '(.armed | not) and (.poll.seeded | not)'
  LEDGER_OFF="$(api GET /triggers/dry/deliveries | jq -c .deliveries)"
  api POST /triggers/dry/test '{"event":{"id":"t2"}}' | jq -e '.outcome == "fired"' >/dev/null \
    || fail "the test dry run refused a disarmed trigger"
  api POST /triggers/dry/poll | jq -e '.seed == true' >/dev/null \
    || fail "the live-poll dry run of a disarmed trigger did not say a real poll would seed"
  [[ "$(api GET /triggers/dry/deliveries | jq -c .deliveries)" == "$LEDGER_OFF" ]] \
    || fail "a dry run of a disarmed trigger wrote the ledger"
  trigger_is dry '.poll.last_poll_at == null' || fail "a dry run of a disarmed trigger stored a cursor"
fi

# ---------------------------------------------------------------------------
if run_scenario 8; then
  echo "== 8. follow_up and retry reactions land their task paused"
  setup s8
  DONE_ID="$(create_task gate-agent "finished work")"
  BLOCKED_ID="$(create_task gate-fail "failed work")"
  wait_state "$DONE_ID" done
  wait_state "$BLOCKED_ID" blocked
  DONE_BRANCH="$(task_field "$DONE_ID" branch_name)"
  BLOCKED_BRANCH="$(task_field "$BLOCKED_ID" branch_name)"

  NUDGES="$TMP/s8/nudges.ndjson"
  RERUNS="$TMP/s8/reruns.ndjson"
  : > "$NUDGES"
  : > "$RERUNS"
  write_trigger nudge "$(command_trigger nudge "$NUDGES" 1s \
    '{"type":"follow_up","target":"branch","branch":"{{ .Event.branch }}","prompt":"Follow up on {{ .Event.id }}"}')"
  write_trigger rerun "$(command_trigger rerun "$RERUNS" 1s \
    '{"type":"retry","target":"branch","branch":"{{ .Event.branch }}"}')"
  wait_for "nudge to seed" 80 trigger_is nudge '.armed and .poll.seeded'
  wait_for "rerun to seed" 80 trigger_is rerun '.armed and .poll.seeded'

  emit "$NUDGES" "$(jq -cn --arg b "$DONE_BRANCH" '{id: "f1", branch: $b}')" \
    '{"id":"f2","branch":"vincent/no-such-branch"}'
  emit "$RERUNS" "$(jq -cn --arg b "$BLOCKED_BRANCH" '{id: "r1", branch: $b}')"

  wait_for "the follow_up to fire" 80 has_outcome nudge fired f1
  wait_for "the retry to fire" 80 has_outcome rerun fired r1
  wait_for "the unmatched branch to be refused" 80 has_outcome nudge refused f2

  [[ "$(delivery_field nudge fired f1 task_id)" == "$DONE_ID" ]] \
    || fail "the follow_up's ledger row does not name the task it acted on"
  [[ "$(delivery_field rerun fired r1 task_id)" == "$BLOCKED_ID" ]] \
    || fail "the retry's ledger row does not name the task it acted on"
  [[ "$(task_field "$DONE_ID" state)" == "paused" ]] \
    || fail "the follow_up under propose left task $DONE_ID $(task_field "$DONE_ID" state), want paused"
  [[ "$(task_field "$BLOCKED_ID" state)" == "paused" ]] \
    || fail "the retry under propose left task $BLOCKED_ID $(task_field "$BLOCKED_ID" state), want paused"
  DETAIL="$(delivery_field nudge refused f2 detail)"
  grep -qF 'vincent/no-such-branch' <<<"$DETAIL" || fail "the refusal does not name the branch: $DETAIL"
  [[ "$(task_count)" == "2" ]] || fail "a reaction created a task"

  # Resume is what admits the held follow-up, and the agent then runs it.
  api POST "/tasks/$DONE_ID/resume" >/dev/null
  wait_state "$DONE_ID" done
fi

# ---------------------------------------------------------------------------
if run_scenario 9; then
  echo "== 9. http ingress takes the bearer token and a valid HMAC, and nothing less"
  setup s9
  write_trigger inbound "$(jq -cn --argjson p "$PROJECT_ID" \
    '{id: "inbound", enabled: true,
      source: {type: "http", project: $p,
               signature: {scheme: "github_hmac_sha256", secret_env: "M16_GATE_SECRET"}},
      action: {type: "create_task", workflow: "gate-agent", title: "pushed {{ .Event.id }}"}}')"
  wait_for "inbound to arm" 80 trigger_is inbound '.armed'

  # The body is signed and sent from one file, so the bytes the signature
  # covers are exactly the bytes curl delivers.
  PUSH="$TMP/s9/push.json"
  printf '%s' '{"id":"push-1","ref":"refs/heads/main"}' > "$PUSH"
  SIG="$("$FAKEAGENT" hmac M16_GATE_SECRET < "$PUSH" | tr -d '\r')"
  WRONG="$(M16_WRONG_SECRET=not-the-secret "$FAKEAGENT" hmac M16_WRONG_SECRET < "$PUSH" | tr -d '\r')"

  push() { # push FILE [curl args...] -> status; the body lands in $TMP/body.json
    local file="$1"
    shift
    curl -sS -o "$TMP/body.json" -w '%{http_code}' -X POST -H "Content-Type: application/json" \
      "$@" --data-binary "@$(hostpath "$file")" "$BASE/triggers/inbound/events" \
      || fail "curl POST /v1/triggers/inbound/events failed"
  }

  CODE="$(push "$PUSH" -H "Authorization: Bearer $TOKEN" -H "X-Hub-Signature-256: $SIG")"
  [[ "$CODE" == "200" ]] || fail "a signed push answered $CODE: $(cat "$TMP/body.json")"
  jq -e '.outcome == "fired" and .event_id == "push-1" and (.task_id | type) == "number"' \
    "$TMP/body.json" >/dev/null || fail "a signed push did not fire: $(cat "$TMP/body.json")"
  PUSHED_ID="$(jq -r .task_id "$TMP/body.json")"
  [[ "$(task_field "$PUSHED_ID" state)" == "paused" ]] || fail "the pushed task is not a paused proposal"
  ROWS="$(ledger_size inbound)"

  CODE="$(push "$PUSH" -H "Authorization: Bearer $TOKEN" -H "X-Hub-Signature-256: $WRONG")"
  [[ "$CODE" == "401" ]] || fail "a push signed with the wrong secret answered $CODE"
  CODE="$(push "$PUSH" -H "Authorization: Bearer $TOKEN")"
  [[ "$CODE" == "401" ]] || fail "an unsigned push answered $CODE"
  CODE="$(push "$PUSH" -H "X-Hub-Signature-256: $SIG")"
  [[ "$CODE" == "401" ]] || fail "a validly signed push with no bearer token answered $CODE"
  [[ "$(ledger_size inbound)" == "$ROWS" ]] || fail "a refused push wrote a ledger row"

  # No `id` in the payload: the delivery header supplies it.
  PUSH2="$TMP/s9/push2.json"
  printf '%s' '{"ref":"refs/heads/other"}' > "$PUSH2"
  SIG2="$("$FAKEAGENT" hmac M16_GATE_SECRET < "$PUSH2" | tr -d '\r')"
  CODE="$(push "$PUSH2" -H "Authorization: Bearer $TOKEN" -H "X-Hub-Signature-256: $SIG2" \
    -H "X-GitHub-Delivery: gate-delivery-2")"
  [[ "$CODE" == "200" ]] || fail "a push identified by X-GitHub-Delivery answered $CODE: $(cat "$TMP/body.json")"
  jq -e '.outcome == "fired" and .event_id == "gate-delivery-2"' "$TMP/body.json" >/dev/null \
    || fail "the delivery header did not become the event id: $(cat "$TMP/body.json")"

  # The same delivery again is a redelivery, not a second task.
  CODE="$(push "$PUSH" -H "Authorization: Bearer $TOKEN" -H "X-Hub-Signature-256: $SIG")"
  [[ "$CODE" == "200" ]] || fail "a redelivery answered $CODE"
  jq -e '.outcome == "deduped"' "$TMP/body.json" >/dev/null \
    || fail "a redelivery was not deduped: $(cat "$TMP/body.json")"
  [[ "$(task_count)" == "2" ]] || fail "two distinct pushes and a redelivery made $(task_count) tasks"
fi

# ---------------------------------------------------------------------------
if run_scenario 10; then
  echo "== 10. apply installs a staged proposal disarmed, and refuses one that arms"
  setup s10
  # No task has to exist: the proposal id only names the staging directory,
  # which is what a built-in's agent step writes before its apply step runs.
  STAGE="$DATA_DIR/trigger-proposals/9001"
  mkdir -p "$STAGE"
  EVENTS="$TMP/s10/events.ndjson"
  emit "$EVENTS" '{"id":"e1"}'
  printf '%s\n' '{"proposed":"absent"}' > "$STAGE/manifest.json"

  # command_trigger writes enabled: true, which is exactly the switch a
  # proposal may not throw.
  printf '%s\n' "$(command_trigger proposed "$EVENTS" 1s "$CREATE_ACTION")" > "$STAGE/proposed.yaml"
  if OUT="$("$VINCENT" trigger apply --proposal 9001 --project "$PROJECT_ID" 2>&1)"; then
    fail "apply installed a proposal that enables its trigger: $OUT"
  fi
  grep -q "proposed.yaml: enabled:" <<<"$OUT" || fail "the refusal does not name the arming key: $OUT"
  [[ ! -e "$CONFIG_DIR/triggers/proposed.yaml" ]] || fail "a refused apply wrote the trigger"
  [[ -d "$STAGE" ]] || fail "a refused apply removed its staging directory"

  printf '%s\n' "$(command_trigger proposed "$EVENTS" 1s "$CREATE_ACTION" '{"enabled":false}')" > "$STAGE/proposed.yaml"
  OUT="$("$VINCENT" trigger apply --proposal 9001 --project "$PROJECT_ID" 2>&1)" \
    || fail "apply refused a disarmed proposal: $OUT"
  [[ ! -e "$STAGE" ]] || fail "a successful apply left its staging directory"
  wait_for "the registry to load the applied trigger" 80 trigger_is proposed '.valid and (.enabled | not) and (.armed | not)'
  LISTED="$("$VINCENT" trigger ls --project "$PROJECT_ID" | tr -d '\r')"
  [[ "$LISTED" == *proposed.yaml ]] || fail "trigger ls does not list the applied trigger: $LISTED"
fi

# ---------------------------------------------------------------------------
if run_scenario 11; then
  echo "== 11. a type: schedule trigger anchors on arming, fires once a tick, and fires once when overdue"
  setup s11
  # No new route and no new client field (task 121): the starter writes a
  # command source and one PATCH turns it into a clock, which is the path the
  # TUI form takes too.
  CREATED="$(api POST /triggers "$(jq -cn --argjson p "$PROJECT_ID" --arg fa "$FAKEAGENT_HOST" \
    '{id: "clock", project_id: $p, poll_interval: "1s", command: [$fa, "trigger-poll", "unused"],
      workflow: "gate-agent", title: "from {{ .Event.id }}"}')")"
  PATCHED="$(api PATCH /triggers/clock "$(jq -cn --arg v "$(jq -r .version <<<"$CREATED")" \
    '{version: $v, ops: [
      {op: "set", path: "source.type", value: "schedule"},
      {op: "set", path: "source.every", value: "2s"},
      {op: "remove", path: "source.poll_interval"},
      {op: "remove", path: "source.command"}]}')")"
  jq -e '.errors == []' <<<"$PATCHED" >/dev/null || fail "the schedule did not validate: $PATCHED"
  trigger_is clock '.valid and (.enabled | not) and (.armed | not) and (.poll.seeded | not)' \
    || fail "a created schedule is not born valid, disabled and unanchored: $(cat "$TMP/body.json")"

  # There is no source to run once, so the live-poll dry run refuses.
  CODE="$(api_code POST /triggers/clock/poll)"
  [[ "$CODE" == "400" ]] || fail "POST /triggers/clock/poll on a schedule answered $CODE, want 400"
  grep -q "no poll" "$TMP/body.json" || fail "the refusal does not say the source has no poll: $(cat "$TMP/body.json")"

  # Arming anchors the clock and fires nothing.
  api PATCH /triggers/clock "$(jq -cn --arg v "$(jq -r .version <<<"$PATCHED")" \
    '{version: $v, ops: [{op: "set", path: "enabled", value: "true"}]}')" >/dev/null
  wait_for "the clock to anchor" 80 trigger_is clock '.armed and .poll.seeded'
  [[ "$(ledger_size clock)" == "0" ]] || fail "arming a schedule wrote a ledger row"
  [[ "$(task_count)" == "0" ]] || fail "arming a schedule created a task"

  # The next tick past the first occurrence fires exactly one paused task.
  wait_for "the first occurrence to fire" 80 fired_at_least clock 1
  FIRST="$(api GET "/triggers/clock/deliveries?limit=1000" \
    | jq -r '[.deliveries[] | select(.outcome == "fired")] | sort_by(.event_id) | .[0].task_id')"
  [[ "$FIRST" =~ ^[0-9]+$ ]] || fail "the fired occurrence names no task: $FIRST"
  TASK="$(api GET "/tasks/$FIRST")"
  jq -e '.state == "paused" and .restricted == true and (.title | startswith("from 20"))' <<<"$TASK" >/dev/null \
    || fail "a scheduled task is not a paused, restricted proposal titled from the occurrence: $TASK"
  STREAM="$(curl -sS --max-time 3 -H "Authorization: Bearer $TOKEN" \
    -H "Last-Event-ID: 1" -N "$BASE/events?types=trigger.fired" 2>/dev/null \
    | tr -d '\r' | head -c 200000 || true)"
  grep -q '"trigger_id":"clock"' <<<"$STREAM" || fail "trigger.fired does not name the schedule: $STREAM"

  # A daemon stop is not a disarm: the anchor survives it, so the occurrences
  # that fall while the daemon is down produce exactly one fire when it comes
  # back — not one per occurrence. Proved from the ledger rather than from a
  # count read across the stop: each fired row's event_id *is* its occurrence,
  # so a run that skipped nothing has every gap at one interval, and this one
  # must have exactly one gap several intervals wide.
  daemon_down
  sleep 10
  "$VINCENT" daemon start >/dev/null
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
  wait_for "the overdue occurrence to fire" 80 fired_at_least clock 2

  GAPS="$(api GET "/triggers/clock/deliveries?limit=1000" | jq -c \
    '[.deliveries[] | select(.outcome == "fired") | (.event_id[0:19] + "Z" | fromdateiso8601)]
     | sort | [range(1; length) as $i | .[$i] - .[$i - 1]]')"
  BIG="$(jq '[.[] | select(. > 3)] | length' <<<"$GAPS")"
  WIDEST="$(jq 'max // 0' <<<"$GAPS")"
  [[ "$BIG" == "1" ]] || fail "$BIG gaps wider than one occurrence in $GAPS: the downtime fired more than once"
  [[ "$WIDEST" -ge 6 ]] || fail "the widest gap in $GAPS is $WIDEST: the downtime skipped no occurrences"

  # Cron is asserted through the validator, not by waiting: its finest
  # granularity is a minute, which no gate should sit through.
  VALID="$(api POST /triggers/validate "$(jq -cn --argjson p "$PROJECT_ID" --arg src \
    "id: weekdays
source:
  type: schedule
  project: $PROJECT_ID
  cron: \"0 9 * * 1-5\"
  timezone: Europe/Budapest
action:
  type: create_task
  title: 'sweep {{ .Event.date }}'
" '{id: "weekdays", source: $src}')")"
  jq -e '.valid' <<<"$VALID" >/dev/null || fail "a weekday cron schedule did not validate: $VALID"
  BAD="$(api POST /triggers/validate "$(jq -cn --argjson p "$PROJECT_ID" --arg src \
    "id: weekdays
source:
  type: schedule
  project: $PROJECT_ID
  cron: \"@daily\"
action:
  type: create_task
  title: 'sweep'
" '{id: "weekdays", source: $src}')")"
  jq -e '.valid | not' <<<"$BAD" >/dev/null || fail "a cron descriptor the grammar refuses validated: $BAD"
fi

# ---------------------------------------------------------------------------
if run_scenario 12; then
  echo "== 12. overrun: skip holds the group while a proposal is unadmitted"
  setup s12
  EVENTS="$TMP/s12/events.ndjson"
  : > "$EVENTS"
  write_trigger hold "$(command_trigger hold "$EVENTS" 1s "$CREATE_ACTION" \
    '{"overrun":"skip","concurrency_key":"{{ .Event.ticket }}"}')"
  wait_for "hold to seed" 80 trigger_is hold '.armed and .poll.seeded'

  emit "$EVENTS" '{"id":"h1","ticket":"V-1"}'
  wait_for "h1 to fire" 80 has_outcome hold fired h1
  FIRST="$(delivery_field hold fired h1 task_id)"
  GROUP="$(delivery_field hold fired h1 concurrency_key)"
  [[ "$GROUP" == "V-1" ]] || fail "the fired row records no group: $GROUP"
  # on_fire defaults to propose, so the task is paused — and a paused task
  # holds its group (task 121 decision 3).
  [[ "$(task_field "$FIRST" state)" == "paused" ]] || fail "the first task is not a proposal"

  emit "$EVENTS" '{"id":"h2","ticket":"V-1"}'
  wait_for "h2 to be superseded" 80 has_outcome hold superseded h2
  [[ "$(task_count)" == "1" ]] || fail "a skipped event created a task: $(task_count) tasks"

  # A different ticket is a different group.
  emit "$EVENTS" '{"id":"h3","ticket":"V-2"}'
  wait_for "h3 to fire" 80 has_outcome hold fired h3
  [[ "$(task_count)" == "2" ]] || fail "the second group did not fire: $(task_count) tasks"

  # The dry run reports the decision and writes nothing.
  BEFORE="$(ledger_size hold)"
  JUDGE="$(api POST /triggers/hold/test '{"event":{"id":"h4","ticket":"V-1"}}')"
  jq -e '.outcome == "superseded" and .would_skip == true and .concurrency_key == "V-1"' <<<"$JUDGE" >/dev/null \
    || fail "the dry run does not report the overrun decision: $JUDGE"
  [[ "$(ledger_size hold)" == "$BEFORE" ]] || fail "the dry run wrote a ledger row"

  # Settling the first task releases the group. This source re-shows its whole
  # window every poll, so the event that was skipped is judged again and fires
  # now that nothing in V-1 is unfinished — a skip is a drop, not a ban.
  api POST "/tasks/$FIRST/resume" >/dev/null
  wait_state "$FIRST" done
  wait_for "the released group to fire again" 80 tasks_at_least 3
fi

# ---------------------------------------------------------------------------
if run_scenario 13; then
  echo "== 13. overrun: cancel_previous cancels the group and records the link"
  setup s13
  EVENTS="$TMP/s13/events.ndjson"
  : > "$EVENTS"
  write_trigger sweep "$(command_trigger sweep "$EVENTS" 1s "$CREATE_ACTION" \
    '{"overrun":"cancel_previous","concurrency_key":"{{ .Event.ticket }}"}')"
  wait_for "sweep to seed" 80 trigger_is sweep '.armed and .poll.seeded'

  emit "$EVENTS" '{"id":"c1","ticket":"V-9"}'
  wait_for "c1 to fire" 80 has_outcome sweep fired c1
  FIRST="$(delivery_field sweep fired c1 task_id)"

  emit "$EVENTS" '{"id":"c2","ticket":"V-9"}'
  wait_for "c2 to fire" 80 has_outcome sweep fired c2
  SECOND="$(delivery_field sweep fired c2 task_id)"
  LINK="$(delivery_field sweep fired c2 superseded_task_id)"
  [[ "$LINK" == "$FIRST" ]] || fail "the second delivery's supersede link is $LINK, want $FIRST"
  [[ "$SECOND" != "$FIRST" ]] || fail "cancel_previous reused the cancelled task"
  wait_state "$FIRST" aborted
  # The cancelled task keeps its own branch, and the new one has its own.
  [[ "$(task_field "$FIRST" branch_name)" != "$(task_field "$SECOND" branch_name)" ]] \
    || fail "the superseding task took the cancelled task's branch"
fi

# ---------------------------------------------------------------------------
if run_scenario 14; then
  echo "== 14. overrun: queue_coalesce drains the newest held event"
  setup s14
  EVENTS="$TMP/s14/events.ndjson"
  : > "$EVENTS"
  write_trigger newest "$(command_trigger newest "$EVENTS" 1s "$CREATE_ACTION" \
    '{"overrun":"queue_coalesce","concurrency_key":"{{ .Event.ticket }}"}')"
  wait_for "newest to seed" 80 trigger_is newest '.armed and .poll.seeded'

  emit "$EVENTS" '{"id":"q1","ticket":"V-3"}'
  wait_for "q1 to fire" 80 has_outcome newest fired q1
  FIRST="$(delivery_field newest fired q1 task_id)"

  emit "$EVENTS" '{"id":"q2","ticket":"V-3"}' '{"id":"q3","ticket":"V-3"}' '{"id":"q4","ticket":"V-3"}'
  wait_for "three events to be held" 80 has_outcome newest queued q4
  [[ "$(task_count)" == "1" ]] || fail "a held event created a task: $(task_count) tasks"

  api POST "/tasks/$FIRST/resume" >/dev/null
  wait_state "$FIRST" done
  wait_for "the newest held event to drain" 80 has_outcome newest fired q4
  wait_for "the coalesced events to be recorded" 80 has_outcome newest superseded q2
  has_outcome newest superseded q3 || fail "q3 was not recorded as coalesced"
  [[ "$(outcomes newest fired q2)" == "0" ]] || fail "a coalesced event fired"
  [[ "$(task_count)" == "2" ]] || fail "a coalesced drain created $(task_count) tasks, want 2"
fi

# ---------------------------------------------------------------------------
if run_scenario 15; then
  echo "== 15. overrun: queue_serial drains in order, across a daemon restart"
  setup s15
  EVENTS="$TMP/s15/events.ndjson"
  : > "$EVENTS"
  write_trigger queue "$(command_trigger queue "$EVENTS" 1s "$CREATE_ACTION" \
    '{"overrun":"queue_serial","concurrency_key":"{{ .Event.ticket }}"}')"
  wait_for "queue to seed" 80 trigger_is queue '.armed and .poll.seeded'

  emit "$EVENTS" '{"id":"s1","ticket":"V-7"}'
  wait_for "s1 to fire" 80 has_outcome queue fired s1
  FIRST="$(delivery_field queue fired s1 task_id)"

  emit "$EVENTS" '{"id":"s2","ticket":"V-7"}' '{"id":"s3","ticket":"V-7"}'
  wait_for "s2 to be held" 80 has_outcome queue queued s2
  wait_for "s3 to be held" 80 has_outcome queue queued s3

  # The backlog is a table, not a field: it outlives the daemon that held it.
  daemon_down
  daemon_up
  wait_for "the trigger to arm again" 80 trigger_is queue '.armed'

  api POST "/tasks/$FIRST/resume" >/dev/null
  wait_state "$FIRST" done
  wait_for "s2 to drain first" 80 has_outcome queue fired s2
  [[ "$(outcomes queue fired s3)" == "0" ]] || fail "queue_serial drained two events at once"
  SECOND="$(delivery_field queue fired s2 task_id)"
  api POST "/tasks/$SECOND/resume" >/dev/null
  wait_state "$SECOND" done
  wait_for "s3 to drain second" 80 has_outcome queue fired s3
  # Every event fired exactly once, in arrival order. A `superseded` row here
  # is this source re-showing an event already held, not a dropped one.
  for e in s1 s2 s3; do
    [[ "$(outcomes queue fired "$e")" == "1" ]] || fail "$e fired $(outcomes queue fired "$e") times, want 1"
  done
  [[ "$(task_count)" == "3" ]] || fail "queue_serial created $(task_count) tasks, want 3"
fi

echo "GATE PASS: m16 (event triggers)"
