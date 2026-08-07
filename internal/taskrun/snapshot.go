package taskrun

import "github.com/lezli01/vincent/internal/workflow"

// AdhocSnapshot is the workflow stored as workflow_snapshot for tasks
// created without naming one. It is now the built-in registry entry (phase 2
// decision); T2.3 replaces this indirection with a registry lookup that also
// serves global and project workflows.
const AdhocSnapshot = workflow.AdhocSource
