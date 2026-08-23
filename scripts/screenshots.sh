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
# Requirements: bash, go, git, curl, jq, vhs (`brew install vhs`).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SHOTS="${VINCENT_SHOTS_DIR:-/tmp/vincent-demo}"
SHOTS="${SHOTS%/}"

BIN="$SHOTS/bin"
CONFIG_DIR="$SHOTS/config"
DATA_DIR="$SHOTS/data"
REPOS="$SHOTS/repos"
TAPES="$SHOTS/tapes"
GIFS="$SHOTS/gifs"
OUT="$ROOT/docs/assets"

VINCENT="$BIN/vincent"
FAKEAGENT="$BIN/fakeagent"

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

daemon_up() {
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

# agent_wrapper NAME ENV... — the config points each adapter at its own
# wrapper so one daemon can run three different fake CLIs at once: a slow one
# whose tasks stay `running` for the camera, a fast one that drives tasks
# through to a gate, and one that asks a question. The alternative — one
# FAKEAGENT_SCENARIO for the whole process — cannot hold a board in more than
# one state at a time.
agent_wrapper() {
  local name="$1"; shift
  {
    printf '#!/bin/sh\n'
    printf 'exec env'
    for kv in "$@"; do printf ' %s' "$kv"; done
    printf ' "%s" "$@"\n' "$FAKEAGENT"
  } > "$BIN/$name"
  chmod +x "$BIN/$name"
}

do_seed() {
  command -v vhs >/dev/null 2>&1 || fail "vhs is not on PATH — brew install vhs"
  command -v jq >/dev/null 2>&1 || fail "jq is not on PATH"

  do_clean >/dev/null 2>&1 || true
  mkdir -p "$BIN" "$CONFIG_DIR/workflows" "$DATA_DIR" "$REPOS" "$TAPES" "$GIFS"

  # Built with the release ldflags rather than plain `go build`: the TUI
  # header prints its own version, and an uninjected build prints the module
  # pseudo-version (`v0.5.1-0.2026…+dirty`), which is noise in a screenshot.
  say "build vincent + fakeagent"
  local vpkg="github.com/lezli01/vincent/internal/version" vtag
  vtag="$(git -C "$ROOT" describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)"
  (cd "$ROOT" && go build -trimpath \
    -ldflags "-X $vpkg.version=${vtag#v} -X $vpkg.commit=$(git -C "$ROOT" rev-parse --short HEAD) -X $vpkg.date=$(date -u +%Y-%m-%d)" \
    -o "$BIN/" ./cmd/vincent ./cmd/fakeagent)

  agent_wrapper agent-slow FAKEAGENT_DIALECT=codex FAKEAGENT_SCENARIO_CODEX=success FAKEAGENT_DELAY_MS=3600000
  agent_wrapper agent-fast FAKEAGENT_DIALECT=cursor FAKEAGENT_SCENARIO_CURSOR=success FAKEAGENT_DELAY_MS=1500
  agent_wrapper agent-ask FAKEAGENT_SCENARIO=ask-question FAKEAGENT_ASK_MULTI=1

  say "config"
  cat > "$CONFIG_DIR/config.yaml" <<EOF
# Seeded by scripts/screenshots.sh. Each adapter points at a wrapper around
# cmd/fakeagent so no real agent CLI — and no real spend — is involved.
max_parallel_tasks: 4
branch_template: "feat/{{.ID}}{{with .Slug}}-{{.}}{{end}}"
agents:
  claude:
    path: "$BIN/agent-ask"
  codex:
    path: "$BIN/agent-slow"
  cursor:
    path: "$BIN/agent-fast"
EOF

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

  say "repositories"
  local p
  for p in api web docs-portal platform-infra agent-adapters release-tooling security-labs; do
    make_repo "$p"
  done

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

  T_ASK="$(add "$P_INFRA" incident-response 'restore the eu-west read replica' '"agent":"claude"')"
  wait_state "$T_ASK" awaiting_input 120

  # Blocked: a command that fails with retries exhausted. It is its own
  # workflow so nothing the other shots need has to be sabotaged.
  cat > "$CONFIG_DIR/workflows/publish-check.yaml" <<'EOF'
name: publish-check
description: Verify the published artifacts are reachable.
defaults:
  max_retries: 0
steps:
  - id: fetch
    type: command
    run: git log -1 --oneline
  - id: verify
    type: command
    run: exit 1
EOF
  sleep 2 # the registry watcher
  T_BLOCK="$(add "$P_REL" publish-check 'verify the signed checksums')"
  wait_state "$T_BLOCK" blocked 120

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

  sleep 6
  say "seeded — $(api GET /tasks | jq 'length') tasks across $(api GET /projects | jq 'length') projects"
}

# ---------------------------------------------------------------------------
# Capture
# ---------------------------------------------------------------------------

# tape NAME HEIGHT_PX BODY — writes a tape with the shared frame settings and
# runs it. Only the launch is hidden — VHS writes no screenshot for a frame
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
Type "clear && vincent" Enter
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

  # The three panels — task table, timeline, output — with a running task
  # open. Filtered to the running tasks so the table is short enough that the
  # timeline and the live tail get the room the layout is about.
  tape tui-board 1250 '
Type "/"
Sleep 500ms
Type "harden"
Sleep 1s
Tab
Sleep 1s
Enter
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
Tab
Sleep 1s
Type "]"
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
Down 3
Sleep 1s
Type "g"
Sleep 4s
Screenshot "'"$OUT"'/tui-workflow-graph.png"
'
}

case "${1:-all}" in
  seed) do_seed ;;
  capture) do_capture ;;
  clean) do_clean ;;
  all) do_seed; do_capture; do_clean ;;
  *) fail "unknown argument: $1 (expected seed, capture, clean or all)" ;;
esac
