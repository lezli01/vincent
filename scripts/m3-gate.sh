#!/usr/bin/env bash
# M3 phase gate (T3.8; spec §19 M3) — seeding only.
#
# Unlike m1-gate.sh and m2-gate.sh, this script asserts nothing. M1 and M2
# are curl flows because an HTTP API has a scriptable surface; M3's acceptance
# is "the full loop is doable without leaving the TUI", which is a judgement
# about a terminal program, and the Phase 3 decision ruled out driving one
# from tests (no teatest, no golden frames). So this script builds the
# binaries, seeds two repos, a bare remote, three workflows and a config, and
# then prints what a human needs to run the walkthrough in
# docs/versions/v0/m3-gate.md. It never starts the daemon and never registers
# a project: auto-start and registration are the first two checklist items.
#
# It deliberately does not exec `vincent` either. On Windows this runs under
# Git Bash, which hands a native console application a pipe rather than a
# console, and a Bubble Tea alt-screen app in that host is a coin flip — a
# gate that fails on its own terminal host proves nothing about the TUI.
# Printing a launch line instead makes the terminal host a recorded variable
# (Windows Terminal + pwsh vs. Terminal.app) rather than an artifact of how
# the script was invoked, and lets the walkthrough relaunch `vincent` after
# the deliberate reconnect kill without re-seeding.
#
# Usage:
#   scripts/m3-gate.sh          seed, then print the launch instructions
#   scripts/m3-gate.sh clean    stop the daemon and remove the seeded state
#
#   VINCENT_GATE_AGENT=claude   (default) real claude on the question leg
#   VINCENT_GATE_AGENT=fake     zero-spend rehearsal, fakeagent everywhere
#   VINCENT_GATE_DIR=<path>     where to seed (default: $TMPDIR/vincent-m3-gate)
#   VINCENT_GATE_RESET=1        re-seed over an existing directory
#
# Requirements: bash, go, git. jq and curl are not needed — nothing here
# talks to the API.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATE="${VINCENT_GATE_DIR:-${TMPDIR:-/tmp}/vincent-m3-gate}"
GATE="${GATE%/}"

BIN="$GATE/bin"
CONFIG_DIR="$GATE/config"
DATA_DIR="$GATE/data"
REPO_A="$GATE/repo-app"
REPO_B="$GATE/repo-spare"
REMOTE="$GATE/remote.git"

VINCENT="$BIN/vincent"
FAKEAGENT="$BIN/fakeagent"
if [[ "${OS:-}" == "Windows_NT" ]]; then
  VINCENT+=".exe"
  FAKEAGENT+=".exe"
fi

# The delay that makes three tasks observable side by side. Long enough to
# open a task, watch its tail move and get back to the board; two seconds is
# not (PR N decision).
DELAY_MS=25000

fail() { echo "GATE SETUP FAIL: $*" >&2; exit 1; }

# hostpath converts a bash path for consumption by vincent — Git Bash /tmp
# paths are meaningless to a native Windows binary. -m keeps forward slashes
# so nothing needs YAML/JSON escaping. (m1/m2-gate convention.)
hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

# resolved_editor mirrors tui/editor.go:editorCommand(). Reported, never
# overridden: pinning $EDITOR would test this script's export instead of the
# product, and the Windows failure worth catching is the human's real editor
# doing something on exit the TUI did not expect.
resolved_editor() {
  if [[ -n "${VISUAL:-}" ]]; then printf '%s (VISUAL)\n' "$VISUAL"
  elif [[ -n "${EDITOR:-}" ]]; then printf '%s (EDITOR)\n' "$EDITOR"
  elif [[ "${OS:-}" == "Windows_NT" ]]; then printf 'notepad (built-in default)\n'
  else printf 'vi (built-in default)\n'
  fi
}

# ---------------------------------------------------------------------------
# clean — the counterpart to seeding. The script exits before the walkthrough
# starts, so it cannot trap-remove its own output the way m1/m2-gate do; the
# seeded tree has to outlive it, and after a failed gate the transcripts and
# the daemon log are the entire diagnosis.
# ---------------------------------------------------------------------------
if [[ "${1:-}" == "clean" ]]; then
  if [[ -x "$VINCENT" ]]; then
    VINCENT_CONFIG_DIR="$(hostpath "$CONFIG_DIR")" \
    VINCENT_DATA_DIR="$(hostpath "$DATA_DIR")" \
      "$VINCENT" daemon stop --force >/dev/null 2>&1 || true
  fi
  rm -rf "$GATE"
  echo "removed $GATE"
  exit 0
fi
[[ $# -eq 0 ]] || fail "unknown argument: $1 (expected none, or 'clean')"

if [[ -e "$GATE" && "${VINCENT_GATE_RESET:-0}" != "1" ]]; then
  fail "$GATE already exists — re-run with VINCENT_GATE_RESET=1, or 'scripts/m3-gate.sh clean' first"
fi
rm -rf "$GATE"

REAL_AGENT=1
[[ "${VINCENT_GATE_AGENT:-claude}" == "fake" ]] && REAL_AGENT=0

# ---------------------------------------------------------------------------
# Preflight. The question leg is leg 4 of the loop: discovering there that
# claude is missing or logged out costs the whole walkthrough and reads, in
# the moment, exactly like a product bug.
# ---------------------------------------------------------------------------
if (( REAL_AGENT )); then
  command -v claude >/dev/null 2>&1 \
    || fail "claude is not on PATH — install it, or rehearse with VINCENT_GATE_AGENT=fake"
  CLAUDE_VERSION="$(claude --version 2>/dev/null | head -1)" \
    || fail "claude --version failed — check the CLI is usable before starting"
  [[ -n "$CLAUDE_VERSION" ]] || fail "claude --version printed nothing"
else
  CLAUDE_VERSION="(rehearsal: fakeagent)"
fi

echo "== build vincent + fakeagent"
mkdir -p "$BIN"
(cd "$ROOT" && go build -o "$(hostpath "$BIN")/" ./cmd/vincent ./cmd/fakeagent)

# ---------------------------------------------------------------------------
# Config. Split adapters: claude resolves the real CLI (empty path) while
# codex points at fakeagent, so one TUI session runs a real agent on the
# question leg and fake agents for parallelism, and the new-task form's
# per-task agent override picks between them. Re-running a script to swap
# agents mid-loop is exactly the "leaving the TUI" the acceptance forbids.
# ---------------------------------------------------------------------------
mkdir -p "$CONFIG_DIR" "$DATA_DIR"
CLAUDE_PATH=""
(( REAL_AGENT )) || CLAUDE_PATH="$(hostpath "$FAKEAGENT")"

cat > "$CONFIG_DIR/config.yaml" <<EOF
# Seeded by scripts/m3-gate.sh (T3.8). max_parallel_tasks is the default 3,
# stated explicitly because the walkthrough creates four tasks and watches
# the fourth wait for a slot.
max_parallel_tasks: 3
agents:
  claude:
    path: "$CLAUDE_PATH"
  codex:
    path: "$(hostpath "$FAKEAGENT")"
EOF

# ---------------------------------------------------------------------------
# Workflows. m3-loop drives the acceptance loop on the real agent; the
# m3-parallel pair exists twice on purpose — global and project-scoped — so
# the Workflows view's shadowing row has something true to report, and the
# two differ in step id and step count so the board itself names which file
# won rather than the UI grading its own note.
# ---------------------------------------------------------------------------
mkdir -p "$CONFIG_DIR/workflows"
MODEL_LINE=""
(( REAL_AGENT )) && MODEL_LINE="  model: haiku"

cat > "$CONFIG_DIR/workflows/m3-loop.yaml" <<EOF
name: m3-loop
description: M3 gate — ask a question, commit the answer, gate, publish.
defaults:
  agent: claude
  max_retries: 0
$MODEL_LINE
steps:
  - id: ask
    type: agent
    prompt: |
      Use the AskUserQuestion tool to ask the user which color they prefer
      (options: Red, Blue). After the answer arrives, append one line stating
      the chosen color to README.md in the current directory. Do not do
      anything else. Task: {{.Task.Title}}
  - id: commit
    type: command
    run: 'git add -A && git commit -m "m3 gate: record the answer"'
  - id: review
    type: manual
    instructions: |
      Inspect the diff of task #{{.Task.ID}} before it is published.
  - id: publish
    type: command
    run: git push publish {{.Task.BranchName}}
EOF

cat > "$CONFIG_DIR/workflows/m3-parallel.yaml" <<'EOF'
name: m3-parallel
description: M3 gate — global registry copy. One slow agent step.
defaults:
  agent: codex
  max_retries: 0
steps:
  - id: work-global
    type: agent
    prompt: |
      Global copy of m3-parallel. Task: {{.Task.Title}}
EOF

# Deliberately invalid, for the Workflows view's invalid-entry state. It is a
# third file rather than a broken copy of one the loop needs: the sweep gets
# its state without the walkthrough depending on a file it just sabotaged.
cat > "$CONFIG_DIR/workflows/m3-broken.yaml" <<'EOF'
name: m3-broken
description: M3 gate — invalid on purpose (unknown step type).
steps:
  - id: nope
    type: not-a-step-type
    run: this should never load
EOF

# ---------------------------------------------------------------------------
# Repos. Nothing is registered: registration is leg 1 of the §19 loop, and
# pre-doing it deletes a checklist item. Writing .vincent/workflows before
# the project exists is fine — ReloadProject reads it when the project
# appears, and the watcher handles a directory created later (watch.go:28).
# ---------------------------------------------------------------------------
make_repo() { # make_repo PATH README_LINE
  git init -q -b main "$1"
  git -C "$1" config user.name "m3 gate"
  git -C "$1" config user.email gate@example.invalid
  git -C "$1" config commit.gpgsign false
  printf '%s\n' "$2" > "$1/README.md"
  git -C "$1" add . && git -C "$1" commit -qm init
}

make_repo "$REPO_A" "m3 gate — application repo"
# The spare exists so the Projects view is never a one-row list and so
# delete-refused-by-a-running-task has somewhere to happen.
make_repo "$REPO_B" "m3 gate — spare repo"

git init -q --bare "$REMOTE"
git -C "$REPO_A" remote add publish "$(hostpath "$REMOTE")"

mkdir -p "$REPO_A/.vincent/workflows"
cat > "$REPO_A/.vincent/workflows/m3-parallel.yaml" <<'EOF'
name: m3-parallel
description: M3 gate — project-scoped shadow. This is the copy that should run.
defaults:
  agent: codex
  max_retries: 0
steps:
  - id: work-shadow
    type: agent
    prompt: |
      Project-scoped shadow of m3-parallel. Task: {{.Task.Title}}
  - id: confirm-shadow
    type: command
    run: git status --porcelain
EOF
git -C "$REPO_A" add .vincent && git -C "$REPO_A" commit -qm "add project-scoped workflow"

# ---------------------------------------------------------------------------
# Banner. Both shells are printed because the walkthrough runs on Windows
# Terminal + pwsh and on a POSIX terminal, and the env vars are what carry the
# seeded dirs and the fake-agent knobs into the daemon the TUI starts.
#
# The POSIX form is one-shot `env VAR=… vincent`, not `export`: an exported
# FAKEAGENT_DELAY_MS outlives the walkthrough and the next `go test ./...` in
# that shell inherits it, which fails cmd/fakeagent and internal/agent/codex
# with what look like real regressions (M3 gate finding, macOS). pwsh keeps
# the assignment form — there is no readable one-shot there — and the
# "when you are done" line below clears it.
# ---------------------------------------------------------------------------
H_CONFIG="$(hostpath "$CONFIG_DIR")"
H_DATA="$(hostpath "$DATA_DIR")"
H_VINCENT="$(hostpath "$VINCENT")"

cat <<EOF

=====================================================================
 M3 gate seeded.  Checklist: docs/versions/v0/m3-gate.md
=====================================================================

 mode              $( (( REAL_AGENT )) && echo "real claude on the question leg" || echo "rehearsal — fakeagent everywhere" )
 claude            $CLAUDE_VERSION
 \$EDITOR           $(resolved_editor)
 vincent commit    $(git -C "$ROOT" rev-parse --short HEAD)

 seeded state      $GATE
 app repo          $(hostpath "$REPO_A")
 spare repo        $(hostpath "$REPO_B")
 bare remote       $(hostpath "$REMOTE")

 workflows         m3-loop        (global, real agent, ask→commit→gate→publish)
                   m3-parallel    (global AND shadowed in the app repo)
                   m3-broken      (global, invalid on purpose)

Start the TUI in the terminal you want to judge — not from Git Bash, which
gives a native console app a pipe rather than a console.

  bash / zsh:
    env VINCENT_CONFIG_DIR="$H_CONFIG" \\
        VINCENT_DATA_DIR="$H_DATA" \\
        FAKEAGENT_DELAY_MS=$DELAY_MS \\
        FAKEAGENT_EDIT_FILE=README.md \\
        FAKEAGENT_SCENARIO_CODEX=success \\
$( (( REAL_AGENT )) || echo "        FAKEAGENT_SCENARIO=ask-question \\
        FAKEAGENT_ASK_MULTI=1 \\" )
        "$H_VINCENT"

  PowerShell:
    \$env:VINCENT_CONFIG_DIR="$H_CONFIG"
    \$env:VINCENT_DATA_DIR="$H_DATA"
    \$env:FAKEAGENT_DELAY_MS="$DELAY_MS"
    \$env:FAKEAGENT_EDIT_FILE="README.md"
    \$env:FAKEAGENT_SCENARIO_CODEX="success"
$( (( REAL_AGENT )) || echo "    \$env:FAKEAGENT_SCENARIO=\"ask-question\"
    \$env:FAKEAGENT_ASK_MULTI=\"1\"" )
    & "$H_VINCENT"

No daemon is running and no project is registered — the TUI starting the
daemon is checklist item 1, registering the app repo is item 2.

When you are done:  scripts/m3-gate.sh clean

In PowerShell, also clear the session's variables — they outlive the gate and
the daemon a later run starts would inherit them:

  Remove-Item Env:VINCENT_CONFIG_DIR,Env:VINCENT_DATA_DIR,Env:FAKEAGENT_* -EA 0

EOF
