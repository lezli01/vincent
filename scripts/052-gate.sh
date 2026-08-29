#!/usr/bin/env bash
# Task 052 gate (§13.2, §12.3): prove via curl alone that vincent can list a
# GitHub project's open pull requests, that its reconciler links one to the
# task whose branch matches, that a human can link and unlink by hand, and
# that the unlink survives the next reconciler tick.
#
#   1. the listing: open only, newest first, with the merged row absent —
#      and the capability probe saying yes before it
#   2. the reconciler's auto-link on a head-branch match, and the task DTO
#      and GET /tasks/{id}/github/pull agreeing about it
#   3. the human link: POST names a different number and wins over `auto`
#   4. the sticky unlink: DELETE, then a second reconciler tick, and the link
#      is still gone
#   5. the compare URL for a task with a branch and no link — built, and
#      never fetched: the fake `gh` records every call it is asked to make,
#      and none of them is for this
#
# Task-numbered rather than `mN` because this is not a §19 milestone; the
# `docs/gates/` records are already task-numbered (017, 032).
#
# No agent CLI is involved: every task here is created and then left where it
# is. What is exercised is the GitHub half, against cmd/fakegh installed as
# `gh` on the daemon's PATH — there is no `gh_path` config key, and the
# daemon resolves `gh` from PATH when GHPath is empty.
#
# The gate observes the rules CLAUDE.md records: no `grep -q` on a pipe
# (capture first, match a here-string), `| tr -d '\r'` on multi-line `jq`
# captures, and committed executable.
#
# Requirements: bash, go, git, curl, jq.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
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
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent)
# cmd/fakegh is built *as* `gh`, so the daemon's PATH lookup finds it exactly
# the way it would find the real CLI. Nothing else about the daemon changes.
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/gh$EXE" ./cmd/fakegh)
PATH="$BIN:$PATH"
export PATH

# GH_ARGV is where the fake records every call it was asked to make, so the
# "built, never fetched" assertion is about observed process behaviour rather
# than about reading the daemon's source.
GH_ARGV="$TMP/gh-argv.txt"
export FAKEGH_ARGV_FILE="$(hostpath "$GH_ARGV")"
export FAKEGH_SCENARIO=success

CONFIG_DIR="" DATA_DIR=""
scenario_dirs() { # scenario_dirs NAME
  CONFIG_DIR="$TMP/$1/config"
  DATA_DIR="$TMP/$1/data"
  mkdir -p "$CONFIG_DIR" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
  : > "$GH_ARGV"
}

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

# The gate's projects need a github.com origin: that is the whole of what
# makes a project a GitHub project (there is no stored flag — §13.2).
make_repo() { # make_repo PATH
  git init -q -b main "$1"
  git -C "$1" config user.name gate
  git -C "$1" config user.email gate@example.invalid
  git -C "$1" config commit.gpgsign false
  printf 'gate repo\n' > "$1/README.md"
  git -C "$1" add . && git -C "$1" commit -qm init
  git -C "$1" remote add origin https://github.com/octo/repo.git
}

register_project() { api POST /projects \
  "$(jq -cn --arg p "$(hostpath "$1")" '{path: $p}')" | jq -r .id; }

write_workflow() { # write_workflow NAME YAML
  mkdir -p "$CONFIG_DIR/workflows"
  printf '%s' "$2" > "$CONFIG_DIR/workflows/$1.yaml"
}

# The gate's one workflow. A single command step, so no agent CLI is involved
# and the task settles in seconds — what the gate needs from it is the branch
# the worktree was made on, which is what a pull request's head is matched
# against. `exit 0` is the whole body, which is what makes it portable to the
# daemon's pwsh on Windows (§8.3).
GATE_WORKFLOW='name: gate
steps:
  - id: noop
    type: command
    run: exit 0
'

create_task() { # create_task PROJECT_ID TITLE -> id
  api POST /tasks "$(jq -cn --argjson p "$1" --arg t "$2" \
    '{project_id: $p, workflow: "gate", title: $t, description: "gate task"}')" | jq -r .id
}

wait_for_branch() { # wait_for_branch TASK_ID -> branch
  local id="$1" branch=""
  for _ in $(seq 1 30); do
    branch="$(api GET "/tasks/$id" | jq -r .branch_name)"
    [[ -n "$branch" && "$branch" != "null" ]] && { printf '%s' "$branch"; return 0; }
    sleep 1
  done
  fail "task $id never got a branch"
}

# gh_calls is every argv the fake recorded, as one string safe to match a
# here-string against. jq is not involved, so no CRLF dance is needed here —
# but the file is written by a Go program on Windows, so strip anyway.
gh_calls() { tr -d '\r' < "$GH_ARGV"; }

run_scenario() { # run_scenario N NAME
  [[ -z "$ONLY" || "$ONLY" == "$1" ]]
}

# ---------------------------------------------------------------- scenario 1
# The probe answers yes, and the listing is open-only.
if run_scenario 1; then
  echo "== scenario 1: the capability probe and the open-only listing"
  scenario_dirs s1
  make_repo "$TMP/s1/repo"
  write_workflow gate "$GATE_WORKFLOW"
  daemon_up
  pid="$(register_project "$TMP/s1/repo")"

  probe="$(api GET "/projects/$pid/github")"
  [[ "$(jq -r .available <<<"$probe")" == "true" ]] \
    || fail "the probe says unavailable for a github.com origin: $probe"
  [[ "$(jq -r .repo <<<"$probe")" == "octo/repo" ]] \
    || fail "the probe named repo $(jq -r .repo <<<"$probe"), want octo/repo"

  pulls="$(api GET "/projects/$pid/github/pulls")"
  numbers="$(jq -r '.[].number' <<<"$pulls" | tr -d '\r')"
  [[ "$numbers" == $'412\n401' ]] \
    || fail "listing returned numbers $(printf '%s' "$numbers" | tr '\n' ' '), want 412 401"

  # The merged row is in the corpus and must not be in an open listing: it is
  # what the durable link exists to serve, through the task route instead.
  merged="$(jq -r '[.[] | select(.merged)] | length' <<<"$pulls")"
  [[ "$merged" == "0" ]] || fail "an open-only listing carried $merged merged rows"

  draft="$(jq -r '[.[] | select(.draft)] | length' <<<"$pulls")"
  [[ "$draft" == "1" ]] || fail "the draft row is missing from the listing"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 2
# The reconciler links a pull request to the task whose branch it heads.
if run_scenario 2; then
  echo "== scenario 2: the reconciler's auto-link on a head-branch match"
  scenario_dirs s2
  make_repo "$TMP/s2/repo"
  write_workflow gate "$GATE_WORKFLOW"
  # A short poll interval so the tick happens inside the gate's patience.
  printf 'github:\n  enabled: true\n  poll_interval: 1s\n' > "$CONFIG_DIR/config.yaml"
  daemon_up
  pid="$(register_project "$TMP/s2/repo")"
  tid="$(create_task "$pid" "Add a thing")"
  branch="$(wait_for_branch "$tid")"

  # Point the fake's first pull request at this task's branch and let the
  # reconciler tick.
  daemon_down
  export FAKEGH_PR_BRANCH="$branch"
  daemon_up

  linked=""
  for _ in $(seq 1 30); do
    linked="$(api GET "/tasks/$tid/github/pull" | jq -r .number)"
    [[ "$linked" == "412" ]] && break
    sleep 1
  done
  [[ "$linked" == "412" ]] || fail "the reconciler never linked #412 (last: $linked)"

  row="$(api GET "/tasks/$tid/github/pull")"
  [[ "$(jq -r .source <<<"$row")" == "auto" ]] \
    || fail "link source is $(jq -r .source <<<"$row"), want auto"
  [[ "$(jq -r .pull.title <<<"$row")" == "Add a thing" ]] \
    || fail "the live pull request did not come back with the link: $row"

  # And the listing agrees about who claims the row.
  claim="$(api GET "/projects/$pid/github/pulls" | jq -r '.[] | select(.number == 412) | .task_id')"
  [[ "$claim" == "$tid" ]] || fail "the listing says task $claim claims #412, want $tid"

  unset FAKEGH_PR_BRANCH
  daemon_down
fi

# ---------------------------------------------------------------- scenario 3
# A human names a different number, and it wins.
if run_scenario 3; then
  echo "== scenario 3: the human link"
  scenario_dirs s3
  make_repo "$TMP/s3/repo"
  write_workflow gate "$GATE_WORKFLOW"
  daemon_up
  pid="$(register_project "$TMP/s3/repo")"
  tid="$(create_task "$pid" "Rework the board header")"

  api POST "/tasks/$tid/github/pull" '{"number": 401}' > /dev/null
  row="$(api GET "/tasks/$tid/github/pull")"
  [[ "$(jq -r .number <<<"$row")" == "401" ]] \
    || fail "the human link did not take: $row"
  [[ "$(jq -r .source <<<"$row")" == "human" ]] \
    || fail "link source is $(jq -r .source <<<"$row"), want human"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 4
# The unlink is sticky: the next reconciler tick does not undo it.
if run_scenario 4; then
  echo "== scenario 4: the sticky unlink"
  scenario_dirs s4
  make_repo "$TMP/s4/repo"
  write_workflow gate "$GATE_WORKFLOW"
  printf 'github:\n  enabled: true\n  poll_interval: 1s\n' > "$CONFIG_DIR/config.yaml"
  daemon_up
  pid="$(register_project "$TMP/s4/repo")"
  tid="$(create_task "$pid" "Ship the thing")"
  branch="$(wait_for_branch "$tid")"

  daemon_down
  export FAKEGH_PR_BRANCH="$branch"
  daemon_up

  linked=""
  for _ in $(seq 1 30); do
    linked="$(api GET "/tasks/$tid/github/pull" | jq -r .number)"
    [[ "$linked" == "412" ]] && break
    sleep 1
  done
  [[ "$linked" == "412" ]] || fail "the reconciler never linked #412 (last: $linked)"

  api DELETE "/tasks/$tid/github/pull" > /dev/null
  row="$(api GET "/tasks/$tid/github/pull")"
  [[ "$(jq -r .linked <<<"$row")" == "false" ]] || fail "DELETE left the link in place: $row"

  # Several ticks' worth of patience: the point is that it never comes back.
  sleep 5
  row="$(api GET "/tasks/$tid/github/pull")"
  [[ "$(jq -r .linked <<<"$row")" == "false" ]] \
    || fail "the reconciler re-applied a link a human removed: $row"
  [[ "$(jq -r .suppressed <<<"$row")" == "true" ]] \
    || fail "the refusal was not recorded as suppressed: $row"

  unset FAKEGH_PR_BRANCH
  daemon_down
fi

# ---------------------------------------------------------------- scenario 5
# The compare URL is built and never fetched.
if run_scenario 5; then
  echo "== scenario 5: the compare URL is built, never fetched"
  scenario_dirs s5
  make_repo "$TMP/s5/repo"
  write_workflow gate "$GATE_WORKFLOW"
  daemon_up
  pid="$(register_project "$TMP/s5/repo")"
  tid="$(create_task "$pid" "Add a thing")"
  wait_for_branch "$tid" > /dev/null

  : > "$GH_ARGV"
  row="$(api GET "/tasks/$tid/github/pull")"
  [[ "$(jq -r .linked <<<"$row")" == "false" ]] || fail "a fresh task is already linked: $row"
  compare="$(jq -r .compare_url <<<"$row")"
  [[ "$compare" == https://github.com/octo/repo/compare/* ]] \
    || fail "compare_url is $compare, want a github.com compare page"

  # Never piped into grep: the producer would die of SIGPIPE on the match.
  calls="$(gh_calls)"
  if grep -q 'compare' <<<"$calls"; then
    fail "building the compare URL made a gh call: $calls"
  fi
  if grep -q 'pr create' <<<"$calls"; then
    fail "vincent asked gh to create a pull request: $calls"
  fi

  daemon_down
fi

echo "GATE PASS"
