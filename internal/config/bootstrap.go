package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// defaultConfigYAML is written on first daemon start when no config.yaml
// exists (phase 1 decision). It must always parse and equal Default();
// config_test.go enforces that.
const defaultConfigYAML = `# vincent daemon configuration (spec §12.3).
# Generated with defaults on first start. Edit freely: the daemon watches this
# file and hot-reloads valid changes. Invalid edits are logged and rejected,
# and the last good configuration stays active. Changes to "listen" take
# effect on the next daemon restart.

# Address the HTTP API binds to. Loopback only. Port 0 picks an ephemeral
# port, published for clients in {data_dir}/daemon.json.
listen: 127.0.0.1:0

# Global cap on concurrently running tasks. Per-project caps are configured
# on each project.
max_parallel_tasks: 3

# Fallback step timeouts, used when a workflow step declares none.
# input_timeout bounds each wait for an answer to an agent's input request
# (awaiting_input, §7.4); on expiry the attempt fails under the retry policy.
defaults:
  agent_timeout: 60m
  command_timeout: 15m
  input_timeout: 24h

# Delete a task's branch when it is archived and carries no commits past the
# base it was cut from — the branch a workflow that never writes to the
# repository leaves behind. A branch holding any commit is always kept, and a
# check git cannot answer keeps the branch too.
delete_empty_branch_on_archive: true

# Also delete that branch's upstream counterpart, when it has one. Off by
# default and honoured only when a human archives the task: a forge is shared
# with other people and the deletion cannot be undone. Nothing happens here
# unless delete_empty_branch_on_archive is also on.
delete_remote_branch_on_archive: false

# Transcripts of archived tasks older than this many days are pruned.
transcript_retention_days: 90

# Per-attempt transcript cap. A step whose transcript passes this fails with
# transcript_limit rather than filling the disk.
transcript_max_bytes: 512MB

# Ceiling on what one task may spend, in US dollars, summed over every attempt
# of every step it runs. Past it the task blocks with cost_limit at the next
# attempt boundary, so expect to overshoot by at most one attempt; raise this
# and retry to carry on. 0 disables it, which is the default.
#
# It counts one task. Each fan_out lane is its own task and gets its own
# budget, so a tree of twenty lanes may spend twenty times this. Only agents
# that report cost are counted — codex and cursor report none, so the cap is
# inert on them.
max_task_cost_usd: 0

# How long a task waits before trying again after its agent reported that the
# usage quota for the window is spent, when the CLI named no reset time. When
# it does name one, that wins and this is unused. The task keeps its place in
# the queue and holds no slot while it waits.
usage_limit_recheck_interval: 15m

# Daemon log verbosity: debug | info | warn | error.
log_level: info

# Record how every step was actually invoked in its transcript: resolved
# agent/model/effort, permission mode, working directory, and the full argv.
# Turn this on when a run does something you cannot explain, then paste the
# transcript. Off by default because argv includes the rendered prompt.
debug: false

# What child processes — agent steps, command steps and their checks —
# inherit from the daemon (T4.23). Resolved in one order: inherit, then
# unset, then set. Command steps layer the VINCENT_* variables and their own
# "env:" on top, so those are never affected.
#
# The default inherits everything, which is what the daemon always did
# implicitly. Pin it when a run has to be reproducible, or drop a single
# variable that breaks a CLI — on Windows, a daemon started from Git Bash
# carries MSYSTEM, which blocks every cursor tool call.
#
# Values under "set" are literal: "$" is not special and nothing is expanded.
environment:
  inherit: all          # all | none | a list of names, e.g. [PATH, HOME]
  # unset:
  #   - MSYSTEM
  # set:
  #   LANG: C.UTF-8

# Agent CLI locations. An empty path resolves the binary from PATH.
agents:
  claude:
    path: ""
  codex:
    path: ""
  # cursor resolves the "cursor-agent" binary — not "cursor", which is the
  # editor launcher (§9.7).
  cursor:
    path: ""

# Reading GitHub issues, so a task can be created from one (task 035). It
# applies only to a project whose "origin" remote is a github.com repository,
# and the daemon makes no call at all until you open the issue picker or name
# an issue on the command line.
#
# vincent stores no credential: it drives the "gh" CLI when it is installed
# and authenticated, and otherwise reads GITHUB_TOKEN or GH_TOKEN out of the
# environment the daemon inherited. Set enabled to false to stop the daemon
# reading GitHub at all.
#
# poll_interval is how often the daemon reconciles the link between a task and
# its pull request, by matching an open pull request's head branch against the
# task's branch (task 052). It is one of vincent's two standing background
# calls — the other is the release check below — and it fires only for
# projects hosted on github.com: set it to 0 to switch the reconciler off and
# keep the rest of the integration, which then calls GitHub only when you ask
# it to.
github:
  enabled: true
  poll_interval: 5m

# Check whether a newer vincent has been released (task 055). The daemon asks
# GitHub for the project's latest stable release once a day, caches the answer
# in memory and serves it on GET /v1/update; "vincent doctor" and "vincent
# daemon status" show it. Prereleases are never reported.
#
# It is one unauthenticated GET. Nothing identifying is sent: no token, no
# telemetry, no machine or install identifier — vincent asks GitHub a public
# question and reads the answer.
#
# Unlike the GitHub reconciler above, this one fires for every install, which
# is why it has its own switch. With check: false the daemon makes no request
# for it at all; only running "vincent update" does, and that command makes
# its own call rather than going through the daemon. poll_interval: 0 has the
# same effect and keeps the key visible.
#
# Nothing is ever downloaded or installed by the check. "vincent update"
# applies one, and only when you run it.
update:
  check: true
  poll_interval: 24h

# Tell someone when a task needs them, without a client attached (task 046).
# The daemon runs "command" whenever a task enters one of the states in "on",
# and writes a JSON envelope describing the transition to the command's stdin
# — task id and title, from/to, block_reason, project, workflow, step cursor,
# branch and worktree path — so a one-line script can write a message without
# calling back into the API.
#
# "command" is argv, not a shell line: nothing is expanded, quoted or split,
# and there is no shell on any platform. Both keys are needed; either alone
# loads and warns. Off by default.
#
# The states are the ones from §6, most usefully: blocked, awaiting_gate,
# awaiting_input, done. Only root tasks notify — a fan_out lane is a task row,
# and a twenty-lane tree would otherwise send twenty-one messages.
#
# It is fire-and-forget: at most 4 notifiers run at once, a child is killed
# after 10 seconds, failures are logged and never retried, and nothing is
# replayed for events that happened while the daemon was down.
#
# WARNING: this is arbitrary code the daemon runs as you, and its argv can
# carry a secret (a webhook URL), which is part of why this file is
# owner-only.
#
# notify:
#   on: [blocked, awaiting_gate, awaiting_input]
#   command: ["/usr/local/bin/notify-me"]

# What clients render, not what the daemon does. The daemon validates these,
# hot-reloads them and serves them on GET /v1/config; the TUI reads them from
# there.
#
# group_by nests the task table under headers, outermost level first. Accepted
# levels: project, workflow. Use [] for one flat list of tasks. A grouped
# level drops its own column — the header already names it — and "g" cycles
# the grouping for the session without touching this file.
tui:
  board:
    group_by: [project, workflow]
`

// EnsureDefaultFile writes the commented default config.yaml into dir when
// none exists, creating dir if needed, both owner-only (§12.2). It reports
// whether the file was created. An existing file's *contents* are never
// touched, but its mode is re-tightened on every call the way
// daemon.EnsureToken re-tightens {data_dir}/token: an installation created
// before the modes were tightened would otherwise keep a group- and
// world-readable config.yaml forever, and that file can hold literal
// environment.set values (§12.3). The daemon logs what it changed — see
// CheckPermissions — rather than reshaping a user's file in silence.
func EnsureDefaultFile(dir string) (created bool, err error) {
	path := filepath.Join(dir, FileName)
	if _, err := os.Stat(path); err == nil {
		return false, tightenPermissions(dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	// MkdirAll leaves an existing directory's mode alone, so a config dir that
	// predates this and has lost only its config.yaml is tightened below.
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}
	//nolint:gosec // G304: path is ConfigPath() under the dir resolved above, never caller input
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, FilePerm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil // lost a create race; the existing file wins
		}
		return false, fmt.Errorf("write default config: %w", err)
	}
	if _, err := f.WriteString(defaultConfigYAML); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("write default config: %w", err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("write default config: %w", err)
	}
	return true, tightenPermissions(dir)
}
