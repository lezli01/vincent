#!/usr/bin/env bash
# M12 phase gate (task 061; spec §16, §20): prove against a real container
# runtime that a task's steps run inside one container, and that the container
# is created, kept and removed at the moments the spec says.
#
#   1. a containerized task runs a body that can only succeed inside the image,
#      exactly one container carries its label, and archiving removes it
#   2. `container.image: ""` — the default — runs on the host and creates no
#      container at all, which is the regression that matters most
#   3. a step timeout stops the process inside the container and the task's
#      container **survives**, so a retry finds what an earlier step installed
#   4. a daemon killed mid-step leaves no container behind: recovery removes it
#   5. `container.network: false` with `mcp.wire_steps: true` is refused at task
#      creation with a 400 naming both keys
#
# It **skips cleanly** (exit 0, one line saying why) on a host that cannot run
# the feature. CI runs its assertions on the Linux leg only, and the two skips
# are not the same skip:
#
#   - macOS: the GitHub runner has no docker daemon, so `docker info` fails.
#   - Windows: the runner *does* ship docker, and its daemon answers — but in
#     Windows-container mode, where `FROM alpine:3` fails with "no matching
#     manifest for windows/amd64". A daemon probe alone therefore does not skip
#     there, and the gate has to say so itself. It would be a pointless run in
#     any case: a Windows daemon refuses containerized tasks at creation
#     (decision 2), so there is nothing on that platform for this to assert.
#
# That is a real coverage gap and it is stated rather than implied: a gate that
# has never run on a platform is not known to pass there.
#
# No agent CLI is involved: the steps are `command` steps, so the gate is as
# fast on CI as it is locally. Their `run:` bodies are the one place in this
# repository that are **not** held to the sh∩pwsh intersection, because the
# feature under test is precisely that a containerized body executes under the
# image's /bin/sh (§8.3's inverse). The one uncontainerized task's body is held
# to the intersection as usual.
#
# Requirements: bash, go, git, curl, jq, docker.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Before the runtime probes, because it is a statement about the daemon under
# test and not about docker: a Windows daemon refuses a containerized task at
# creation (decision 2), so every assertion below is unreachable there.
if [[ "${OS:-}" == "Windows_NT" ]]; then
  echo "SKIP: a windows daemon refuses containerized tasks (decision 2); the container gate needs a posix host"
  exit 0
fi

DOCKER="${VINCENT_GATE_RUNTIME:-docker}"
if ! command -v "$DOCKER" >/dev/null 2>&1; then
  echo "SKIP: $DOCKER is not installed; the container gate needs a real runtime"
  exit 0
fi
if ! "$DOCKER" info >/dev/null 2>&1; then
  echo "SKIP: $DOCKER is installed but no daemon answered; the container gate needs a real runtime"
  exit 0
fi
# A daemon that answered still may not run the gate's image. Docker in
# Windows-container mode is the case that turned up in CI: it answers `info`
# and then fails `FROM alpine:3` with "no matching manifest for windows/amd64".
OSTYPE_OF_RUNTIME="$("$DOCKER" info --format '{{.OSType}}' 2>/dev/null || true)"
if [[ "$OSTYPE_OF_RUNTIME" != "linux" ]]; then
  echo "SKIP: $DOCKER runs ${OSTYPE_OF_RUNTIME:-unknown} containers; the container gate needs linux images"
  exit 0
fi

# `pwd -P` matters and is not tidiness: on macOS `$TMPDIR` lives under `/var`,
# which is a symlink to `/private/var`. git resolves a worktree's `gitdir:`
# pointer to the physical path, and mounting the logical one leaves that
# pointer naming a directory the container does not have. The same trap waits
# for a project whose own path runs through a symlink — decision 2's "identical
# inside and out" is about the *physical* path, and the configuration reference
# says so.
TMP="$(cd "$(mktemp -d)" && pwd -P)"
BIN="$TMP/bin"
IMAGE="vincent-m12-gate:$$"

VINCENT="$BIN/vincent"
if [[ "${OS:-}" == "Windows_NT" ]]; then
  VINCENT+=".exe"
fi

fail() { echo "GATE FAIL: $*" >&2; exit 1; }

cleanup() {
  "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  # Any container this gate created, by the label the daemon stamps on them.
  local leftovers
  leftovers="$("$DOCKER" ps -aq --filter "label=$LABEL_KEY" 2>/dev/null || true)"
  [[ -n "$leftovers" ]] && "$DOCKER" rm -f $leftovers >/dev/null 2>&1
  "$DOCKER" rmi -f "$IMAGE" >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
LABEL_KEY="com.vincent.task"
trap cleanup EXIT

hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

echo "== build vincent"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent)

echo "== build the gate image (alpine + git)"
# The image is the user's, and this is the smallest honest stand-in for one: a
# base with git on it. Nothing vincent ships is in it, which is the point.
printf 'FROM alpine:3\nRUN apk add --no-cache git\n' > "$TMP/Dockerfile"
"$DOCKER" build -q -t "$IMAGE" "$TMP" >/dev/null || fail "could not build the gate image"

CONFIG_DIR="$TMP/config"
DATA_DIR="$TMP/data"
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
export VINCENT_CONFIG_DIR
VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
export VINCENT_DATA_DIR
VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"

PORT="" TOKEN="" BASE=""
daemon_up() {
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
}
daemon_down() { "$VINCENT" daemon stop --force >/dev/null 2>&1 || true; }

api() { # api METHOD PATH [JSON_BODY]
  local method="$1" path="$2" body="${3:-}" out status
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  [[ "$status" == 2* ]] || fail "$method $path -> HTTP $status: $out"
  printf '%s' "$out"
}

# api_status prints "STATUS<newline>BODY" without failing on a 4xx, for the
# scenario whose whole point is the 400.
api_status() { # api_status METHOD PATH [JSON_BODY]
  local method="$1" path="$2" body="${3:-}" out
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  printf '%s\n%s' "${out##*$'\n'}" "${out%$'\n'*}"
}

make_repo() { # make_repo PATH
  git init -q -b main "$1"
  git -C "$1" config user.name gate
  git -C "$1" config user.email gate@example.invalid
  git -C "$1" config commit.gpgsign false
  printf 'gate repo\n' > "$1/README.md"
  git -C "$1" add . && git -C "$1" commit -qm init
}

register_project() { api POST /projects \
  "$(jq -cn --arg p "$(hostpath "$1")" '{path: $p}')" | jq -r .id; }

write_workflow() { # write_workflow NAME YAML
  mkdir -p "$CONFIG_DIR/workflows"
  printf '%s' "$2" > "$CONFIG_DIR/workflows/$1.yaml"
}

write_config() { # write_config YAML
  printf '%s' "$1" > "$CONFIG_DIR/config.yaml"
}

create_task() { # create_task PROJECT_ID WORKFLOW TITLE
  api POST /tasks "$(jq -cn --argjson p "$1" --arg w "$2" --arg t "$3" \
    '{project_id: $p, workflow: $w, title: $t}')" | jq -r .id
}

wait_for_state() { # wait_for_state TASK_ID STATE TRIES
  local id="$1" want="$2" tries="$3" state=""
  for _ in $(seq 1 "$tries"); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "aborted" ]] && [[ "$want" != "aborted" ]]; then
      api GET "/tasks/$id" | jq . >&2
      fail "task $id reached $state while waiting for $want"
    fi
    sleep 1
  done
  api GET "/tasks/$id" | jq . >&2
  fail "task $id never reached $want (last: $state)"
}

# containers_for prints the ids of every container carrying a task's label,
# running or not. A count is the assertion: "one container per task" is the
# issue's own decision 1, and two would mean a container per step.
containers_for() { # containers_for TASK_ID
  "$DOCKER" ps -aq --filter "label=$LABEL_KEY=$1"
}

count_containers_for() { # count_containers_for TASK_ID
  local ids
  ids="$(containers_for "$1")"
  [[ -z "$ids" ]] && { printf '0\n'; return; }
  printf '%s\n' "$ids" | wc -l | tr -d ' '
}

echo "== scenario 1: a containerized task runs in the image, in one container"
REPO="$TMP/repo"
make_repo "$REPO"
write_config "container:
  image: $IMAGE
mcp:
  wire_steps: false
"
# `cat /etc/alpine-release` is the assertion: it exists in the gate image and
# on neither CI host, so a step that succeeds ran inside the container. The
# second body proves the worktree mount is the same directory on both sides —
# the commit it makes is read back through git on the host afterwards.
write_workflow contained 'name: contained
steps:
  - id: prove-image
    type: command
    run: cat /etc/alpine-release
  - id: commit
    type: command
    run: git commit --allow-empty -m m12-gate-ran-in-container
'
# Every workflow this gate uses is written before the daemon starts. The
# registry reloads on change, but a task created in the same second as the file
# lands can beat the watcher, and a gate must not assert on that race.
#
# `onhost` pins `image: ""` in its own defaults, which is the documented way to
# force one workflow onto the host while the daemon is containerized — it
# exercises the per-field merge as well as the default.
write_workflow onhost 'name: onhost
defaults:
  container:
    image: ""
steps:
  - id: commit
    type: command
    run: git commit --allow-empty -m m12-gate-ran-on-host
'
write_workflow slow 'name: slow
steps:
  - id: hang
    type: command
    timeout: 5s
    max_retries: 0
    run: sleep 300
'
daemon_up
PROJECT="$(register_project "$REPO")"
TASK="$(create_task "$PROJECT" contained "contained task")"
wait_for_state "$TASK" done 120

COUNT="$(count_containers_for "$TASK")"
[[ "$COUNT" == 1 ]] || fail "task $TASK has $COUNT containers, want exactly 1"

NAME="$("$DOCKER" inspect --format '{{.Name}}' "$(containers_for "$TASK")" | tr -d '/')"
[[ "$NAME" == "vincent-task-$TASK" ]] || fail "container is named $NAME, want vincent-task-$TASK"

# The commit the containerized step made is a real commit on the host's disk,
# which is what "the worktree is the same directory on both sides" means.
BRANCH="$(api GET "/tasks/$TASK" | jq -r .branch_name)"
LOG="$(git -C "$REPO" log --format=%s "$BRANCH")"
grep -qx m12-gate-ran-in-container <<<"$LOG" \
  || fail "the containerized step's commit is not on $BRANCH: $LOG"

api POST "/tasks/$TASK/archive" '{}' >/dev/null
for _ in $(seq 1 30); do
  [[ "$(count_containers_for "$TASK")" == 0 ]] && break
  sleep 1
done
[[ "$(count_containers_for "$TASK")" == 0 ]] || fail "archiving task $TASK left its container behind"
echo "   ok: one container, named for the task, gone with the worktree"

echo "== scenario 2: container.image unset runs on the host and creates nothing"
HOST_TASK="$(create_task "$PROJECT" onhost "host task")"
wait_for_state "$HOST_TASK" done 120
[[ "$(count_containers_for "$HOST_TASK")" == 0 ]] \
  || fail "an uncontainerized task created a container"
echo "   ok: no runtime consulted, no container created"

echo "== scenario 3: a step timeout stops the process and the container survives"
SLOW_TASK="$(create_task "$PROJECT" slow "slow task")"
wait_for_state "$SLOW_TASK" blocked 120
REASON="$(api GET "/tasks/$SLOW_TASK" | jq -r .block_reason)"
[[ "$REASON" == timeout ]] || fail "task $SLOW_TASK blocked $REASON, want timeout"
[[ "$(count_containers_for "$SLOW_TASK")" == 1 ]] \
  || fail "the step timeout removed the task's container; a retry would lose its state"
RUNNING="$("$DOCKER" inspect --format '{{.State.Running}}' "$(containers_for "$SLOW_TASK")")"
[[ "$RUNNING" == true ]] || fail "the task's container is not running after a step timeout"
# And nothing is left of the step itself: the pid file's process is gone, so
# `sleep 300` is not still burning inside the container.
LEFT="$("$DOCKER" exec "$(containers_for "$SLOW_TASK")" sh -c 'ps -o args= | grep -c "[s]leep 300"' || true)"
[[ "${LEFT:-0}" == 0 ]] || fail "the timed-out process is still running inside the container"
echo "   ok: process stopped, container kept"

echo "== scenario 4: a daemon killed mid-step leaves no container behind"
KILL_TASK="$(create_task "$PROJECT" slow "killed task")"
wait_for_state "$KILL_TASK" running 120
for _ in $(seq 1 30); do
  [[ "$(count_containers_for "$KILL_TASK")" == 1 ]] && break
  sleep 1
done
[[ "$(count_containers_for "$KILL_TASK")" == 1 ]] || fail "task $KILL_TASK never got a container"
# The orphan is identified *before* the kill, and the assertion below is about
# this id and not about a count. Recovery re-runs the interrupted step as an
# attempt that does not consume a retry, and that admission creates the task a
# fresh container under the same name — so "no container for this task" is true
# only in the window between the removal and the re-creation, and polling for
# it is a race that lost about one run in three. A container id is never
# reused, so "this one is gone" is a settled fact instead of a moment.
ORPHAN="$(containers_for "$KILL_TASK")"
DAEMON_PID="$(jq -r .pid "$DATA_DIR/daemon.json")"
kill -9 "$DAEMON_PID" 2>/dev/null || fail "could not kill the daemon"
sleep 2
daemon_up
for _ in $(seq 1 30); do
  "$DOCKER" inspect "$ORPHAN" >/dev/null 2>&1 || break
  sleep 1
done
if "$DOCKER" inspect "$ORPHAN" >/dev/null 2>&1; then
  fail "recovery left task $KILL_TASK's container $ORPHAN behind"
fi
echo "   ok: recovery removed the orphaned container"

echo "== scenario 5: no network with wired MCP is refused at creation"
write_config "container:
  image: $IMAGE
  network: false
mcp:
  wire_steps: true
"
daemon_down
daemon_up
OUT="$(api_status POST /tasks \
  "$(jq -cn --argjson p "$PROJECT" '{project_id: $p, workflow: "contained", title: "no network"}')")"
STATUS="${OUT%%$'\n'*}"
BODY="${OUT#*$'\n'}"
[[ "$STATUS" == 400 ]] || fail "a no-network containerized task returned HTTP $STATUS: $BODY"
MESSAGE="$(printf '%s' "$BODY" | jq -r .error.message)"
grep -q container.network <<<"$MESSAGE" || fail "the refusal does not name container.network: $MESSAGE"
grep -q mcp.wire_steps <<<"$MESSAGE" || fail "the refusal does not name mcp.wire_steps: $MESSAGE"
echo "   ok: 400 naming both keys"

daemon_down
echo "GATE PASS: m12 (container step execution)"
