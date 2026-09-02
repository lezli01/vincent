package workflow

// The `needs:` graph a `fan_out` step's lanes form (spec §7.6, task 080).
//
// A lane list is a set until a lane names another; then it is a DAG, and the
// engine walks it in rounds — merge what is done, spawn what is ready, park.
// The wave numbering here is what makes those rounds derivable rather than
// declared: no workflow names a wave, and none names a maximum.
//
// The same two failures are checked in two places for one reason. A statically
// declared list is judged at **load**, where the person who wrote it is still
// looking; a derived list's ids do not exist until they are rendered, so the
// identical check runs at **spawn** and blocks (decision 6). Both call the
// functions below, so there is one definition of "this graph is broken".

import (
	"fmt"
	"sort"
	"text/template"
)

// LaneProblem is one fault in a lane list's `needs:` graph: an edge to a lane
// that does not exist, or a cycle. Index is the offending lane's position in
// the list, so a load-time caller can build its YAML path and a spawn-time one
// can name the lane.
type LaneProblem struct {
	Index   int
	LaneID  string
	Message string
}

// LaneGraphProblems reports every fault in a lane list's `needs:` edges: an
// edge naming a lane the step does not declare, a lane naming itself, and any
// cycle among them.
//
// Lanes whose ids are empty or duplicated are skipped rather than reported —
// those are separate findings the caller has already made, and repeating them
// as "unknown lane" would send the reader after the wrong thing.
func LaneGraphProblems(lanes []Lane) []LaneProblem {
	ids := make(map[string]bool, len(lanes))
	for _, lane := range lanes {
		if lane.ID != "" {
			ids[lane.ID] = true
		}
	}
	var problems []LaneProblem
	for i, lane := range lanes {
		for _, need := range lane.Needs {
			switch {
			case need == lane.ID:
				problems = append(problems, LaneProblem{
					Index: i, LaneID: lane.ID,
					Message: fmt.Sprintf("lane %q needs itself", lane.ID),
				})
			case !ids[need]:
				problems = append(problems, LaneProblem{
					Index: i, LaneID: lane.ID,
					Message: fmt.Sprintf("lane %q needs %q, which is not a lane of this step", lane.ID, need),
				})
			}
		}
	}
	if len(problems) > 0 {
		// A cycle walk over a graph with dangling edges reports the dangle
		// twice, in two vocabularies. One finding per fault.
		return problems
	}
	if cycle := laneCycle(lanes); len(cycle) > 0 {
		i := 0
		for j, lane := range lanes {
			if lane.ID == cycle[0] {
				i = j
				break
			}
		}
		return []LaneProblem{{
			Index: i, LaneID: cycle[0],
			Message: "lane dependency cycle: " + joinArrow(cycle),
		}}
	}
	return nil
}

// LaneWaves numbers each lane by how many rounds must precede it: 0 for a lane
// that needs nothing, and one more than the deepest lane it needs otherwise.
// It is the round scheduler's derivation of wave structure from the graph.
//
// It assumes a graph LaneGraphProblems accepted; an edge to an unknown lane is
// ignored rather than guessed at, which is what a guarded-off lane's dependents
// also get (§7.6: a lane that was never spawned imposes no ordering).
func LaneWaves(lanes []Lane) map[string]int {
	byID := make(map[string]Lane, len(lanes))
	for _, lane := range lanes {
		byID[lane.ID] = lane
	}
	waves := make(map[string]int, len(lanes))
	var depth func(id string, seen map[string]bool) int
	depth = func(id string, seen map[string]bool) int {
		if w, ok := waves[id]; ok {
			return w
		}
		if seen[id] {
			return 0 // a cycle the caller was supposed to have refused
		}
		seen[id] = true
		w := 0
		for _, need := range byID[id].Needs {
			if _, ok := byID[need]; !ok {
				continue
			}
			if d := depth(need, seen) + 1; d > w {
				w = d
			}
		}
		delete(seen, id)
		waves[id] = w
		return w
	}
	for _, lane := range lanes {
		depth(lane.ID, map[string]bool{})
	}
	return waves
}

// laneCycle returns one cycle's ids, first repeated at neither end, or nil.
func laneCycle(lanes []Lane) []string {
	byID := make(map[string]Lane, len(lanes))
	for _, lane := range lanes {
		byID[lane.ID] = lane
	}
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[string]int, len(lanes))
	var stack []string
	var walk func(id string) []string
	walk = func(id string) []string {
		color[id] = grey
		stack = append(stack, id)
		for _, need := range byID[id].Needs {
			if _, ok := byID[need]; !ok {
				continue
			}
			switch color[need] {
			case grey:
				for i, on := range stack {
					if on == need {
						return append(append([]string{}, stack[i:]...), need)
					}
				}
			case white:
				if cycle := walk(need); cycle != nil {
					return cycle
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return nil
	}
	// Sorted so the reported cycle does not depend on map iteration order.
	ids := make([]string, 0, len(lanes))
	for _, lane := range lanes {
		ids = append(ids, lane.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if color[id] == white {
			stack = stack[:0]
			if cycle := walk(id); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

func joinArrow(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += " → "
		}
		out += id
	}
	return out
}

// validateFanOutShape checks that a `fan_out` step declares exactly one lane
// source: a static `lanes:` list, or a single `lane:` template driven by
// `for_each:` (§7.6, task 080).
func validateFanOutShape(step Step, base string, add func(string, string, ...any)) {
	switch {
	case len(step.Lanes) == 0 && step.Lane == nil:
		add(base+".lanes", "fan_out steps require at least one lane, "+
			"or a lane: template with for_each: to derive lanes from")
	case len(step.Lanes) > 0 && step.Lane != nil:
		add(base, "a fan_out has either a lanes list or a lane template, not both")
	}
	if step.Lane != nil && len(step.ForEach) == 0 {
		add(base+".for_each", "a lane: template needs for_each: to derive its lanes from")
	}
	if step.Lane == nil && len(step.ForEach) > 0 {
		add(base+".lane", "for_each: on a fan_out needs a lane: template to render per item")
	}
	if step.MaxLanes != nil && *step.MaxLanes < 1 {
		add(base+".max_lanes", "max_lanes must be at least 1, got %d", *step.MaxLanes)
	}
	switch step.Schedule {
	case "", ScheduleBarrier, ScheduleEager:
	default:
		add(base+".schedule", "schedule must be %s or %s, got %q",
			ScheduleBarrier, ScheduleEager, step.Schedule)
	}
	for i, item := range step.ForEach {
		if _, err := template.New("for_each").Parse(item); err != nil {
			add(fmt.Sprintf("%s.for_each[%d]", base, i), "template does not parse: %v", err)
		}
	}
}
