package apiclient_test

import (
	"reflect"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestLoopRollupClauses pins the order a narrow column drops from: the
// counter, then the `for_each` item, then the body step — so dropping from
// the tail loses the body step first and the counter last (issue #317).
func TestLoopRollupClauses(t *testing.T) {
	tests := []struct {
		name   string
		rollup *apiclient.LoopRollup
		want   []string
	}{
		{name: "absent"},
		{
			name:   "before the first iteration has a row",
			rollup: &apiclient.LoopRollup{Driver: "for_each", MaxIterations: 10},
		},
		{
			name: "a count loop has no item clause",
			rollup: &apiclient.LoopRollup{
				Driver: "count", Iteration: 4, Total: 10, MaxIterations: 10,
				BodyStep: "repair", BodyIndex: 2, BodyTotal: 3,
			},
			want: []string{"loop 4/10", "repair 2/3"},
		},
		{
			name: "all three, in priority order",
			rollup: &apiclient.LoopRollup{
				Driver: "for_each", Iteration: 4, Total: 10, MaxIterations: 10,
				Item: "alpha", BodyStep: "repair", BodyIndex: 2, BodyTotal: 3,
			},
			want: []string{"loop 4/10", "alpha", "repair 2/3"},
		},
		{
			name: "no body step, no body clause",
			rollup: &apiclient.LoopRollup{
				Driver: "for_each", Iteration: 2, Total: 3, MaxIterations: 10, Item: "beta",
			},
			want: []string{"loop 2/3", "beta"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rollup.Clauses(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Clauses() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLoopRollupCountsAgainstTheExtent: the ceiling is a bound, not a
// denominator. A 3-item `for_each` under a ceiling of 10 reads `loop 2/3`,
// and only a rollup with no extent at all — a row written before the server
// recorded one — counts against the bound instead.
func TestLoopRollupCountsAgainstTheExtent(t *testing.T) {
	derived := &apiclient.LoopRollup{Driver: "for_each", Iteration: 2, Total: 3, MaxIterations: 10}
	if got, want := derived.Display(), "loop 2/3"; got != want {
		t.Errorf("Display() = %q, want %q", got, want)
	}
	legacy := &apiclient.LoopRollup{Driver: "for_each", Iteration: 2, MaxIterations: 10}
	if got, want := legacy.Display(), "loop 2/10"; got != want {
		t.Errorf("Display() with no extent = %q, want %q", got, want)
	}
}

// TestLoopRollupDisplayJoinsItsClauses: Display is the whole rollup, which is
// what the detail header renders; the board reads Clauses instead because it
// has a width to fit them to.
func TestLoopRollupDisplayJoinsItsClauses(t *testing.T) {
	r := &apiclient.LoopRollup{
		Driver: "for_each", Iteration: 4, Total: 10, MaxIterations: 10,
		Item: "alpha", BodyStep: "repair", BodyIndex: 2, BodyTotal: 3,
	}
	if got, want := r.Display(), "loop 4/10 · alpha · repair 2/3"; got != want {
		t.Errorf("Display() = %q, want %q", got, want)
	}
	var absent *apiclient.LoopRollup
	if got := absent.Display(); got != "" {
		t.Errorf("a nil rollup renders %q, want nothing at all", got)
	}
}
