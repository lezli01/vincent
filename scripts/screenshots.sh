#!/usr/bin/env bash
# Documentation screenshots — the real TUI, captured from a real terminal.
#
# Every image under docs/assets/tui-*.png is produced by this script and
# nothing else. It seeds a throwaway vincent installation (its own config and
# data dirs, its own git repos, its own daemon), drives the workload into the
# states the docs talk about, and then photographs the actual program with
# VHS — a real pty, a real terminal emulator, a real frame. Nothing here is
# drawn by hand: if a panel changes, the next run of this script shows the
# change, and a screenshot that no longer matches the code is a bug this
# script fixes rather than a picture someone has to redraw.
#
#   scripts/screenshots.sh          seed, capture every shot, clean up
#   scripts/screenshots.sh seed     seed and leave the daemon running
#   scripts/screenshots.sh capture  capture against an already-seeded tree
#   scripts/screenshots.sh clean    stop the daemon and remove the tree
#
#   VINCENT_SHOTS_DIR=<path>   where to seed (default: /tmp/vincent-demo — short
#                              on purpose: three of the shots show a repo path)
#   VINCENT_SHOTS_ONLY=<name>  capture one tape (e.g. `projects`)
#
# Unlike the acceptance gates this is **not** cross-platform and CI does not
# run it: VHS needs ttyd and ffmpeg, and the seeded workflows use a POSIX
# shell rather than the sh∩pwsh intersection the gates are held to. It is a
# maintainer tool, run on demand when the UI it photographs has moved.
#
# Requirements: bash, go, git, curl, jq, vhs (`brew install vhs`) — but **not
# vhs 0.12.0**, which renders nothing at all. It cancels the context its own
# ffmpeg step then runs under, so `exec.CommandContext` refuses to start the
# process: every tape exits 0, prints "Creating …", and writes neither the GIF
# nor the Screenshot. 0.11.0 renders. The `[[ -f … ]]` check after each tape is
# what turns that silence into a failure instead of an empty success.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SHOTS="${VINCENT_SHOTS_DIR:-/tmp/vincent-demo}"
SHOTS="${SHOTS%/}"

BIN="$SHOTS/bin"
CONFIG_DIR="$SHOTS/config"
DATA_DIR="$SHOTS/data"
HOME_DIR="$SHOTS/home"
REPOS="$SHOTS/repos"
TAPES="$SHOTS/tapes"
GIFS="$SHOTS/gifs"
SESSIONS="$SHOTS/sessions"
OUT="$ROOT/docs/assets"

VINCENT="$BIN/vincent"
FAKEAGENT="$BIN/fakeagent"
FAKEGH="$BIN/fakegh"

export VINCENT_CONFIG_DIR="$CONFIG_DIR"
export VINCENT_DATA_DIR="$DATA_DIR"

# The frame. 2400×1400 at font size 28 is ~140×40 cells — comfortably past
# the 128×24 threshold where New task, Projects and Workflows switch to their
# guided two-pane layouts (§15), which is the form the docs describe.
COLS_PX=2400
ROWS_PX=1400
FONT_PX=28

fail() { echo "SHOTS FAIL: $*" >&2; exit 1; }
say() { printf '\n== %s\n' "$*"; }

# ---------------------------------------------------------------------------
# API plumbing, borrowed from the gates: the daemon writes its port and token
# under the data dir, and everything the seed does afterwards goes over the
# same localhost API a client would use.
# ---------------------------------------------------------------------------
PORT="" TOKEN="" BASE=""
api() { # api METHOD PATH [JSON_BODY]
  local method="$1" path="$2" body="${3:-}" out status
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOKEN" -w $'\n%{http_code}')
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" -d "$body")
  out="$(curl "${args[@]}" "$BASE$path")" || fail "curl $method $path failed"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  [[ "$status" == 2* ]] || fail "$method $path -> $status: $out"
  printf '%s' "$out"
}

wait_state() { # wait_state ID STATE [TIMEOUT_S]
  local id="$1" want="$2" limit="${3:-90}" state
  for (( i = 0; i < limit * 2; i++ )); do
    state="$(api GET "/tasks/$id" | jq -r .state)"
    [[ "$state" == "$want" ]] && return 0
    sleep 0.5
  done
  fail "task $id never reached $want (last: ${state:-unknown})"
}

wait_hold() { # wait_hold ID [TIMEOUT_S] — queued on an agent's usage window
  local id="$1" limit="${2:-90}" reason
  for (( i = 0; i < limit * 2; i++ )); do
    reason="$(api GET "/tasks/$id" | jq -r '.queued_reason // "null"')"
    [[ "$reason" == "usage_limit" ]] && return 0
    sleep 0.5
  done
  fail "task $id never parked on a usage-limit hold (last: ${reason:-unknown})"
}

wait_chat() { # wait_chat ID STATE [TIMEOUT_S]
  local id="$1" want="$2" limit="${3:-90}" state
  for (( i = 0; i < limit * 2; i++ )); do
    # GET /v1/chats/{id} answers {chat, turns}: the state is the chat's.
    state="$(api GET "/chats/$id" | jq -r .chat.state)"
    [[ "$state" == "$want" ]] && return 0
    sleep 0.5
  done
  fail "chat $id never reached $want (last: ${state:-unknown})"
}

daemon_up() {
  # $BIN leads the daemon's PATH so the `gh` it resolves is the wrapper around
  # cmd/fakegh, and no token reaches it: a `gh` that failed to answer would
  # otherwise fall back to a REST call against api.github.com.
  #
  # $HOME is the seeded one (see seed_home), so the skills the daemon view
  # reports are the seed's and not the maintainer's. Go's caches are pinned
  # to the real ones first: the soak step runs `go test` under this HOME, and
  # a cold build cache would spend the first minute of it compiling the
  # standard library.
  local gocache gomodcache gopath
  gocache="$(go env GOCACHE)"
  gomodcache="$(go env GOMODCACHE)"
  gopath="$(go env GOPATH)"
  env -u GITHUB_TOKEN -u GH_TOKEN HOME="$HOME_DIR" GOCACHE="$gocache" \
    GOMODCACHE="$gomodcache" GOPATH="$gopath" PATH="$BIN:$PATH" \
    "$VINCENT" daemon start >/dev/null
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
}

daemon_attach() {
  [[ -f "$DATA_DIR/daemon.json" ]] || fail "nothing seeded at $SHOTS — run 'scripts/screenshots.sh seed' first"
  PORT="$(jq -r .port "$DATA_DIR/daemon.json")"
  TOKEN="$(cat "$DATA_DIR/token")"
  BASE="http://127.0.0.1:$PORT/v1"
  curl -sS -f -H "Authorization: Bearer $TOKEN" "$BASE/health" >/dev/null \
    || fail "the seeded daemon is not answering on $BASE — re-seed"
}

do_clean() {
  if [[ -x "$VINCENT" ]]; then
    "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  fi
  rm -rf "$SHOTS"
  echo "removed $SHOTS"
}

# ---------------------------------------------------------------------------
# Seeding
# ---------------------------------------------------------------------------

# make_repo NAME — a small, real repository with enough files that a diff of
# it is worth a picture.
make_repo() {
  local name="$1" dir="$REPOS/$1"
  mkdir -p "$dir/internal" "$dir/docs"
  git init -q -b main "$dir"
  git -C "$dir" config user.name "vincent docs"
  git -C "$dir" config user.email docs@example.invalid
  git -C "$dir" config commit.gpgsign false
  cat > "$dir/README.md" <<EOF
# $name

Demo repository seeded by scripts/screenshots.sh.
EOF
  printf 'package server\n\nfunc Handle() {}\n' > "$dir/internal/server.go"
  printf 'package server\n\nfunc Limit() int { return 100 }\n' > "$dir/internal/limits.go"
  printf 'package server\n\nfunc Cache() {}\n' > "$dir/internal/cache.go"
  printf '# %s\n\nHow this service is operated.\n' "$name" > "$dir/docs/runbook.md"
  cat > "$dir/internal/limits_test.go" <<'GOTEST'
package server

import "testing"

func TestLimitDefault(t *testing.T) {
	if Limit() != 100 {
		t.Fatalf("Limit() = %d, want 100", Limit())
	}
}

func TestLimitIsPositive(t *testing.T) {
	if Limit() <= 0 {
		t.Fatal("the limit must be positive")
	}
}

func TestHandleDoesNotPanic(t *testing.T) {
	Handle()
}

func TestCacheDoesNotPanic(t *testing.T) {
	Cache()
}
GOTEST
  printf 'module example.com/%s\n\ngo 1.24\n' "$name" > "$dir/go.mod"
  git -C "$dir" add -A
  git -C "$dir" commit -qm "initial commit"
  git init -q --bare "$REPOS/$name.git"
  git -C "$dir" remote add origin "$REPOS/$name.git"
  git -C "$dir" push -q origin main
}

# seed_home — the $HOME the daemon and every tape's TUI run under (issue
# #415). Two things vincent reads hang off it: the published-skill store the
# daemon view's skills line reports on (§9.8, task 095), and claude's
# settings.json, whose status line the same view offers to set up (task 082).
# Without it those two lines would photograph the maintainer's own machine.
# One skill is installed a version behind and linked into claude, and the
# other is not installed at all, so the offer has both kinds of row.
seed_home() {
  local store="$HOME_DIR/.agents/skills/vincent-workflows"
  mkdir -p "$store" "$HOME_DIR/.claude/skills"
  sed 's/^  version: .*/  version: 1.0.0/' "$ROOT/skills/vincent-workflows/SKILL.md" > "$store/SKILL.md"
  grep -qx '  version: 1.0.0' "$store/SKILL.md" \
    || fail "seed_home: skills/vincent-workflows/SKILL.md carries no metadata.version to wind back"
  ln -s ../../.agents/skills/vincent-workflows "$HOME_DIR/.claude/skills/vincent-workflows"
}

# agent_wrapper NAME ENV... — the config points each adapter at its own
# wrapper so one daemon can run three different fake CLIs at once: a slow one
# whose tasks stay `running` for the camera, a fast one that drives tasks
# through to a gate, and one that asks a question. The alternative — one
# FAKEAGENT_SCENARIO for the whole process — cannot hold a board in more than
# one state at a time.
agent_wrapper() {
  local name="$1"; shift
  wrap "$FAKEAGENT" "$name" "$@"
}

# wrap TARGET NAME ENV... — $BIN/NAME runs TARGET with ENV added. Each
# assignment is single-quoted, so a value may carry spaces.
wrap() {
  local target="$1" name="$2"; shift 2
  {
    printf '#!/bin/sh\n'
    printf 'exec env'
    for kv in "$@"; do printf " '%s'" "$kv"; done
    printf ' "%s" "$@"\n' "$target"
  } > "$BIN/$name"
  chmod +x "$BIN/$name"
}

# write_config CLAUDE_WRAPPER — the claude adapter is swapped three times
# mid-seed, because one process-wide FAKEAGENT_SCENARIO cannot hold a
# conversation, ask two different questions and run out of quota at once. The
# daemon hot-reloads config.yaml (§12.3), so this needs no restart.
#
# The order is fixed and one-way: chat, then the two that ask (triage for the
# task, ask for the chat), then walled. An observed
# usage window is recorded against the **adapter** and outlives the swap (§11,
# task 026), so anything that needs claude to answer has to be seeded before
# `agent-walled` has ever run — after it, every claude task parks on the hold
# and every claude chat turn fails.
write_config() {
  cat > "$CONFIG_DIR/config.yaml" <<EOF
# Seeded by scripts/screenshots.sh. Each adapter points at a wrapper around
# cmd/fakeagent so no real agent CLI — and no real spend — is involved.
max_parallel_tasks: 4
branch_template: "feat/{{.ID}}{{with .Slug}}-{{.}}{{end}}"
agents:
  claude:
    path: "$BIN/$1"
  codex:
    path: "$BIN/agent-slow"
  cursor:
    path: "$BIN/agent-fast"
# On, so the triggers shot shows an armed trigger rather than the global-off
# banner. The seeded triggers only ever seed, so no task comes of it.
triggers:
  enabled: true
# The pull-request reconciler on a short tick, so the web project's pull
# request is linked to its task seconds after the seed pushes the branch
# rather than on the five-minute default. Every call it makes is to
# cmd/fakegh (see daemon_up).
github:
  poll_interval: 5s
EOF
}

do_seed() {
  command -v vhs >/dev/null 2>&1 || fail "vhs is not on PATH — brew install vhs"
  command -v jq >/dev/null 2>&1 || fail "jq is not on PATH"

  do_clean >/dev/null 2>&1 || true
  mkdir -p "$BIN" "$CONFIG_DIR/workflows" "$CONFIG_DIR/triggers" "$DATA_DIR" "$HOME_DIR" "$REPOS" "$TAPES" "$GIFS" "$SESSIONS"
  seed_home

  # Built with the release ldflags rather than plain `go build`: the TUI
  # header prints its own version, and an uninjected build prints the module
  # pseudo-version (`v0.5.1-0.2026…+dirty`), which is noise in a screenshot.
  say "build vincent + fakeagent"
  local vpkg="github.com/lezli01/vincent/internal/version" vtag
  vtag="$(git -C "$ROOT" describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)"
  (cd "$ROOT" && go build -trimpath \
    -ldflags "-X $vpkg.version=${vtag#v} -X $vpkg.commit=$(git -C "$ROOT" rev-parse --short HEAD) -X $vpkg.date=$(date -u +%Y-%m-%d)" \
    -o "$BIN/" ./cmd/vincent ./cmd/fakeagent ./cmd/fakegh)

  agent_wrapper agent-slow FAKEAGENT_DIALECT=codex FAKEAGENT_SCENARIO_CODEX=success FAKEAGENT_DELAY_MS=3600000
  agent_wrapper agent-fast FAKEAGENT_DIALECT=cursor FAKEAGENT_SCENARIO_CURSOR=success FAKEAGENT_DELAY_MS=1500
  agent_wrapper agent-ask FAKEAGENT_SCENARIO=ask-question FAKEAGENT_ASK_MULTI=1
  # The question the answer-form shot is of (issue #414), asked in the terms
  # of the task it parks — a replica restore — rather than the fake's built-in
  # colours and toppings. One single-choice question and one multi-select, so
  # the picture has both kinds of row. No apostrophes: wrap single-quotes it.
  agent_wrapper agent-triage FAKEAGENT_SCENARIO=ask-question "FAKEAGENT_ASK_QUESTIONS=$(jq -cn '[
    {header: "Restore from",
     question: "The eu-west replica is 40 minutes behind the primary. What should I rebuild it from?",
     multiSelect: false,
     options: [
       {label: "The 02:00 snapshot", description: "Faster, then replays nine hours of WAL"},
       {label: "A fresh base backup", description: "Slower, and loads the primary while it runs"}]},
    {header: "Before cutover",
     question: "What should happen before it takes read traffic again?",
     multiSelect: true,
     options: [
       {label: "Pause the reporting jobs", description: "They are its heaviest readers"},
       {label: "Page the on-call DBA", description: "A second pair of eyes on the lag"}]}]')"
  # A CLI that answers a chat turn with a markdown document — a heading, a
  # fenced code block, two links — because the chat shots are of prose and of
  # what the reader actions (task 076) can take out of it. The session store
  # is what makes turn 2 a different answer from turn 1: the fake CLI resumes
  # its own conversation exactly as the real one does.
  agent_wrapper agent-chat FAKEAGENT_SCENARIO=chat-reply "FAKEAGENT_SESSION_DIR=$SESSIONS"
  # A CLI whose account has run out, naming its own reset 90 minutes out. It
  # is what leaves one adapter observed-spent for the board header badge and
  # the daemon view's quota line (task 026) — without it those two shots
  # photograph a state no seeded daemon is ever in.
  #
  # It also carries the skill listing the chat-skills shots are of (task
  # 124.19), because this — not agent-chat — is where `claude` points when
  # do_capture runs: the swaps below are one-way, so agent-walled is the last
  # thing write_config wrote, and a listing probe is a fresh run of the
  # *configured* binary rather than anything the seed warmed (skillTTL is five
  # minutes and the chat tapes are taken half an hour later). The version goes
  # with it because claude.skillListingFloor is 2.1.277 and cmd/fakeagent
  # reports 2.1.224 by default, which lists as a positive no with no rows at
  # all — the trap scripts/m14-gate.sh pins the same version against. Neither
  # variable disturbs the quota: `initialize` is answered ahead of any
  # scenario dispatch, so this wrapper still walls every task it is given.
  #
  # Ten skills in the seeded repositories' own terms, one of each kind the row
  # line draws differently — a project skill with an argument hint, a user
  # skill, a long namespaced plugin skill — with each scope written into the
  # description text, since claude reports scope nowhere else. Three names
  # share the `re` prefix the inline shot types, and `review-pr` is found
  # under it by its bare name. The one `builtin` row does not appear: no chat
  # turn runs on this binary, so the cache withholds it until one does (§9.6,
  # task 124.16). No apostrophes: wrap single-quotes every assignment.
  agent_wrapper agent-walled FAKEAGENT_SCENARIO=usage-limit FAKEAGENT_USAGE_LIMIT_RESET=5400 \
    FAKEAGENT_VERSION=2.1.277 "FAKEAGENT_CLAUDE_COMMANDS=$(jq -cn '[
    {name: "replay-request", argumentHint: "[capture-file]",
     description: "Replay a captured HTTP call against a local build. (project)"},
    {name: "rehearse-limits", argumentHint: "",
     description: "Dry run a bucket change over last week traffic. (user)"},
    {name: "retune-buckets", argumentHint: "[plan]",
     description: "Propose per-plan token bucket sizes from the hit rate. (project)"},
    {name: "vincent-workflows:review-pr", argumentHint: "[number]",
     description: "Walk a pull request and leave one comment per finding. (plugin vincent-workflows)"},
    {name: "rate-limit-audit", argumentHint: "",
     description: "Find every path that skips the token bucket. (project)"},
    {name: "bump-quota", argumentHint: "[account]",
     description: "Lift a customer plan limit and log who did it. (project)"},
    {name: "trace-call", argumentHint: "",
     description: "Follow one API call through all of its hops. (user)"},
    {name: "bench-buckets", argumentHint: "",
     description: "Time the limiter hot path at 10k calls a second. (project)"},
    {name: "docs-sync", argumentHint: "[page]",
     description: "Align the API guide with the handler it talks about. (user)"},
    {name: "tidy-imports", argumentHint: "[package]",
     description: "Sort and group Go imports in a package. (project)"},
    {name: "compact", argumentHint: "", builtin: true,
     description: "Free up context by summarizing the conversation so far"}]')"
  # The GitHub half (issue #414): cmd/fakegh as `gh`, which daemon_up puts
  # first on the daemon's PATH. The web project's origin names acme/web, so the
  # fake answers as that repository, and its open pull request #412 — with a
  # check rollup of a failed Actions build, a running test, a passing
  # third-party check and a legacy commit status — is pointed at the branch
  # the design-tokens task is given. That task is created second, which is how
  # its branch can be spelled here before it exists; the seed checks it.
  wrap "$FAKEGH" gh FAKEGH_SCENARIO=success FAKEGH_REPO=acme/web \
    FAKEGH_PR_BRANCH=feat/2-bump-the-design-tokens 'FAKEGH_PR_TITLE=Bump the design tokens'

  say "config"
  write_config agent-chat

  say "workflows"
  cat > "$CONFIG_DIR/workflows/feature-pr.yaml" <<'EOF'
name: feature-pr
description: Implement a task on its own branch, verify it, gate it, and push.
defaults:
  agent: cursor
  max_retries: 2
  timeout: 4h
steps:
  - id: implement
    type: agent
    prompt: |
      Implement the following task in this repository.

      Title: {{.Task.Title}}
      Work on the current branch ({{.Task.BranchName}}).
    check: git status --porcelain
  - id: commit
    type: command
    run: |
      printf 'rate limiting\n' >> internal/limits.go
      printf 'cache warm-up\n' >> internal/cache.go
      printf '\n## Rate limits\n\nRequests are capped per token.\n' >> docs/runbook.md
      git add -A && git commit -q -m "{{.Task.Title}}"
  - id: review
    type: manual
    instructions: |
      Review the diff for task #{{.Task.ID}} on branch {{.Task.BranchName}}.
  - id: publish
    type: command
    run: git push -q -u origin {{.Task.BranchName}}
EOF

  cat > "$CONFIG_DIR/workflows/docs-refresh.yaml" <<'EOF'
name: docs-refresh
description: Regenerate the reference pages and commit the result.
defaults:
  agent: codex
  timeout: 4h
steps:
  - id: draft
    type: agent
    prompt: 'Refresh the documentation for: {{.Task.Title}}'
  - id: commit
    type: command
    run: 'git commit -q --allow-empty -m "docs {{.Task.Title}}"' 
EOF

  cat > "$CONFIG_DIR/workflows/feature-delivery.yaml" <<'EOF'
name: feature-delivery
description: Fan a feature out across the surfaces it touches, then merge.
defaults:
  agent: codex
  timeout: 4h
steps:
  - id: plan
    type: agent
    prompt: 'Plan the delivery of {{.Task.Title}}.'
  - id: delivery_lanes
    type: fan_out
    lanes:
      - id: api
        steps:
          - {id: api_impl, type: agent, prompt: build the api surface}
          - {id: api_test, type: command, run: 'go vet ./...'}
      - id: tui
        steps:
          - {id: tui_impl, type: agent, prompt: build the tui surface}
      - {id: docs, workflow: docs-refresh}
      - {id: package, workflow: release-train, if: '{{ .Fields.release }}'}
    merge:
      on_conflict: agent
      agent: {id: fixup, type: agent, prompt: resolve the merge conflict}
  - id: approve
    type: manual
    instructions: Approve publication of {{.Task.Title}}.
EOF

  cat > "$CONFIG_DIR/workflows/release-train.yaml" <<'EOF'
name: release-train
description: Tag, build and publish the release artifacts.
steps:
  - id: verify
    type: command
    run: git log -1 --oneline
  - id: sign_off
    type: manual
    instructions: Sign off on the release.
EOF

  cat > "$CONFIG_DIR/workflows/incident-response.yaml" <<'EOF'
name: incident-response
description: Triage an incident, mitigate, then write the postmortem.
defaults:
  agent: cursor
steps:
  - id: triage
    type: agent
    prompt: 'Triage: {{.Task.Title}}'
  - id: mitigate
    type: command
    run: git commit -q --allow-empty -m "mitigation applied"
EOF

  cat > "$CONFIG_DIR/workflows/security-audit.yaml" <<'EOF'
name: security-audit
description: Sweep the dependency tree and report what needs attention.
steps:
  - id: sweep
    type: command
    run: git log --oneline -5
EOF

  # Invalid on purpose: the Workflows registry has to be able to show what a
  # broken entry looks like, and it must not be a file any task depends on.
  cat > "$CONFIG_DIR/workflows/legacy-deploy.yaml" <<'EOF'
name: legacy-deploy
description: Kept for reference — no longer loads.
steps:
  - id: deploy
    type: not-a-step-type
    run: ./deploy.sh
EOF

  # The step behind the layout screenshot. Its output is a real `go test -v`
  # run in the task's worktree, on a loop, so the live tail in the picture is
  # a program's own output rather than a fixture's idea of one.
  cat > "$CONFIG_DIR/workflows/verify-build.yaml" <<'EOF'
name: verify-build
description: Keep building and testing the branch while it is worked on.
defaults:
  max_retries: 0
  timeout: 4h
steps:
  - id: soak
    type: command
    run: |
      i=0
      while [ $i -lt 300 ]; do
        i=$((i + 1))
        go test -count=1 -v ./... 2>&1
        sleep 3
      done
  - id: report
    type: command
    run: git log -1 --oneline
EOF

  # Declared fields (tasks 022 and 058), for the New task shot of them: one of
  # every type, a pattern, a required enum with a default, and a `multiple`
  # one. No task is created from it — the form is the picture.
  cat > "$CONFIG_DIR/workflows/change-request.yaml" <<'EOF'
name: change-request
description: Apply a ticketed production change, canary first, and verify it.
fields:
  - name: ticket
    label: Ticket
    description: The change ticket, with its project prefix.
    type: string
    required: true
    pattern: '^CHG-[0-9]+$'
  - name: environment
    label: Environment
    description: Where the change lands first.
    type: enum
    required: true
    values: [dev, staging, prod]
    default: staging
  - name: regions
    label: Regions
    description: Every region the rollout reaches.
    type: enum
    multiple: true
    values: [us-east, us-west, eu-west, ap-south]
  - name: canary
    label: Canary percent
    type: integer
    default: 5
  - name: dry-run
    label: Dry run
    description: Plan the change without applying it.
    type: boolean
    default: true
defaults:
  agent: cursor
steps:
  - id: plan
    type: agent
    prompt: 'Plan {{ index .Task.Fields "ticket" }} for {{ index .Task.Fields "environment" }}: {{.Task.Title}}'
  - id: apply
    type: command
    run: 'git commit -q --allow-empty -m "{{ index .Task.Fields "ticket" }}: {{.Task.Title}}"'
EOF

  # A loop (§7.8, task 083), for the shot of its rollup and its iteration
  # tiers: once per service, and the fourth one fails. The task blocks inside
  # the loop rather than running in it, so it holds no slot and the picture
  # does not depend on when it is taken — three folded passes above the one
  # it stopped on, which is the pass a reader arrives to read.
  cat > "$CONFIG_DIR/workflows/service-migrations.yaml" <<'EOF'
name: service-migrations
description: Migrate each service's schema in turn, verifying each before the next.
defaults:
  agent: cursor
  max_retries: 0
steps:
  - id: preflight
    type: command
    run: git log -1 --oneline
  - id: services
    type: loop
    for_each: [auth, billing, search, ledger, gateway]
    steps:
      - id: migrate
        type: command
        run: |
          echo "{{ .Loop.Item }}: applying 0042_add_tenant_id.up.sql"
          {{ if eq .Loop.Item "ledger" }}echo "ledger: lock timeout after 30s on ledger.entries, held by a reporting job"
          exit 1{{ end }}
          echo "{{ .Loop.Item }}: migrated, 3 tables altered"
      - id: verify
        type: agent
        prompt: 'Check every read path of the {{ .Loop.Item }} service against the migrated schema.'
  - id: sign_off
    type: manual
    instructions: Sign off on the migrated services.
EOF

  # A multi-round fan_out (§7.6, task 084), for the shots of the lane tree,
  # the lane selector and the per-lane diff: two lanes, and a third that
  # `needs` both and so is spawned in a second round, cut from a branch that
  # already has the first two merged into it. It parks at a gate, which holds
  # no slot, so the parent sits in `awaiting_children` with one round merged
  # and one waiting on a human.
  cat > "$CONFIG_DIR/workflows/platform-upgrade.yaml" <<'EOF'
name: platform-upgrade
description: Upgrade the storage driver and its client side by side, then switch the handlers over.
defaults:
  agent: cursor
  max_retries: 0
steps:
  - id: plan
    type: command
    run: |
      printf '# v2 storage driver\n\n1. storage: open connections through the v2 driver\n2. client: adopt the v2 client\n3. handlers: switch over once both have merged\n' > docs/upgrade-plan.md
      git add -A && git commit -q -m "plan the v2 storage upgrade"
  - id: upgrade
    type: fan_out
    lanes:
      - id: storage
        steps:
          - {id: storage_impl, type: agent, prompt: 'Open connections through the v2 storage driver.'}
          - id: storage_commit
            type: command
            run: |
              printf '\n// OpenV2 opens a connection through the v2 driver.\nfunc OpenV2() error { return nil }\n' >> internal/cache.go
              git add -A && git commit -q -m "storage: open connections through the v2 driver"
      - id: client
        steps:
          - {id: client_impl, type: agent, prompt: 'Adopt the v2 storage client in the request path.'}
          - id: client_commit
            type: command
            run: |
              printf '\n// Client names the storage client the request path uses.\nfunc Client() string { return "v2" }\n' >> internal/server.go
              git add -A && git commit -q -m "client: adopt the v2 storage client"
      - id: handlers
        needs: [storage, client]
        steps:
          - {id: handlers_impl, type: agent, prompt: 'Switch the handlers over to the v2 driver and client.'}
          - id: handlers_commit
            type: command
            run: |
              printf '\n// LimitV2 is the limit, read through the v2 driver.\nfunc LimitV2() int { return Limit() }\n' >> internal/limits.go
              git add -A && git commit -q -m "handlers: switch to the v2 driver"
          - id: handlers_review
            type: manual
            instructions: Read the handler diff before the upgrade joins.
  - id: release
    type: manual
    instructions: Approve the v2 storage upgrade.
EOF

  say "repositories"
  local p
  for p in api web docs-portal platform-infra agent-adapters release-tooling security-labs; do
    make_repo "$p"
  done

  # web is the one GitHub project: a github.com origin is the whole of what
  # makes a project one (§13.2), and it is what the Pull Request tab and the
  # open-a-pull-request popup need. Pushes are rewritten to the local bare
  # repository, so the workflow's publish step still lands somewhere real;
  # `git remote get-url` does not apply pushInsteadOf, so the daemon still
  # reads github.com. Nothing fetches: make_repo pushed main without an
  # upstream, so a task's base-branch fetch has nothing to ask for.
  git -C "$REPOS/web" remote set-url origin https://github.com/acme/web.git
  git -C "$REPOS/web" config "url.$REPOS/web.git.pushInsteadOf" https://github.com/acme/web.git

  # A project-scoped copy, so the registry can show real shadowing.
  mkdir -p "$REPOS/api/.vincent/workflows"
  cat > "$REPOS/api/.vincent/workflows/feature-delivery.yaml" <<'EOF'
name: feature-delivery
description: The api repo's own delivery workflow — shadows the global copy.
defaults:
  agent: codex
steps:
  - id: plan
    type: agent
    prompt: 'Plan the delivery of {{.Task.Title}}.'
  - id: ship
    type: command
    run: git commit -q --allow-empty -m "ship {{.Task.Title}}"
EOF
  git -C "$REPOS/api" add .vincent
  git -C "$REPOS/api" commit -qm "add the project-scoped workflow"

  # Acknowledge the first-run full-auto notice up front. It is a real part of
  # the product (§16) but it owns the keyboard until it is dismissed, and the
  # first tape of a run would otherwise spend its keystrokes closing it and
  # photograph the board it never reached.
  printf '{\n  "full_auto_notice_ack": true\n}\n' > "$DATA_DIR/tui.json"

  say "daemon"
  daemon_up

  say "projects"
  register() { # register NAME WORKFLOW CAP
    api POST /projects "{\"path\":\"$REPOS/$1\",\"name\":\"$1\",\"default_workflow\":\"$2\",\"max_parallel_tasks\":$3}" | jq -r .id
  }
  P_API="$(register api feature-pr 4)"
  P_WEB="$(register web feature-pr 3)"
  P_DOCS="$(register docs-portal docs-refresh 1)"
  P_INFRA="$(register platform-infra incident-response 2)"
  P_ADAPT="$(register agent-adapters feature-pr 2)"
  P_REL="$(register release-tooling release-train 2)"
  register security-labs security-audit 2 >/dev/null

  say "tasks"
  add() { # add PROJECT WORKFLOW TITLE [EXTRA_JSON]
    local extra="${4:-}"
    api POST /tasks "{\"project_id\":$1,\"workflow\":\"$2\",\"title\":$(jq -Rn --arg t "$3" '$t')${extra:+,$extra}}" | jq -r .id
  }

  # Fast lane first: these have to finish before the slow tasks take the
  # concurrency slots, otherwise they sit `queued` behind a 15-minute agent.
  T_GATE="$(add "$P_API" feature-pr 'add rate limiting to the public API')"
  wait_state "$T_GATE" awaiting_gate 120

  T_DONE="$(add "$P_WEB" feature-pr 'bump the design tokens')"
  wait_state "$T_DONE" awaiting_gate 120
  api POST "/tasks/$T_DONE/approve" >/dev/null
  wait_state "$T_DONE" done 120

  # Its pull request (issue #414): the gh wrapper named this task's branch as
  # #412's head, and the reconciler links it on its next tick, exactly as it
  # links one a human opened on GitHub.
  local branch linked=""
  branch="$(api GET "/tasks/$T_DONE" | jq -r .branch_name)"
  [[ "$branch" == "feat/2-bump-the-design-tokens" ]] \
    || fail "the design-tokens task's branch is $branch, not the head the gh wrapper names"
  for (( i = 0; i < 60; i++ )); do
    linked="$(api GET "/tasks/$T_DONE/github/pull" | jq -r .linked)"
    [[ "$linked" == "true" ]] && break
    sleep 0.5
  done
  [[ "$linked" == "true" ]] || fail "the reconciler never linked #412 to task $T_DONE"

  # Finished and pushed with no pull request: the task the open-a-pull-request
  # popup is photographed on, since that is exactly what `P` offers to fix.
  # The description is what the popup guesses the pull request's body from.
  T_NOPR="$(add "$P_WEB" feature-pr 'tighten the focus ring contrast' \
    '"description":"The focus ring fails WCAG AA against the dark header. Raise it to 3:1 and keep the 2px offset."')"
  wait_state "$T_NOPR" awaiting_gate 120
  api POST "/tasks/$T_NOPR/approve" >/dev/null
  wait_state "$T_NOPR" done 120

  # Chats (task 067, issue #413), seeded while claude still points at the
  # conversational CLI — the three swaps below are one-way.
  say "chats"
  newchat() { # newchat PROJECT AGENT TITLE
    api POST /chats "{\"project_id\":$1,\"agent\":\"$2\",\"title\":$(jq -Rn --arg t "$3" '$t')}" | jq -r .id
  }
  chat_send() { # chat_send ID MESSAGE
    api POST "/chats/$1/send" "{\"message\":$(jq -Rn --arg m "$2" '$m')}" >/dev/null
  }

  # The conversation the workspace shots are of. Two finished turns, so a
  # prompt bubble sits *inside* the history rather than only at the top of it,
  # and the second answer differs from the first — which is the fake CLI
  # resuming its own session rather than starting over. Both answers are
  # markdown with a fenced block and links in them, because the copy picker
  # and the link picker (task 076) are pictures of what prose contains.
  C_RATE="$(newchat "$P_API" claude 'where is the rate limit applied?')"
  chat_send "$C_RATE" 'Where does the rate limiter actually cap traffic? Read internal/limits.go and internal/server.go before you answer.'
  wait_chat "$C_RATE" idle 120
  chat_send "$C_RATE" 'What should the 429 body say, and which header carries the wait?'
  wait_chat "$C_RATE" idle 120

  # A second finished chat, on another adapter and another project, so the
  # board groups more than one thing.
  C_WEB="$(newchat "$P_WEB" cursor 'why does the header flicker on first paint?')"
  chat_send "$C_WEB" 'The header flickers on first paint. Where is it rendered twice?'
  wait_chat "$C_WEB" idle 120

  # A `running` row: codex is the 15-minute wrapper, so this turn is still
  # going when the camera arrives, which is what the turning glyph in the
  # state cell and the counting last-activity cell are pictures of (task 089).
  C_INFRA="$(newchat "$P_INFRA" codex 'walk me through the eu-west failover')"
  chat_send "$C_INFRA" 'Walk me through the eu-west failover, one step at a time.'
  wait_chat "$C_INFRA" running 120

  write_config agent-triage
  sleep 3 # the config watcher, then the adapter's binary-identity re-probe

  T_ASK="$(add "$P_INFRA" incident-response 'restore the eu-west read replica' '"agent":"claude"')"
  wait_state "$T_ASK" awaiting_input 120

  write_config agent-ask
  sleep 3

  # A chat waiting on a human: the row the chats board sorts to the top and
  # the only thing its header badge counts.
  C_ASK="$(newchat "$P_ADAPT" claude 'should the adapter probe models on every start?')"
  chat_send "$C_ASK" 'Should the adapter probe its model catalog on every start, or cache it?'
  wait_chat "$C_ASK" awaiting_input 120

  # Archived chats (task 092, issue #415): two finished conversations, ended.
  # cursor rather than claude — claude now points at the wrapper that asks —
  # and after the chats above, so the live board's chat ids are unchanged.
  ended_chat() { # ended_chat PROJECT TITLE MESSAGE
    local id
    id="$(newchat "$1" cursor "$2")"
    chat_send "$id" "$3"
    wait_chat "$id" idle 120
    api POST "/chats/$id/archive" >/dev/null
  }
  ended_chat "$P_REL" 'how are the release notes assembled?' \
    'Where do the release notes come from, and who edits them before a tag?'
  ended_chat "$P_API" 'is the retry budget per step or per task?' \
    'Is max_retries spent per step, or across the whole task?'

  # The usage window, last of the claude work for the reason write_config
  # gives: the observation outlives the swap, so nothing that needs an answer
  # from claude may come after this. The task parks on the §11 hold task 003
  # gives it, and the observation task 026 records is what the board header
  # badge and the daemon view's quota line are pictures of. The CLI names a
  # 90-minute reset, so nothing re-admits this task during a capture run.
  write_config agent-walled
  sleep 3
  T_WALLED="$(add "$P_ADAPT" incident-response 'retune the planner prompts' '"agent":"claude"')"
  wait_hold "$T_WALLED" 120

  # Blocked: a command that fails with retries exhausted. It is its own
  # workflow so nothing the other shots need has to be sabotaged. It is also
  # the task the Steps & Attempts shot is of (issue #414), which is why it has
  # an agent step — a row with tokens and a cost — and why the failing step
  # prints before it exits and gets one retry: two failed attempts, each with
  # the tail of its output as its result summary.
  cat > "$CONFIG_DIR/workflows/publish-check.yaml" <<'EOF'
name: publish-check
description: Verify the published artifacts are reachable.
defaults:
  max_retries: 1
steps:
  - id: fetch
    type: command
    run: git log -1 --oneline
  - id: collect
    type: agent
    agent: cursor
    prompt: 'List the release artifacts for {{.Task.Title}} and the checksum each should carry.'
  - id: verify
    type: command
    run: |
      echo "vincent_darwin_arm64.tar.gz: OK"
      echo "vincent_linux_amd64.tar.gz: FAILED"
      echo "sha256sum: WARNING: 1 computed checksum did NOT match"
      exit 1
EOF
  sleep 2 # the registry watcher
  T_BLOCK="$(add "$P_REL" publish-check 'verify the signed checksums')"
  wait_state "$T_BLOCK" blocked 120

  # Everything from here to the slow lane was added for issue #415, and is
  # seeded here rather than at the end because it has to run: once the slow
  # lane below is admitted, every slot is taken for the rest of the run.

  # The loop: three passes that succeed and a fourth that blocks, so the
  # board's STEP column carries the rollup and the timeline the tiers.
  T_LOOP="$(add "$P_INFRA" service-migrations 'add tenant_id to every service schema')"
  wait_state "$T_LOOP" blocked 120

  # The fan-out: round 0's two lanes run, finish and are merged, and the
  # merge spawns round 1's lane, which parks at its gate.
  T_FAN="$(add "$P_API" platform-upgrade 'move to the v2 storage driver')"
  local gated=""
  for (( i = 0; i < 240; i++ )); do
    gated="$(api GET "/tasks?parent_id=$T_FAN" | jq -r '[.[] | select(.state == "awaiting_gate")] | length')"
    [[ "$gated" == "1" ]] && break
    sleep 0.5
  done
  [[ "$gated" == "1" ]] || fail "task $T_FAN never spawned its second round to a gate: $(api GET "/tasks?parent_id=$T_FAN" | jq -c '[.[] | {id, state}]')"
  wait_state "$T_FAN" awaiting_children 60

  # Archived tasks (task 092): what the archived board lists — one that
  # finished on its own, one that went through its gate, and one cancelled
  # before it ever ran.
  local archived
  archived="$(add "$P_INFRA" incident-response 'roll back the eu-west DNS change')"
  wait_state "$archived" done 120
  api POST "/tasks/$archived/archive" >/dev/null
  archived="$(add "$P_ADAPT" feature-pr 'drop the codex 0.9 compatibility shim')"
  wait_state "$archived" awaiting_gate 120
  api POST "/tasks/$archived/approve" >/dev/null
  wait_state "$archived" done 120
  api POST "/tasks/$archived/archive" >/dev/null
  archived="$(add "$P_DOCS" docs-refresh 'retire the v1 API reference' '"paused":true')"
  wait_state "$archived" paused 30
  api POST "/tasks/$archived/cancel" >/dev/null
  wait_state "$archived" aborted 30
  api POST "/tasks/$archived/archive" >/dev/null

  # A transcript long enough that the output pane has to truncate it. The cap
  # is 5000 records and only *live* chunks trip it (internal/tui/detail.go), so
  # this has to keep producing while the TUI watches rather than have produced:
  # 100 lines a second crosses the cap in under a minute and keeps going —
  # slow enough that paging back to the top outruns the arriving tail.
  cat > "$CONFIG_DIR/workflows/corpus-index.yaml" <<'EOF'
name: corpus-index
description: Reindex every document in the corpus.
defaults:
  max_retries: 0
  timeout: 4h
steps:
  - id: reindex
    type: command
    run: |
      i=0
      b=0
      while [ $b -lt 600 ]; do
        b=$((b + 1))
        j=0
        while [ $j -lt 100 ]; do
          i=$((i + 1))
          j=$((j + 1))
          echo "indexed document $i of 300000 - docs/corpus/page-$i.md"
        done
        sleep 1
      done
EOF
  sleep 2

  # The task the layout screenshot is taken of: a long build-and-test soak
  # whose output pane has something real moving through it.
  add "$P_API" verify-build 'harden the rate limiter' >/dev/null
  add "$P_DOCS" corpus-index 'reindex the documentation corpus' >/dev/null

  # Slow lane: these stay `running` for the camera (15-minute agent step).
  add "$P_API" feature-delivery 'ship the usage-limit banner' >/dev/null
  add "$P_ADAPT" feature-pr 'probe cursor models over the network' '"agent":"codex"' >/dev/null
  add "$P_WEB" docs-refresh 'document the new cache headers' >/dev/null
  add "$P_API" docs-refresh 'document the rate-limit headers' >/dev/null

  T_PAUSE="$(add "$P_INFRA" docs-refresh 'write the eu-west postmortem')"
  sleep 3
  api POST "/tasks/$T_PAUSE/pause" >/dev/null || true

  add "$P_ADAPT" docs-refresh 'refresh the adapter capability table' >/dev/null
  add "$P_REL" docs-refresh 'refresh the release checklist' >/dev/null

  # Triggers (task 096): an armed command source whose first poll records the
  # two events it already shows as `seeded` ledger rows and fires nothing —
  # the events file is never appended to, so no task comes of it and the board
  # shots are unchanged — beside a disabled GitHub source, which never polls.
  # The interval is an hour because the first poll is the one that matters:
  # every later one records the same two events again as a `deduped` pair, and
  # at 30s a full capture run reached the triggers tape with a ledger of
  # nothing else.
  say "triggers"
  printf '%s\n' '{"id":"build-4211","branch":"feat/1-add-rate-limiting"}' \
    '{"id":"build-4212","branch":"feat/2-bump-the-design-tokens"}' > "$SHOTS/ci-events.ndjson"
  cat > "$CONFIG_DIR/triggers/ci-red.yaml" <<EOF
id: ci-red
enabled: true
source:
  type: command
  project: $P_API
  poll_interval: 1h
  command: ["$FAKEAGENT", "trigger-poll", "$SHOTS/ci-events.ndjson"]
action:
  type: create_task
  workflow: feature-pr
  title: 'fix the red build {{ .Event.id }}'
dedupe_key: 'ci:{{ .Event.id }}'
limits:
  max_per_hour: 5
EOF
  cat > "$CONFIG_DIR/triggers/label-to-task.yaml" <<EOF
id: label-to-task
enabled: false
source:
  type: github_issues
  project: $P_WEB
match:
  action: labeled
  labels: [agent-please]
action:
  type: create_task
  workflow: feature-pr
  title: '{{ .Event.Issue.Title }}'
  github_issue: '{{ .Event.Issue.Number }}'
dedupe_key: 'gh:issue:{{ .Event.Issue.Number }}:label:agent-please'
EOF
  chmod 600 "$CONFIG_DIR"/triggers/*.yaml
  # Not api(): the file reaches the registry on the watcher's next reload, so
  # a 404 before it does is expected rather than fatal.
  local seeded="" got=""
  for (( i = 0; i < 60; i++ )); do
    got="$(curl -sS -H "Authorization: Bearer $TOKEN" "$BASE/triggers/ci-red" || true)"
    seeded="$(jq -r '.poll.seeded // false' <<<"$got" 2>/dev/null || true)"
    [[ "$seeded" == "true" ]] && break
    sleep 0.5
  done
  [[ "$seeded" == "true" ]] || fail "trigger ci-red never seeded: $got"

  sleep 6
  say "seeded — $(api GET /tasks | jq 'length') tasks and $(api GET /chats | jq '.chats | length') chats across $(api GET /projects | jq 'length') projects"
}

# ---------------------------------------------------------------------------
# Capture
# ---------------------------------------------------------------------------

# tape NAME HEIGHT_PX BODY — writes a tape with the shared frame settings and
# runs it. The TUI is started under the seeded $HOME, for the status-line row
# seed_home explains; VHS itself is not, because its headless browser lives
# under the real one. Only the launch is hidden — VHS writes no screenshot for a frame
# reached by keys pressed while hidden, so every tape shows its own driving —
# and every keystroke is followed by
# a sleep: VHS types faster than a human, and a key that lands before Bubble
# Tea has repainted goes to the previous layer (a Down meant for the form's
# rail ends up inside the textarea that still has the keyboard).
tape() {
  local name="$1" height="$2" body="$3"
  [[ -n "${VINCENT_SHOTS_ONLY:-}" && "${VINCENT_SHOTS_ONLY}" != "$name" ]] && return 0
  local file="$TAPES/$name.tape"
  cat > "$file" <<EOF
Output "$GIFS/$name.gif"
Require vincent

Set Shell "bash"
Set FontSize $FONT_PX
Set Width $COLS_PX
Set Height $height
Set Padding 24
Set Margin 36
Set MarginFill "#11141a"
Set BorderRadius 10
Set WindowBar Colorful
Set WindowBarSize 44
Set Theme "TokyoNight"
Set TypingSpeed 12ms

Hide
Type "clear && HOME=$HOME_DIR vincent" Enter
Sleep 5s
Show
$body
# VHS writes a Screenshot on the *next* frame it captures, so a tape that ends
# on one records nothing at all. This trailing sleep is what makes the file
# appear.
Sleep 2s
EOF
  say "capture $name"
  rm -f "$OUT/$name.png" # so a tape that records nothing fails loudly
  ( cd "$SHOTS" && PATH="$BIN:$PATH" vhs "$file" >/dev/null ) || fail "vhs failed on $name"
  [[ -f "$OUT/$name.png" ]] || fail "vhs produced no $OUT/$name.png"
}

do_capture() {
  command -v vhs >/dev/null 2>&1 || fail "vhs is not on PATH — brew install vhs"
  daemon_attach
  mkdir -p "$TAPES" "$GIFS" "$OUT"

  # The three Workflows tapes open the fan-out workflow by its row, and the
  # global block is sorted by name — so every workflow the seed or the
  # built-ins add moves it. The row is looked up rather than counted by hand,
  # which is how those tapes once photographed docs-refresh instead.
  local wf_row
  wf_row="$(api GET /workflows | jq '[.workflows[].name] | sort | index("feature-delivery")')"
  [[ "$wf_row" =~ ^[0-9]+$ ]] || fail "feature-delivery is not in the global registry"

  # The board-only home screen, filtered to one running task.
  tape tui-board 1250 '
Type "/"
Sleep 500ms
Type "harden"
Sleep 1s
Tab
Sleep 6s
Screenshot "'"$OUT"'/tui-board.png"
'

  # Grouping: project › workflow, the shape the board takes out of the box.
  tape tui-grouping 1400 '
Sleep 3s
Screenshot "'"$OUT"'/tui-grouping.png"
'

  # Bulk selection — `V` takes every row the filter is showing, and the
  # footer reports what the action keys would act on.
  tape tui-multi-select 1400 '
Sleep 2s
Type "V"
Sleep 3s
Screenshot "'"$OUT"'/tui-multi-select.png"
'

  # The Diff tab: grouped by file, collapsed, with one file expanded.
  tape tui-diff 1250 '
Type "/"
Sleep 500ms
Type "design tokens"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "4"
Sleep 4s
Down 1
Sleep 500ms
Enter
Sleep 3s
Screenshot "'"$OUT"'/tui-diff.png"
'

  # New task, driven to its review stage with a real request in it.
  tape tui-new-task 1050 '
Type "n"
Sleep 3s
Down 2
Sleep 500ms
Enter
Sleep 500ms
Type "cap the public API per token per minute"
Sleep 500ms
Enter
Sleep 500ms
Down 1
Sleep 500ms
Enter
Sleep 500ms
Type "Return 429 with a Retry-After header once a token is over budget."
Sleep 500ms
Escape
Sleep 1s
Down 4
Sleep 500ms
Enter
Sleep 500ms
Backspace 3
Type "80"
Sleep 500ms
Enter
Sleep 1s
Down 1
Sleep 500ms
Enter
Sleep 1s
Down 2
Sleep 500ms
Enter
Sleep 1s
Down 3
Sleep 1s
Sleep 1s
Screenshot "'"$OUT"'/tui-new-task.png"
'

  # Projects: the registry on the left, the selected repository and its
  # current workload on the right.
  tape tui-projects 1250 '
Type ":"
Sleep 1s
Type "projects"
Sleep 1s
Enter
Sleep 3s
Sleep 2s
Screenshot "'"$OUT"'/tui-projects.png"
'

  # Workflows, expanded into the control-flow graph of the fan-out workflow.
  tape tui-workflow-graph 1400 '
Type ":"
Sleep 1s
Type "workflows"
Sleep 1s
Enter
Sleep 3s
Down '"$wf_row"'
Sleep 1s
Type "g"
Sleep 4s
Screenshot "'"$OUT"'/tui-workflow-graph.png"
'

  # The same graph with the step-detail popup open on `plan`, the node the
  # graph opens on: the prompt in
  # full, and the values inherited from the file'"'"'s defaults block marked as
  # inherited. The graph beneath is the shot above and is unchanged, which is
  # why this is a second tape rather than a replacement.
  #
  # `enter` is pressed *outside* a Hide block and the tape does not end on the
  # Screenshot: keys inside Hide never reach a screenshot, and a Screenshot is
  # written on the next captured frame, so a tape ending on one records
  # nothing.
  # The structured editor (task 065), open on the same fan-out workflow: rows
  # rendered from the served §8.2 schema, with the row under the cursor
  # explaining itself. `i` is pressed outside a Hide block and the tape does
  # not end on the Screenshot, for the two VHS traps documented above.
  tape tui-workflow-editor 1400 '
Type ":"
Sleep 1s
Type "workflows"
Sleep 1s
Enter
Sleep 3s
Down '"$wf_row"'
Sleep 1s
Type "i"
Sleep 4s
Down 4
Sleep 2s
Screenshot "'"$OUT"'/tui-workflow-editor.png"
Sleep 2s
'

  tape tui-workflow-step 1400 '
Type ":"
Sleep 1s
Type "workflows"
Sleep 1s
Enter
Sleep 3s
Down '"$wf_row"'
Sleep 1s
Type "g"
Sleep 4s
Enter
Sleep 3s
Screenshot "'"$OUT"'/tui-workflow-step.png"
Sleep 2s
'

  # Triggers (task 096): the list with the armed command source selected and
  # its ledger of `seeded` deliveries beside it.
  tape tui-triggers 1250 '
Type ":"
Sleep 1s
Type "triggers"
Sleep 1s
Enter
Sleep 4s
Screenshot "'"$OUT"'/tui-triggers.png"
Sleep 2s
'

  # Chats (task 067): the second board, grouped by project, with the chat
  # waiting on a human sorted to the top and counted in the header badge, and
  # a `running` row below it.
  tape tui-chats 900 '
Type ":"
Sleep 1s
Type "chats"
Sleep 1s
Enter
Sleep 4s
Screenshot "'"$OUT"'/tui-chats.png"
Sleep 2s
'

  # The chat workspace, at `quiet` — the reading level (task 071), which is
  # where the answer is prose and nothing else. ctrl+r cycles quiet → compact
  # → normal → verbose and starts at normal, so two presses reach it. The
  # board'"'"'s filter commits with enter rather than tab: this list types into
  # its filter field, and a tab would be typed into it.
  tape tui-chat 1400 '
Type ":"
Sleep 1s
Type "chats"
Sleep 1s
Enter
Sleep 4s
Type "/"
Sleep 500ms
Type "rate limit"
Sleep 1s
Enter
Sleep 1s
Enter
Sleep 5s
Ctrl+R
Sleep 1s
Ctrl+R
Sleep 3s
Screenshot "'"$OUT"'/tui-chat.png"
Sleep 2s
'

  # The handoff form (task 074): the new-task form opened on the chat. It
  # opens on the title row, three rows above the two that make it a handoff
  # rather than a new task — the base branch and the branch, marked
  # `(from the chat)` because they name a worktree that already exists — so
  # the tape walks down to them.
  tape tui-chat-handoff 1050 '
Type ":"
Sleep 1s
Type "chats"
Sleep 1s
Enter
Sleep 4s
Type "/"
Sleep 500ms
Type "rate limit"
Sleep 1s
Enter
Sleep 1s
Enter
Sleep 5s
Ctrl+T
Sleep 4s
Down 3
Sleep 2s
Screenshot "'"$OUT"'/tui-chat-handoff.png"
Sleep 2s
'

  # The copy picker (task 076): one row per payload of each assistant message
  # — the markdown, the plain text, and every fenced block in it.
  tape tui-chat-copy 1400 '
Type ":"
Sleep 1s
Type "chats"
Sleep 1s
Enter
Sleep 4s
Type "/"
Sleep 500ms
Type "rate limit"
Sleep 1s
Enter
Sleep 1s
Enter
Sleep 5s
Ctrl+Y
Sleep 3s
Screenshot "'"$OUT"'/tui-chat-copy.png"
Sleep 2s
'

  # Everything below was added for issue #414 and runs last on purpose. A
  # tape's picture depends on how long after the seed it is taken — the
  # triggers ledger grows a `deduped` pair on every poll and the board's
  # clocks keep counting — so new tapes go after the old ones rather than
  # beside the tab they are of, and the pictures above stay the ones they were.

  # The task workspace's other six tabs (issue #414), each on the task whose
  # state is what the tab is for. `enter` on a board row opens the workspace
  # on Steps & Attempts; the digits pick a tab.

  # Steps & Attempts, on the blocked task: a command step and an agent step
  # that succeeded, then both failed attempts of the step it is blocked on,
  # each with vincent'"'"'s failure reason and the step'"'"'s result summary.
  tape tui-task-steps 1250 '
Type "/"
Sleep 500ms
Type "signed checksums"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 4s
Screenshot "'"$OUT"'/tui-task-steps.png"
Sleep 2s
'

  # Task Details, on the task at its gate: the sectioned inspector, one
  # section down from the description it opens on.
  tape tui-task-details 1400 '
Type "/"
Sleep 500ms
Type "public API"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "2"
Sleep 2s
Down 1
Sleep 2s
Screenshot "'"$OUT"'/tui-task-details.png"
Sleep 2s
'

  # Output, on the soak: a real `go test -v` run arriving live.
  tape tui-task-output 1250 '
Type "/"
Sleep 500ms
Type "harden"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "3"
Sleep 5s
Screenshot "'"$OUT"'/tui-task-output.png"
Sleep 2s
'

  # Workflow, on the task at its gate: the graph with the run on it — two
  # steps done, the gate it is parked at, and the step it never reached.
  tape tui-task-workflow 1400 '
Type "/"
Sleep 500ms
Type "public API"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "5"
Sleep 4s
Screenshot "'"$OUT"'/tui-task-workflow.png"
Sleep 2s
'

  # Step Details, on the same task'"'"'s agent step: the prompt it was
  # actually handed, and where each resolved value came from. The tab lands
  # on the gate'"'"'s attempt, the newest; the agent step is two above it.
  tape tui-task-step-details 1400 '
Type "/"
Sleep 500ms
Type "public API"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "6"
Sleep 2s
Up 2
Sleep 3s
Screenshot "'"$OUT"'/tui-task-step-details.png"
Sleep 2s
'

  # Pull Request, on the finished task the reconciler linked to #412: its
  # facts, and one row per check on its head commit. The cursor is moved to
  # the failed Actions check, the one row `ctrl+r` is offered on.
  tape tui-task-pull 1250 '
Type "/"
Sleep 500ms
Type "design tokens"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "7"
Sleep 4s
Down 1
Sleep 2s
Screenshot "'"$OUT"'/tui-task-pull.png"
Sleep 2s
'

  # The four popups (issue #414). None of them is ever submitted: the tape
  # ends with the popup open, and quitting the TUI discards the draft.

  # Repair (`R`), on the blocked task, with a prompt written and kept —
  # `ctrl+s` inside the field keeps the text; only a second one would start
  # the repair.
  tape tui-repair 1250 '
Type "/"
Sleep 500ms
Type "signed checksums"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "R"
Sleep 2s
Enter
Sleep 1s
Type "The verify step exits 1. Find out why the checksum comparison fails and fix it."
Sleep 1s
Ctrl+S
Sleep 2s
Screenshot "'"$OUT"'/tui-repair.png"
Sleep 2s
'

  # Follow-up (`F`), on the finished task, with a prompt written and kept.
  tape tui-follow-up 1250 '
Type "/"
Sleep 500ms
Type "design tokens"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "F"
Sleep 2s
Down 1
Sleep 500ms
Enter
Sleep 1s
Type "Rebase the branch onto main and re-run the token snapshot tests."
Sleep 1s
Ctrl+S
Sleep 2s
Screenshot "'"$OUT"'/tui-follow-up.png"
Sleep 2s
'

  # The answer form, on the task whose claude step asked two questions (the
  # agent-triage wrapper): the single-choice one answered, and one box ticked
  # on the multi-select one below it — three rows down, past the free-text
  # row every question ends with. Nothing is submitted.
  tape tui-answer 1250 '
Type "/"
Sleep 500ms
Type "read replica"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Enter
Sleep 2s
Space
Sleep 1s
Down 3
Sleep 1s
Space
Sleep 2s
Screenshot "'"$OUT"'/tui-answer.png"
Sleep 2s
'

  # Open a pull request (`P`, from Task Details), on the finished task that
  # has none: the title and body vincent guessed, and the draft toggle.
  tape tui-create-pr 1250 '
Type "/"
Sleep 500ms
Type "focus ring"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "2"
Sleep 2s
Type "P"
Sleep 3s
Screenshot "'"$OUT"'/tui-create-pr.png"
Sleep 2s
'

  # Everything below was added for issue #415, and follows the tapes of #414
  # for the reason given above them.

  # New task on the workflow that declares fields (tasks 022, 058). The
  # workflow picker narrows only behind `/` — a letter typed into it is a
  # key, and the `q` in "request" quits the TUI — so the tape filters, then
  # commits the filter and the choice with two enters. In Fields the ticket
  # gets a value, and the `multiple` enum is left open with two regions
  # ticked: the list is the only way to change one.
  tape tui-new-task-fields 1250 '
Type "n"
Sleep 3s
Down 1
Sleep 500ms
Enter
Sleep 1s
Type "/"
Sleep 500ms
Type "change"
Sleep 1s
Enter
Sleep 1s
Enter
Sleep 1s
Down 1
Sleep 500ms
Enter
Sleep 500ms
Type "rotate the eu-west database credentials"
Sleep 500ms
Enter
Sleep 500ms
Down 2
Sleep 500ms
Enter
Sleep 1s
Enter
Sleep 500ms
Type "CHG-2291"
Sleep 500ms
Enter
Sleep 1s
Down 2
Sleep 500ms
Enter
Sleep 1s
Space
Sleep 500ms
Down 2
Sleep 500ms
Space
Sleep 2s
Screenshot "'"$OUT"'/tui-new-task-fields.png"
Sleep 2s
'

  # The loop (task 083), on the task blocked in its fourth pass: the rollup
  # on the header line, and the passes folded shut with the one it stopped on
  # open. `up` stops on the folded pass above it and `right` opens that one
  # too, so the picture has a pass that succeeded beside the one that did not.
  tape tui-loop 1400 '
Type "/"
Sleep 500ms
Type "tenant_id"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 4s
Up 1
Sleep 1s
Right
Sleep 2s
Screenshot "'"$OUT"'/tui-loop.png"
Sleep 2s
'

  # The fan-out (task 084), parked between its two rounds. On the board, `L`
  # hangs its three lanes under it.
  tape tui-lanes 1250 '
Type "/"
Sleep 500ms
Type "v2 storage"
Sleep 1s
Tab
Sleep 1s
Type "L"
Sleep 3s
Screenshot "'"$OUT"'/tui-lanes.png"
Sleep 2s
'

  # The Output tab, with `>` moved off the task and onto its first lane.
  tape tui-lane-output 1250 '
Type "/"
Sleep 500ms
Type "v2 storage"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "3"
Sleep 2s
Type ">"
Sleep 3s
Screenshot "'"$OUT"'/tui-lane-output.png"
Sleep 2s
'

  # The Diff tab, grouped by lane: `O` opens everything, and `enter` folds
  # the first lane again so the frame holds all three sections.
  tape tui-lane-diff 1250 '
Type "/"
Sleep 500ms
Type "v2 storage"
Sleep 1s
Tab
Sleep 1s
Enter
Sleep 3s
Type "4"
Sleep 4s
Type "O"
Sleep 2s
Enter
Sleep 2s
Screenshot "'"$OUT"'/tui-lane-diff.png"
Sleep 2s
'

  # The pull requests screen (task 052): the open listing of the one GitHub
  # project, with #412 claimed by the task the reconciler linked it to.
  tape tui-pull-requests 1050 '
Type ":"
Sleep 1s
Type "pull requests"
Sleep 1s
Enter
Sleep 5s
Screenshot "'"$OUT"'/tui-pull-requests.png"
Sleep 2s
'

  # The two archived boards (task 092), each opened from the palette.
  tape tui-archived 1050 '
Type ":"
Sleep 1s
Type "archived tasks"
Sleep 1s
Enter
Sleep 4s
Screenshot "'"$OUT"'/tui-archived.png"
Sleep 2s
'

  tape tui-archived-chats 1050 '
Type ":"
Sleep 1s
Type "archived chats"
Sleep 1s
Enter
Sleep 4s
Screenshot "'"$OUT"'/tui-archived-chats.png"
Sleep 2s
'

  # The daemon view with `tab` on the config list (task 060): every key,
  # its value, and the default where the two differ, with the adapters and
  # their usage windows below.
  tape tui-daemon-config 1400 '
Type ":"
Sleep 1s
Type "daemon"
Sleep 1s
Enter
Sleep 4s
Tab
Sleep 1s
Down 3
Sleep 2s
Screenshot "'"$OUT"'/tui-daemon-config.png"
Sleep 2s
'

  # The skills offer (`S`, task 095), over the seeded home: one skill a
  # version behind and linked into claude, one not installed.
  tape tui-skills 1400 '
Type ":"
Sleep 1s
Type "daemon"
Sleep 1s
Enter
Sleep 4s
Type "S"
Sleep 2s
Screenshot "'"$OUT"'/tui-skills.png"
Sleep 2s
'

  # The chat skill list (task 124.19), on the same seeded conversation
  # tui-chat is of. One tape, two pictures, because the list has two ways in:
  # `tab` browses it, and typing the sigil the agent itself uses opens it
  # filtered. Browse goes first — it is what pays for the probe, so
  # the inline frame is not racing a spawn of the CLI. A `Down` precedes each
  # Screenshot: both openers start with nothing highlighted, so that `enter`
  # still sends the draft as typed, and the three reserved description lines
  # are blank until a row is picked.
  tape tui-chat-skills 1400 '
Type ":"
Sleep 1s
Type "chats"
Sleep 1s
Enter
Sleep 4s
Type "/"
Sleep 500ms
Type "rate limit"
Sleep 1s
Enter
Sleep 1s
Enter
Sleep 5s
Tab
Sleep 4s
Down
Sleep 2s
Screenshot "'"$OUT"'/tui-chat-skills.png"
Sleep 2s
Escape
Sleep 1s
Type "/re"
Sleep 3s
Down
Sleep 2s
Screenshot "'"$OUT"'/tui-chat-skills-inline.png"
Sleep 2s
'

  # The chat composer's `@` file picker (task 126.11), on the same seeded
  # conversation, in a tape of its own because the picker and the skills list
  # are never drawn together — one TUI can only photograph one of them.
  # `@internal` is typed rather than a bare `@`: a bare sigil opens nothing,
  # and the query is what puts the ranking in the picture. It matches the four
  # files make_repo writes under `internal/` in every seeded repository, on
  # the whole-path tier — pick another query and check it against that
  # function first, since these repositories hold eight files and not this
  # one's. A `Down` precedes the Screenshot for the reason the two above it
  # do, and one more: the line reserved under the rows carries the highlighted
  # row's whole path and is blank until a row is picked.
  tape tui-chat-files 1400 '
Type ":"
Sleep 1s
Type "chats"
Sleep 1s
Enter
Sleep 4s
Type "/"
Sleep 500ms
Type "rate limit"
Sleep 1s
Enter
Sleep 1s
Enter
Sleep 5s
Type "@internal"
Sleep 3s
Down
Sleep 2s
Screenshot "'"$OUT"'/tui-chat-files.png"
Sleep 2s
'
}

case "${1:-all}" in
  seed) do_seed ;;
  capture) do_capture ;;
  clean) do_clean ;;
  all) do_seed; do_capture; do_clean ;;
  *) fail "unknown argument: $1 (expected seed, capture, clean or all)" ;;
esac
