package workflow

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lezli01/vincent/skills"
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

// CreateWorkflowName is the built-in workflow that writes another workflow
// (task 024). Its deliverable is a registry file, not a change to the task's
// own worktree.
const CreateWorkflowName = "create-workflow"

// promptIndent is the column the `prompt: |` block scalar below sits at.
// Every line spliced into it carries this prefix.
const promptIndent = "      "

// createWorkflowHeader is everything before the embedded skill: the task
// framing, the destination the `global` field selects, and the standing
// corrections the skill cannot make for itself — it does not know which
// registry this run writes to, that its own references/ are absent, or what
// asking costs under a daemon (§7.4).
//
// The `global` field picks the destination registry (task 024 decision 1).
// False — its zero value, and what an omitted field renders as — resolves to
// `{{.Project.Path}}/.vincent/workflows`, which the template knows exactly
// because the destination is always the task's *own* project (decision 2).
// Only the global branch needs a lookup, and `vincent doctor` is the one
// command that prints the config dir without a running daemon.
//
// Both destinations are live registry directories rather than the task's
// worktree: the registry watches project repo roots, so a file written into a
// worktree would not be a workflow until the branch merged. max_retries is 0
// for the reason §8.4 gives for any step whose replay is not provably safe —
// a second attempt starts a fresh session that would find the first attempt's
// file already on disk.
const createWorkflowHeader = `# Built into vincent: authors a new workflow and installs it in the global or
# project registry (task 024). Shadowed by a global or project workflow named
# "create-workflow".
#
# The design rules below the "How to design it" heading are
# skills/vincent-workflows/SKILL.md, embedded at build time (decision 7) —
# edit the skill, not this file.
name: create-workflow
description: Author a new workflow and install it in the global or project registry
defaults:
  agent: claude
fields:
  - name: workflow_name
    label: Workflow name
    type: string
    required: true
    # The same vocabulary isSlug accepts for a field name. The schema's own
    # rule for a workflow name is looser — anything without whitespace or a
    # path separator — but this value is also a file name, so it is held to
    # the stricter of the two (decision 10).
    pattern: '^[a-z0-9][a-z0-9._-]*$'
    description: >-
      Name for the new workflow. It becomes both its name: field and its file
      name, so it is lowercase and has no spaces or path separators.
  - name: global
    label: Install globally
    type: boolean
    description: >-
      true installs the new workflow in {config_dir}/workflows, where every
      project can run it. false, or left unset, installs it in this project's
      .vincent/workflows, where the repository versions it.
steps:
  - id: author
    type: agent
    max_retries: 0
    # Explicit, though it is also the engine's fallback: this step is meant to
    # stop and ask, and the prompt says so. Leaving it unset would make that
    # agreement depend on a default nothing here states (decision 9).
    on_input: wait
    prompt: |
      You are running unattended in a dedicated git worktree created for this
      task; the current working directory is that worktree. Your deliverable is
      a single new vincent workflow YAML file, written to the destination named
      under "Destination" below. That destination is a live registry directory
      outside this worktree, so the daemon picks the file up as soon as you
      write it and it is not part of this task's diff. Leave the worktree
      itself unchanged.

      Task: {{.Task.Title}}

      {{.Task.Description}}

      ## Destination

      {{ if eq (index .Task.Fields "global") "true" -}}
      The global registry. Run 'vincent doctor --json' and read paths.config_dir
      from its output; the directory is that path plus /workflows. Ignore
      doctor's exit code — a non-zero exit reports unrelated findings and the
      path is printed either way. Create the directory if it does not exist. A
      global workflow is available to every project, and no repository versions
      it.
      {{- else -}}
      This project's registry: {{.Project.Path}}/.vincent/workflows. Create the
      directory if it does not exist. That path is the project's own checkout,
      not this worktree, so the file lands outside this task's branch and is
      never reviewed as part of its diff.
      {{- end }}

      The name is not yours to choose: use {{ index .Task.Fields "workflow_name" }}
      verbatim, both as the workflow's own name field and as the file name with
      a .yaml extension. If a file of that name is already in the destination,
      do not overwrite it — that is exactly the kind of question rule 1 below
      is for, so ask whether to amend the existing workflow or write a
      different one.

      ## How to design it

      What follows is the vincent-workflows skill, reproduced verbatim. Follow
      it, with three corrections it cannot make for itself — where they
      disagree with it, they win:

      1. You may stop and ask, but asking is expensive here and nobody may be
         watching. A question parks this task in awaiting_input, where it holds
         its concurrency slot with your process alive, and if it goes
         unanswered the step fails on the input timeout — there is no path
         where an unanswered question falls back to your own judgement. So ask
         only where the skill's questions are load-bearing: an answer that
         changes the YAML and that you cannot settle from the repository, its
         agent instructions, or the workflows already in both registries.
         Batch what you must ask into as few exchanges as you can, decide
         everything else yourself, and list in your final message both the
         questions you asked and the ones you answered on your own.
      2. The destination is the "Destination" section above, not the
         .vincent/workflows path the skill's authoring step names.
      3. The skill's own references/ files are not on disk here. Read
         docs/reference/workflow-schema.md from the repository if this is a
         vincent checkout, which the skill already prefers; otherwise work from
         what the skill states directly.

`

// createWorkflowFooter closes the prompt after the skill. The skill already
// requires the validator run, the session envelope and the design summary, so
// this only says where they go.
const createWorkflowFooter = `
      ## When you are done

      Deliver the skill's own design summary as your final message, with the
      path you wrote, the validator's verdict, and the questions and
      assumptions rule 1 above asks for.
`

// CreateWorkflowSource is the built-in workflow-authoring workflow: one agent
// step whose design rules are the `vincent-workflows` skill (task 023),
// embedded at build time so the published skill stays the single copy
// (decision 7).
//
// It is a var rather than a const because the skill is spliced in at init.
// Nothing may reassign it; the built-in scope parses it once.
var CreateWorkflowSource = createWorkflowHeader +
	indentBlock(EscapeTemplate(skillInstructions(skills.VincentWorkflows)), promptIndent) +
	createWorkflowFooter

// skillInstructions drops an Agent Skill's YAML front matter, keeping the
// Markdown a model is meant to act on. A file with no front matter is
// returned unchanged, so this cannot silently eat the first section.
func skillInstructions(src string) string {
	const fence = "---\n"
	if !strings.HasPrefix(src, fence) {
		return src
	}
	rest := src[len(fence):]
	end := strings.Index(rest, "\n"+fence)
	if end < 0 {
		return src
	}
	return strings.TrimLeft(rest[end+len("\n"+fence):], "\n")
}

// indentBlock prefixes every non-empty line so the text sits inside a YAML
// block scalar at that column. Empty lines stay empty: trailing whitespace is
// preserved by "|" and would show up in the rendered prompt.
func indentBlock(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}

var (
	builtinOnce    sync.Once
	builtinEntries map[string]Entry
)

// builtinSources is the built-in scope's contents, in no particular order —
// the registry sorts. Adding an entry here is all a new built-in needs.
var builtinSources = map[string]string{
	AdhocName:          AdhocSource,
	CreateWorkflowName: CreateWorkflowSource,
}

// builtins returns the built-in scope, parsed once. A parse failure here is
// a programming error in this package, so it panics rather than silently
// serving an empty scope.
func builtins() map[string]Entry {
	builtinOnce.Do(func() {
		builtinEntries = make(map[string]Entry, len(builtinSources))
		for name, src := range builtinSources {
			wf, _, err := Parse([]byte(src), Options{})
			if err != nil {
				panic(fmt.Sprintf("built-in workflow %q is invalid: %v", name, err))
			}
			builtinEntries[name] = Entry{
				Name:     wf.Name,
				Scope:    ScopeBuiltin,
				Source:   src,
				Workflow: wf,
			}
		}
	})
	return builtinEntries
}
