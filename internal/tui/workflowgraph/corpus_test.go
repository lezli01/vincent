package workflowgraph

import "github.com/lezli01/vincent/internal/apiclient"

// The corpus is the acceptance list of task 017 as compact workflows built in
// code rather than screenshots of hand-written ANSI. Every shape the graph
// has to draw appears here once, and the same fixtures back the diagram,
// geometry and golden-render tests — so a change that moves the picture moves
// exactly one set of expectations.

func intp(v int) *int { return &v }

func step(id, typ string) apiclient.WorkflowStepDef {
	return apiclient.WorkflowStepDef{ID: id, Type: typ}
}

func body(steps ...apiclient.WorkflowStepDef) *apiclient.WorkflowBody {
	return &apiclient.WorkflowBody{Name: "corpus", Steps: steps}
}

// 1 — three ordinary sequential steps.
func fixtureSequential() *apiclient.WorkflowBody {
	return body(step("plan", "agent"), step("build", "command"), step("ship", "manual"))
}

// 2 — a guarded ordinary step, which skips and rejoins the same sequence and
// therefore draws no second branch (decision 5).
func fixtureGuarded() *apiclient.WorkflowBody {
	guarded := step("maybe", "command")
	guarded.If = `{{ eq .Fields.mode "full" }}`
	checked := step("plan", "agent")
	checked.Check = "go build ./..."
	return body(checked, guarded, step("ship", "command"))
}

// 3 — a condition with a true continuation and a false edge to END.
func fixtureCondition() *apiclient.WorkflowBody {
	gate := step("gate", "condition")
	gate.If = `{{ .Fields.deploy }}`
	return body(step("plan", "agent"), gate, step("deploy", "command"))
}

// 4 — a parallel group with three members and a join.
func fixtureParallel() *apiclient.WorkflowBody {
	group := step("checks", "parallel")
	group.MaxParallel = intp(2)
	guarded := step("e2e", "command")
	guarded.If = `{{ .Fields.slow }}`
	group.Steps = []apiclient.WorkflowStepDef{step("lint", "command"), step("unit", "command"), guarded}
	return body(step("plan", "agent"), group, step("ship", "command"))
}

// 5 — a fan_out with an inline lane, a named-workflow lane and an agent merge.
func fixtureFanOut() *apiclient.WorkflowBody {
	spread := step("spread", "fan_out")
	spread.Lanes = []apiclient.WorkflowLaneDef{
		{ID: "api", Steps: []apiclient.WorkflowStepDef{step("api_impl", "agent"), step("api_test", "command")}},
		{ID: "web", Workflow: "web-feature", If: `{{ .Fields.web }}`},
	}
	spread.Merge = &apiclient.WorkflowMergeDef{OnConflict: "agent", Agent: &apiclient.WorkflowStepDef{
		ID: "fixup", Type: "agent", Prompt: "resolve",
	}}
	return body(step("plan", "agent"), spread, step("ship", "command"))
}

// 5b — a derived fan_out whose lanes form a `needs:` DAG and whose schedule
// is eager: two lanes in round one, one that needs both in round two, and the
// provenance the runner recorded when it materialized the list (§7.6, tasks
// 080 and 081).
func fixtureLaneDAG() *apiclient.WorkflowBody {
	spread := step("spread", "fan_out")
	spread.Schedule = "eager"
	spread.DerivedFrom = &apiclient.WorkflowDerivationDef{
		Lane:    "{{ .Item.id }}",
		ForEach: []string{"{{ .Steps.plan.Result }}"},
	}
	lane := func(id string, needs ...string) apiclient.WorkflowLaneDef {
		return apiclient.WorkflowLaneDef{
			ID: id, Needs: needs,
			Steps: []apiclient.WorkflowStepDef{step(id+"_work", "agent")},
		}
	}
	spread.Lanes = []apiclient.WorkflowLaneDef{
		lane("api"), lane("db"), lane("wire", "api", "db"),
	}
	return body(step("plan", "agent"), spread, step("ship", "command"))
}

// 6 — a counted loop with a body and a back-edge.
func fixtureLoop() *apiclient.WorkflowBody {
	loop := step("repeat", "loop")
	loop.Count = intp(3)
	loop.MaxIterations = intp(5)
	loop.Steps = []apiclient.WorkflowStepDef{step("work", "agent"), step("verify", "command")}
	return body(step("plan", "agent"), loop, step("ship", "command"))
}

// 7 — a loop containing a break exit.
func fixtureLoopBreak() *apiclient.WorkflowBody {
	brk := step("enough", "break")
	brk.If = `{{ .Steps.work.Success }}`
	loop := step("repeat", "loop")
	loop.ForEach = []string{`{{ .Steps.plan.Result }}`}
	loop.Steps = []apiclient.WorkflowStepDef{step("work", "agent"), brk}
	return body(step("plan", "agent"), loop, step("ship", "command"))
}

// 8 — nesting that is currently legal: a loop whose body carries a condition
// (which ends the iteration, not the workflow), beside a fan_out with a
// guarded lane.
func fixtureNested() *apiclient.WorkflowBody {
	cond := step("skip", "condition")
	cond.If = `{{ .Steps.work.Success }}`
	loop := step("repeat", "loop")
	loop.Count = intp(2)
	loop.Steps = []apiclient.WorkflowStepDef{step("work", "agent"), cond, step("record", "command")}

	spread := step("spread", "fan_out")
	spread.Lanes = []apiclient.WorkflowLaneDef{
		{ID: "one", Steps: []apiclient.WorkflowStepDef{step("first", "command")}},
		{ID: "two", If: `{{ .Fields.two }}`, Steps: []apiclient.WorkflowStepDef{step("second", "command")}},
	}
	return body(loop, spread)
}

// 9 — labels containing wide characters, which must be measured by display
// width rather than by rune count (decision 6).
func fixtureWideLabels() *apiclient.WorkflowBody {
	a := step("wide", "agent")
	a.Name = "実装をレビューしてからマージする"
	b := step("emoji", "command")
	b.Name = "🚀🚀🚀 deploy 🚀🚀🚀 everything everywhere"
	return body(a, b)
}

// 10 — includes, at the top level and inside a loop body. Each draws as one
// collapsed node labelled with the workflow it splices in: the graph shows the
// file as authored, and as authored an include *is* one step (task 019
// decision 12).
func fixtureInclude() *apiclient.WorkflowBody {
	inc := step("verify", "include")
	inc.Workflow = "go-checks"
	nested := step("recheck", "include")
	nested.Workflow = "go-checks"
	loop := step("repeat", "loop")
	loop.Count = intp(2)
	loop.Steps = []apiclient.WorkflowStepDef{step("attempt", "agent"), nested}
	return body(step("plan", "agent"), inc, loop, step("ship", "command"))
}
