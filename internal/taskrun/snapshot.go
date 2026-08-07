package taskrun

// adhocIntro opens the adhoc prompt; the snapshot below embeds it so the
// stored workflow definition and the hardcoded M1 prompt cannot drift.
const adhocIntro = `You are running unattended in a dedicated git worktree created for this task; the current working directory is that worktree. Complete the task described below. Everything you change in the working tree (committed or not) will be reviewed by an engineer as a diff afterwards.`

// AdhocSnapshot is the synthesized one-step workflow stored as
// workflow_snapshot for M1 tasks (phase 1 decision): DB rows are
// shape-final, and T2.3 swaps synthesis for registry lookup. max_retries is
// pinned 0 so the snapshot stays truthful — the M1 runner does not retry
// (T1.7–T1.9 decision).
const AdhocSnapshot = `# Synthesized by vincent for an ad-hoc task (spec §5.3; the workflow
# registry replaces this synthesis in T2.3).
name: adhoc
description: Ad-hoc single-step agent task
defaults:
  agent: claude
steps:
  - id: run
    type: agent
    max_retries: 0
    prompt: |
      ` + adhocIntro + `

      Task: {{.Task.Title}}

      {{.Task.Description}}
`
