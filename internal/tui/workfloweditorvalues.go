package tui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The editor's readers: what a row shows for the value the file currently
// holds. There is one table per block, keyed by the schema field name the
// descriptor publishes, rather than one switch over every name — a switch
// answers "" for a field nobody added a case for, and the renderer prints ""
// as `(unset)`, so a step that set `timeout: 2m` reported that it had set
// nothing (issue #320). A table can be enumerated, which is how
// TestEveryPublishedFieldHasAReader now fails instead of a person finding out.
//
// A pointer field renders the value it points at, `max_retries: 0` included.
// That distinction between unset and set-to-zero is the whole reason those
// fields are pointers, and a form showing both as `(unset)` would offer to
// remove a key the file does set.
//
// Lists and maps are rendered the way the write path parses them back:
// comma-joined for a list (renderFlowList's input) and `k=v, k=v` with sorted
// keys for a map, so what a row shows is what a commit sends.

// wfControlLane is workflow.ControlLane — the single `lane:` template a
// derived fan-out renders once per `for_each` item (§7.6, task 080).
// apiclient publishes no constant for it and its file is not this unit's to
// edit, so the wire string lives beside the code that branches on it.
const wfControlLane = "lane"

// wfStructuralControls are the controls a row descends into rather than reads.
// Their value is a summary the row builder computes — "3 steps", the merge
// policy — and enter opens the block, so they have no entry in any reader
// table. Listing them here with the reason is what makes "no reader" a
// recorded decision rather than the oversight it was for twelve other fields.
var wfStructuralControls = map[string]string{
	apiclient.WorkflowControlSteps:     "a nested step body: the row descends into it",
	apiclient.WorkflowControlLanes:     "a fan_out's lane list: the row descends into it",
	wfControlLane:                      "a fan_out's single lane template: the row descends into it",
	apiclient.WorkflowControlMerge:     "a nested block — merge, merge.agent, defaults: the row descends into it",
	apiclient.WorkflowControlFields:    "the declared-field list: the row descends into it",
	apiclient.WorkflowControlContainer: "defaults.container: the row descends into it",
}

// wfTopLevelReaders reads §8.1's scalar fields. Its three block fields —
// fields, defaults, steps — are structural and have none.
var wfTopLevelReaders = map[string]func(apiclient.WorkflowBody) string{
	"name":        func(b apiclient.WorkflowBody) string { return b.Name },
	"description": func(b apiclient.WorkflowBody) string { return b.Description },
	"platforms":   func(b apiclient.WorkflowBody) string { return wfList(b.Platforms) },
}

// wfStepReaders covers every field any step type publishes, common fields
// included: the form draws one step at a time and asks this table for each
// row the type accepts.
var wfStepReaders = map[string]func(apiclient.WorkflowStepDef) string{
	// common
	"id":            func(s apiclient.WorkflowStepDef) string { return s.ID },
	"name":          func(s apiclient.WorkflowStepDef) string { return s.Name },
	"type":          func(s apiclient.WorkflowStepDef) string { return s.Type },
	"if":            func(s apiclient.WorkflowStepDef) string { return s.If },
	"allow_failure": func(s apiclient.WorkflowStepDef) string { return wfBool(s.AllowFailure) },
	"max_retries":   func(s apiclient.WorkflowStepDef) string { return wfInt(s.MaxRetries) },
	"retry_backoff": func(s apiclient.WorkflowStepDef) string { return wfStr(s.RetryBackoff) },
	"timeout":       func(s apiclient.WorkflowStepDef) string { return wfStr(s.Timeout) },
	// agent
	"prompt":          func(s apiclient.WorkflowStepDef) string { return s.Prompt },
	"agent":           func(s apiclient.WorkflowStepDef) string { return s.Agent },
	"model":           func(s apiclient.WorkflowStepDef) string { return s.Model },
	"effort":          func(s apiclient.WorkflowStepDef) string { return s.Effort },
	"permission_mode": func(s apiclient.WorkflowStepDef) string { return s.PermissionMode },
	"on_input":        func(s apiclient.WorkflowStepDef) string { return s.OnInput },
	"input_timeout":   func(s apiclient.WorkflowStepDef) string { return wfStr(s.InputTimeout) },
	// agent and command
	"check":         func(s apiclient.WorkflowStepDef) string { return s.Check },
	"check_timeout": func(s apiclient.WorkflowStepDef) string { return wfStr(s.CheckTimeout) },
	// command
	"run":   func(s apiclient.WorkflowStepDef) string { return s.Run },
	"shell": func(s apiclient.WorkflowStepDef) string { return s.Shell },
	"env":   func(s apiclient.WorkflowStepDef) string { return wfMap(s.Env) },
	// manual
	"instructions": func(s apiclient.WorkflowStepDef) string { return s.Instructions },
	// parallel
	"max_parallel": func(s apiclient.WorkflowStepDef) string { return wfInt(s.MaxParallel) },
	// fan_out
	"max_lanes": func(s apiclient.WorkflowStepDef) string { return wfInt(s.MaxLanes) },
	"schedule":  func(s apiclient.WorkflowStepDef) string { return s.Schedule },
	// loop, and a derived fan_out's driver
	"for_each":       func(s apiclient.WorkflowStepDef) string { return wfList(s.ForEach) },
	"count":          func(s apiclient.WorkflowStepDef) string { return wfInt(s.Count) },
	"max_iterations": func(s apiclient.WorkflowStepDef) string { return wfInt(s.MaxIterations) },
	// include
	"workflow": func(s apiclient.WorkflowStepDef) string { return s.Workflow },
}

// wfDefaultsReaders is §8.1's defaults block, `container` excepted.
var wfDefaultsReaders = map[string]func(apiclient.WorkflowDefaults) string{
	"agent":           func(d apiclient.WorkflowDefaults) string { return d.Agent },
	"model":           func(d apiclient.WorkflowDefaults) string { return d.Model },
	"effort":          func(d apiclient.WorkflowDefaults) string { return d.Effort },
	"permission_mode": func(d apiclient.WorkflowDefaults) string { return d.PermissionMode },
	"on_input":        func(d apiclient.WorkflowDefaults) string { return d.OnInput },
	"input_timeout":   func(d apiclient.WorkflowDefaults) string { return wfStr(d.InputTimeout) },
	"max_retries":     func(d apiclient.WorkflowDefaults) string { return wfInt(d.MaxRetries) },
	"retry_backoff":   func(d apiclient.WorkflowDefaults) string { return wfStr(d.RetryBackoff) },
	"timeout":         func(d apiclient.WorkflowDefaults) string { return wfStr(d.Timeout) },
}

// wfFieldReaders is one §8.1.2 declared field.
var wfFieldReaders = map[string]func(apiclient.WorkflowField) string{
	"name":        func(f apiclient.WorkflowField) string { return f.Name },
	"label":       func(f apiclient.WorkflowField) string { return f.Label },
	"description": func(f apiclient.WorkflowField) string { return f.Description },
	"type":        func(f apiclient.WorkflowField) string { return f.Type },
	"required":    func(f apiclient.WorkflowField) string { return wfBool(f.Required) },
	"pattern":     func(f apiclient.WorkflowField) string { return f.Pattern },
	"values":      func(f apiclient.WorkflowField) string { return wfList(f.Values) },
	"multiple":    func(f apiclient.WorkflowField) string { return wfBool(f.Multiple) },
	"default":     func(f apiclient.WorkflowField) string { return f.Default },
}

// wfLaneReaders is one fan_out lane, and the single `lane:` template — the
// two are the same descriptor and differ only in arity.
var wfLaneReaders = map[string]func(apiclient.WorkflowLaneDef) string{
	"id":       func(l apiclient.WorkflowLaneDef) string { return l.ID },
	"if":       func(l apiclient.WorkflowLaneDef) string { return l.If },
	"needs":    func(l apiclient.WorkflowLaneDef) string { return wfList(l.Needs) },
	"workflow": func(l apiclient.WorkflowLaneDef) string { return l.Workflow },
	"fields":   func(l apiclient.WorkflowLaneDef) string { return wfMap(l.Fields) },
	"agent":    func(l apiclient.WorkflowLaneDef) string { return l.Agent },
	"model":    func(l apiclient.WorkflowLaneDef) string { return l.Model },
	"effort":   func(l apiclient.WorkflowLaneDef) string { return l.Effort },
	"priority": func(l apiclient.WorkflowLaneDef) string { return wfInt(l.Priority) },
}

// wfMergeReaders is the join. Its `agent` is the resolving step, which the
// row descends into.
var wfMergeReaders = map[string]func(apiclient.WorkflowMergeDef) string{
	"on_conflict": func(m apiclient.WorkflowMergeDef) string { return m.OnConflict },
}

// wfContainerReaders is `defaults.container` (§8.6). Every value is a pointer
// on the wire because `image: ""` means "run this one on the host" and is not
// the same answer as an absent key.
var wfContainerReaders = map[string]func(apiclient.WorkflowContainerDef) string{
	"image":              func(c apiclient.WorkflowContainerDef) string { return wfStr(c.Image) },
	"runtime":            func(c apiclient.WorkflowContainerDef) string { return wfStr(c.Runtime) },
	"mount_agent_config": func(c apiclient.WorkflowContainerDef) string { return wfBoolPtr(c.MountAgentConfig) },
	"network":            func(c apiclient.WorkflowContainerDef) string { return wfBoolPtr(c.Network) },
	"extra_mounts":       func(c apiclient.WorkflowContainerDef) string { return wfList(c.ExtraMounts) },
}

// wfRead is the one lookup every builder goes through: a field with no reader
// shows as unset, which is the old behaviour kept as the fallback for a
// *newer daemon's* field this client has never heard of. A field this
// daemon's descriptor publishes has a reader, and a test says so.
func wfRead[T any](readers map[string]func(T) string, src T, name string) string {
	read, ok := readers[name]
	if !ok {
		return ""
	}
	return read(src)
}

// wfInt renders a set-to-zero pointer as "0" and only an absent one as unset.
func wfInt(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func wfStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func wfBoolPtr(p *bool) string {
	if p == nil {
		return ""
	}
	return wfBoolText(*p)
}

// wfBool renders a plain bool. False is unset rather than "false": the wire
// omits an absent `allow_failure:`, so the two are one value here and writing
// "false" would offer to add a key that means nothing.
func wfBool(v bool) string {
	if !v {
		return ""
	}
	return "true"
}

func wfBoolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// wfList renders a list the way renderFlowList reads one back.
func wfList(items []string) string { return strings.Join(items, ", ") }

// wfMap renders a mapping as `k=v, k=v` with keys sorted, so the row is
// stable across reads and is what a map row will commit back.
func wfMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}
