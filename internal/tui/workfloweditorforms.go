package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The editor's addressing and its forms. A breadcrumb is a dotted path into
// the definition; this file resolves one to the block it names and draws that
// block's rows from the served descriptor.
//
// The grammar is not invented here. It is the one internal/workflow/edit.go
// already resolves and the one the editor already addresses its set/remove
// ops to — the reader was simply narrower than the writer, so a fan-out's
// lanes and its merge were rows that answered "the step at … is no longer
// there" (issue #320). Making the two agree is the whole change:
//
//	steps[i]                     steps[i].steps[j]     steps[i].steps
//	steps[i].lanes               steps[i].lanes[j]     steps[i].lanes[j].steps[k]
//	steps[i].lane                steps[i].merge        steps[i].merge.agent
//	fields                       fields[i]
//	defaults                     defaults.container

// wfNodeKind names the kind of block a breadcrumb resolved to. rebuild picks
// the form from it, which is why resolve returns one of these rather than a
// step plus a handful of booleans.
type wfNodeKind int

const (
	// wfNodeRoot is the whole workflow: the empty breadcrumb.
	wfNodeRoot wfNodeKind = iota
	wfNodeStep
	// wfNodeSteps is a step *list* — a parallel group's members, a loop
	// body, a lane's inline steps. It is what the `steps` row of a group
	// descends into, which is a target the row builder has always written
	// and the resolver never accepted.
	wfNodeSteps
	wfNodeLanes
	// wfNodeLane is one lane of a list and the single `lane:` template
	// alike: they are the same descriptor and differ only in arity.
	wfNodeLane
	wfNodeMerge
	wfNodeFields
	wfNodeField
	wfNodeDefaults
	wfNodeContainer
)

// wfNode is a resolved breadcrumb. Only the member its kind names is set; the
// rest are zero, and a block the file does not have resolves to its zero
// value rather than to a failure — a `merge:` a step has not written yet is a
// form whose rows all read unset, and setting one is what creates the block.
type wfNode struct {
	kind  wfNodeKind
	step  apiclient.WorkflowStepDef
	steps []apiclient.WorkflowStepDef
	lane  apiclient.WorkflowLaneDef
	lanes []apiclient.WorkflowLaneDef
	merge apiclient.WorkflowMergeDef
	field apiclient.WorkflowField
}

// resolve walks a breadcrumb to the block it addresses.
func (e *wfEditorLayer) resolve(path string) (wfNode, bool) {
	if e.def == nil {
		return wfNode{}, false
	}
	node := wfNode{kind: wfNodeRoot}
	if path == "" {
		return node, true
	}
	for _, seg := range strings.Split(path, ".") {
		next, ok := e.descend(node, seg)
		if !ok {
			return wfNode{}, false
		}
		node = next
	}
	return node, true
}

// descend takes one segment of a breadcrumb. Which segments are legal depends
// on the node they are taken from, which is §8.2's nesting rules read as an
// addressing scheme.
func (e *wfEditorLayer) descend(node wfNode, seg string) (wfNode, bool) {
	key, idx, indexed := splitSegment(seg)
	switch key {
	case "steps":
		var list []apiclient.WorkflowStepDef
		switch node.kind {
		case wfNodeRoot:
			list = e.def.Steps
		case wfNodeStep:
			list = node.step.Steps
		case wfNodeLane:
			list = node.lane.Steps
		default:
			return wfNode{}, false
		}
		if !indexed {
			return wfNode{kind: wfNodeSteps, steps: list}, true
		}
		if idx < 0 || idx >= len(list) {
			return wfNode{}, false
		}
		return wfNode{kind: wfNodeStep, step: list[idx]}, true
	case "lanes":
		if node.kind != wfNodeStep {
			return wfNode{}, false
		}
		if !indexed {
			return wfNode{kind: wfNodeLanes, lanes: node.step.Lanes}, true
		}
		if idx < 0 || idx >= len(node.step.Lanes) {
			return wfNode{}, false
		}
		return wfNode{kind: wfNodeLane, lane: node.step.Lanes[idx]}, true
	case wfControlLane:
		if node.kind != wfNodeStep || indexed {
			return wfNode{}, false
		}
		lane := apiclient.WorkflowLaneDef{}
		if node.step.Lane != nil {
			lane = *node.step.Lane
		}
		return wfNode{kind: wfNodeLane, lane: lane}, true
	case "merge":
		if node.kind != wfNodeStep || indexed {
			return wfNode{}, false
		}
		merge := apiclient.WorkflowMergeDef{}
		if node.step.Merge != nil {
			merge = *node.step.Merge
		}
		return wfNode{kind: wfNodeMerge, merge: merge}, true
	case "agent":
		// Only a merge's resolver is a block; every other `agent:` is a
		// scalar row and has no form of its own.
		if node.kind != wfNodeMerge || indexed {
			return wfNode{}, false
		}
		step := apiclient.WorkflowStepDef{}
		if node.merge.Agent != nil {
			step = *node.merge.Agent
		}
		return wfNode{kind: wfNodeStep, step: step}, true
	case "fields":
		// The root's declared-field list. A lane's `fields:` is a mapping
		// row, not a block, which is why the kind is checked.
		if node.kind != wfNodeRoot {
			return wfNode{}, false
		}
		if !indexed {
			return wfNode{kind: wfNodeFields}, true
		}
		if idx < 0 || idx >= len(e.def.Fields) {
			return wfNode{}, false
		}
		return wfNode{kind: wfNodeField, field: e.def.Fields[idx]}, true
	case "defaults":
		if node.kind != wfNodeRoot || indexed {
			return wfNode{}, false
		}
		return wfNode{kind: wfNodeDefaults}, true
	case "container":
		if node.kind != wfNodeDefaults || indexed {
			return wfNode{}, false
		}
		return wfNode{kind: wfNodeContainer}, true
	}
	return wfNode{}, false
}

// splitSegment splits "lanes[2]" into its key and index. A segment with no
// bracket — "merge", "defaults" — is the block itself.
func splitSegment(seg string) (key string, idx int, indexed bool) {
	open := strings.IndexByte(seg, '[')
	if open < 0 || !strings.HasSuffix(seg, "]") {
		return seg, 0, false
	}
	n, err := strconv.Atoi(seg[open+1 : len(seg)-1])
	if err != nil {
		// Not an index: the whole segment is the key, which no case
		// matches, so the path fails to resolve rather than resolving to
		// something else.
		return seg, 0, false
	}
	return seg[:open], n, true
}

// contextOf reports which §8.2 nesting context a path sits in, which is what
// decides the members of its `type` row. It asks the parent breadcrumb rather
// than pattern-matching the string: the answer for `steps[3].merge.agent` is
// the merge context, and there is no `.steps[` in it to match on.
func (e *wfEditorLayer) contextOf(path string) string {
	// parentPath drops the whole last dotted segment, index and all, so the
	// parent of "steps[1].steps[0]" is the group "steps[1]" and the parent of
	// "steps[2].merge.agent" is the merge — which is exactly what decides a
	// context, and why this needs no string matching of its own.
	node, ok := e.resolve(parentPath(path))
	if !ok {
		return apiclient.WorkflowContextBody
	}
	switch node.kind {
	case wfNodeMerge:
		// §7.6's resolver is one agent step and nothing else.
		return apiclient.WorkflowContextMerge
	case wfNodeStep:
		switch node.step.Type {
		case "parallel":
			return apiclient.WorkflowContextParallel
		case "loop":
			return apiclient.WorkflowContextLoop
		}
	}
	// The top level and a lane's inline steps are both ordinary bodies.
	return apiclient.WorkflowContextBody
}

// buildTopLevel draws §8.1's own fields plus a row per top-level step.
func (e *wfEditorLayer) buildTopLevel() {
	for _, f := range e.schema.TopLevel {
		row := wfEditRow{field: f, path: f.Name, value: wfRead(wfTopLevelReaders, *e.def, f.Name)}
		switch f.Name {
		case "fields":
			// The declared-field list is a block of its own, and enter opens
			// it: before this it was a row that did nothing at all.
			row.path, row.descend = "", "fields"
			row.value = fmt.Sprintf("%d declared", len(e.def.Fields))
		case "defaults":
			row.path, row.descend = "", "defaults"
			row.value = defaultsSummary(e.def.Defaults)
		case "steps":
			// The steps are the rows below rather than a block to open.
			row.path = ""
			row.value = fmt.Sprintf("%d steps", len(e.def.Steps))
		}
		e.rows = append(e.rows, row)
	}
	e.buildSteps("steps", e.def.Steps)
}

// buildSteps draws one row per member of a step list. list is the dotted path
// of the sequence itself — "steps", "steps[3].steps", "steps[7].lanes[0].steps"
// — which is what the insert/remove/move keys address.
func (e *wfEditorLayer) buildSteps(list string, steps []apiclient.WorkflowStepDef) {
	for i, st := range steps {
		e.rows = append(e.rows, stepRow(list, i, st))
	}
}

// stepRow is the summary line a step gets in a list: its id, its type, and
// the form it descends into.
func stepRow(list string, index int, st apiclient.WorkflowStepDef) wfEditRow {
	path := fmt.Sprintf("%s[%d]", list, index)
	label := st.ID
	if label == "" {
		label = "(no id)"
	}
	return wfEditRow{
		field:   apiclient.WorkflowSchemaField{Name: path, Control: apiclient.WorkflowControlSteps},
		label:   label,
		value:   st.Type,
		descend: path,
		list:    list,
		index:   index,
	}
}

func defaultsSummary(d apiclient.WorkflowDefaults) string {
	var parts []string
	if d.Agent != "" {
		parts = append(parts, "agent "+d.Agent)
	}
	if d.Model != "" {
		parts = append(parts, "model "+d.Model)
	}
	if d.Container != nil {
		parts = append(parts, "container")
	}
	if len(parts) == 0 {
		return "(unset)"
	}
	return strings.Join(parts, " · ")
}

// buildStep draws the form for one step: its type's schema fields plus the
// common fields that type accepts, and a descend row per nested body.
func (e *wfEditorLayer) buildStep(path string, st apiclient.WorkflowStepDef) {
	typ, known := e.stepType(st.Type)
	if !known {
		e.rows = append(e.rows, wfEditRow{
			field: apiclient.WorkflowSchemaField{Name: "type", Control: apiclient.WorkflowControlString},
			path:  path + ".type", value: st.Type,
		})
		return
	}
	accepts := map[string]bool{}
	for _, name := range typ.Common {
		accepts[name] = true
	}
	// `type` first: it is the discriminator, and changing it changes every
	// row below.
	e.rows = append(e.rows, wfEditRow{
		field: apiclient.WorkflowSchemaField{
			Name: "type", Control: apiclient.WorkflowControlEnum,
			Values: e.typesFor(e.contextOf(path)), Required: true, Help: typ.Help,
		},
		path: path + ".type", value: st.Type,
	})
	for _, f := range e.schema.Common {
		if !accepts[f.Name] {
			continue
		}
		e.rows = append(e.rows, e.stepFieldRow(path, f, st))
	}
	for _, f := range typ.Fields {
		e.rows = append(e.rows, e.stepFieldRow(path, f, st))
	}
	// The nested body's own steps, so descending twice is not needed to see
	// what is inside a group.
	e.buildSteps(path+".steps", st.Steps)
}

// stepFieldRow is one row of a step's form: a value to edit, or a block to
// descend into with a summary of what is in it.
func (e *wfEditorLayer) stepFieldRow(path string, f apiclient.WorkflowSchemaField, st apiclient.WorkflowStepDef) wfEditRow {
	row := wfEditRow{field: f, path: path + "." + f.Name, value: wfRead(wfStepReaders, st, f.Name)}
	block := func(target, value string) {
		row.path, row.descend, row.value = "", target, value
	}
	switch f.Control {
	case apiclient.WorkflowControlSteps:
		block(path+"."+f.Name, fmt.Sprintf("%d steps", len(st.Steps)))
	case apiclient.WorkflowControlLanes:
		block(path+".lanes", fmt.Sprintf("%d lanes", len(st.Lanes)))
	case wfControlLane:
		value := unsetMarker
		if st.Lane != nil {
			value = st.Lane.ID
		}
		block(path+".lane", value)
	case apiclient.WorkflowControlMerge:
		value := unsetMarker
		if st.Merge != nil {
			value = st.Merge.ConflictPolicy()
		}
		block(path+".merge", value)
	}
	return row
}

// buildLanes draws a fan_out's lane list, one row per lane.
func (e *wfEditorLayer) buildLanes(path string, lanes []apiclient.WorkflowLaneDef) {
	for i, lane := range lanes {
		e.rows = append(e.rows, wfLaneRow(path, i, lane))
	}
}

// wfLaneRow is the summary line a lane gets in the list: its id, and either the
// registry workflow it names or how many inline steps it carries — the two
// shapes §7.6 allows.
func wfLaneRow(list string, index int, lane apiclient.WorkflowLaneDef) wfEditRow {
	path := fmt.Sprintf("%s[%d]", list, index)
	label := lane.ID
	if label == "" {
		label = "(no id)"
	}
	value := fmt.Sprintf("%d steps", len(lane.Steps))
	switch {
	case lane.Workflow != "":
		value = lane.Workflow
	case lane.ResolvedFrom != "":
		value = lane.ResolvedFrom + " (resolved)"
	}
	return wfEditRow{
		field:   apiclient.WorkflowSchemaField{Name: path, Control: apiclient.WorkflowControlLanes},
		label:   label,
		value:   value,
		descend: path,
		list:    list,
		index:   index,
	}
}

// buildLane draws one lane — or the single `lane:` template, which is the
// same descriptor at a different path.
func (e *wfEditorLayer) buildLane(path string, lane apiclient.WorkflowLaneDef) {
	for _, f := range e.schema.Lane {
		row := wfEditRow{field: f, path: path + "." + f.Name, value: wfRead(wfLaneReaders, lane, f.Name)}
		if f.Control == apiclient.WorkflowControlSteps {
			row.path, row.descend = "", path+".steps"
			row.value = fmt.Sprintf("%d steps", len(lane.Steps))
		}
		e.rows = append(e.rows, row)
	}
	// A lane's inline steps are an ordinary body, listed the way a group's
	// members are.
	e.buildSteps(path+".steps", lane.Steps)
}

// buildMerge draws a fan_out's join, whose `agent` row descends into a step
// form built in the merge context.
func (e *wfEditorLayer) buildMerge(path string, merge apiclient.WorkflowMergeDef) {
	for _, f := range e.schema.Merge {
		row := wfEditRow{field: f, path: path + "." + f.Name, value: wfRead(wfMergeReaders, merge, f.Name)}
		if f.Name == "agent" {
			row.path, row.descend = "", path+".agent"
			row.value = unsetMarker
			if merge.Agent != nil {
				row.value = merge.Agent.DisplayName()
			}
		}
		e.rows = append(e.rows, row)
	}
}

// buildFields draws the §8.1.2 declaration list, one row per declared field.
func (e *wfEditorLayer) buildFields() {
	for i, f := range e.def.Fields {
		path := fmt.Sprintf("fields[%d]", i)
		label := f.Name
		if label == "" {
			label = "(no name)"
		}
		e.rows = append(e.rows, wfEditRow{
			field:   apiclient.WorkflowSchemaField{Name: path, Control: apiclient.WorkflowControlFields},
			label:   label,
			value:   f.Type,
			descend: path,
			list:    "fields",
			index:   i,
		})
	}
}

// buildField draws one declared field from the descriptor's Field block.
func (e *wfEditorLayer) buildField(path string, f apiclient.WorkflowField) {
	for _, sf := range e.schema.Field {
		e.rows = append(e.rows, wfEditRow{
			field: sf, path: path + "." + sf.Name,
			value: wfRead(wfFieldReaders, f, sf.Name),
		})
	}
}

// buildDefaults draws §8.1's defaults block, whose `container` row descends
// into the §8.6 override.
func (e *wfEditorLayer) buildDefaults() {
	for _, f := range e.schema.Defaults {
		row := wfEditRow{
			field: f, path: "defaults." + f.Name,
			value: wfRead(wfDefaultsReaders, e.def.Defaults, f.Name),
		}
		if f.Control == apiclient.WorkflowControlContainer {
			row.path, row.descend = "", "defaults.container"
			row.value = unsetMarker
			if c := e.def.Defaults.Container; c != nil {
				row.value = wfStr(c.Image)
				if row.value == "" {
					row.value = "(set)"
				}
			}
		}
		e.rows = append(e.rows, row)
	}
}

// buildContainer draws `defaults.container` (§8.6, task 061).
func (e *wfEditorLayer) buildContainer() {
	c := apiclient.WorkflowContainerDef{}
	if e.def.Defaults.Container != nil {
		c = *e.def.Defaults.Container
	}
	for _, f := range e.schema.Container {
		e.rows = append(e.rows, wfEditRow{
			field: f, path: "defaults.container." + f.Name,
			value: wfRead(wfContainerReaders, c, f.Name),
		})
	}
}

// typesFor is StepTypesFor, read off the served descriptor rather than
// re-derived: a type a context forbids is one the row does not offer.
func (e *wfEditorLayer) typesFor(context string) []string {
	var out []string
	for _, s := range e.schema.Steps {
		for _, c := range s.Contexts {
			if c == context {
				out = append(out, s.Type)
				break
			}
		}
	}
	return out
}

func (e *wfEditorLayer) stepType(typ string) (apiclient.WorkflowSchemaStepType, bool) {
	for _, s := range e.schema.Steps {
		if s.Type == typ {
			return s, true
		}
	}
	return apiclient.WorkflowSchemaStepType{}, false
}
