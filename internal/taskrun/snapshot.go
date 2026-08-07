package taskrun

import "github.com/lezli01/vincent/internal/workflow"

// adhocIntro opens the adhoc prompt. It is the same text the built-in
// workflow embeds, so the stored snapshot and the M1 runner's hardcoded
// prompt cannot drift.
const adhocIntro = workflow.AdhocIntro

// AdhocSnapshot is the workflow stored as workflow_snapshot for tasks
// created without naming one. It is now the built-in registry entry (phase 2
// decision); T2.3 replaces this indirection with a registry lookup that also
// serves global and project workflows.
const AdhocSnapshot = workflow.AdhocSource
