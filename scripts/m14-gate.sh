#!/usr/bin/env bash
# M14 phase gate (task 067, closing 063.2; spec §5.5, §11, §13.2, §13.3):
# drive a chat end to end over the wire, against the committed fakeagent so CI
# never calls a real agent CLI.
#
#   1. POST /v1/chats creates a chat with a worktree and a branch
#   2. two sends in a row: turn 2 answers with what only turn 1 supplied, so
#      continuity came from the agent resuming its own session — walked on
#      every shipped adapter, since each spells resume differently (task 070)
#   3. GET /v1/chats/{id}/turns/{seq}/transcript serves the turn, and a
#      resume from its X-Next-Offset returns only what was appended
#   4. GET /v1/chats/{id}/events streams this chat's events and nobody else's
#   5. an awaiting_input chat is answered over /answer and the turn finishes
#   6. the max_parallel_chats+1-th send is 409 chat_cap_reached — refused,
#      never queued — and leaves no turn row behind
#   7. cancel stops a live turn; archive removes the worktree
#   8. chats never appear in GET /v1/tasks
#   9. handoff: a task adopts the chat's worktree and branch (task 074)
#  10. the listing excludes both terminal states by default and ?archived=
#      brings them back, an explicit ?state= still winning (task 079)
#  11. a chat opened on a blocked task (task 119): its turn edits the task's
#      own worktree, every §6 action but cancel is 409 task_locked_by_chat
#      while it is open, close lifts the lock and leaves the worktree, and the
#      retry the chat's edit makes pass runs the task to done
#  12. the skills a chat's agent CLI loads, and invoking one (task 124.15):
#      each adapter's own listing mechanism against the fake, the directory
#      the list is about — a linked chat's is its task's worktree — the
#      per-directory cache and ?refresh=, a `/name` invocation reported back
#      as one agent.skill by the human, a `$name` that reaches codex
#      verbatim, 409 invalid_state on a terminal chat, and `vincent chat
#      send --message-file -` delivering a leading `/` byte for byte, which
#      is the assertion the Windows leg exists for
#
# Legs 1–10 are one chain rather than separable scenarios — leg 3 reads leg
# 1's turn, leg 6 answers leg 5's parked chat, leg 10 lists the chats legs 7
# and 9 ended — so VINCENT_GATE_SCENARIO=N for any N in 1..10 runs that chain.
# Legs 11 and 12 each stand alone, needing nothing before them, so
# VINCENT_GATE_SCENARIO=11 and =12 run one of them on its own.
#
# The `agent_cannot_resume` refusal is deliberately *not* here. Since task 070
# no shipped adapter is refused, so a real daemon has no subject to reach it
# with; it stays proven in Go against agenttest.StubNonResuming, in
# internal/api and internal/chatrun. Registering a fake adapter in the
# daemon's own registry to keep the leg would put a test double in the
# production registry, which is the §9.1 property the refusal exists to guard.
#
# A chat has no workflow, so the tasks legs 11 and 12 park a chat on carry the
# only `run:` bodies here, and both are the same `git commit -a` spelled in the
# sh∩pwsh intersection like every other gate's. This script's own bash obeys
# the two standing rules: `| tr -d '\r'` on any multi-line jq capture, and
# never `| grep -q`.
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

ONLY="${VINCENT_GATE_SCENARIO:-}"
case "$ONLY" in
  "" | [1-9] | 10 | 11 | 12) ;;
  *) fail "unknown VINCENT_GATE_SCENARIO: $ONLY" ;;
esac

hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

echo "== build vincent + fakeagent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent ./cmd/fakeagent)

CONFIG_DIR="$TMP/config"
DATA_DIR="$TMP/data"
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"

# The fake CLI's own conversation store, deliberately outside the data dir:
# continuity has to come from the agent remembering, not from anything vincent
# kept.
export FAKEAGENT_SESSION_DIR
FAKEAGENT_SESSION_DIR="$(hostpath "$TMP/sessions")"
mkdir -p "$TMP/sessions"

REPO="$TMP/repo"
mkdir -p "$REPO"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email gate@example.com
git -C "$REPO" config user.name "M14 Gate"
git -C "$REPO" commit -q --allow-empty -m "root"

cat > "$CONFIG_DIR/config.yaml" <<EOF
max_parallel_chats: 1
agents:
  claude:
    path: "$(hostpath "$FAKEAGENT")"
  codex:
    path: "$(hostpath "$FAKEAGENT")"
  cursor:
    path: "$(hostpath "$FAKEAGENT")"
EOF

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

# wait_chat CHAT_ID STATE — poll until the chat reaches a state.
wait_chat() {
  local id="$1" want="$2" i=0 state
  while (( i < 300 )); do
    state="$(api GET "/chats/$id" | jq -r .chat.state)"
    if [[ "$state" == "$want" ]]; then return 0; fi
    i=$(( i + 1 ))
    sleep 1
  done
  fail "chat $id never reached $want (it is $state)"
}

# wait_turn CHAT_ID SEQ — poll until that turn is no longer running, echoing
# its state.
wait_turn() {
  local id="$1" seq="$2" i=0 state
  while (( i < 300 )); do
    state="$(api GET "/chats/$id" | jq -r --argjson s "$seq" \
      '.turns[] | select(.seq == $s) | .state')"
    if [[ -n "$state" && "$state" != "running" ]]; then
      printf '%s\n' "$state"
      return 0
    fi
    i=$(( i + 1 ))
    sleep 1
  done
  fail "turn $seq of chat $id never finished"
}

# wait_task TASK_ID STATE — poll until the task reaches a state. `aborted` ends
# the wait early, since a task never comes back from it.
wait_task() {
  local id="$1" want="$2" i=0 state
  while (( i < 300 )); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    if [[ "$state" == "$want" ]]; then return 0; fi
    if [[ "$state" == "aborted" ]]; then
      fail "task $id aborted while waiting for $want: $(api GET "/tasks/$id")"
    fi
    i=$(( i + 1 ))
    sleep 1
  done
  fail "task $id never reached $want (it is $state)"
}

# Leg 11 is a function, defined ahead of leg 1, only so that
# VINCENT_GATE_SCENARIO=11 can run it without walking legs 1–10 first: it
# brings its own repo, project, workflow and daemon environment. Unselected, it
# runs last, after leg 10.
chat_on_a_task() {
  echo "== 11. a chat on a blocked task works in its worktree and locks it (task 119)"
  # The step's body is `git commit -a`, which is in the sh∩pwsh intersection and
  # says pass or fail by its own exit code: 1 on a clean worktree ("nothing to
  # commit"), 0 once a tracked file changed. So the task blocks on its first
  # pass, and the only thing that can make its retry pass is an edit to the
  # task's own worktree — which is what the chat is claimed to make.
  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/chat-on-a-task.yaml" <<'EOF'
name: chat-on-a-task
steps:
  - id: land
    type: command
    max_retries: 0
    run: git commit -a -m chat-fixed
EOF

  LINK_REPO="$TMP/linked"
  mkdir -p "$LINK_REPO"
  git -C "$LINK_REPO" init -q -b main
  git -C "$LINK_REPO" config user.email gate@example.com
  git -C "$LINK_REPO" config user.name "M14 Gate"
  printf 'the chat appends below\n' > "$LINK_REPO/chat.txt"
  git -C "$LINK_REPO" add chat.txt
  git -C "$LINK_REPO" commit -q -m "root"

  # FAKEAGENT_EDIT_FILE appends a line to an existing worktree-relative file,
  # and only a daemon started with it passes it on. The workflow file above is
  # written before the start for m12's reason: a task created in the second
  # the file lands can beat the registry's watcher.
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  unset FAKEAGENT_SCENARIO
  export FAKEAGENT_EDIT_FILE=chat.txt
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"

  LINK_PROJECT="$(api POST /projects -d "{\"path\": \"$(hostpath "$LINK_REPO")\"}")" \
    || fail "registering the linked-chat project failed"
  LINK_PROJECT_ID="$(printf '%s' "$LINK_PROJECT" | jq -r .id)"
  LINK_TASK="$(api POST /tasks \
    -d "{\"project_id\": $LINK_PROJECT_ID, \"workflow\": \"chat-on-a-task\", \"title\": \"land the chat's fix\"}")" \
    || fail "POST /v1/tasks failed"
  LINK_TASK_ID="$(printf '%s' "$LINK_TASK" | jq -r .id)"
  [[ "$LINK_TASK_ID" != "null" && -n "$LINK_TASK_ID" ]] || fail "creating the task failed: $LINK_TASK"
  wait_task "$LINK_TASK_ID" blocked
  LINK_TASK="$(api GET "/tasks/$LINK_TASK_ID")"
  REASON="$(printf '%s' "$LINK_TASK" | jq -r .block_reason)"
  [[ "$REASON" == "nonzero_exit" ]] || fail "task $LINK_TASK_ID blocked $REASON, want nonzero_exit"
  LINK_WORKTREE="$(printf '%s' "$LINK_TASK" | jq -r .worktree_path)"
  [[ -d "$LINK_WORKTREE" ]] || fail "the blocked task has no worktree at $LINK_WORKTREE"

  CODE="$(api_status POST "/tasks/$LINK_TASK_ID/chat" -d '{"agent": "claude"}')"
  [[ "$CODE" == "201" ]] || fail "opening a chat on a blocked task answered $CODE: $(cat "$TMP/body.json")"
  LINK_CHAT="$(cat "$TMP/body.json")"
  LINK_CHAT_ID="$(printf '%s' "$LINK_CHAT" | jq -r .id)"
  printf '%s' "$LINK_CHAT" | jq -e --argjson t "$LINK_TASK_ID" \
    '.state == "idle" and .linked_task_id == $t' >/dev/null \
    || fail "the linked chat is wrong: $LINK_CHAT"

  # Opening moved nothing: the task is still blocked, and says who holds it.
  LINK_TASK="$(api GET "/tasks/$LINK_TASK_ID")"
  printf '%s' "$LINK_TASK" | jq -e --argjson c "$LINK_CHAT_ID" \
    '.state == "blocked" and .open_chat_id == $c and .available_actions == ["cancel"]' >/dev/null \
    || fail "the task is not locked by chat $LINK_CHAT_ID: $LINK_TASK"

  api POST "/chats/$LINK_CHAT_ID/send" -d '{"message": "make the failing step pass"}' >/dev/null \
    || fail "the linked send failed"
  STATE="$(wait_turn "$LINK_CHAT_ID" 1)"
  [[ "$STATE" == "done" ]] || fail "the linked turn is $STATE, want done"
  # The fake agent's edit is in the task's worktree, which is where the turn ran.
  EDITED="$(tr -d '\r' < "$LINK_WORKTREE/chat.txt")"
  grep -x 'fakeagent was here' <<<"$EDITED" >/dev/null \
    || fail "the linked turn did not edit task $LINK_TASK_ID's worktree; chat.txt is: $EDITED"

  CODE="$(api_status POST "/tasks/$LINK_TASK_ID/retry" -d '{}')"
  [[ "$CODE" == "409" ]] || fail "retry on a locked task answered $CODE, want 409"
  REASON="$(jq -r .error.code < "$TMP/body.json")"
  [[ "$REASON" == "task_locked_by_chat" ]] || fail "the refusal is $REASON, want task_locked_by_chat"
  HOLDER="$(jq -r .error.details.chat_id < "$TMP/body.json")"
  [[ "$HOLDER" == "$LINK_CHAT_ID" ]] || fail "the refusal names chat $HOLDER, want $LINK_CHAT_ID"

  CODE="$(api_status POST "/chats/$LINK_CHAT_ID/close")"
  [[ "$CODE" == "200" ]] || fail "closing the linked chat answered $CODE: $(cat "$TMP/body.json")"
  STATE="$(jq -r .state < "$TMP/body.json")"
  [[ "$STATE" == "closed" ]] || fail "the closed chat is $STATE, want closed"
  # The worktree is the task's: closing the chat neither removes it nor
  # discards the edit the retry is about to commit.
  [[ -d "$LINK_WORKTREE" ]] || fail "closing the chat removed the task's worktree at $LINK_WORKTREE"
  LINK_TASK="$(api GET "/tasks/$LINK_TASK_ID")"
  printf '%s' "$LINK_TASK" | jq -e \
    '.open_chat_id == null and any(.available_actions[]; . == "retry")' >/dev/null \
    || fail "closing the chat did not lift the lock: $LINK_TASK"

  CODE="$(api_status POST "/tasks/$LINK_TASK_ID/retry" -d '{}')"
  [[ "$CODE" == 2* ]] || fail "retry after the close answered $CODE: $(cat "$TMP/body.json")"
  wait_task "$LINK_TASK_ID" done
  BRANCH="$(api GET "/tasks/$LINK_TASK_ID" | jq -r .branch_name)"
  LOG="$(git -C "$LINK_REPO" log --format=%s "$BRANCH")"
  grep -x chat-fixed <<<"$LOG" >/dev/null \
    || fail "the retry did not commit the chat's edit on $BRANCH: $LOG"
  [[ -d "$LINK_WORKTREE" ]] || fail "the task's worktree is gone after it finished"

  # The closed chat is terminal, so only ?archived= lists it, and ?task_id=
  # lists it alone.
  LISTED="$(api GET "/chats?task_id=$LINK_TASK_ID&archived=all" \
    | jq -r --argjson id "$LINK_CHAT_ID" '[.chats[] | .id] == [$id]')"
  [[ "$LISTED" == "true" ]] || fail "GET /v1/chats?task_id=$LINK_TASK_ID does not list chat $LINK_CHAT_ID alone"
  unset FAKEAGENT_EDIT_FILE
}

# Leg 12 is a function for leg 11's reason: it brings its own repos, project,
# workflow and daemon environment, so VINCENT_GATE_SCENARIO=12 runs it without
# walking anything before it. Unselected, it runs last, after leg 11.
chat_skills() {
  echo "== 12. the skills a chat's agent loads, listed and invoked (task 124.15)"
  # One daemon for the whole leg. FAKEAGENT_SCENARIO=skill-human is the claude
  # scenario that replays a `/name` invocation the way claude 2.1.277 does
  # under --replay-user-messages; the codex and cursor dialects do not know
  # the name and fall through to their success scenarios, and the claude
  # listing probe answers `initialize` ahead of any scenario at all.
  #
  # FAKEAGENT_VERSION is the listing floor: the fake reports 2.1.224 by
  # default, below the 2.1.277 from which claude marks its own rows `builtin`,
  # and below it the adapter answers a positive no. Without it every claude
  # assertion here would be about that refusal rather than about the daemon.
  # No shipped adapter has a version floor of its own, so one value serves all
  # three dialects.
  #
  # The workflow file is written before the start for m12's reason: a task
  # created in the second the file lands can beat the registry's watcher.
  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/chat-skills-task.yaml" <<'EOF'
name: chat-skills-task
steps:
  - id: land
    type: command
    max_retries: 0
    run: git commit -a -m skills-gate
EOF

  # The listing repo: the same skill under both layouts, because claude reads
  # `.claude/skills` and codex `.agents/skills`, and the leg lists the one
  # name through both mechanisms. The front matter is the fake CLIs' own
  # source for the name, description and argument hint.
  SKILL_REPO="$TMP/skillrepo"
  mkdir -p "$SKILL_REPO/.claude/skills/gate-skill" "$SKILL_REPO/.agents/skills/gate-skill"
  cat > "$SKILL_REPO/.claude/skills/gate-skill/SKILL.md" <<'EOF'
---
name: gate-skill
description: A skill the gate committed.
argument-hint: "[target]"
---

Reply with GATE-SKILL.
EOF
  cp "$SKILL_REPO/.claude/skills/gate-skill/SKILL.md" "$SKILL_REPO/.agents/skills/gate-skill/SKILL.md"
  git -C "$SKILL_REPO" init -q -b main
  git -C "$SKILL_REPO" config user.email gate@example.com
  git -C "$SKILL_REPO" config user.name "M14 Gate"
  git -C "$SKILL_REPO" add .
  git -C "$SKILL_REPO" commit -q -m "root"

  # The linked-chat repo: a different skill, on the branch the task's worktree
  # is cut from, so the name can only appear because the list was taken in
  # that worktree.
  SKILL_TASK_REPO="$TMP/skilltaskrepo"
  mkdir -p "$SKILL_TASK_REPO/.claude/skills/task-skill"
  cat > "$SKILL_TASK_REPO/.claude/skills/task-skill/SKILL.md" <<'EOF'
---
name: task-skill
description: A skill only the task's branch carries.
argument-hint: "[what]"
---

Reply with TASK-SKILL.
EOF
  git -C "$SKILL_TASK_REPO" init -q -b main
  git -C "$SKILL_TASK_REPO" config user.email gate@example.com
  git -C "$SKILL_TASK_REPO" config user.name "M14 Gate"
  git -C "$SKILL_TASK_REPO" add .
  git -C "$SKILL_TASK_REPO" commit -q -m "root"

  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  export FAKEAGENT_SCENARIO=skill-human
  export FAKEAGENT_VERSION=2.1.277
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"

  SKILL_PROJECT_ID="$(api POST /projects \
    -d "{\"path\": \"$(hostpath "$SKILL_REPO")\"}" | jq -r .id)"
  [[ -n "$SKILL_PROJECT_ID" && "$SKILL_PROJECT_ID" != "null" ]] \
    || fail "registering the skills project failed"

  echo "== 12a. claude lists the chat's own worktree, before any turn"
  SKILL_CHAT="$(api POST /chats \
    -d "{\"project_id\": $SKILL_PROJECT_ID, \"title\": \"skills\", \"agent\": \"claude\"}")" \
    || fail "creating the claude skills chat failed"
  SKILL_CHAT_ID="$(printf '%s' "$SKILL_CHAT" | jq -r .id)"
  SKILL_WORKTREE="$(printf '%s' "$SKILL_CHAT" | jq -r .worktree_path)"
  [[ -d "$SKILL_WORKTREE" ]] || fail "the skills chat has no worktree at $SKILL_WORKTREE"
  LISTING="$(api GET "/chats/$SKILL_CHAT_ID/skills")" || fail "GET /v1/chats/{id}/skills failed"
  printf '%s' "$LISTING" | jq -e --arg dir "$SKILL_WORKTREE" '
    .list_verdict == "supported" and .invoke_verdict == "supported"
      and .invoke_sigil == "/" and .invoke_position == "leading"
      and .work_dir == $dir' >/dev/null \
    || fail "the claude chat's verdicts or directory are wrong: $LISTING"
  # Every cell is the CLI's own word, and the invocation is the adapter's.
  printf '%s' "$LISTING" | jq -e 'any(.skills[];
    .name == "gate-skill" and .invocation == "/gate-skill"
      and .description == "A skill the gate committed."
      and .argument_hint == "[target]")' >/dev/null \
    || fail "the committed skill is not listed as the CLI reported it: $LISTING"

  echo "== 12b. the per-directory cache, and ?refresh="
  mkdir -p "$SKILL_WORKTREE/.claude/skills/fresh-skill"
  cat > "$SKILL_WORKTREE/.claude/skills/fresh-skill/SKILL.md" <<'EOF'
---
name: fresh-skill
description: Written after the first listing.
---

Reply with FRESH-SKILL.
EOF
  # probed_at is RFC3339 to the second, so a refresh landing in the same
  # second as the first probe would report the same instant and prove
  # nothing. A whole second of sleep is not a guess at a cause: the second
  # probe then starts at least a second after the first one answered, so its
  # timestamp cannot be the same one.
  sleep 1
  CACHED="$(api GET "/chats/$SKILL_CHAT_ID/skills")"
  printf '%s' "$CACHED" | jq -e 'any(.skills[]; .name == "fresh-skill") | not' >/dev/null \
    || fail "a plain GET re-probed rather than serving the cached list: $CACHED"
  REFRESHED="$(api GET "/chats/$SKILL_CHAT_ID/skills?refresh=true")"
  printf '%s' "$REFRESHED" | jq -e 'any(.skills[]; .name == "fresh-skill")' >/dev/null \
    || fail "?refresh=true did not pick up the new skill: $REFRESHED"
  BEFORE="$(printf '%s' "$CACHED" | jq -r .probed_at)"
  AFTER="$(printf '%s' "$REFRESHED" | jq -r .probed_at)"
  [[ "$AFTER" > "$BEFORE" ]] \
    || fail "probed_at did not move: $BEFORE then $AFTER"

  echo "== 12c. a /name invocation is reported back as one agent.skill"
  api POST "/chats/$SKILL_CHAT_ID/send" -d '{"message": "/gate-skill hello"}' >/dev/null \
    || fail "the invoking send failed"
  STATE="$(wait_turn "$SKILL_CHAT_ID" 1)"
  [[ "$STATE" == "done" ]] || fail "the invoking turn is $STATE, want done"
  NORMALIZED="$TMP/skill-claude-1.ndjson"
  curl -sS -o "$NORMALIZED" -H "Authorization: Bearer $TOKEN" \
    "$BASE/chats/$SKILL_CHAT_ID/turns/1/transcript?format=normalized" \
    || fail "the normalized transcript failed"
  # One jq over the whole file rather than a capture, so there is no
  # multi-line output to strip CRs from and nothing an early-exiting consumer
  # could read. agent.raw is the type an unrecognized line takes: the replay
  # must be recognized, not merely carried.
  jq -s -e '[.[] | select(.type == "agent.skill")] as $s
    | ($s | length) == 1
      and $s[0].name == "gate-skill" and $s[0].args == "hello" and $s[0].by == "human"
      and (any(.[]; .type == "agent.raw") | not)' < "$NORMALIZED" >/dev/null \
    || fail "the claude turn's records are wrong: $(jq -r .type < "$NORMALIZED" | sort | uniq -c)"

  echo "== 12d. codex lists through the app-server, and takes its message verbatim"
  CODEX_CHAT="$(api POST /chats \
    -d "{\"project_id\": $SKILL_PROJECT_ID, \"title\": \"codex skills\", \"agent\": \"codex\"}")" \
    || fail "creating the codex skills chat failed"
  CODEX_CHAT_ID="$(printf '%s' "$CODEX_CHAT" | jq -r .id)"
  LISTING="$(api GET "/chats/$CODEX_CHAT_ID/skills")"
  # "$gate-skill" is a jq string literal, not a jq variable: jq interpolates
  # only through \(...).
  printf '%s' "$LISTING" | jq -e '
    .list_verdict == "supported" and .invoke_sigil == "$" and .invoke_position == "anywhere"
      and any(.skills[]; .name == "gate-skill" and .invocation == "$gate-skill")' >/dev/null \
    || fail "codex did not list the skill through the app-server: $LISTING"
  # Single-quoted in this bash, so the shell leaves it alone and the daemon is
  # asked to deliver exactly what codex recognizes.
  api POST "/chats/$CODEX_CHAT_ID/send" -d '{"message": "$gate-skill"}' >/dev/null \
    || fail "the codex send failed"
  STATE="$(wait_turn "$CODEX_CHAT_ID" 1)"
  [[ "$STATE" == "done" ]] || fail "the codex turn is $STATE, want done"
  WANT_VERBATIM='$gate-skill'
  RAW="$(curl -sS -H "Authorization: Bearer $TOKEN" \
    "$BASE/chats/$CODEX_CHAT_ID/turns/1/transcript" | tr -d '\r')"
  case "$RAW" in
    *"$WANT_VERBATIM"*) ;;
    *) fail "codex did not receive $WANT_VERBATIM verbatim; transcript: $RAW" ;;
  esac
  curl -sS -o "$TMP/skill-codex-1.ndjson" -H "Authorization: Bearer $TOKEN" \
    "$BASE/chats/$CODEX_CHAT_ID/turns/1/transcript?format=normalized" \
    || fail "the codex normalized transcript failed"
  # codex reports no invocation, and vincent never invents one from the text.
  jq -s -e 'any(.[]; .type == "agent.skill") | not' < "$TMP/skill-codex-1.ndjson" >/dev/null \
    || fail "a codex turn produced an agent.skill record"

  echo "== 12e. cursor invokes without listing"
  CURSOR_CHAT="$(api POST /chats \
    -d "{\"project_id\": $SKILL_PROJECT_ID, \"title\": \"cursor skills\", \"agent\": \"cursor\"}")" \
    || fail "creating the cursor skills chat failed"
  CURSOR_CHAT_ID="$(printf '%s' "$CURSOR_CHAT" | jq -r .id)"
  LISTING="$(api GET "/chats/$CURSOR_CHAT_ID/skills")"
  printf '%s' "$LISTING" | jq -e '
    .list_verdict == "unsupported" and (.unavailable_reason | length) > 0
      and .skills == [] and .invoke_verdict == "supported"
      and .invoke_sigil == "/" and .invoke_position == "anywhere"' >/dev/null \
    || fail "cursor's verdicts are wrong: $LISTING"

  echo "== 12f. a linked chat lists its task's worktree, and invokes on its first turn"
  SKILL_TASK_PROJECT_ID="$(api POST /projects \
    -d "{\"path\": \"$(hostpath "$SKILL_TASK_REPO")\"}" | jq -r .id)"
  [[ -n "$SKILL_TASK_PROJECT_ID" && "$SKILL_TASK_PROJECT_ID" != "null" ]] \
    || fail "registering the linked-skills project failed"
  # `git commit -a` exits 1 on a clean worktree, so the task blocks on its
  # first pass and keeps the worktree a chat can be opened on.
  SKILL_TASK_ID="$(api POST /tasks \
    -d "{\"project_id\": $SKILL_TASK_PROJECT_ID, \"workflow\": \"chat-skills-task\", \"title\": \"hold a worktree\"}" \
    | jq -r .id)"
  [[ -n "$SKILL_TASK_ID" && "$SKILL_TASK_ID" != "null" ]] || fail "creating the skills task failed"
  wait_task "$SKILL_TASK_ID" blocked
  SKILL_TASK_WORKTREE="$(api GET "/tasks/$SKILL_TASK_ID" | jq -r .worktree_path)"
  [[ -d "$SKILL_TASK_WORKTREE" ]] || fail "the blocked task has no worktree at $SKILL_TASK_WORKTREE"

  CODE="$(api_status POST "/tasks/$SKILL_TASK_ID/chat" -d '{"agent": "claude"}')"
  [[ "$CODE" == "201" ]] || fail "opening the linked chat answered $CODE: $(cat "$TMP/body.json")"
  LINKED_CHAT_ID="$(jq -r .id < "$TMP/body.json")"
  LISTING="$(api GET "/chats/$LINKED_CHAT_ID/skills")"
  printf '%s' "$LISTING" | jq -e --arg dir "$SKILL_TASK_WORKTREE" '
    .work_dir == $dir and any(.skills[];
      .name == "task-skill" and .invocation == "/task-skill"
        and .argument_hint == "[what]")' >/dev/null \
    || fail "the linked chat did not list its task's worktree: $LISTING"
  # The other project's free chat is the control: same daemon, same adapter,
  # a different directory and so a different list.
  api GET "/chats/$SKILL_CHAT_ID/skills" \
    | jq -e 'any(.skills[]; .name == "task-skill") | not' >/dev/null \
    || fail "a free chat on the other project lists the task's skill"

  # The first turn is the one that matters: a linked chat's opening context
  # rides ahead of the message as its own block, so the invocation is leading
  # only if the two arrived in that order.
  api POST "/chats/$LINKED_CHAT_ID/send" -d '{"message": "/task-skill hi"}' >/dev/null \
    || fail "the linked send failed"
  STATE="$(wait_turn "$LINKED_CHAT_ID" 1)"
  [[ "$STATE" == "done" ]] || fail "the linked turn is $STATE, want done"
  curl -sS -o "$TMP/skill-linked-1.ndjson" -H "Authorization: Bearer $TOKEN" \
    "$BASE/chats/$LINKED_CHAT_ID/turns/1/transcript?format=normalized" \
    || fail "the linked normalized transcript failed"
  jq -s -e 'any(.[]; .type == "agent.skill" and .name == "task-skill" and .by == "human")' \
    < "$TMP/skill-linked-1.ndjson" >/dev/null \
    || fail "the linked first turn reported no human invocation of task-skill: $(
      jq -r .type < "$TMP/skill-linked-1.ndjson" | sort | uniq -c)"

  echo "== 12g. a terminal chat has no next turn to list for"
  CODE="$(api_status POST "/chats/$LINKED_CHAT_ID/close")"
  [[ "$CODE" == "200" ]] || fail "closing the linked chat answered $CODE: $(cat "$TMP/body.json")"
  CODE="$(api_status GET "/chats/$LINKED_CHAT_ID/skills")"
  [[ "$CODE" == "409" ]] || fail "skills on a closed chat answered $CODE, want 409"
  REASON="$(jq -r .error.code < "$TMP/body.json")"
  [[ "$REASON" == "invalid_state" ]] || fail "the refusal is $REASON, want invalid_state"
  api POST "/chats/$CURSOR_CHAT_ID/archive" >/dev/null || fail "archiving the cursor chat failed"
  wait_chat "$CURSOR_CHAT_ID" archived
  CODE="$(api_status GET "/chats/$CURSOR_CHAT_ID/skills")"
  [[ "$CODE" == "409" ]] || fail "skills on an archived chat answered $CODE, want 409"
  REASON="$(jq -r .error.code < "$TMP/body.json")"
  [[ "$REASON" == "invalid_state" ]] || fail "the archived refusal is $REASON, want invalid_state"

  echo "== 12h. the CLI: the same names, and a leading / delivered byte for byte"
  CLI_NAMES="$("$VINCENT" chat skills "$SKILL_CHAT_ID" --json | jq -r '[.skills[].name] | join(",")')" \
    || fail "vincent chat skills failed"
  API_NAMES="$(api GET "/chats/$SKILL_CHAT_ID/skills" | jq -r '[.skills[].name] | join(",")')"
  [[ "$CLI_NAMES" == "$API_NAMES" ]] \
    || fail "the CLI lists [$CLI_NAMES], the API [$API_NAMES]"
  # The whole reason --message-file exists (task 124.5, issue #501): under Git
  # Bash MSYS rewrites an argv beginning with /name into a Windows path before
  # a native .exe sees it. A message read from stdin never passes through argv
  # at all, and this is the assertion the Windows leg of the matrix carries.
  printf '/gate-skill hi' | "$VINCENT" chat send "$SKILL_CHAT_ID" --message-file - >/dev/null \
    || fail "chat send --message-file - failed"
  PROMPT="$(api GET "/chats/$SKILL_CHAT_ID" | jq -r '.turns[] | select(.seq == 2) | .prompt')"
  [[ "$PROMPT" == "/gate-skill hi" ]] \
    || fail "the stored prompt is [$PROMPT], want [/gate-skill hi]"

  unset FAKEAGENT_SCENARIO
  unset FAKEAGENT_VERSION
}

if [[ "$ONLY" == "11" ]]; then
  chat_on_a_task
  echo "GATE PASS: m14 (leg 11)"
  exit 0
fi

if [[ "$ONLY" == "12" ]]; then
  chat_skills
  echo "GATE PASS: m14 (leg 12)"
  exit 0
fi

echo "== register the project"
PROJECT="$(api POST /projects -d "{\"path\": \"$(hostpath "$REPO")\"}")" \
  || fail "POST /v1/projects failed"
PROJECT_ID="$(printf '%s' "$PROJECT" | jq -r .id)"

echo "== 1. create a chat"
CHAT="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"gate talk\", \"agent\": \"claude\"}")" \
  || fail "POST /v1/chats failed"
CHAT_ID="$(printf '%s' "$CHAT" | jq -r .id)"
printf '%s' "$CHAT" | jq -e '.state == "idle" and (.branch | startswith("vincent/"))' >/dev/null \
  || fail "the created chat is wrong: $CHAT"
WORKTREE="$(printf '%s' "$CHAT" | jq -r .worktree_path)"
[[ -d "$WORKTREE" ]] || fail "the chat has no worktree at $WORKTREE"

echo "== 2. two turns, and the second remembers the first"
api POST "/chats/$CHAT_ID/send" \
  -d '{"message": "my favourite colour is heliotrope"}' >/dev/null \
  || fail "the first send failed"
STATE="$(wait_turn "$CHAT_ID" 1)"
[[ "$STATE" == "done" ]] || fail "turn 1 is $STATE, want done"
SESSION="$(api GET "/chats/$CHAT_ID" | jq -r .chat.session_id)"
[[ -n "$SESSION" && "$SESSION" != "null" ]] \
  || fail "the chat recorded no session id, so there is nothing to resume"

api POST "/chats/$CHAT_ID/send" -d '{"message": "what is my favourite colour?"}' >/dev/null \
  || fail "the second send failed"
STATE="$(wait_turn "$CHAT_ID" 2)"
[[ "$STATE" == "done" ]] || fail "turn 2 is $STATE, want done"
# The recall line is in the turn's durable record, which is what the new
# transcript route serves — so this assertion proves continuity and the route
# at once.
RECALL="$(curl -sS -H "Authorization: Bearer $TOKEN" \
  "$BASE/chats/$CHAT_ID/turns/2/transcript" | tr -d '\r')"
case "$RECALL" in
  *heliotrope*) ;;
  *) fail "turn 2 did not see turn 1; transcript: $RECALL" ;;
esac
case "$RECALL" in
  *recalled:*) ;;
  *) fail "turn 2 ran a fresh session rather than resuming one" ;;
esac

echo "== 2b. the same two turns on codex and cursor"
# Each adapter spells resume differently — claude and cursor take
# `--resume <id>`, codex an `exec --json resume <id>` subcommand — and each
# reports its id off a different stream shape. A daemon that dropped the id for
# one of them fails here by omission: a fresh session emits no recall line.
for AGENT in codex cursor; do
  OTHER="$(api POST /chats \
    -d "{\"project_id\": $PROJECT_ID, \"title\": \"$AGENT talk\", \"agent\": \"$AGENT\"}")" \
    || fail "POST /v1/chats on $AGENT failed"
  OTHER_ID="$(printf '%s' "$OTHER" | jq -r .id)"
  api POST "/chats/$OTHER_ID/send" \
    -d '{"message": "my favourite colour is heliotrope"}' >/dev/null \
    || fail "the first send on $AGENT failed"
  STATE="$(wait_turn "$OTHER_ID" 1)"
  [[ "$STATE" == "done" ]] || fail "$AGENT turn 1 is $STATE, want done"
  SESSION="$(api GET "/chats/$OTHER_ID" | jq -r .chat.session_id)"
  [[ -n "$SESSION" && "$SESSION" != "null" ]] \
    || fail "the $AGENT chat recorded no session id, so there is nothing to resume"

  api POST "/chats/$OTHER_ID/send" -d '{"message": "what is my favourite colour?"}' >/dev/null \
    || fail "the second send on $AGENT failed"
  STATE="$(wait_turn "$OTHER_ID" 2)"
  [[ "$STATE" == "done" ]] || fail "$AGENT turn 2 is $STATE, want done"
  RECALL="$(curl -sS -H "Authorization: Bearer $TOKEN" \
    "$BASE/chats/$OTHER_ID/turns/2/transcript" | tr -d '\r')"
  case "$RECALL" in
    *heliotrope*) ;;
    *) fail "$AGENT turn 2 did not see turn 1; transcript: $RECALL" ;;
  esac
  case "$RECALL" in
    *recalled:*) ;;
    *) fail "$AGENT turn 2 ran a fresh session rather than resuming one" ;;
  esac
  api POST "/chats/$OTHER_ID/archive" >/dev/null || fail "archiving the $AGENT chat failed"
done

echo "== 3. the per-turn transcript, and its offset seam"
HEADERS="$TMP/transcript.headers"
BODY1="$TMP/transcript1.ndjson"
curl -sS -D "$HEADERS" -o "$BODY1" -H "Authorization: Bearer $TOKEN" \
  "$BASE/chats/$CHAT_ID/turns/1/transcript" || fail "the transcript route failed"
[[ -s "$BODY1" ]] || fail "the turn 1 transcript is empty"
NEXT="$(tr -d '\r' < "$HEADERS" | awk 'tolower($1) == "x-next-offset:" { print $2 }')"
[[ "$NEXT" =~ ^[0-9]+$ ]] || fail "no X-Next-Offset on the transcript response"
BODY2="$TMP/transcript2.ndjson"
curl -sS -o "$BODY2" -H "Authorization: Bearer $TOKEN" \
  "$BASE/chats/$CHAT_ID/turns/1/transcript?offset=$NEXT" \
  || fail "the transcript resume failed"
# Nothing was appended after the turn ended, so resuming from the reported
# offset must return nothing at all. A non-empty body here is a seam that
# would double-print in a client.
[[ ! -s "$BODY2" ]] || fail "resuming at X-Next-Offset re-served $(wc -c < "$BODY2") bytes"
CODE="$(api_status GET "/chats/$CHAT_ID/turns/1/transcript?offset=1&tail=1")"
[[ "$CODE" == "400" ]] || fail "offset with tail answered $CODE, want 400"
CODE="$(api_status GET "/chats/$CHAT_ID/turns/99/transcript")"
[[ "$CODE" == "404" ]] || fail "an unknown turn answered $CODE, want 404"

echo "== 4. the per-chat stream carries this chat and no other"
OTHER="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"someone else\", \"agent\": \"claude\"}")" \
  || fail "creating the second chat failed"
OTHER_ID="$(printf '%s' "$OTHER" | jq -r .id)"
STREAM="$TMP/stream.sse"
curl -sN -H "Authorization: Bearer $TOKEN" "$BASE/chats/$CHAT_ID/events" > "$STREAM" &
STREAM_PID=$!
sleep 2
# Both of these happen; only one belongs on the stream above.
api POST "/chats/$OTHER_ID/archive" >/dev/null || fail "archiving the other chat failed"
api POST "/chats/$CHAT_ID/send" -d '{"message": "still there?"}' >/dev/null \
  || fail "the third send failed"
wait_turn "$CHAT_ID" 3 >/dev/null
sleep 2
kill "$STREAM_PID" 2>/dev/null || true
wait "$STREAM_PID" 2>/dev/null || true
# A multi-line jq capture, so `tr -d '\r'`: jq writes CRLF on Windows, $( )
# strips only the trailing one, and the interior CRs would fail the match below.
IDS="$(sed -n 's/^data: //p' "$STREAM" | jq -r 'select(.payload) | .payload.id' | sort -u | tr -d '\r')"
case "$IDS" in
  *"$OTHER_ID"*) fail "another chat's events arrived on chat $CHAT_ID's stream" ;;
esac
printf '%s\n' "$IDS" | grep -x "$CHAT_ID" >/dev/null \
  || fail "this chat's own events never arrived on its stream: $IDS"

# Task 071 (issue #282): a chat's live-output chunks carry §13.3's typed names
# and normalized fields, with the agent's verbatim line kept beside them. Both
# halves are asserted here because both are the contract: a client renders the
# normalized fields, and `raw` stayed so nothing that read it reads less.
# Multi-line captures, so `tr -d '\r'` — jq writes CRLF on Windows.
NAMES="$(sed -n 's/^event: //p' "$STREAM" | tr -d '\r')"
CHUNK_TYPES="$(grep '^agent\.' <<<"$NAMES" | sort -u || true)"
[ -n "$CHUNK_TYPES" ] \
  || fail "the chat stream carried no §13.3 chunk types at all: $NAMES"
grep -x 'agent.output' <<<"$CHUNK_TYPES" >/dev/null \
  || fail "no agent.output chunk on the chat stream; got: $CHUNK_TYPES"
if grep -x 'output' <<<"$NAMES" >/dev/null; then
  fail "the pre-071 catch-all 'output' chunk type is still published"
fi
# One chunk carrying both a normalized field and the raw line is the whole
# claim; the transcript route is what a client refetches it from.
BOTH="$(sed -n 's/^data: //p' "$STREAM" \
  | jq -r 'select(.turn_id != null and .text != null and .text != "" and .raw != null and .raw != "") | .turn_id' \
  | sort -u | tr -d '\r')"
[ -n "$BOTH" ] \
  || fail "no chat chunk carried both a normalized field and its raw line"

echo "== 5. an awaiting_input chat is answered over the API"
"$VINCENT" daemon stop --force >/dev/null 2>&1 || true
export FAKEAGENT_SCENARIO=ask-question
"$VINCENT" daemon start
PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
TOKEN="$(cat "$DATA_DIR/token")"
BASE="http://127.0.0.1:$PORT/v1"

ASK="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"a question\", \"agent\": \"claude\"}")" \
  || fail "creating the question chat failed"
ASK_ID="$(printf '%s' "$ASK" | jq -r .id)"
api POST "/chats/$ASK_ID/send" -d '{"message": "ask me something"}' >/dev/null \
  || fail "the send failed"
wait_chat "$ASK_ID" awaiting_input
QUESTION="$(api GET "/chats/$ASK_ID" | jq -r '.chat.pending_input.Questions[0].Text // .chat.pending_input.questions[0].text')"
[[ -n "$QUESTION" ]] || fail "the parked chat carries no question"

echo "== 6. the cap refuses rather than queues"
CAP="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"over the cap\", \"agent\": \"claude\"}")" \
  || fail "creating the cap chat failed"
CAP_ID="$(printf '%s' "$CAP" | jq -r .id)"
CODE="$(api_status POST "/chats/$CAP_ID/send" -d '{"message": "me too"}')"
[[ "$CODE" == "409" ]] || fail "a send over max_parallel_chats answered $CODE, want 409"
REASON="$(jq -r .error.code < "$TMP/body.json")"
[[ "$REASON" == "chat_cap_reached" ]] || fail "the refusal is $REASON, want chat_cap_reached"
TURNS="$(api GET "/chats/$CAP_ID" | jq '.turns | length')"
[[ "$TURNS" == "0" ]] || fail "a refused send left $TURNS turn rows behind"

api POST "/chats/$ASK_ID/answer" \
  -d "{\"answers\": {\"$QUESTION\": [\"blue\"]}}" >/dev/null \
  || fail "POST /v1/chats/{id}/answer failed"
wait_turn "$ASK_ID" 1 >/dev/null
wait_chat "$ASK_ID" idle

echo "== 7. cancel stops a live turn; archive removes the worktree"
unset FAKEAGENT_SCENARIO
export FAKEAGENT_SCENARIO=hang
"$VINCENT" daemon stop --force >/dev/null 2>&1 || true
"$VINCENT" daemon start
PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
TOKEN="$(cat "$DATA_DIR/token")"
BASE="http://127.0.0.1:$PORT/v1"

api POST "/chats/$CAP_ID/send" -d '{"message": "run forever"}' >/dev/null \
  || fail "the hanging send failed"
wait_chat "$CAP_ID" running
api POST "/chats/$CAP_ID/cancel" >/dev/null || fail "POST /v1/chats/{id}/cancel failed"
STATE="$(wait_turn "$CAP_ID" 1)"
[[ "$STATE" == "failed" ]] || fail "a cancelled turn is $STATE, want failed"
wait_chat "$CAP_ID" idle

CAP_WORKTREE="$(api GET "/chats/$CAP_ID" | jq -r .chat.worktree_path)"
api POST "/chats/$CAP_ID/archive" >/dev/null || fail "POST /v1/chats/{id}/archive failed"
wait_chat "$CAP_ID" archived
[[ ! -d "$CAP_WORKTREE" ]] || fail "the archived chat's worktree is still at $CAP_WORKTREE"

echo "== 8. chats are not tasks"
TASKS="$(api GET /tasks | jq 'if type == "array" then length else (.tasks | length) end')"
[[ "$TASKS" == "0" ]] || fail "GET /v1/tasks lists $TASKS rows on a daemon with only chats"

echo "== 9. handoff: a task adopts the chat's worktree and branch (task 074)"
unset FAKEAGENT_SCENARIO
HAND="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"an exploration\", \"agent\": \"claude\"}")" \
  || fail "creating the handoff chat failed"
HAND_ID="$(printf '%s' "$HAND" | jq -r .id)"
HAND_WORKTREE="$(printf '%s' "$HAND" | jq -r .worktree_path)"
HAND_BRANCH="$(printf '%s' "$HAND" | jq -r .branch)"
HAND_BASE="$(printf '%s' "$HAND" | jq -r .base_branch)"
[[ -d "$HAND_WORKTREE" ]] || fail "the handoff chat has no worktree at $HAND_WORKTREE"

# Uncommitted work, written the way every gate writes a file: `git config -f`
# is in the sh∩pwsh intersection, and this is the change the handoff must
# preserve without committing it.
git -C "$HAND_WORKTREE" config -f explored.ini gate.explored yes \
  || fail "seeding the dirty file failed"
git -C "$HAND_WORKTREE" commit --allow-empty -m "chat work" >/dev/null 2>&1 \
  || fail "committing on the chat branch failed"
HAND_HEAD="$(git -C "$HAND_WORKTREE" rev-parse HEAD)"

HANDOFF="$(api POST "/chats/$HAND_ID/handoff" \
  -d '{"title": "finish the exploration"}')" || fail "POST /v1/chats/{id}/handoff failed"
TASK_ID="$(printf '%s' "$HANDOFF" | jq -r .task.id)"
[[ "$TASK_ID" != "null" && -n "$TASK_ID" ]] || fail "the handoff returned no task: $HANDOFF"

# The inheritance, field by field, off the task the daemon actually stored.
TASK="$(api GET "/tasks/$TASK_ID")"
for pair in "worktree_path:$HAND_WORKTREE" "branch_name:$HAND_BRANCH" "base_branch:$HAND_BASE"; do
  FIELD="${pair%%:*}"
  WANT="${pair#*:}"
  GOT="$(printf '%s' "$TASK" | jq -r ".$FIELD")"
  [[ "$GOT" == "$WANT" ]] || fail "the task's $FIELD is $GOT, want the chat's $WANT"
done
[[ "$(printf '%s' "$TASK" | jq -r .source_chat_id)" == "$HAND_ID" ]] \
  || fail "the task does not link back to chat $HAND_ID"

# The workspace is untouched: no new commit, and the uncommitted file is still
# uncommitted and still there.
[[ "$(git -C "$HAND_WORKTREE" rev-parse HEAD)" == "$HAND_HEAD" ]] \
  || fail "the handoff moved HEAD in $HAND_WORKTREE"
[[ "$(git -C "$HAND_WORKTREE" config -f explored.ini --get gate.explored)" == "yes" ]] \
  || fail "the uncommitted file did not survive the handoff"
STATUS="$(git -C "$HAND_WORKTREE" status --porcelain)"
[[ -n "$STATUS" ]] || fail "the handoff committed the dirty worktree"

# The chat is terminal, links the task, and has released its claim.
wait_chat "$HAND_ID" handed_off
HAND_AFTER="$(api GET "/chats/$HAND_ID" | jq .chat)"
[[ "$(printf '%s' "$HAND_AFTER" | jq -r .handoff_task_id)" == "$TASK_ID" ]] \
  || fail "the chat does not link task $TASK_ID"
[[ "$(printf '%s' "$HAND_AFTER" | jq -r '.worktree_path // ""')" == "" ]] \
  || fail "the handed-off chat still claims a worktree"

# Terminal means terminal: every chat action is a 409, archive included, so
# chat cleanup can never reach the task's worktree.
for ROUTE in handoff send cancel archive; do
  BODY='{}'
  if [[ "$ROUTE" == "send" ]]; then BODY='{"message": "still there?"}'; fi
  if [[ "$ROUTE" == "handoff" ]]; then BODY='{"title": "again"}'; fi
  CODE="$(api_status POST "/chats/$HAND_ID/$ROUTE" -d "$BODY")"
  [[ "$CODE" == "409" ]] || fail "$ROUTE on a handed-off chat is $CODE, want 409"
done
[[ -d "$HAND_WORKTREE" ]] || fail "a refused archive removed the task's worktree"

# And the ownership claim gc sees: the inherited directory is not an orphan.
ORPHANS="$(api GET /info | jq -r '.orphans // 0')"
[[ "$ORPHANS" == "0" ]] || fail "the handoff left $ORPHANS orphan(s) behind"

echo "== 10. the listing hides terminal chats, and ?archived= brings them back (issue #298)"
# Two terminal chats exist by now and neither may be in the default listing:
# $CAP_ID was archived in leg 7 and $HAND_ID was handed off in leg 9. Each
# capture is a single line of jq output, so no `tr -d '\r'` is needed here.
listed() { # listed QUERY ID -> true|false
  api GET "/chats$1" | jq -r --argjson id "$2" 'any(.chats[]; .id == $id)'
}
for ID in "$CAP_ID" "$HAND_ID"; do
  for QUERY in "" "?archived=false"; do
    [[ "$(listed "$QUERY" "$ID")" == "false" ]] \
      || fail "GET /v1/chats$QUERY still lists terminal chat $ID"
  done
  [[ "$(listed "?archived=true" "$ID")" == "true" ]] \
    || fail "archived=true does not list terminal chat $ID"
  [[ "$(listed "?archived=all" "$ID")" == "true" ]] \
    || fail "archived=all does not list terminal chat $ID"
done
# A live chat is the other half: the default listing keeps it, and asking for
# the terminal ones alone does not.
LIVE="$(api POST /chats \
  -d "{\"project_id\": $PROJECT_ID, \"title\": \"still talking\", \"agent\": \"claude\"}")" \
  || fail "creating the live chat failed"
LIVE_ID="$(printf '%s' "$LIVE" | jq -r .id)"
[[ "$(listed "" "$LIVE_ID")" == "true" ]] || fail "the default listing dropped idle chat $LIVE_ID"
[[ "$(listed "?archived=true" "$LIVE_ID")" == "false" ]] \
  || fail "archived=true lists idle chat $LIVE_ID"
# An explicit state wins over the default, exactly as it does for tasks.
[[ "$(listed "?state=archived" "$CAP_ID")" == "true" ]] \
  || fail "state=archived does not list archived chat $CAP_ID"
CODE="$(api_status GET "/chats?archived=yes")"
[[ "$CODE" == "400" ]] || fail "GET /v1/chats?archived=yes is $CODE, want 400"

if [[ -z "$ONLY" ]]; then
  chat_on_a_task
  chat_skills
fi

echo "GATE PASS: m14"
