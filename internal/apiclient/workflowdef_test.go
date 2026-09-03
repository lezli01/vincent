package apiclient

import (
	"encoding/json"
	"testing"
)

// The step DTO is a hand-written mirror of a hand-written server DTO, so the
// fields the graph draws from are pinned against the JSON the daemon writes:
// a lane's `needs:`, a fan_out's `schedule:`, and the `derived_from:` a task's
// snapshot carries once its fan-out has rendered its lanes (§7.6, tasks 080
// and 081). The live tests hold the whole envelope; this holds the three keys
// the picture would silently lose.
func TestStepDefDecodesTheLaneDAG(t *testing.T) {
	const wire = `{
	  "id": "spread",
	  "type": "fan_out",
	  "schedule": "eager",
	  "lanes": [
	    {"id": "api"},
	    {"id": "wire", "needs": ["api"]}
	  ],
	  "derived_from": {
	    "lane": "{{ .Item.id }}",
	    "for_each": ["{{ .Steps.plan.Result }}"]
	  }
	}`
	var got WorkflowStepDef
	if err := json.Unmarshal([]byte(wire), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Schedule != "eager" {
		t.Errorf("schedule = %q", got.Schedule)
	}
	if len(got.Lanes) != 2 || len(got.Lanes[1].Needs) != 1 || got.Lanes[1].Needs[0] != "api" {
		t.Errorf("lanes = %+v, want the second one needing the first", got.Lanes)
	}
	if got.DerivedFrom == nil {
		t.Fatal("derived_from did not decode; the graph cannot tell a derived list from an authored one")
	}
	if got.DerivedFrom.Lane != "{{ .Item.id }}" || len(got.DerivedFrom.ForEach) != 1 {
		t.Errorf("derived_from = %+v", *got.DerivedFrom)
	}
}
