#!/usr/bin/env bash
# Task 064 gate (§10, §13.2): prove via curl alone that a pull request becomes
# a runnable task on the pull request's own head branch, against a local bare
# repository standing in for GitHub's git side and cmd/fakegh standing in for
# its API side.
#
#   1. the listing's state filter: open by default, closed carries the merged
#      row, all carries every row — fork included (decision 9)
#   2. a same-repository pull request becomes a task on its head branch: the
#      prefill, the link written at creation as `human`, base_sha at the
#      fetched head, the upstream configured, a workflow's `git push` reaching
#      the pull request's branch, and branch_override refused with a 409
#      (decisions 1, 2, 6, 7, 8, 10)
#   3. a fork pull request runs from refs/pull/{n}/head with no upstream, and
#      no remote is added for it (decision 5)
#   4. archive never touches a branch vincent did not cut, even a merged pull
#      request's empty head with both delete options on (decision 3)
#
# Task-numbered rather than `mN` because this is not a §19 milestone, like
# 052's.
#
# How the bare repository stands in for GitHub. A project is a GitHub project
# only because `git remote get-url origin` parses as github.com, and get-url
# applies `url.*.insteadOf` — so rewriting origin would stop the project being
# one. Each scenario's repository therefore keeps two remotes: `origin`, a
# github.com URL that is identity only and is never fetched, and `local`, the
# bare repository, which `main` tracks. The daemon fetches a pull request's
# head from the base branch's upstream (worktree.pullRemote), and task 056's
# base fetch reads the same upstream, so nothing in this gate touches the
# network.
#
# No agent CLI is involved: every workflow is `command` steps whose whole body
# is `exit 0`, `git commit --allow-empty -m gate-commit` or `git push`, which
# is what makes them portable to the daemon's pwsh on Windows (§8.3).
#
# The gate observes the rules CLAUDE.md records: no `grep -q` on a pipe
# (capture first, match a here-string), `| tr -d '\r'` on multi-line captures,
# and committed executable.
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

# shellpath turns a path the daemon reported (C:\... on Windows) into one this
# bash can stat.
shellpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -u "$1"; else printf '%s\n' "$1"; fi
}

echo "== build vincent and the fake gh"
mkdir -p "$BIN"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent)
# cmd/fakegh is built *as* `gh`, so the daemon's PATH lookup finds it exactly
# the way it would find the real CLI.
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/gh$EXE" ./cmd/fakegh)
PATH="$BIN:$PATH"
export PATH
export FAKEGH_SCENARIO=success

# #412's head is moved off fakegh's default `vincent/1-add-a-thing` on
# purpose: that is exactly the name the default branch template gives task 1
# titled "Add a thing", so "the task is on the pull request's head branch"
# would pass for the wrong reason.
PR_BRANCH="feature/add-a-thing"
export FAKEGH_PR_BRANCH="$PR_BRANCH"
FORK_BRANCH="typo-fix"
MERGED_BRANCH="vincent/3-ship-the-thing"

CONFIG_DIR="" DATA_DIR=""
scenario_dirs() { # scenario_dirs NAME
  CONFIG_DIR="$TMP/$1/config"
  DATA_DIR="$TMP/$1/data"
  mkdir -p "$CONFIG_DIR" "$DATA_DIR"
  export VINCENT_CONFIG_DIR
  VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")"
  export VINCENT_DATA_DIR
  VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")"
}

PORT="" TOKEN="" BASE=""
daemon_up() {
  "$VINCENT" daemon start
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
}
daemon_down() { "$VINCENT" daemon stop --force >/dev/null 2>&1 || true; }

api() { # api METHOD PATH [JSON_BODY] — fails the gate on anything but 2xx
  local method="$1" path="$2" body="${3:-}" out status
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  [[ "$status" == 2* ]] || fail "$method $path -> HTTP $status: $out"
  printf '%s' "$out"
}

api_status() { # api_status METHOD PATH [JSON_BODY] — prints the status, body in $TMP/body.json
  local method="$1" path="$2" body="${3:-}"
  local args=(-sS -o "$TMP/body.json" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $TOKEN")
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  curl "${args[@]}" "$BASE$path" || fail "curl $method $path failed"
}

REPO="" REMOTE="" PR_SHA="" FORK_SHA=""
# make_project DIR — the project repository and the bare repository its
# `main` tracks, seeded with every head the corpus's pull requests name.
make_project() {
  REPO="$1/repo"
  REMOTE="$1/remote.git"
  git init -q --bare -b main "$REMOTE"
  git init -q -b main "$REPO"
  # The daemon inherits the invoking user's global git config; nothing here
  # may depend on it.
  git -C "$REPO" config user.name gate
  git -C "$REPO" config user.email gate@example.invalid
  git -C "$REPO" config commit.gpgsign false
  git -C "$REPO" config push.default simple
  printf 'gate repo\n' > "$REPO/README.md"
  git -C "$REPO" add . && git -C "$REPO" commit -qm init
  git -C "$REPO" remote add origin https://github.com/octo/repo.git
  git -C "$REPO" remote add local "$(hostpath "$REMOTE")"
  git -C "$REPO" push -q local main
  git -C "$REPO" config branch.main.remote local
  git -C "$REPO" config branch.main.merge refs/heads/main

  # Every head is pushed from a bare commit object, so no local branch of any
  # head name exists when a task is admitted: the path under test is
  # "create the branch at the fetched head".
  PR_SHA="$(git -C "$REPO" commit-tree "HEAD^{tree}" -p HEAD -m "pull request 412")"
  git -C "$REPO" push -q local "$PR_SHA:refs/heads/$PR_BRANCH"
  # A fork's head is on no branch of this repository at all, only under
  # GitHub's refs/pull/{n}/head.
  FORK_SHA="$(git -C "$REPO" commit-tree "HEAD^{tree}" -p HEAD -m "pull request 355 from a fork")"
  git -C "$REPO" push -q local "$FORK_SHA:refs/pull/355/head"
  # The merged pull request's head sits at main's own commit: no commits past
  # its base, which is exactly what delete_empty_branch_on_archive fires on.
  git -C "$REPO" push -q local "HEAD:refs/heads/$MERGED_BRANCH"
}

register_project() { api POST /projects \
  "$(jq -cn --arg p "$(hostpath "$1")" '{path: $p}')" | jq -r .id; }

write_workflow() { # write_workflow NAME YAML
  mkdir -p "$CONFIG_DIR/workflows"
  printf '%s' "$2" > "$CONFIG_DIR/workflows/$1.yaml"
}

NOOP_WORKFLOW='name: gate
steps:
  - id: noop
    type: command
    run: exit 0
'

# Two steps rather than one: `exit N`, `git ...` and `sleep N` are the whole
# vocabulary sh and pwsh share, and `&&` is not in it.
PUSH_WORKFLOW='name: gate-push
steps:
  - id: commit
    type: command
    run: git commit --allow-empty -m gate-commit
  - id: push
    type: command
    run: git push
'

# create_from_pull PROJECT_ID WORKFLOW NUMBER -> the create response. No title
# and no description: the prefill supplies both.
create_from_pull() {
  api POST /tasks "$(jq -cn --argjson p "$1" --arg w "$2" --argjson n "$3" \
    '{project_id: $p, workflow: $w, github_pull: $n}')"
}

# wait_state ID STATE — poll until the task reaches STATE.
wait_state() {
  local id="$1" want="$2" task state=""
  for _ in $(seq 1 120); do
    task="$(api GET "/tasks/$id")"
    state="$(jq -r .state <<<"$task")"
    [[ "$state" == "$want" ]] && return 0
    if [[ "$state" == "blocked" || "$state" == "aborted" ]]; then
      fail "task $id went $state waiting for $want: $(jq -c '{block_reason, block_message}' <<<"$task")"
    fi
    sleep 1
  done
  fail "task $id never reached $want (stuck in $state)"
}

run_scenario() { # run_scenario N
  [[ -z "$ONLY" || "$ONLY" == "$1" ]]
}

# ---------------------------------------------------------------- scenario 1
# The listing's state filter, written against the corpus as it is now: the
# fork row #355 is open, so it is in the default listing.
if run_scenario 1; then
  echo "== scenario 1: the listing's state filter"
  scenario_dirs s1
  make_project "$TMP/s1"
  write_workflow gate "$NOOP_WORKFLOW"
  daemon_up
  pid="$(register_project "$REPO")"

  numbers="$(api GET "/projects/$pid/github/pulls" | jq -r '.[].number' | tr -d '\r')"
  [[ "$numbers" == $'412\n401\n355' ]] \
    || fail "the default listing returned $(printf '%s' "$numbers" | tr '\n' ' '), want 412 401 355"

  closed="$(api GET "/projects/$pid/github/pulls?state=closed")"
  numbers="$(jq -r '.[].number' <<<"$closed" | tr -d '\r')"
  [[ "$numbers" == "377" ]] \
    || fail "state=closed returned $(printf '%s' "$numbers" | tr '\n' ' '), want 377"
  [[ "$(jq -r '.[0].merged' <<<"$closed")" == "true" ]] \
    || fail "state=closed's #377 is not merged: $closed"

  numbers="$(api GET "/projects/$pid/github/pulls?state=all" | jq -r '.[].number' | tr -d '\r')"
  [[ "$numbers" == $'412\n401\n377\n355' ]] \
    || fail "state=all returned $(printf '%s' "$numbers" | tr '\n' ' '), want 412 401 377 355"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 2
# A same-repository pull request becomes a task on its head branch, and a
# workflow's push reaches that branch.
if run_scenario 2; then
  echo "== scenario 2: a same-repository pull request becomes a task on its head branch"
  scenario_dirs s2
  make_project "$TMP/s2"
  write_workflow gate-push "$PUSH_WORKFLOW"
  daemon_up
  pid="$(register_project "$REPO")"

  created="$(create_from_pull "$pid" gate-push 412)"
  tid="$(jq -r .id <<<"$created")"
  [[ "$(jq -r .branch_name <<<"$created")" == "$PR_BRANCH" ]] \
    || fail "the task's branch is $(jq -r .branch_name <<<"$created"), want the head $PR_BRANCH"
  title="$(jq -r .title <<<"$created")"
  [[ "$title" == "#412"* ]] || fail "the prefilled title is \"$title\", want one starting #412"
  link="$(jq -c .github_pull <<<"$created")"
  [[ "$(jq -r .number <<<"$link")" == "412" ]] || fail "the create response's link: $link"
  [[ "$(jq -r .source <<<"$link")" == "human" ]] || fail "the link at creation is not human: $link"
  [[ "$(jq -r .branch <<<"$link")" == "true" ]] || fail "the link does not record the branch: $link"
  [[ "$(jq -r '.fork // false' <<<"$link")" == "false" ]] || fail "a same-repository pull request says fork: $link"

  # Immediately, not after a reconciler tick: the default poll_interval is 5m,
  # and a reconciler's link would say `auto`.
  row="$(api GET "/tasks/$tid/github/pull")"
  [[ "$(jq -r .linked <<<"$row")" == "true" && "$(jq -r .source <<<"$row")" == "human" ]] \
    || fail "the task route does not show the creation-time link: $row"
  claim="$(api GET "/projects/$pid/github/pulls" | jq -r '.[] | select(.number == 412) | .task_id')"
  [[ "$claim" == "$tid" ]] || fail "the listing says task $claim claims #412, want $tid"

  wait_state "$tid" "done"
  task="$(api GET "/tasks/$tid")"
  [[ "$(jq -r .base_sha <<<"$task")" == "$PR_SHA" ]] \
    || fail "base_sha is $(jq -r .base_sha <<<"$task"), want the head at admission $PR_SHA"
  wt="$(jq -r .worktree_path <<<"$task")"
  [[ "$wt" != "null" ]] || fail "a done task has no worktree: $task"
  on="$(git -C "$wt" rev-parse --abbrev-ref HEAD)"
  [[ "$on" == "$PR_BRANCH" ]] || fail "the worktree is on $on, want $PR_BRANCH"
  upstream="$(git -C "$REPO" config --get "branch.$PR_BRANCH.remote")" \
    || fail "branch $PR_BRANCH has no upstream"
  [[ "$upstream" == "local" ]] || fail "branch $PR_BRANCH tracks $upstream, want local"

  head="$(git -C "$wt" rev-parse HEAD)"
  parent="$(git -C "$wt" rev-parse HEAD~1)"
  [[ "$head" != "$PR_SHA" && "$parent" == "$PR_SHA" ]] \
    || fail "the worktree's HEAD $head is not one commit past the head $PR_SHA"
  pushed="$(git -C "$REMOTE" rev-parse "refs/heads/$PR_BRANCH")"
  [[ "$pushed" == "$head" ]] \
    || fail "the pull request's branch on the remote is $pushed, want the task's commit $head"

  # details.branch, not details.state: this is the pull-request refusal, not
  # the generic "branch_override needs a blocked task" one.
  status="$(api_status POST "/tasks/$tid/retry" '{"branch_override": "elsewhere"}')"
  body="$(cat "$TMP/body.json")"
  [[ "$status" == "409" ]] || fail "branch_override on a pull-request task answered $status: $body"
  [[ "$(jq -r .error.code <<<"$body")" == "invalid_state" ]] \
    || fail "the refusal's code is $(jq -r .error.code <<<"$body"), want invalid_state"
  [[ "$(jq -r .error.details.branch <<<"$body")" == "$PR_BRANCH" ]] \
    || fail "the refusal is not the pull-request one: $body"
  [[ "$(api GET "/tasks/$tid" | jq -r .branch_name)" == "$PR_BRANCH" ]] \
    || fail "a refused branch_override still moved the branch"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 3
# A fork pull request runs, with no upstream and no remote added. No push is
# attempted: with no upstream, what git does depends on the user's global
# push.autoSetupRemote and would fall through to origin, a github.com URL.
if run_scenario 3; then
  echo "== scenario 3: a fork pull request runs, with no upstream and no remote added"
  scenario_dirs s3
  make_project "$TMP/s3"
  write_workflow gate "$NOOP_WORKFLOW"
  daemon_up
  pid="$(register_project "$REPO")"

  created="$(create_from_pull "$pid" gate 355)"
  tid="$(jq -r .id <<<"$created")"
  [[ "$(jq -r .branch_name <<<"$created")" == "$FORK_BRANCH" ]] \
    || fail "the fork task's branch is $(jq -r .branch_name <<<"$created"), want $FORK_BRANCH"
  link="$(jq -c .github_pull <<<"$created")"
  [[ "$(jq -r .fork <<<"$link")" == "true" && "$(jq -r .branch <<<"$link")" == "true" ]] \
    || fail "the fork task's link does not say fork and branch: $link"

  wait_state "$tid" "done"
  task="$(api GET "/tasks/$tid")"
  [[ "$(jq -r .base_sha <<<"$task")" == "$FORK_SHA" ]] \
    || fail "base_sha is $(jq -r .base_sha <<<"$task"), want refs/pull/355/head $FORK_SHA"
  wt="$(jq -r .worktree_path <<<"$task")"
  on="$(git -C "$wt" rev-parse --abbrev-ref HEAD)"
  [[ "$on" == "$FORK_BRANCH" ]] || fail "the worktree is on $on, want $FORK_BRANCH"

  rc=0
  git -C "$REPO" config --get "branch.$FORK_BRANCH.remote" >/dev/null || rc=$?
  [[ "$rc" == "1" ]] || fail "the fork's branch has an upstream (git config exited $rc)"
  remotes="$(git -C "$REPO" remote | tr -d '\r')"
  [[ "$remotes" == $'local\norigin' ]] \
    || fail "the project's remotes are $(printf '%s' "$remotes" | tr '\n' ' '), want local origin"

  daemon_down
fi

# ---------------------------------------------------------------- scenario 4
# Archive never touches a branch vincent did not cut. Both delete legs are on,
# and #377's head has no commits past its base, so both would fire on a
# branch vincent had cut.
if run_scenario 4; then
  echo "== scenario 4: archive leaves a merged pull request's head branch alone"
  scenario_dirs s4
  make_project "$TMP/s4"
  write_workflow gate "$NOOP_WORKFLOW"
  printf 'delete_empty_branch_on_archive: true\ndelete_remote_branch_on_archive: true\n' \
    > "$CONFIG_DIR/config.yaml"
  daemon_up
  pid="$(register_project "$REPO")"

  created="$(create_from_pull "$pid" gate 377)"
  tid="$(jq -r .id <<<"$created")"
  [[ "$(jq -r .branch_name <<<"$created")" == "$MERGED_BRANCH" ]] \
    || fail "the merged task's branch is $(jq -r .branch_name <<<"$created"), want $MERGED_BRANCH"

  wait_state "$tid" "done"
  wt="$(api GET "/tasks/$tid" | jq -r .worktree_path)"
  [[ "$wt" != "null" && -d "$(shellpath "$wt")" ]] || fail "the done task has no worktree on disk ($wt)"

  archived="$(api POST "/tasks/$tid/archive")"
  [[ "$(jq -r .branch.result <<<"$archived")" == "not_ours" ]] \
    || fail "archive's branch result: $(jq -c .branch <<<"$archived"), want not_ours"
  [[ "$(jq -r '.branch.remote.result // "none"' <<<"$archived")" != "deleted" ]] \
    || fail "archive deleted the remote head: $(jq -c .branch <<<"$archived")"

  git -C "$REPO" rev-parse -q --verify "refs/heads/$MERGED_BRANCH" >/dev/null \
    || fail "archive deleted the local head branch $MERGED_BRANCH"
  git -C "$REMOTE" rev-parse -q --verify "refs/heads/$MERGED_BRANCH" >/dev/null \
    || fail "archive deleted $MERGED_BRANCH on the remote"
  [[ ! -e "$(shellpath "$wt")" ]] || fail "archive left the worktree at $wt"

  daemon_down
fi

echo "GATE PASS"
