package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// expandOpts is Expand's options with the parts every test wants the same:
// a generous depth and a pinned platform, so a test asserting about ids or
// defaults never fails because of the host it ran on.
func expandOpts(lookup LookupFunc) ExpandOptions {
	return ExpandOptions{Lookup: lookup, Limits: IncludeLimits{MaxDepth: 5}, Platform: "linux"}
}

func stepIDsOf(steps []Step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.ID
	}
	return out
}

// TestExpandSplicesCalleeSteps is the whole feature: the include is gone and
// the callee's steps stand in its place, at the caller's own level.
func TestExpandSplicesCalleeSteps(t *testing.T) {
	lookup := registry(t, map[string]string{
		"checks": "name: checks\nsteps:\n" +
			"  - {id: lint, type: command, run: make lint}\n" +
			"  - {id: test, type: command, run: make test}\n",
	})
	root := mustParse(t, `
name: root
steps:
  - {id: implement, type: agent, prompt: go}
  - {id: verify, type: include, workflow: checks}
  - {id: review, type: agent, prompt: done}
`)
	got, err := Expand(root, expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := []string{"implement", "lint", "test", "review"}
	if ids := stepIDsOf(got.Steps); strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v, want %v", ids, want)
	}
	for _, s := range got.Steps {
		if s.Type == StepInclude {
			t.Fatal("an include survived expansion")
		}
	}
	if len(root.Steps) != 3 {
		t.Error("Expand mutated its input")
	}
}

// TestExpandRecordsProvenanceChain is decision 6: a step spliced through two
// includes says so, outermost first, and a step the caller wrote itself
// carries nothing.
func TestExpandRecordsProvenanceChain(t *testing.T) {
	lookup := registry(t, map[string]string{
		"outer": "name: outer\nsteps:\n" +
			"  - {id: before, type: command, run: make a}\n" +
			"  - {id: deep, type: include, workflow: inner}\n",
		"inner": "name: inner\nsteps:\n  - {id: leaf, type: command, run: make b}\n",
	})
	got, err := Expand(mustParse(t, `
name: root
steps:
  - {id: own, type: command, run: make own}
  - {id: call, type: include, workflow: outer}
`), expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := map[string]string{"own": "", "before": "outer", "leaf": "outer,inner"}
	for _, s := range got.Steps {
		if chain := strings.Join(s.ResolvedFrom, ","); chain != want[s.ID] {
			t.Errorf("%s resolved_from = %q, want %q", s.ID, chain, want[s.ID])
		}
	}
}

// TestExpandRejectsDuplicateStepID is decision 5. The collision is reported
// against the include that introduced the second use, naming where the id was
// already taken — the alternative was prefixing ids, which would mean
// rewriting the callee's own templates.
func TestExpandRejectsDuplicateStepID(t *testing.T) {
	lookup := registry(t, map[string]string{
		"checks": "name: checks\nsteps:\n  - {id: build, type: command, run: make}\n",
	})
	_, err := Expand(mustParse(t, `
name: root
steps:
  - {id: build, type: command, run: make first}
  - {id: verify, type: include, workflow: checks}
`), expandOpts(lookup))
	if err == nil {
		t.Fatal("Expand accepted a duplicate step id")
	}
	// Both sides are named in the words the author would use: after a splice,
	// `steps[4]` points at nothing anyone wrote, while "from checks" does.
	if !strings.Contains(err.Error(), `"build"`) ||
		!strings.Contains(err.Error(), `in "root" itself`) ||
		!strings.Contains(err.Error(), `included workflow "checks"`) {
		t.Errorf("error does not name both sides: %v", err)
	}
}

// TestExpandLaneIDsAreTheirOwnNamespace: a lane becomes a separate child task
// with its own flat snapshot (§8.2), so an id it shares with the root is two
// tasks and no collision.
func TestExpandLaneIDsAreTheirOwnNamespace(t *testing.T) {
	lookup := registry(t, map[string]string{
		"checks": "name: checks\nsteps:\n  - {id: build, type: command, run: make}\n",
	})
	if _, err := Expand(mustParse(t, `
name: root
steps:
  - {id: build, type: command, run: make first}
  - id: spread
    type: fan_out
    lanes:
      - {id: one, steps: [{id: verify, type: include, workflow: checks}]}
`), expandOpts(lookup)); err != nil {
		t.Fatalf("Expand rejected a lane reusing a root id: %v", err)
	}
}

// TestExpandDetectsCycles: A including B while B includes A never terminates,
// and no bound would report it usefully. The message names the path.
func TestExpandDetectsCycles(t *testing.T) {
	lookup := registry(t, map[string]string{
		"a": "name: a\nsteps:\n  - {id: tob, type: include, workflow: b}\n",
		"b": "name: b\nsteps:\n  - {id: toa, type: include, workflow: a}\n",
	})
	_, err := Expand(mustParse(t, "name: a\nsteps:\n  - {id: tob, type: include, workflow: b}\n"),
		expandOpts(lookup))
	if err == nil {
		t.Fatal("Expand accepted a cycle")
	}
	if !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "a → b → a") {
		t.Errorf("cycle error does not name the path: %v", err)
	}
}

// TestExpandEnforcesMaxDepth: depth is unlimited by design and bounded by
// config, so a deeper chain is a config edit and not a code change.
func TestExpandEnforcesMaxDepth(t *testing.T) {
	lookup := registry(t, map[string]string{
		"one":   "name: one\nsteps:\n  - {id: c2, type: include, workflow: two}\n",
		"two":   "name: two\nsteps:\n  - {id: c3, type: include, workflow: three}\n",
		"three": "name: three\nsteps:\n  - {id: leaf, type: command, run: make}\n",
	})
	root := mustParse(t, "name: root\nsteps:\n  - {id: c1, type: include, workflow: one}\n")
	opts := expandOpts(lookup)
	if _, err := Expand(root, opts); err != nil {
		t.Fatalf("depth 3 rejected under max_depth 5: %v", err)
	}
	opts.Limits.MaxDepth = 2
	_, err := Expand(root, opts)
	if err == nil {
		t.Fatal("Expand accepted an expansion past max_depth")
	}
	if !strings.Contains(err.Error(), "include.max_depth") {
		t.Errorf("error does not name the bound: %v", err)
	}
}

// TestExpandUnknownWorkflow: a name this project cannot see is a 400 in front
// of the person typing, not a step that fails six hours in.
func TestExpandUnknownWorkflow(t *testing.T) {
	_, err := Expand(mustParse(t, "name: root\nsteps:\n  - {id: c, type: include, workflow: nope}\n"),
		expandOpts(registry(t, nil)))
	if err == nil || !strings.Contains(err.Error(), `"nope" not found`) {
		t.Fatalf("error = %v, want a not-found naming the workflow", err)
	}
}

// TestExpandRejectsUnsupportedPlatform is decision 8: §8.1.1 travels with the
// steps, so a caller splicing in a POSIX-only fragment cannot run here either.
func TestExpandRejectsUnsupportedPlatform(t *testing.T) {
	lookup := registry(t, map[string]string{
		"posix": "name: posix\nplatforms: [posix]\nsteps:\n  - {id: sh, type: command, run: make}\n",
	})
	root := mustParse(t, "name: root\nsteps:\n  - {id: c, type: include, workflow: posix}\n")
	opts := expandOpts(lookup)
	opts.Platform = "windows"
	if _, err := Expand(root, opts); err == nil ||
		!strings.Contains(err.Error(), "does not run on windows") {
		t.Fatalf("error = %v, want a platform refusal", err)
	}
	opts.Platform = "linux"
	if _, err := Expand(root, opts); err != nil {
		t.Errorf("Expand rejected a fragment this platform supports: %v", err)
	}
}

// TestExpandMaterialisesCalleeDefaults is decision 7's core: a fragment keeps
// the behaviour it was written with rather than silently adopting its
// caller's.
func TestExpandMaterialisesCalleeDefaults(t *testing.T) {
	lookup := registry(t, map[string]string{
		"strict": "name: strict\ndefaults: {max_retries: 0, timeout: 30m, retry_backoff: 45s}\n" +
			"steps:\n  - {id: once, type: command, run: make}\n" +
			"  - {id: pinned, type: command, run: make two, timeout: 5m, retry_backoff: 5s}\n",
	})
	got, err := Expand(mustParse(t, `
name: root
defaults: {max_retries: 3, retry_backoff: 10m}
steps:
  - {id: c, type: include, workflow: strict}
`), expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	once := got.Steps[0]
	if once.MaxRetries == nil || *once.MaxRetries != 0 {
		t.Errorf("max_retries = %v, want the callee's 0 rather than the caller's 3", once.MaxRetries)
	}
	if once.Timeout == nil || once.Timeout.Std() != 30*60*1e9 {
		t.Errorf("timeout = %v, want the callee's 30m", once.Timeout)
	}
	// `retry_backoff` is an inheritable field like every other one (task 028):
	// the callee's pacing travels with its steps rather than the caller's.
	if once.RetryBackoff == nil || once.RetryBackoff.Std() != 45*time.Second {
		t.Errorf("retry_backoff = %v, want the callee's 45s rather than the caller's 10m", once.RetryBackoff)
	}
	// A step that set the field itself outranks the defaults that would have
	// filled it, which is §8.6's order and not a special case.
	pinned := got.Steps[1]
	if pinned.Timeout == nil || pinned.Timeout.Std() != 5*60*1e9 {
		t.Errorf("pinned timeout = %v, want its own 5m", pinned.Timeout)
	}
	if pinned.RetryBackoff == nil || pinned.RetryBackoff.Std() != 5*time.Second {
		t.Errorf("pinned retry_backoff = %v, want its own 5s", pinned.RetryBackoff)
	}
}

// TestExpandTaskOverrideOutranksCalleeDefaults is the other half of decision
// 7: the value a human just typed beats a fragment's default, which is what
// materialising has to avoid inverting by writing a default into a step field.
func TestExpandTaskOverrideOutranksCalleeDefaults(t *testing.T) {
	lookup := registry(t, map[string]string{
		"fixed": "name: fixed\ndefaults: {agent: codex}\n" +
			"steps:\n  - {id: think, type: agent, prompt: go}\n",
	})
	opts := expandOpts(lookup)
	opts.Override = agent.Level{Agent: "claude"}
	got, err := Expand(mustParse(t, "name: root\nsteps:\n  - {id: c, type: include, workflow: fixed}\n"), opts)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if a := got.Steps[0].Agent; a != "claude" {
		t.Errorf("agent = %q, want the task override %q", a, "claude")
	}
}

// TestExpandLeavesAnUndeclaredTripleAlone keeps the common case free of baked
// values: with nothing to contribute, the run-time resolve reaches the same
// answer from the caller's defaults, so the snapshot should not pin one.
func TestExpandLeavesAnUndeclaredTripleAlone(t *testing.T) {
	lookup := registry(t, map[string]string{
		"plain": "name: plain\nsteps:\n  - {id: think, type: agent, prompt: go}\n",
	})
	got, err := Expand(mustParse(t, "name: root\nsteps:\n  - {id: c, type: include, workflow: plain}\n"),
		expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if s := got.Steps[0]; s.Agent != "" || s.Model != "" || s.Effort != "" {
		t.Errorf("triple = (%q, %q, %q), want all empty", s.Agent, s.Model, s.Effort)
	}
}

// TestExpandNeverWritesRetriesOntoACondition: `timeout` and `max_retries`
// bind to an attempt, which a condition does not have — writing the callee's
// defaults onto one would produce a snapshot §8.2 rejects.
func TestExpandNeverWritesRetriesOntoACondition(t *testing.T) {
	lookup := registry(t, map[string]string{
		"guarded": "name: guarded\ndefaults: {max_retries: 2, timeout: 9m}\n" +
			"steps:\n  - {id: gate, type: condition, if: 'true'}\n" +
			"  - {id: work, type: command, run: make}\n",
	})
	got, err := Expand(mustParse(t, "name: root\nsteps:\n  - {id: c, type: include, workflow: guarded}\n"),
		expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if gate := got.Steps[0]; gate.MaxRetries != nil || gate.Timeout != nil {
		t.Fatalf("condition carries retries/timeout: %v %v", gate.MaxRetries, gate.Timeout)
	}
	// And the expansion still validates, which is the assertion that matters:
	// a snapshot §8.2 rejects would block the task on its first admission.
	out, err := Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, _, err := Parse(out, Options{}); err != nil {
		t.Fatalf("expanded snapshot does not re-validate: %v", err)
	}
}

// TestExpandInsideLoopAndGroupBodies is decision 9: an include may appear
// anywhere a step may, which is the payoff of splicing rather than nesting.
func TestExpandInsideLoopAndGroupBodies(t *testing.T) {
	lookup := registry(t, map[string]string{
		"one": "name: one\nsteps:\n  - {id: inner, type: command, run: make}\n",
		"two": "name: two\nsteps:\n  - {id: side, type: command, run: make two}\n",
	})
	got, err := Expand(mustParse(t, `
name: root
steps:
  - id: repeat
    type: loop
    count: 2
    steps: [{id: c1, type: include, workflow: one}]
  - id: both
    type: parallel
    steps: [{id: keep, type: command, run: make keep}, {id: c2, type: include, workflow: two}]
`), expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if ids := stepIDsOf(got.Steps[0].Steps); strings.Join(ids, ",") != "inner" {
		t.Errorf("loop body = %v, want [inner]", ids)
	}
	if ids := stepIDsOf(got.Steps[1].Steps); strings.Join(ids, ",") != "keep,side" {
		t.Errorf("group members = %v, want [keep side]", ids)
	}
}

// TestExpandInsideLaneSteps: a lane's steps become a child task's snapshot,
// and that child never consults the registry — so an include there has to be
// resolved at creation like every other one.
func TestExpandInsideLaneSteps(t *testing.T) {
	lookup := registry(t, map[string]string{
		"one": "name: one\nsteps:\n  - {id: inner, type: command, run: make}\n",
	})
	got, err := Expand(mustParse(t, `
name: root
steps:
  - id: spread
    type: fan_out
    lanes:
      - {id: only, steps: [{id: c, type: include, workflow: one}]}
`), expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	lane := got.Steps[0].Lanes[0]
	if ids := stepIDsOf(lane.Steps); strings.Join(ids, ",") != "inner" {
		t.Errorf("lane steps = %v, want [inner]", ids)
	}
}

// TestExpandNestedDefaultsNearestWins: a step that came through two includes
// keeps the *nearest* enclosing workflow's defaults, and the outer one fills
// only what is still unset.
func TestExpandNestedDefaultsNearestWins(t *testing.T) {
	lookup := registry(t, map[string]string{
		"outer": "name: outer\ndefaults: {max_retries: 5, timeout: 1h}\n" +
			"steps:\n  - {id: deep, type: include, workflow: inner}\n",
		"inner": "name: inner\ndefaults: {max_retries: 0}\n" +
			"steps:\n  - {id: leaf, type: command, run: make}\n",
	})
	got, err := Expand(mustParse(t, "name: root\nsteps:\n  - {id: c, type: include, workflow: outer}\n"),
		expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	leaf := got.Steps[0]
	if leaf.MaxRetries == nil || *leaf.MaxRetries != 0 {
		t.Errorf("max_retries = %v, want the nearest callee's 0", leaf.MaxRetries)
	}
	if leaf.Timeout == nil || leaf.Timeout.Std() != 60*60*1e9 {
		t.Errorf("timeout = %v, want the outer callee's 1h filling what inner left unset", leaf.Timeout)
	}
}

// TestIncludeWarnings is decision 8's load-time half: a warning, because
// shadowing decides which files even participate and a project workflow may
// shadow the very name that closed the loop — or the one that was missing.
func TestIncludeWarnings(t *testing.T) {
	lookup := registry(t, map[string]string{
		"b": "name: b\nsteps:\n  - {id: toa, type: include, workflow: a}\n",
	})
	root := mustParse(t, "name: a\nsteps:\n  - {id: tob, type: include, workflow: b}\n")
	warns := IncludeWarnings(root, lookup)
	if len(warns) != 1 || !strings.Contains(warns[0], "cycle") {
		t.Fatalf("warnings = %v, want one cycle warning", warns)
	}
	missing := mustParse(t, "name: c\nsteps:\n  - {id: x, type: include, workflow: gone}\n")
	if w := IncludeWarnings(missing, lookup); len(w) != 1 || !strings.Contains(w[0], "not found") {
		t.Errorf("warnings for an unresolvable include = %v, want one not-found", w)
	}
	clean := mustParse(t, "name: c\nsteps:\n  - {id: x, type: command, run: make}\n")
	if w := IncludeWarnings(clean, lookup); len(w) != 0 {
		t.Errorf("warnings on an include-free workflow = %v", w)
	}
}

// TestIncludeNames is what GET /v1/workflows reports: the direct dependencies,
// without resolving anything.
func TestIncludeNames(t *testing.T) {
	wf := mustParse(t, `
name: root
steps:
  - {id: a, type: include, workflow: checks}
  - id: loop
    type: loop
    count: 2
    steps: [{id: b, type: include, workflow: deploy}, {id: c, type: include, workflow: checks}]
`)
	if got := strings.Join(IncludeNames(wf), ","); got != "checks,deploy" {
		t.Errorf("IncludeNames = %q, want %q", got, "checks,deploy")
	}
	if got := IncludeNames(mustParse(t, "name: n\nsteps:\n  - {id: x, type: command, run: make}\n")); got != nil {
		t.Errorf("IncludeNames on an include-free workflow = %v", got)
	}
}

// TestHasInclude guards the fast path task creation takes for the workflows
// that are the overwhelming majority.
func TestHasInclude(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"none", "name: n\nsteps:\n  - {id: x, type: command, run: make}\n", false},
		{"top level", "name: n\nsteps:\n  - {id: x, type: include, workflow: c}\n", true},
		{"loop body", "name: n\nsteps:\n  - {id: l, type: loop, count: 1, steps: [{id: x, type: include, workflow: c}]}\n", true},
		{"lane", "name: n\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: o, steps: [{id: x, type: include, workflow: c}]}]}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasInclude(mustParse(t, tc.src)); got != tc.want {
				t.Errorf("HasInclude = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIncludeStepFieldValidation is decision 11: an include is resolved away
// at creation, so it owns no attempt and nothing that binds to one — `if:`
// included, which is the rejection with a real alternative behind it.
func TestIncludeStepFieldValidation(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{
			"workflow required",
			"name: n\nsteps:\n  - {id: c, type: include}\n",
			"include steps require the name",
		},
		{
			"if rejected",
			"name: n\nsteps:\n  - {id: c, type: include, workflow: x, if: 'true'}\n",
			"if is not valid on a include step",
		},
		{
			"timeout rejected",
			"name: n\nsteps:\n  - {id: c, type: include, workflow: x, timeout: 5m}\n",
			"timeout is not valid on a include step",
		},
		{
			"retry_backoff rejected",
			"name: n\nsteps:\n  - {id: c, type: include, workflow: x, retry_backoff: 30s}\n",
			"retry_backoff is not valid on a include step",
		},
		{
			"workflow rejected elsewhere",
			"name: n\nsteps:\n  - {id: c, type: command, run: make, workflow: x}\n",
			"workflow is not valid on a command step",
		},
		{
			"resolved_from is machine-written",
			"name: n\nsteps:\n  - {id: c, type: include, workflow: x, resolved_from: [y]}\n",
			"resolved_from is set by task creation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Parse([]byte(tc.src), Options{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// TestIncludeStepAccepted keeps the positive case honest: the four fields an
// include may carry, and nothing else, parse and validate.
func TestIncludeStepAccepted(t *testing.T) {
	if _, _, err := Parse([]byte(
		"name: n\nsteps:\n  - {id: c, name: Shared checks, type: include, workflow: checks}\n"),
		Options{}); err != nil {
		t.Fatalf("a well-formed include did not validate: %v", err)
	}
}

// TestExpandReportsACycleAheadOfTheDepthBound: a cycle crosses any bound
// eventually, so whichever check runs first decides what the person sees.
// "past include.max_depth" invites raising a limit that will never help.
func TestExpandReportsACycleAheadOfTheDepthBound(t *testing.T) {
	lookup := registry(t, map[string]string{
		"b": "name: b\nsteps:\n  - {id: back, type: include, workflow: a}\n",
	})
	opts := expandOpts(lookup)
	opts.Limits.MaxDepth = 1
	_, err := Expand(mustParse(t, "name: a\nsteps:\n  - {id: to, type: include, workflow: b}\n"), opts)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error = %v, want the cycle rather than the bound it also crosses", err)
	}
}
