package workflow

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lezli01/vincent/skills"
)

// authorableKeys is every key the served §8.2 descriptor offers, in every
// block of it: the top level, defaults, a declared field, the common step
// fields, each step type's own, a lane, a merge block and a container block.
// The descriptor is what TestSchemaCoversEveryStepField already holds against
// Parse, so a key that reaches Parse reaches this set. The snapshot-only keys
// (`derived_from`, `resolved_from`) are withheld from the descriptor and so
// never appear here.
func authorableKeys() []string {
	d := SchemaDescriptor()
	seen := map[string]bool{}
	add := func(fs []SchemaField) {
		for _, f := range fs {
			seen[f.Name] = true
		}
	}
	add(d.TopLevel)
	add(d.Defaults)
	add(d.Field)
	add(d.Common)
	for _, s := range d.Steps {
		add(s.Fields)
	}
	add(d.Lane)
	add(d.Merge)
	add(d.Container)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// namesKey reports whether text names key as a whole word, so `timeout` is
// not satisfied by `input_timeout` nor `lane` by `lanes`.
func namesKey(text, key string) bool {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(key) + `([^A-Za-z0-9_]|$)`).
		MatchString(text)
}

// updateWorkflowsChecklist is the text under "The bar" in update-workflows'
// prompt: the version-coupled list task 037 decision 5 makes part of shipping
// a workflow feature.
func updateWorkflowsChecklist(t *testing.T) string {
	t.Helper()
	const start, end = "## The bar", "## What you may not change"
	i := strings.Index(UpdateWorkflowsSource, start)
	j := strings.Index(UpdateWorkflowsSource, end)
	if i < 0 || j < i {
		t.Fatalf("update-workflows no longer has a %q section ending at %q", start, end)
	}
	return UpdateWorkflowsSource[i:j]
}

// checklistExempt are the descriptor keys the checklist need not name. The
// checklist lists what a workflow can be *behind* on; these are keys no
// workflow can lack and still be one, or keys item 9 forbids the pass to add.
var checklistExempt = map[string]string{
	"id":           "every step has one; nothing to modernize",
	"name":         "every workflow, step and field has one; nothing to modernize",
	"description":  "prose, not a feature",
	"steps":        "every workflow has them",
	"prompt":       "required by every agent step",
	"run":          "required by every command step",
	"instructions": "required by every manual step",
	"workflow":     "required by include and named by a lane; item 2 covers both as features",
	// Which image a project runs in is a deployment decision the pass may not
	// make (item 9), so the block is named and its keys are not.
	"image":              "container sub-key; item 9 forbids adding a container block",
	"runtime":            "container sub-key; item 9 forbids adding a container block",
	"mount_agent_config": "container sub-key; item 9 forbids adding a container block",
	"network":            "container sub-key; item 9 forbids adding a container block",
	"extra_mounts":       "container sub-key; item 9 forbids adding a container block",
}

// pendingAudit are descriptor keys whose code has landed ahead of the skill
// and update-workflows text that names them, because a change that is
// delivered in units gives the skills and the built-ins to one audit step
// after every unit has merged. It is not an exemption: each entry fails the
// two tests below the moment the skill or the checklist names its key, so
// the audit that adds the text is also the edit that deletes the entry, and
// the set is empty again on master.
var pendingAudit = map[string]string{
	"hand_off": "task 137.15 (#848): the skills and built-ins are audited after the units land",
}

// pendingAuditFor reports whether key is still awaiting its audit, and fails
// the test when text already names it — the entry outlived its reason.
func pendingAuditFor(t *testing.T, text, where, key string) bool {
	t.Helper()
	if _, ok := pendingAudit[key]; !ok {
		return false
	}
	if namesKey(text, key) {
		t.Errorf("%s now names %q: delete its pendingAudit entry", where, key)
	}
	return true
}

// TestSkillNamesEveryAuthorableField (issue #376): create-workflow's header
// tells its agent that the skill's references/ are not on disk, so outside a
// vincent checkout SKILL.md is everything it knows about the schema. A key
// the skill never names is one create-workflow cannot author. No exemptions.
func TestSkillNamesEveryAuthorableField(t *testing.T) {
	var missing []string
	for _, key := range authorableKeys() {
		if pendingAuditFor(t, skills.VincentWorkflows, "skills/vincent-workflows/SKILL.md", key) {
			continue
		}
		if !namesKey(skills.VincentWorkflows, key) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Errorf("skills/vincent-workflows/SKILL.md never names %d authorable workflow key(s): %s",
			len(missing), strings.Join(missing, ", "))
	}
}

// TestUpdateWorkflowsChecklistNamesEveryField (issue #376, task 037 decision
// 5): "a feature missing from it is one the built-in will never propagate".
func TestUpdateWorkflowsChecklistNamesEveryField(t *testing.T) {
	keys := authorableKeys()
	known := map[string]bool{}
	for _, k := range keys {
		known[k] = true
	}
	for k := range checklistExempt {
		if !known[k] {
			t.Errorf("checklistExempt names %q, which the schema descriptor no longer offers", k)
		}
	}

	checklist := updateWorkflowsChecklist(t)
	var missing []string
	for _, key := range keys {
		if _, ok := checklistExempt[key]; ok {
			continue
		}
		if pendingAuditFor(t, checklist, "update-workflows' checklist", key) {
			continue
		}
		if !namesKey(checklist, key) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Errorf("update-workflows' checklist never names %d workflow feature key(s): %s",
			len(missing), strings.Join(missing, ", "))
	}
}
