package workflow

import (
	"strings"
	"testing"
)

// laneDAG is a fan_out whose lanes carry the given `needs:` lines.
func laneDAG(lanes string) string {
	return "name: root\nsteps:\n  - id: build\n    type: fan_out\n    lanes:\n" + lanes
}

func TestLaneNeedsRejectedAtLoad(t *testing.T) {
	cases := []struct {
		name  string
		lanes string
		want  string
	}{
		{
			name: "unknown id",
			lanes: "      - { id: api, workflow: impl }\n" +
				"      - { id: wire, workflow: impl, needs: [api, nope] }\n",
			want: `needs "nope"`,
		},
		{
			name:  "self edge",
			lanes: "      - { id: api, workflow: impl, needs: [api] }\n",
			want:  "needs itself",
		},
		{
			name: "cycle",
			lanes: "      - { id: a, workflow: impl, needs: [b] }\n" +
				"      - { id: b, workflow: impl, needs: [a] }\n",
			want: "lane dependency cycle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Parse([]byte(laneDAG(tc.lanes)), Options{})
			if err == nil {
				t.Fatalf("a %s was accepted at load", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestLaneNeedsAcceptsADAG(t *testing.T) {
	src := laneDAG("      - { id: api, workflow: impl }\n" +
		"      - { id: db, workflow: impl }\n" +
		"      - { id: wire, workflow: impl, needs: [api, db] }\n")
	wf, _, err := Parse([]byte(src), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	waves := LaneWaves(wf.Steps[0].Lanes)
	for id, want := range map[string]int{"api": 0, "db": 0, "wire": 1} {
		if waves[id] != want {
			t.Errorf("wave of %q = %d, want %d", id, waves[id], want)
		}
	}
}

// A `needs:` may be written as a scalar, which is what a derived list's
// `'{{ .Item.needs }}'` is, and both spellings decode to the same slice.
func TestLaneNeedsAcceptsAScalar(t *testing.T) {
	src := laneDAG("      - { id: api, workflow: impl }\n" +
		"      - { id: wire, workflow: impl, needs: api }\n")
	wf, _, err := Parse([]byte(src), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := wf.Steps[0].Lanes[1].Needs
	if len(got) != 1 || got[0] != "api" {
		t.Errorf("needs = %v, want [api]", got)
	}
}

func TestDerivedFanOutShape(t *testing.T) {
	cases := []struct {
		name string
		step string
		want string
	}{
		{
			name: "lane without for_each",
			step: "  - id: build\n    type: fan_out\n    lane:\n      id: '{{ .Item.id }}'\n      workflow: impl\n",
			want: "needs for_each:",
		},
		{
			name: "for_each without lane",
			step: "  - id: build\n    type: fan_out\n    for_each: '{{ .Steps.plan.Result }}'\n" +
				"    lanes:\n      - { id: api, workflow: impl }\n",
			want: "needs a lane: template",
		},
		{
			name: "neither",
			step: "  - id: build\n    type: fan_out\n",
			want: "require at least one lane",
		},
		{
			name: "max_lanes below one",
			step: "  - id: build\n    type: fan_out\n    max_lanes: 0\n" +
				"    lanes:\n      - { id: api, workflow: impl }\n",
			want: "max_lanes must be at least 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Parse([]byte("name: root\nsteps:\n"+tc.step), Options{})
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestDerivedFanOutAccepted(t *testing.T) {
	src := "name: root\nsteps:\n" +
		"  - id: build\n    type: fan_out\n    for_each: '{{ .Steps.plan.Result }}'\n" +
		"    max_lanes: 8\n" +
		"    lane:\n      id: '{{ .Item.id }}'\n      needs: '{{ .Item.needs }}'\n      workflow: impl\n"
	wf, _, err := Parse([]byte(src), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if wf.Steps[0].Lane == nil || wf.Steps[0].Lane.Workflow != "impl" {
		t.Fatalf("lane template = %+v, want one naming workflow impl", wf.Steps[0].Lane)
	}
	if len(wf.Steps[0].ForEach) != 1 {
		t.Errorf("for_each = %v, want one entry", wf.Steps[0].ForEach)
	}
}

func TestSplitNeeds(t *testing.T) {
	cases := map[string][]string{
		"api":          {"api"},
		"[api db]":     {"api", "db"},
		"api, db":      {"api", "db"},
		"[]":           nil,
		"":             nil,
		"api\ndb\n":    {"api", "db"},
		`["api" "db"]`: {"api", "db"},
	}
	for in, want := range cases {
		got := SplitNeeds(in)
		if len(got) != len(want) {
			t.Errorf("SplitNeeds(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("SplitNeeds(%q) = %v, want %v", in, got, want)
				break
			}
		}
	}
}

// .Item is the one place §8.4's plain-string rule gives way, and it must not
// leak: the ordinary render context has no Item at all.
func TestRenderLaneItem(t *testing.T) {
	rc := RenderContext{Task: TaskContext{Title: "t"}}
	got, err := RenderLane("lane.id", "{{ .Item.id }}-{{ .Task.Title }}", rc,
		map[string]any{"id": "api"})
	if err != nil {
		t.Fatalf("RenderLane: %v", err)
	}
	if got != "api-t" {
		t.Errorf("rendered %q, want %q", got, "api-t")
	}
	if _, err := Render("prompt", "{{ .Item.id }}", rc); err == nil {
		t.Error("`.Item` rendered outside a lane template; the widening must stay scoped")
	}
}

// TestFanOutScheduleAtLoad covers the field's whole load-time contract (task
// 081): both values, the absent default, an unknown value rejected with a
// message naming the two, and `schedule:` on a step that is not a fan_out.
func TestFanOutScheduleAtLoad(t *testing.T) {
	lanes := "    lanes:\n      - { id: api, workflow: impl }\n"
	accepted := map[string]string{
		"barrier": "    schedule: barrier\n",
		"eager":   "    schedule: eager\n",
		"absent":  "",
	}
	want := map[string]string{"barrier": ScheduleBarrier, "eager": ScheduleEager, "absent": ScheduleBarrier}
	for name, line := range accepted {
		t.Run(name, func(t *testing.T) {
			src := "name: root\nsteps:\n  - id: build\n    type: fan_out\n" + line + lanes
			wf, _, err := Parse([]byte(src), Options{})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := wf.Steps[0].ScheduleMode(); got != want[name] {
				t.Errorf("ScheduleMode() = %q, want %q", got, want[name])
			}
		})
	}

	t.Run("unknown value", func(t *testing.T) {
		src := "name: root\nsteps:\n  - id: build\n    type: fan_out\n    schedule: asap\n" + lanes
		_, _, err := Parse([]byte(src), Options{})
		if err == nil {
			t.Fatal("schedule: asap was accepted")
		}
		for _, want := range []string{ScheduleBarrier, ScheduleEager, "asap"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to mention %q", err, want)
			}
		}
	})

	t.Run("not a fan_out", func(t *testing.T) {
		src := "name: root\nsteps:\n  - id: build\n    type: command\n    run: echo hi\n    schedule: eager\n"
		_, _, err := Parse([]byte(src), Options{})
		if err == nil {
			t.Fatal("schedule: on a command step was accepted")
		}
		if !strings.Contains(err.Error(), "schedule is not valid on a command step") {
			t.Errorf("error = %v, want it to reject schedule on a command step", err)
		}
	})
}

// TestFanOutScheduleSurvivesAMarshalRoundTrip: a step that names no schedule
// marshals without the key, so a snapshot written before the field existed is
// byte-for-byte what it was.
func TestFanOutScheduleSurvivesAMarshalRoundTrip(t *testing.T) {
	src := "name: root\nsteps:\n  - id: build\n    type: fan_out\n" +
		"    lanes:\n      - { id: api, workflow: impl }\n"
	wf, _, err := Parse([]byte(src), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Marshal(wf)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(out), "schedule") {
		t.Errorf("a step naming no schedule marshalled one:\n%s", out)
	}
	wf.Steps[0].Schedule = ScheduleEager
	out, err = Marshal(wf)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), "schedule: eager") {
		t.Errorf("an eager step did not marshal its schedule:\n%s", out)
	}
}

// The snapshot-only provenance field (§7.6, task 080 decision 5 as amended).
//
// `derived_from:` is the record a materialized fan-out keeps of the
// `lane:`/`for_each:` pair that produced its lanes. Three properties make it
// safe, and all three are load-time facts:

// A materialized snapshot re-validates. It is re-parsed on every later
// admission (§5.3), so a `lanes:` list beside a derivation record has to be
// accepted where a `lanes:` list beside a live `lane:` template is refused.
func TestMaterializedSnapshotValidatesWithItsProvenance(t *testing.T) {
	src := []byte(`name: root
steps:
  - id: spread
    type: fan_out
    lanes:
      - id: api
        steps:
          - id: work
            type: command
            run: go test ./...
      - id: wire
        needs: [api]
        steps:
          - id: work
            type: command
            run: go test ./...
    derived_from:
      lane: '{{ .Item.id }}'
      for_each: '{{ .Steps.plan.Result }}'
`)
	wf, _, err := Parse(src, Options{})
	if err != nil {
		t.Fatalf("a materialized snapshot must re-validate: %v", err)
	}
	got := wf.Steps[0].DerivedFrom
	if got == nil {
		t.Fatal("derived_from did not survive the round trip")
	}
	if got.Lane != "{{ .Item.id }}" || len(got.ForEach) != 1 {
		t.Errorf("derived_from = %+v, want the lane and for_each templates it was written with", *got)
	}
	// And it survives being marshalled back out, which is the shape the
	// snapshot is stored in.
	out, merr := Marshal(wf)
	if merr != nil {
		t.Fatalf("Marshal: %v", merr)
	}
	if !strings.Contains(string(out), "derived_from:") {
		t.Errorf("the re-encoded snapshot dropped its provenance:\n%s", out)
	}
}

// A hand-written document carrying it is refused the way an unknown key is.
// Strict decoding cannot say so — the key has to decode out of a snapshot —
// so the refusal lands in validation, and only for a document somebody wrote.
func TestDerivedFromIsNotAuthorable(t *testing.T) {
	src := []byte(`name: root
steps:
  - id: spread
    type: fan_out
    lanes:
      - id: api
        steps:
          - id: work
            type: command
            run: go test ./...
    derived_from:
      for_each: '{{ .Steps.plan.Result }}'
`)
	_, _, err := Parse(src, Options{Authored: true})
	if err == nil {
		t.Fatal("an authored document carrying derived_from was accepted")
	}
	var errs Errors
	if !asErrors(err, &errs) {
		t.Fatalf("Parse returned %T, want Errors", err)
	}
	var found bool
	for _, e := range errs {
		if e.Path == "steps[0].derived_from" && strings.Contains(e.Message, "unknown field") {
			found = true
		}
	}
	if !found {
		t.Errorf("findings %v name no unknown field at steps[0].derived_from", errs)
	}
}

// The registry is where authored documents enter — a file on disk, or a
// candidate POSTed to be judged exactly as a file would be — so it is the
// registry that carries the refusal to every one of those surfaces.
func TestRegistryParsesAsAuthored(t *testing.T) {
	if !NewRegistry(t.TempDir(), Options{}, nil).Options().Authored {
		t.Error("the registry judges files as authored documents; " +
			"without that, POST /v1/workflows/validate accepts a snapshot-only field")
	}
}
