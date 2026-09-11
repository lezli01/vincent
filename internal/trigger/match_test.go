package trigger

import (
	"encoding/json"
	"testing"
)

func TestMatchEvent(t *testing.T) {
	var event map[string]any
	if err := json.Unmarshal([]byte(`{"id":"e1","result":"FAILURE","number":42,
		"fields":{"status":"Ready for Dev"},"labels":["bug","vincent"],"draft":false}`), &event); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		match map[string]any
		want  bool
		miss  string
	}{
		{"empty matches everything", nil, true, ""},
		{"string", map[string]any{"result": "FAILURE"}, true, ""},
		{"nested", map[string]any{"fields.status": "Ready for Dev"}, true, ""},
		{"yaml int against json number", map[string]any{"number": uint64(42)}, true, ""},
		{"bool", map[string]any{"draft": false}, true, ""},
		{"array contains", map[string]any{"labels": "vincent"}, true, ""},
		{"any of", map[string]any{"result": []any{"UNSTABLE", "FAILURE"}}, true, ""},
		{"wrong value", map[string]any{"result": "SUCCESS"}, false, "result"},
		{"absent path is a miss", map[string]any{"fields.priority": "high"}, false, "fields.priority"},
		{"first failing key named", map[string]any{"a": 1, "result": "FAILURE"}, false, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, miss := matchEvent(tc.match, event)
			if got != tc.want || miss != tc.miss {
				t.Errorf("matchEvent = %v, %q; want %v, %q", got, miss, tc.want, tc.miss)
			}
		})
	}
}
