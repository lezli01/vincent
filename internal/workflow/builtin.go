package workflow

import (
	"fmt"
	"sync"
)

// AdhocName is the workflow used when a task is created without naming one
// (spec §13.2: the `workflow` parameter is optional).
const AdhocName = "adhoc"

// AdhocIntro opens the ad-hoc prompt. It lives here so the built-in
// definition below and the M1 runner's prompt cannot drift apart.
const AdhocIntro = `You are running unattended in a dedicated git worktree created for this task; the current working directory is that worktree. Complete the task described below. Everything you change in the working tree (committed or not) will be reviewed by an engineer as a diff afterwards.`

// AdhocSource is the built-in ad-hoc workflow: one agent step whose prompt
// embeds the task title and description. It is a real registry entry at the
// lowest precedence (phase 2 decision), so a file named `adhoc.yaml` in the
// global or project scope shadows it. max_retries is pinned to 0 — an
// ad-hoc run fails fast rather than paying for a second agent attempt.
const AdhocSource = `# Built into vincent: the workflow used for tasks created without one
# (spec §5.3). Shadowed by a global or project workflow named "adhoc".
name: adhoc
description: Ad-hoc single-step agent task
defaults:
  agent: claude
steps:
  - id: run
    type: agent
    max_retries: 0
    prompt: |
      ` + AdhocIntro + `

      Task: {{.Task.Title}}

      {{.Task.Description}}
`

var (
	builtinOnce    sync.Once
	builtinEntries map[string]Entry
)

// builtins returns the built-in scope, parsed once. A parse failure here is
// a programming error in this package, so it panics rather than silently
// serving an empty scope.
func builtins() map[string]Entry {
	builtinOnce.Do(func() {
		wf, err := Parse([]byte(AdhocSource), Options{})
		if err != nil {
			panic(fmt.Sprintf("built-in workflow %q is invalid: %v", AdhocName, err))
		}
		builtinEntries = map[string]Entry{
			AdhocName: {
				Name:     wf.Name,
				Scope:    ScopeBuiltin,
				Source:   AdhocSource,
				Workflow: wf,
			},
		}
	})
	return builtinEntries
}
