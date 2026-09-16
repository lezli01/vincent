#!/usr/bin/env bash
# Task 069 gate (§10, §13.2): prove over the wire that vincent pushes a task's
# branch to `origin` and opens its pull request, and that each way that can go
# wrong ends where the task's decisions say it does.
#
#   1. a ready pull request through the API: the branch is on the remote with
#      the step's commit, its upstream is set, the link is written `human`,
#      and the fake `gh` was asked for exactly one `pr create`, with no
#      --draft
#   2. a draft pull request through `vincent github pr create --draft --json`,
#      the surface that command exists to give a gate
#   3. double submission: a task that is still linked is refused 409
#      `pull_already_linked` before any push or `gh` call (decision 7); after
#      a human unlink (suppressed does not count as linked, decision 4) the
#      create reaches GitHub, whose own refusal is the backstop — a 200
#      fallback carrying `pull_exists` and a compare URL
#   4. the create fails after the push: a 200 fallback carrying
#      `no_write_scope` (it was `forbidden` until 2026-09-15, when task 068.4
#      gave every write's 403 one spelling), whose compare URL names a branch
#      the remote really has, and no link
#   5. a rejected push creates nothing: a diverged branch on the remote is
#      refused 409 `push_rejected`, never forced over (decision 5), and `gh`
#      is never asked
#
# Task-numbered rather than `mN` because this is not a §19 milestone; 017, 032
# and 052 set that precedent.
#
# No agent CLI is involved. The gate's one workflow is a single command step
# that commits, so each task's branch is one real commit ahead of `main` and a
# push carries something. What is exercised is the GitHub half: the push goes
# to a local bare repository through `remote.origin.pushurl`, while
# `remote.origin.url` stays a github.com URL — identity comes from the fetch
# URL, the push goes where the gate can inspect it, the split
# internal/api/githubpullcreate_route_test.go uses. The create goes to
# cmd/fakegh, installed as `gh` on the daemon's PATH: there is no `gh_path`
# config key, and the daemon resolves `gh` from PATH when GHPath is empty.
#
# The gate observes the rules CLAUDE.md records: `run:` bodies in the sh∩pwsh
# intersection, no `grep -q` on a pipe (capture first, match a here-string),
# `| tr -d '\r'` on multi-line captures, and committed executable.
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
# the way it would find the real CLI.
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/gh$EXE" ./cmd/fakegh)
PATH="$BIN:$PATH"
export PATH

# Credential selection prefers an authenticated `gh` and falls back to these.
# With them unset, a `gh` that failed to resolve is a `no_credential` failure
# of this gate — never a request to api.github.com.
unset GITHUB_TOKEN GH_TOKEN
export FAKEGH_SCENARIO=success

CONFIG_DIR="" DATA_DIR="" REPO="" BARE="" GH_ARGV=""
scenario_dirs() { # scenario_dirs NAME
  CONFIG_DIR="$TMP/$1/config"
  DATA_DIR="$TMP/$1/data"
  REPO="$TMP/$1/repo"
  BARE="$TMP/$1/remote.git"
  mkdir -p "$CONFIG_DIR" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
  # GH_ARGV is every call the fake was asked to make, so "gh was never asked"
  # is observed process behaviour. The created file is what lets the fake's
  # `pr view 999` read back the pull request its `pr create` made.
  GH_ARGV="$TMP/$1/gh-argv.txt"
  : > "$GH_ARGV"
  export FAKEGH_ARGV_FILE
  FAKEGH_ARGV_FILE="$(hostpath "$GH_ARGV")"
  export FAKEGH_CREATED_FILE
  FAKEGH_CREATED_FILE="$(hostpath "$TMP/$1/gh-created.json")"
}

PORT="" TOKEN="" BASE=""
daemon_up() {
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
}
daemon_down() { "$VINCENT" daemon stop --force >/dev/null 2>&1 || true; }

api() { # api METHOD PATH [JSON_BODY] — fails the gate on a non-2xx answer
  local method="$1" path="$2" body="${3:-}" out status
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  [[ "$status" == 2* ]] || fail "$method $path -> HTTP $status: $out"
  printf '%s' "$out"
}

# api_status METHOD PATH [JSON_BODY] sets STATUS and BODY and never fails, so
# a refusal can be asserted. Called directly, never inside $(...), or the two
# globals would be set in a subshell and lost.
STATUS="" BODY=""
api_status() {
  local method="$1" path="$2" body="${3:-}" out
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  STATUS="${out##*$'\n'}"
  BODY="${out%$'\n'*}"
}

# expect JSON WHAT FILTER [JQ_ARGS...] fails the gate unless FILTER is true of
# JSON, and prints the JSON when it is not.
expect() {
  local json="$1" what="$2" filter="$3"
  shift 3
  jq -e "$@" "$filter" <<<"$json" >/dev/null || fail "$what: $json"
}

# The project needs a github.com origin — that is the whole of what makes it a
# GitHub project (§13.2) — and a push URL that is a real repository on this
# host. On Windows that is a drive-letter path, which is part of the point.
make_repo() {
  git init -q -b main "$REPO"
  git -C "$REPO" config user.name gate
  git -C "$REPO" config user.email gate@example.invalid
  git -C "$REPO" config commit.gpgsign false
  printf 'gate repo\n' > "$REPO/README.md"
  git -C "$REPO" add . && git -C "$REPO" commit -qm init
  git -C "$REPO" remote add origin https://github.com/octo/repo.git
  git init -q --bare "$BARE"
  git -C "$REPO" config remote.origin.pushurl "$(hostpath "$BARE")"
}

# The gate's one workflow. `git commit --allow-empty` is the whole body, which
# is what makes it portable to the daemon's pwsh on Windows (§8.3), and it
# leaves the branch one commit ahead of `main`.
write_workflow() {
  mkdir -p "$CONFIG_DIR/workflows"
  cat > "$CONFIG_DIR/workflows/gate.yaml" <<'YAML'
name: gate
description: One step that leaves a commit on the task's branch.
steps:
  - id: work
    type: command
    run: git commit --allow-empty -m gate
YAML
}

PROJECT_ID=""
register_project() {
  PROJECT_ID="$(api POST /projects \
    "$(jq -cn --arg p "$(hostpath "$REPO")" '{path: $p}')" | jq -r .id)"
}

# Titles are chosen so no task's branch is one of the fake's corpus heads
# (vincent/1-add-a-thing, vincent/3-ship-the-thing,
# vincent/9-rework-the-board-header): the reconciler would otherwise auto-link
# a corpus row, and a create would be refused `pull_already_linked` for the
# wrong reason.
create_task() { # create_task TITLE -> id
  api POST /tasks "$(jq -cn --argjson p "$PROJECT_ID" --arg t "$1" \
    '{project_id: $p, workflow: "gate", title: $t, description: "gate task"}')" | jq -r .id
}

wait_state() { # wait_state ID STATE
  local id="$1" want="$2" state=""
  for _ in $(seq 1 120); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "aborted" || "$state" == "blocked" ]] && [[ "$want" != "$state" ]]; then
      fail "task $id went $state waiting for $want"
    fi
    sleep 1
  done
  fail "task $id never reached $want (stuck in $state)"
}

# setup NAME TITLE: a fresh installation, a daemon, a project and one task run
# to done. Sets TASK_ID and BRANCH.
TASK_ID="" BRANCH=""
setup() {
  scenario_dirs "$1"
  make_repo
  write_workflow
  daemon_up
  register_project
  TASK_ID="$(create_task "$2")"
  wait_state "$TASK_ID" done
  BRANCH="$(api GET "/tasks/$TASK_ID" | jq -r .branch_name)"
  [[ -n "$BRANCH" && "$BRANCH" != "null" ]] || fail "task $TASK_ID finished with no branch"
  local subject
  subject="$(git -C "$REPO" log -1 --format=%s "refs/heads/$BRANCH")"
  [[ "$subject" == "gate" ]] || fail "the step's commit is not on $BRANCH (tip: $subject)"
}

create_body() { # create_body TITLE DRAFT
  jq -cn --arg t "$1" --argjson d "$2" '{title: $t, body: "Opened by the 069 gate.", draft: $d}'
}

# gh_calls is every argv the fake recorded. The file is written by a Go
# program, with the platform's own line endings, so CRs are stripped.
gh_calls() { tr -d '\r' < "$GH_ARGV"; }

# pr_creates prints the recorded `pr create` lines, one per call.
pr_creates() {
  local calls
  calls="$(gh_calls)"
  grep '^pr create ' <<<"$calls" || true
}

# expect_creates N: exactly N `pr create` calls were made.
expect_creates() {
  local lines count=0
  lines="$(pr_creates)"
  [[ -n "$lines" ]] && count="$(grep -c '' <<<"$lines")"
  [[ "$count" == "$1" ]] || fail "gh was asked for $count pr create calls, want $1: $(gh_calls)"
}

remote_ref() { git -C "$BARE" rev-parse --verify -q "refs/heads/$BRANCH" || true; }
local_ref() { git -C "$REPO" rev-parse "refs/heads/$BRANCH"; }

expect_unlinked() {
  local row
  row="$(api GET "/tasks/$TASK_ID/github/pull")"
  expect "$row" "the task is linked" '.linked == false'
}

run_scenario() { # run_scenario N
  [[ -z "$ONLY" || "$ONLY" == "$1" ]]
}

# ---------------------------------------------------------------- scenario 1
if run_scenario 1; then
  echo "== scenario 1: a ready pull request through the API"
  setup s1 "Open a ready pull request"

  api_status POST "/tasks/$TASK_ID/github/pull/create" "$(create_body "Open a ready pull request" false)"
  [[ "$STATUS" == "200" ]] || fail "create answered $STATUS, want 200: $BODY"
  expect "$BODY" "the create did not report a pushed, created pull request" \
    '.created == true and .pushed == true and .remote == "origin" and .branch == $b' \
    --arg b "$BRANCH"
  expect "$BODY" "the created pull request is not a ready #999" \
    '.pull.number == 999 and .pull.draft == false'
  expect "$BODY" "the returned task does not carry a human link to octo/repo" \
    '.task.github_pull.source == "human" and .task.github_pull.repo == "octo/repo" and .task.github_pull.number == 999'

  # The remote really has the branch, at the step's commit rather than main.
  [[ "$(remote_ref)" == "$(local_ref)" ]] \
    || fail "the remote's $BRANCH is '$(remote_ref)', want the local $(local_ref)"
  [[ "$(local_ref)" != "$(git -C "$REPO" rev-parse refs/heads/main)" ]] \
    || fail "$BRANCH is main, so the push carried nothing"
  upstream="$(git -C "$REPO" config --get "branch.$BRANCH.remote" || true)"
  [[ "$upstream" == "origin" ]] || fail "the push set no upstream (branch.$BRANCH.remote is '$upstream')"

  task="$(api GET "/tasks/$TASK_ID")"
  expect "$task" "GET /tasks/{id} does not carry the human link" \
    '.github_pull.number == 999 and .github_pull.source == "human"'
  row="$(api GET "/tasks/$TASK_ID/github/pull")"
  expect "$row" "GET /tasks/{id}/github/pull disagrees" \
    '.linked == true and .number == 999 and .source == "human" and .pull.draft == false'

  expect_creates 1
  line="$(pr_creates)"
  [[ " $line " == *" --head $BRANCH "* ]] || fail "pr create did not name --head $BRANCH: $line"
  [[ " $line " == *" --base main "* ]] || fail "pr create did not name --base main: $line"
  [[ " $line " != *" --draft "* ]] || fail "a ready pull request was created with --draft: $line"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 2
if run_scenario 2; then
  echo "== scenario 2: a draft pull request through the CLI"
  setup s2 "Open a draft pull request"

  out="$("$VINCENT" github pr create --task "$TASK_ID" --title "Open a draft pull request" \
    --body "Opened by the 069 gate." --draft --json)" \
    || fail "vincent github pr create exited non-zero: $out"
  expect "$out" "the CLI did not report a created draft" \
    '.created == true and .pushed == true and .pull.number == 999 and .pull.draft == true'

  expect_creates 1
  line="$(pr_creates)"
  [[ " $line " == *" --draft "* ]] || fail "a draft pull request was created without --draft: $line"

  [[ "$(remote_ref)" == "$(local_ref)" ]] || fail "the CLI's create did not push $BRANCH"
  row="$(api GET "/tasks/$TASK_ID/github/pull")"
  expect "$row" "the CLI's create left no human link to #999" \
    '.linked == true and .number == 999 and .source == "human" and .pull.draft == true'

  daemon_down
fi

# ---------------------------------------------------------------- scenario 3
if run_scenario 3; then
  echo "== scenario 3: double submission, then GitHub's backstop"
  setup s3 "Submit one pull request twice"

  api_status POST "/tasks/$TASK_ID/github/pull/create" "$(create_body "Submit one pull request twice" false)"
  [[ "$STATUS" == "200" ]] || fail "the first create answered $STATUS: $BODY"
  expect "$BODY" "the first create did not create" '.created == true'
  pushed="$(remote_ref)"

  # Still linked: refused at the source, before any push or gh call.
  api_status POST "/tasks/$TASK_ID/github/pull/create" "$(create_body "Submit one pull request twice" false)"
  [[ "$STATUS" == "409" ]] || fail "a second create on a linked task answered $STATUS, want 409: $BODY"
  expect "$BODY" "the refusal does not name pull_already_linked" \
    '.error.details.reason == "pull_already_linked"'
  expect_creates 1
  [[ "$(remote_ref)" == "$pushed" ]] || fail "the refused create moved the remote's $BRANCH"

  # A human unlink leaves the link suppressed, which does not count as linked.
  api DELETE "/tasks/$TASK_ID/github/pull" > /dev/null
  row="$(api GET "/tasks/$TASK_ID/github/pull")"
  expect "$row" "the unlink did not leave a suppressed, unlinked task" \
    '.linked == false and .suppressed == true'

  # fakegh reads its scenario from the environment of the daemon that spawns
  # it, so the backstop needs a daemon started with it.
  daemon_down
  FAKEGH_SCENARIO=pr-exists
  daemon_up

  api_status POST "/tasks/$TASK_ID/github/pull/create" "$(create_body "Submit one pull request twice" false)"
  [[ "$STATUS" == "200" ]] || fail "GitHub's refusal answered $STATUS, want the 200 fallback: $BODY"
  expect "$BODY" "the backstop is not a pushed pull_exists fallback" \
    '.created == false and .pushed == true and .reason == "pull_exists"'
  expect "$BODY" "the fallback carries no compare page" \
    '.compare_url | startswith("https://github.com/octo/repo/compare/main...")'
  expect_creates 2
  expect_unlinked

  FAKEGH_SCENARIO=success
  daemon_down
fi

# ---------------------------------------------------------------- scenario 4
if run_scenario 4; then
  echo "== scenario 4: the fallback when the create fails after the push"
  FAKEGH_SCENARIO=forbidden
  # setup starts the daemon, which is what hands the scenario to fakegh.
  setup s4 "Fall back to the compare page"

  api_status POST "/tasks/$TASK_ID/github/pull/create" "$(create_body "Fall back to the compare page" false)"
  [[ "$STATUS" == "200" ]] || fail "a refused create answered $STATUS, want the 200 fallback: $BODY"
  # A 403 on a write is `no_write_scope` — the create included, since task
  # 068.4 (2026-09-15). `forbidden` stays the read side's reason.
  expect "$BODY" "the fallback is not a pushed no_write_scope" \
    '.created == false and .pushed == true and .reason == "no_write_scope"'
  # url.PathEscape escapes the branch's `/`; the slug is otherwise [a-z0-9-].
  escaped="${BRANCH//\//%2F}"
  expect "$BODY" "the compare URL does not name main...$escaped" \
    '.compare_url | contains("/compare/main..." + $e)' --arg e "$escaped"

  # The page that URL opens is live: the branch is on the remote.
  [[ "$(remote_ref)" == "$(local_ref)" ]] \
    || fail "the fallback's branch is not on the remote, so its compare page is dead"
  expect_creates 1
  expect_unlinked

  FAKEGH_SCENARIO=success
  daemon_down
fi

# ---------------------------------------------------------------- scenario 5
if run_scenario 5; then
  echo "== scenario 5: a rejected push creates nothing"
  setup s5 "Refuse a diverged push"

  # Somebody else's commit under the task's branch name, on the remote only.
  theirs="$(git -C "$REPO" commit-tree 'main^{tree}' -p main -m theirs)"
  git -C "$REPO" push -q "$(hostpath "$BARE")" "$theirs:refs/heads/$BRANCH"
  [[ "$(remote_ref)" == "$theirs" ]] || fail "could not seed the diverged branch on the remote"

  api_status POST "/tasks/$TASK_ID/github/pull/create" "$(create_body "Refuse a diverged push" false)"
  [[ "$STATUS" == "409" ]] || fail "a diverged push answered $STATUS, want 409: $BODY"
  expect "$BODY" "the refusal does not name push_rejected" \
    '.error.details.reason == "push_rejected"'

  expect_creates 0
  [[ "$(remote_ref)" == "$theirs" ]] \
    || fail "the remote's $BRANCH moved to $(remote_ref): the push forced"
  expect_unlinked

  daemon_down
fi

echo "GATE PASS: 069 (open a pull request)"
