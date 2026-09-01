package workflow

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
)

// setStepField sets one yaml-named field of a Step to a non-zero value, so
// rejectFields sees it as present. It reports false for a field Step does not
// carry, which is itself a drift the tests below fail on.
func setStepField(step *Step, name string) bool {
	v := reflect.ValueOf(step).Elem()
	t := v.Type()
	for i := range t.NumField() {
		tag := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
		if tag != name {
			continue
		}
		f := v.Field(i)
		switch f.Interface().(type) {
		case string:
			f.SetString("x")
		case bool:
			f.SetBool(true)
		case *int:
			n := 1
			f.Set(reflect.ValueOf(&n))
		case *config.Duration:
			d := config.Duration(1)
			f.Set(reflect.ValueOf(&d))
		case map[string]string:
			f.Set(reflect.ValueOf(map[string]string{"K": "v"}))
		case []Step:
			f.Set(reflect.ValueOf([]Step{{ID: "s", Type: StepCommand, Run: "x"}}))
		case []Lane:
			f.Set(reflect.ValueOf([]Lane{{ID: "l", Workflow: "w"}}))
		case *Lane:
			f.Set(reflect.ValueOf(&Lane{ID: "l", Workflow: "w"}))
		case LaneNeeds:
			f.Set(reflect.ValueOf(LaneNeeds{"a"}))
		case *Merge:
			f.Set(reflect.ValueOf(&Merge{OnConflict: ConflictBlock}))
		case ForEach:
			f.Set(reflect.ValueOf(ForEach{"a"}))
		case []string:
			f.Set(reflect.ValueOf([]string{"a"}))
		default:
			return false
		}
		return true
	}
	return false
}

// rejected reports whether validateStep refuses field on a step of this type,
// which is the §8.2 table this package's schema descriptor claims to mirror.
func rejected(typ, field string) bool {
	step := Step{Type: typ}
	if !setStepField(&step, field) {
		return false
	}
	var refused bool
	validateStep(step, "steps[0]", Options{}, func(path, format string, _ ...any) {
		if path == "steps[0]."+field && strings.Contains(format, "is not valid on a") {
			refused = true
		}
	})
	return refused
}

// TestSchemaMatchesValidation is the drift test of task 065 decision 3. Every
// field the descriptor offers for a type must survive that type's
// rejectFields call, and every field it withholds must be refused by it — so
// a field added to Parse and not to the descriptor, or offered where it is
// illegal, fails here rather than as a 400 under a form.
func TestSchemaMatchesValidation(t *testing.T) {
	every := map[string]bool{}
	for _, f := range commonStepFields() {
		every[f.Name] = true
	}
	for _, s := range SchemaDescriptor().Steps {
		for _, f := range s.Fields {
			every[f.Name] = true
		}
	}
	for _, s := range SchemaDescriptor().Steps {
		offered := map[string]bool{}
		for _, name := range s.Common {
			offered[name] = true
		}
		for _, f := range s.Fields {
			offered[f.Name] = true
		}
		for name := range every {
			if got := rejected(s.Type, name); got == offered[name] {
				verb := "offers"
				if !offered[name] {
					verb = "withholds"
				}
				t.Errorf("schema %s %q for a %s step, but validateStep rejects=%v",
					verb, name, s.Type, got)
			}
		}
	}
}

// TestSchemaCoversEveryStepField fails when a yaml field is added to Step and
// not to the descriptor: the forms are generated from the descriptor, so an
// unlisted field is one no client can ever set.
func TestSchemaCoversEveryStepField(t *testing.T) {
	known := map[string]bool{
		// `type` is the discriminator rather than a row of one type's form:
		// a client fills it from StepTypesFor, which is the context table.
		"type": true,
		// resolved_from is written by Expand at task creation, never by
		// hand, and validateStep refuses a hand-written one (task 019
		// decision 6). It is deliberately unauthorable.
		"resolved_from": true,
	}
	for _, f := range commonStepFields() {
		known[f.Name] = true
	}
	for _, s := range SchemaDescriptor().Steps {
		for _, f := range s.Fields {
			known[f.Name] = true
		}
	}
	t.Run("step", func(t *testing.T) {
		assertFieldsCovered(t, reflect.TypeOf(Step{}), known)
	})
	t.Run("workflow", func(t *testing.T) {
		top := map[string]bool{}
		for _, f := range SchemaDescriptor().TopLevel {
			top[f.Name] = true
		}
		assertFieldsCovered(t, reflect.TypeOf(Workflow{}), top)
	})
	t.Run("defaults", func(t *testing.T) {
		def := map[string]bool{}
		for _, f := range SchemaDescriptor().Defaults {
			def[f.Name] = true
		}
		assertFieldsCovered(t, reflect.TypeOf(Defaults{}), def)
	})
	t.Run("lane", func(t *testing.T) {
		lane := map[string]bool{"resolved_from": true}
		for _, f := range SchemaDescriptor().Lane {
			lane[f.Name] = true
		}
		assertFieldsCovered(t, reflect.TypeOf(Lane{}), lane)
	})
	t.Run("merge", func(t *testing.T) {
		merge := map[string]bool{}
		for _, f := range SchemaDescriptor().Merge {
			merge[f.Name] = true
		}
		assertFieldsCovered(t, reflect.TypeOf(Merge{}), merge)
	})
	t.Run("field", func(t *testing.T) {
		fields := map[string]bool{}
		for _, f := range SchemaDescriptor().Field {
			fields[f.Name] = true
		}
		assertFieldsCovered(t, reflect.TypeOf(FieldDefinition{}), fields)
	})
	// `defaults.container` is `config.ContainerOverride` (§8.6, task 061), so
	// a key added there is a key the forms would silently not offer.
	t.Run("container", func(t *testing.T) {
		container := map[string]bool{}
		for _, f := range SchemaDescriptor().Container {
			container[f.Name] = true
		}
		assertFieldsCovered(t, reflect.TypeOf(config.ContainerOverride{}), container)
	})
}

func assertFieldsCovered(t *testing.T, typ reflect.Type, known map[string]bool) {
	t.Helper()
	for i := range typ.NumField() {
		tag := strings.Split(typ.Field(i).Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if !known[tag] {
			t.Errorf("%s.%s is not in the served schema descriptor", typ.Name(), tag)
		}
	}
}

// TestStepTypesForMatchesNestingRules pins the context table against
// validateSubStep and validateBody: a type a context forbids must not be
// offered there, and `break` must be offered only inside a loop.
func TestStepTypesForMatchesNestingRules(t *testing.T) {
	inParallel := map[string]bool{}
	for _, typ := range StepTypesFor(ContextParallel) {
		inParallel[typ] = true
	}
	for _, typ := range []string{StepManual, StepParallel, StepFanOut, StepCondition, StepLoop, StepBreak} {
		if inParallel[typ] {
			t.Errorf("%s is offered inside a parallel group but validateSubStep refuses it", typ)
		}
	}
	for _, typ := range []string{StepAgent, StepCommand} {
		if !inParallel[typ] {
			t.Errorf("%s is legal inside a parallel group but is not offered there", typ)
		}
	}
	for _, ctx := range []string{ContextBody, ContextParallel, ContextMerge} {
		for _, typ := range StepTypesFor(ctx) {
			if typ == StepBreak {
				t.Errorf("break is offered in the %s context; it is only valid inside a loop body", ctx)
			}
		}
	}
	inLoop := map[string]bool{}
	for _, typ := range StepTypesFor(ContextLoop) {
		inLoop[typ] = true
	}
	for _, typ := range []string{StepManual, StepFanOut, StepParallel, StepLoop} {
		if inLoop[typ] {
			t.Errorf("%s is offered inside a loop body but validateBodyStep refuses it", typ)
		}
	}
	if !inLoop[StepBreak] {
		t.Error("break is not offered inside a loop body, which is the only place it is valid")
	}
	if got := StepTypesFor(ContextMerge); len(got) != 1 || got[0] != StepAgent {
		t.Errorf("merge resolver types = %v, want just %v", got, []string{StepAgent})
	}
	if _, ok := SchemaStep(StepAgent); !ok {
		t.Fatal("SchemaStep(agent) missing")
	}
	if _, ok := SchemaStep("nope"); ok {
		t.Fatal("SchemaStep accepted an unknown type")
	}
}

// TestSchemaCoversEveryStepType fails when a step type is added to StepTypes
// and not to the descriptor — a type without a form is a type the editor
// silently cannot write.
func TestSchemaCoversEveryStepType(t *testing.T) {
	have := map[string]bool{}
	for _, s := range SchemaDescriptor().Steps {
		have[s.Type] = true
	}
	for _, typ := range StepTypes {
		if !have[typ] {
			t.Errorf("step type %q has no schema entry", typ)
		}
	}
	if len(have) != len(StepTypes) {
		t.Errorf("schema declares %d step types, StepTypes has %d", len(have), len(StepTypes))
	}
}
