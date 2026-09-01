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
