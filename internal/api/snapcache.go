package api

import (
	"sync"

	"github.com/lezli01/vincent/internal/workflow"
)

// snapshotSummary is everything the API needs out of a task's workflow
// snapshot: how many steps there are, what each is called, and the text a
// human edits before retrying one (§6 edit+retry).
type snapshotSummary struct {
	stepTotal int
	stepNames []string
	steps     []stepDefinition
}

// stepDefinition is one step of the snapshot as the detail view sees it.
type stepDefinition struct {
	index        int
	id           string
	stepType     string
	prompt       string
	run          string
	instructions string
}

// stepName returns the display name of step index i, or "" when the index is
// out of range — which happens legitimately: a finished task's cursor sits
// one past the last step.
func (s snapshotSummary) stepName(i int) string {
	if i < 0 || i >= len(s.stepNames) {
		return ""
	}
	return s.stepNames[i]
}

// snapshotCache memoizes parsed workflow snapshots by task id.
//
// Editing the workflow *file* mid-task cannot invalidate an entry (§18 —
// execution uses the snapshot), so the cache has exactly one writer to fear:
// edit+retry rewrites the task's own snapshot (§6), and the retry handler
// forgets the entry for it. Without the cache, listing 50 tasks would
// re-parse 50 YAML documents on every refresh, and the board refreshes on
// every task event.
type snapshotCache struct {
	mu sync.Mutex
	m  map[int64]snapshotSummary
}

func newSnapshotCache() *snapshotCache {
	return &snapshotCache{m: make(map[int64]snapshotSummary)}
}

// get returns the summary for taskID, parsing snapshot on first use. A
// snapshot that fails to parse caches as an empty summary: it was valid when
// the task was created, so a parse failure here is a bug, and re-parsing it
// on every request would turn that bug into a hot loop.
func (c *snapshotCache) get(taskID int64, snapshot string) snapshotSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.m[taskID]; ok {
		return s
	}
	s := parseSnapshot(snapshot)
	c.m[taskID] = s
	return s
}

// forget drops one task's entry. Task rows are deleted only by a project
// cascade, which is rare; this keeps the map from pinning them forever.
func (c *snapshotCache) forget(taskID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, taskID)
}

func parseSnapshot(snapshot string) snapshotSummary {
	if snapshot == "" {
		return snapshotSummary{}
	}
	wf, _, err := workflow.Parse([]byte(snapshot), workflow.Options{})
	if err != nil || wf == nil {
		return snapshotSummary{}
	}
	s := snapshotSummary{
		stepTotal: len(wf.Steps),
		stepNames: make([]string, 0, len(wf.Steps)),
		steps:     make([]stepDefinition, 0, len(wf.Steps)),
	}
	for i := range wf.Steps {
		// `name` is optional in the schema; `id` is not, so it is the
		// fallback a board can always render.
		name := wf.Steps[i].Name
		if name == "" {
			name = wf.Steps[i].ID
		}
		s.stepNames = append(s.stepNames, name)
		s.steps = append(s.steps, stepDefinition{
			index:        i,
			id:           wf.Steps[i].ID,
			stepType:     wf.Steps[i].Type,
			prompt:       wf.Steps[i].Prompt,
			run:          wf.Steps[i].Run,
			instructions: wf.Steps[i].Instructions,
		})
	}
	return s
}
