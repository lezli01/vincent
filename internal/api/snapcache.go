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
	// resolvedFrom is the chain of workflows this step was spliced through
	// (§7.9), outermost first, and empty for a step the task's own workflow
	// wrote. It is what the detail view attributes a step to — after a splice
	// there is no include left to point at, so the step has to carry it.
	resolvedFrom []string
	// loop is the §7.8 shape of a `loop` step, nil for every other type. It
	// is what lets the task endpoints report a loop rollup without re-parsing
	// the snapshot per request.
	loop *loopDefinition
}

// loopDefinition is what a `loop` step's rollup needs from the snapshot: its
// driver, and the largest iteration it could reach. For `count:` that is the
// count itself; for `for_each:` the list length is only known at run time, so
// the ceiling is the honest n in a k/n.
type loopDefinition struct {
	driver string
	total  int
	// body is the loop's body step ids in declaration order (§7.8). It is
	// what turns a body row's step id into "repair 2/3": body rows carry the
	// *loop's* step index, so a client has nothing to count a position
	// against unless the snapshot hands it the order.
	body []string
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

// loopAt is the loop shape of step index i, or nil when that step is not a
// loop — which includes the out-of-range index a finished task's cursor sits
// at.
func (s snapshotSummary) loopAt(i int) *loopDefinition {
	if i < 0 || i >= len(s.steps) {
		return nil
	}
	return s.steps[i].loop
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
	// ceiling supplies `loop.max_iterations` for a loop step that declares no
	// bound of its own. It is a func for the reason the registry's is: config
	// hot-reloads (§12.3), and an entry parsed at startup must not pin the
	// number a rollup reports for the rest of the daemon's life.
	ceiling func() int
}

func newSnapshotCache(ceiling func() int) *snapshotCache {
	if ceiling == nil {
		ceiling = func() int { return 0 }
	}
	return &snapshotCache{m: make(map[int64]snapshotSummary), ceiling: ceiling}
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
	s := parseSnapshot(snapshot, c.ceiling())
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

// loopDefinitionOf extracts the §7.8 rollup shape of a `loop` step. ceiling
// is `loop.max_iterations`, used when the step declares no `max_iterations:`
// of its own.
func loopDefinitionOf(step workflow.Step, ceiling int) *loopDefinition {
	if step.Type != workflow.StepLoop {
		return nil
	}
	def := &loopDefinition{driver: step.Driver()}
	for i := range step.Steps {
		def.body = append(def.body, step.Steps[i].ID)
	}
	switch {
	case step.Count != nil:
		def.total = *step.Count
	case step.MaxIterations != nil:
		def.total = *step.MaxIterations
	default:
		def.total = ceiling
	}
	return def
}

func parseSnapshot(snapshot string, ceiling int) snapshotSummary {
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
			resolvedFrom: wf.Steps[i].ResolvedFrom,
			loop:         loopDefinitionOf(wf.Steps[i], ceiling),
		})
	}
	return s
}
