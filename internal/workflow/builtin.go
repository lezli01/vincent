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

// StatusInstruction is the standing request that an agent step narrate itself
// through `vincent status` (§5.6, task 036). The daemon deliberately appends
// no such instruction to a prompt, so an agent reports only because the
// workflow asked — which means every built-in that runs long enough for
// someone to wonder has to ask, and asks in one wording rather than three.
//
// It is spliced into a YAML block scalar, so it is indented by the consumer
// rather than carrying its own column.
const StatusInstruction = `Say what you are doing as you go. Before each significant phase of the work, run:

  vincent status "<one short line about what you are doing now>"

Keep it under ten words. If something fails or you get stuck, set it to what is
actually wrong — "3 tests red in internal/store", not "working on it". It is
free, it is what someone watching the board sees, and the last line you set
stays on the finished attempt.`

// AdhocSource is the built-in ad-hoc workflow: one agent step whose prompt
// embeds the task title and description. It is a real registry entry at the
// lowest precedence (phase 2 decision), so a file named `adhoc.yaml` in the
// global or project scope shadows it. max_retries is pinned to 0 — an
// ad-hoc run fails fast rather than paying for a second agent attempt.
//
// It is a var rather than a const because StatusInstruction is re-indented
// into it at init. Nothing may reassign it.
var AdhocSource = `# Built into vincent: the workflow used for tasks created without one
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

` + indentBlock(StatusInstruction) + `
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
//
// It is a var rather than a const for the same reason AdhocSource is:
// StatusInstruction is re-indented into it at init.
var createWorkflowHeader = `# Built into vincent: authors a new workflow and installs it in the global or
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

      ## While you work

` + indentBlock(StatusInstruction) + `

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
      path you wrote, the validator's and the renderer's verdicts, and the
      questions and assumptions rule 1 above asks for.
`

// CreateWorkflowSource is the built-in workflow-authoring workflow: one agent
// step whose design rules are the `vincent-workflows` skill (task 023),
// embedded at build time so the published skill stays the single copy
// (decision 7).
//
// It is a var rather than a const because the skill is spliced in at init.
// Nothing may reassign it; the built-in scope parses it once.
var CreateWorkflowSource = createWorkflowHeader +
	indentBlock(EscapeTemplate(skillInstructions(skills.VincentWorkflows))) +
	createWorkflowFooter

// UpdateWorkflowsName is the built-in that brings the workflows a project
// already has up to the current feature set and the authoring skill's
// practices (task 037). Its deliverable is a diff on the task's own branch,
// which is the one thing that distinguishes it from create-workflow: those
// files are versioned by the repository, so the reviewed diff — not a write
// into a live registry — is how a change to them lands (decision 1).
const UpdateWorkflowsName = "update-workflows"

// updateWorkflowsHeader is everything before the embedded skill: the framing,
// the file list, the compatibility probe, this file's own review checklist,
// and the corrections the skill cannot make for itself.
//
// The checklist under "The bar" is deliberately version-coupled: it names the
// features a workflow can be behind on, so a workflow feature that lands
// without a line here is a feature this built-in will never propagate. Adding
// that line is part of shipping the feature (decision 5).
//
// `on_input: deny` rather than create-workflow's `wait` (decision 4). The two
// built-ins face opposite ways: create-workflow is designing something that
// does not exist yet and a question may be the only way to get it right,
// while this one has the answers in front of it — the file, its comments, its
// history — and its output is reviewed before it can affect anything. Parking
// a maintenance pass in `awaiting_input` costs a held slot for a question the
// repository already answers.
var updateWorkflowsHeader = `# Built into vincent: updates the workflows a project already versions so they
# use the current schema and follow the authoring skill (task 037). Shadowed by
# a global or project workflow named "update-workflows".
#
# The design rules below the "How to design them" heading are
# skills/vincent-workflows/SKILL.md, embedded at build time — edit the skill,
# not this file. The checklist above them is this file's own, and is coupled to
# the feature set on purpose: a workflow feature that ships without a line there
# is one this workflow will never propagate.
name: update-workflows
description: Update this project's workflows to the current feature set and authoring practices
defaults:
  agent: claude
steps:
  # The probe and the file list in one command: --error-unmatch turns "this
  # project versions no workflows" into a nonzero exit the next step reads,
  # and the stdout it prints on success is the list the agent works from and
  # the loop below iterates. git is one of the few executables spelled the
  # same under /bin/sh and pwsh (§8.3), which is what every run: body here is
  # held to.
  - id: inventory
    name: List the workflows this project versions
    type: command
    max_retries: 0
    allow_failure: true
    run: 'git ls-files --error-unmatch -- ".vincent/workflows/*.y*ml"'
  # A project with no workflows of its own is not a failure — there is simply
  # nothing to update, and the task is done.
  - id: has-workflows
    name: Stop if there are none
    type: condition
    if: '{{ eq (index .Steps "inventory").Status "succeeded" }}'
  - id: modernize
    name: Bring them up to the current feature set
    type: agent
    # One retry, unlike the other two built-ins: the deliverable is edits in a
    # private worktree with no external effect, a second session sees the
    # first one's partial work as ordinary uncommitted changes, and the pass
    # is convergent — every item below asks the file to conform, not to change
    # again.
    max_retries: 1
    on_input: deny
    prompt: |
      You are running unattended in a dedicated git worktree created for this
      task; the current working directory is that worktree. Your deliverable is
      an edit to the workflow files this repository already versions under
      .vincent/workflows, made here, on this task's branch. An engineer reads
      the result as a diff and merges it, and merging is what makes the
      rewritten workflows live — nothing you do here changes what the daemon is
      running right now.

      Task: {{.Task.Title}}

      {{.Task.Description}}

      ## The files

      A previous step listed every workflow file this repository has committed:

      {{ (index .Steps "inventory").Result }}

      Those are the whole job. A workflow the project keeps but has never
      committed is not in this worktree and is out of scope, and so is the
      global registry, which no repository versions. Change nothing outside
      .vincent/workflows: if a document elsewhere in the repository describes a
      workflow you changed, say so in your final message instead of editing it.

      ## Nobody is watching

      This step runs under on_input: deny. A question you ask is answered "no
      user is available" and you carry on alone, so asking buys you nothing.
      Where you are unsure, make the conservative change or make none, and say
      which in your final message. The diff is the conversation.

      ## While you work

` + indentBlock(StatusInstruction) + `

      ## Find out what this vincent can actually do

      The feature set that matters is the installed binary's, not the one you
      remember:

      - Run "vincent version" first and keep its exact output for your final
        message.
      - "vincent workflow validate <file>" is the verdict on every file you
        touch. It needs no daemon and no network, and unknown keys are errors
        rather than ignored settings — which makes it the way to ask whether
        this version has a feature at all: write a candidate to a temporary
        path outside this worktree, validate it there, and never leave a probe
        file in the repository.
      - "vincent workflow render <file>" is the other half of that verdict, and
        also needs no daemon. Validation only checks that a template parses;
        render executes every one of them, which is the only way a typo'd
        "{{"{{"}}.Task.Titel}}" or a ".Task.Fields" key nothing supplies is
        caught before someone creates a task. Run it on every file you write,
        and read the rendered prompts — they are what an agent will actually be
        sent, with placeholders such as <worktree> standing in for what a run
        discovers.
      - If this repository is a vincent checkout, its own
        docs/reference/workflow-schema.md is the exact reference for the
        version it builds. Read it before you rely on anything below.
      - Do not run "vincent workflow init". It writes into a live registry.

      ## The bar

      A file you leave behind must be one you would defend under the authoring
      skill reproduced below. Work through this list for every workflow, and
      report per workflow which items applied:

      1. Cheapest correct primitive. An agent step whose work is a known
         command — a build, a test suite, a formatter, a git operation, a
         structured query, an API call — becomes a command step. Every agent
         step that survives passes the skill's own test: it needs an agent
         because something in it cannot be decided reliably without one.
      2. Native control flow, never an agent asked to simulate it. A prompt
         that says "if X, do Y" is an if: guard or a condition step. "Keep
         going until it passes" is a loop with a break, not a retry budget used
         as iteration. "Do these independently" is parallel, or fan_out when
         they need separate branches. A step sequence duplicated across two of
         this project's workflows is an include. Fan-out lanes that must land in
         a fixed order carry needs:, naming the lanes they depend on — the
         engine derives the waves, so a fan-out split into "phase 1" and
         "phase 2" steps, or a lane whose prompt says "wait for the API lane",
         collapses into one step with edges. Such a step gets schedule: eager
         only where its lanes touch disjoint files and the round barrier is
         costing real wall-clock time: eager makes a lane's starting tree
         timing-dependent, so barrier stays the default and a flat lane list
         is a barrier whatever the field says. A fan-out whose lane count is
         genuinely decided by a run — a planning step emitting one JSON object
         per work unit — becomes for_each: plus a single lane: template with a
         max_lanes: ceiling, in place of a hand-guessed list of guarded lanes.
      3. Verification. A step that changes state a command can check carries a
         check:. An agent's claim that it worked is not verification.
      4. Typed inputs. A value the prompt tells a human to bury in the task
         description becomes a declared field, with a type, and a pattern where
         one exists. A workflow that digs an issue number out of the task title
         reads .Issue instead.
      5. Closed sets and defaults. A field whose legal values are a fixed list
         is type: enum with values:, not a string with a pattern spelling the
         same alternation and not a set restated in prose — only a list can be
         published, and only a published list becomes a picker. multiple: true
         where more than one may be chosen. A field with an obvious value
         carries default:, which is what stops a scripted caller from having to
         restate it.
      6. Failure policy, per step. max_retries: 0 on a probe and on anything
         whose replay is not provably safe; allow_failure: true where a red
         result is data a later guard reads; retry_backoff where retrying
         immediately cannot help.
      7. Human mechanism, deliberately chosen. A manual gate sits immediately
         before the effect it authorizes and names what to inspect.
         on_input: require only where the conversation genuinely is part of the
         run, and never on a step that resolves to an adapter with no control
         channel. on_input: deny on a step meant to run untended.
      8. Visibility. A step that runs for minutes, or that someone waits on,
         reports through vincent status — in the script for a command step, in
         the prompt for an agent step, in the wording used above.
      9. Portability. A run: body is handed to /bin/sh on POSIX and to pwsh on
         Windows, and vincent translates nothing. A body outside the
         intersection of the two is either rewritten into it, or the workflow
         declares platforms:, or the step carries a .Host.OS guard. A workflow
         that already pins defaults.container.image inverts the first half: its
         run: bodies execute under the image's /bin/sh whatever the host is, a
         step pinning shell: pwsh or shell: cmd is refused at load, and
         platforms: still gates on the daemon's host rather than on the image.
         Do not add a container: block to a workflow that has none — which
         image a project runs in is a deployment decision, not one this pass
         gets to make.
      10. Defensive templates. Rendering uses missingkey=error, so an optional
          field is read as {{"{{"}}with index .Task.Fields "x"}}…{{"{{"}}end}}
          and never bare. A required field may be read directly.
      11. Secrets. Nothing in a prompt, a field, an instruction or a run: body
          that you would not want sitting in a transcript.

      ## What you may not change

      Each of these workflows encodes a decision somebody made. You are
      modernizing how it is expressed, not redesigning what it does:

      - A workflow keeps its name, its file keeps its path, and no file is
        deleted.
      - A declared field keeps its name and its meaning. Adding one is fine;
        renaming or repurposing one breaks every task that names it.
      - A manual gate stays, and stays in front of the effect it guards. Never
        turn a gated external effect into an unattended one, never widen a
        permission_mode, and never raise max_retries on a step that publishes,
        deploys, pushes, deletes or spends money.
      - A workflow that is already right is left byte for byte alone. An empty
        diff for it is a correct outcome, and you say so.
      - A comment that is still true is kept; a comment your edit made false is
        corrected. The next person reads the comments.
      - A workflow that is wrong in a way you cannot fix conservatively is left
        alone and reported. A run that changed three workflows and explained
        why it did not touch the fourth is a good run.

      ## How to design them

      What follows is the vincent-workflows skill, reproduced verbatim. It is
      written for designing a new workflow from an outcome; here the outcome is
      already encoded in the file in front of you. Follow it, with three
      corrections it cannot make for itself — where they disagree with it, they
      win:

      1. The deliverable is an edit to the existing files listed above, in this
         worktree — not a new file written into a live registry directory. The
         path its authoring step names is right; the copy you edit is this
         worktree's.
      2. You cannot ask, per "Nobody is watching" above. Every question its
         "Gather only decisions that matter" section raises is one you answer
         from the repository, its agent instructions, the workflow's own
         comments and its git history — or one you leave the current behavior
         alone over.
      3. The skill's own references/ files are not on disk here. Read
         docs/reference/workflow-schema.md from the repository if this is a
         vincent checkout, which the skill already prefers; otherwise work from
         what the skill states directly and from the validator.

`

// updateWorkflowsFooter closes the prompt and carries the steps that verify
// the pass. The validation loop is deliberately not a `check:` on the agent
// step: `vincent workflow validate` takes exactly one file, so the per-file
// iteration a whole registry needs is `for_each` over a step's output — the
// construct §7.8 exists for. It relists
// the files rather than reusing the first inventory so a file the pass added
// (an `include` extracted out of two workflows) is validated too, and it
// counts untracked files because a new file is not committed yet.
//
// A file that fails there blocks the task with that step's own reason, which
// is the right outcome: an invalid workflow must not reach a reviewer as a
// merge candidate.
const updateWorkflowsFooter = `
      ## When you are done

      Your final message is the report an engineer reads before the diff:

      - the exact "vincent version" output;
      - one entry per workflow file: changed or untouched, which numbered items
        from "The bar" applied, and what you deliberately left alone;
      - for every workflow you changed, the maximum number of automatic agent
        sessions it can spend, before and after, computed the way the skill's
        cost rules say;
      - anything that needs a human decision, and anything you would have asked
        if asking were possible;
      - the validator's verdict for every file you touched.

      Every workflow file must pass "vincent workflow validate" and
      "vincent workflow render" before you finish. A later step runs both over
      all of them again, and one that fails there blocks the task. Render is
      the one that catches a template that parses and then does not execute,
      which is a class of bug validation cannot see.
  - id: relist
    name: Relist the workflow files
    type: command
    max_retries: 0
    run: 'git ls-files --cached --others --exclude-standard -- ".vincent/workflows/*.y*ml"'
  - id: validate
    name: Validate every workflow file
    type: loop
    for_each: '{{ (index .Steps "relist").Result }}'
    # Generous, because an iteration here is one local validator run and
    # nothing else. A registry larger than this blocks before the first
    # iteration, naming the count, which is a legible way to be told.
    max_iterations: 50
    steps:
      - id: file
        type: command
        max_retries: 0
        run: 'vincent workflow validate "{{ .Loop.Item }}"'
      - id: rendered
        type: command
        max_retries: 0
        # Validation parses templates; this executes them (§8.4). A file whose
        # prompt reads a field nothing supplies passes the step above and
        # fails on a reviewer's first task, which is exactly what this pass is
        # for. Daemon-free like the validator, so it runs in the same loop.
        run: 'vincent workflow render "{{ .Loop.Item }}"'
  - id: changes
    name: Record what changed
    type: command
    max_retries: 0
    run: |
      vincent status "summarizing the workflow diff"
      git --no-pager diff --stat {{.Task.BaseBranch}}
`

// UpdateWorkflowsSource is the built-in maintenance pass over a project's own
// workflows: a probe, an agent rewrite carrying the same `vincent-workflows`
// skill create-workflow carries, and a per-file validation loop.
//
// It is a var for the same reason CreateWorkflowSource is: the skill is
// spliced in at init. Nothing may reassign it.
var UpdateWorkflowsSource = updateWorkflowsHeader +
	indentBlock(EscapeTemplate(skillInstructions(skills.VincentWorkflows))) +
	updateWorkflowsFooter

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

// indentBlock prefixes every non-empty line with promptIndent, so the text
// sits inside one of the `prompt: |` block scalars above. Empty lines stay
// empty: trailing whitespace is preserved by "|" and would show up in the
// rendered prompt.
//
// The column is not a parameter because every built-in's prompt sits at the
// same one — a second column would mean a built-in whose YAML shape differs
// from the others for no reason.
func indentBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = promptIndent + line
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
	AdhocName:           AdhocSource,
	CreateWorkflowName:  CreateWorkflowSource,
	UpdateWorkflowsName: UpdateWorkflowsSource,
}

// BuiltinSource returns the compiled-in source of a built-in workflow. It is
// how a fork of one reaches the disk (task 065): a built-in has no file, so
// copying its bytes into a writable scope is the only way to change one, and
// §5.2's shadowing then puts the copy in front of the original.
func BuiltinSource(name string) ([]byte, bool) {
	src, ok := builtinSources[name]
	if !ok {
		return nil, false
	}
	return []byte(src), true
}

// IsBuiltin reports whether name belongs to the built-in scope. It is
// derived from builtinSources rather than compared against the two name
// constants, so a third built-in is covered the day it is added — which
// matters to `workflow init`, whose shadow warning is the only place outside
// this package that has to ask.
func IsBuiltin(name string) bool {
	_, ok := builtinSources[name]
	return ok
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
