package workflow

import (
	"fmt"
	"regexp"
	"strings"
)

// SkeletonSource is the file `vincent workflow init` writes when no example
// is named (task 034): a single agent step, heavily commented. It is a
// sibling of AdhocSource and CreateWorkflowSource and lives here for the same
// reason they do — the package that owns Parse is the one that can hold a
// shipped starting point to it, which TestSkeletonValidates does.
//
// It is *not* an example: it is not a `--from` value and it does not ship in
// the release archive. An example teaches one shape of real work; this
// teaches the schema, so its comments name the parts a first author otherwise
// misses — the other two runnable step types, check as a field rather than a
// fourth type, max_retries and timeout — and point at the reference page for
// the rest.
//
// The name below is a placeholder: init rewrites it with SetName, so the
// written file is addressable under the name the user asked for (the registry
// keys on the name field, not the file name — §5.2).
const SkeletonSource = `# A starting point, not a finished workflow: read it before you run it.
#
# A workflow is an ordered list of steps. Every task runs exactly one, in its
# own git worktree on its own branch, and the daemon is what runs it.
#
#   Every field and default   docs/reference/workflow-schema.md
#   Worked examples           vincent workflow init NAME --from feature-pr
#   Check this file, no daemon needed:
#       vincent workflow validate PATH
name: my-workflow
description: Say in one line what a run of this workflow produces.

# What every step inherits, and what any step may override.
defaults:
  agent: claude
  # How many times a failed step is retried before the task blocks for a
  # human. 0 means fail fast.
  max_retries: 1
  # Wall-clock budget for one attempt. A step that runs over is failed as a
  # timeout rather than left running.
  timeout: 30m

steps:
  - id: implement
    type: agent
    prompt: |
      Implement the following task in this repository.

      Title: {{.Task.Title}}
      {{with .Task.Description}}
      Details:
      {{.}}
      {{end}}
      Work on the current branch ({{.Task.BranchName}}). Make the smallest
      change that does the job, follow the conventions already in the code,
      and add or update tests alongside it.
    # check is a field on agent and command steps, not a step type of its own:
    # a shell command run after the step, whose exit code decides whether the
    # attempt actually succeeded. An agent reporting success is a claim; a
    # build is a fact. A failed check retries the step with the failure
    # appended to the prompt. Uncomment it and point it at whatever proves
    # this repository still works.
    # check: go build ./... && go test ./...

  # Two more step types run something, and both of these may carry check too:
  #
  #   - id: commit
  #     type: command                # a shell command; no agent, no tokens
  #     run: 'git add -A && git commit -m "{{.Task.Title}}"'
  #
  #   - id: review
  #     type: manual                 # stops and waits for a person to decide
  #     instructions: |
  #       Review the diff for task #{{.Task.ID}} before anything is pushed.
  #
  # The remaining types are structure rather than work — parallel, fan_out,
  # condition, loop, break and include. docs/guides/workflows.md covers them.
`

// topLevelName matches a workflow's own name key and the rest of its line.
//
// Column zero is the whole rule, and it is exact rather than approximate: in
// block context every nested construct is indented past the key that opens
// it, so a `- name:` under fields:, a name: inside a step, and a name: inside
// a prompt: block scalar are all out of reach by construction. `[^\r\n]*`
// stops before the line ending, so a CRLF file keeps its CRLF.
var topLevelName = regexp.MustCompile(`(?m)^name:[^\r\n]*`)

// SetName rewrites a workflow source's top-level name: to name and returns
// the result, leaving every other byte exactly as it was.
//
// It is a text edit, deliberately, rather than a parse and re-marshal: the
// comments in a shipped example are most of what makes it worth handing to a
// reader, and a round trip through the YAML marshaller drops all of them. The
// one line this touches is the one the registry keys on (§5.2), which is what
// makes the written file addressable under the name that was asked for.
//
// Only the top-level key is rewritten; any header comment above it is left
// alone, including one that repeats the old name. Editing prose this function
// does not own would break the moment a file's header changed shape.
func SetName(src []byte, name string) ([]byte, error) {
	if strings.ContainsAny(name, " \t/\\") {
		return nil, fmt.Errorf("name %q must not contain whitespace or path separators", name)
	}
	loc := topLevelName.FindIndex(src)
	if loc == nil {
		return nil, fmt.Errorf("source declares no top-level name: key")
	}
	out := make([]byte, 0, len(src)+len(name))
	out = append(out, src[:loc[0]]...)
	out = append(out, "name: "+name...)
	out = append(out, src[loc[1]:]...)
	return out, nil
}

// DeclaredName reports the name: a source declares, without validating
// anything else about it. It answers "" for a source that does not parse as
// YAML at all, or that declares no name — such a file has no knowable name,
// which is a different answer from a file that names something.
//
// It exists because the name is what a scope collides on: two files in one
// directory declaring the same name: is a §5.2 duplicate whatever they are
// called on disk, and both the registry and `workflow init` need to see that
// before Parse would have anything to say.
func DeclaredName(src []byte) string {
	var probe struct {
		Name string `yaml:"name"`
	}
	if err := yamlUnmarshalLenient(src, &probe); err != nil {
		return ""
	}
	return probe.Name
}
