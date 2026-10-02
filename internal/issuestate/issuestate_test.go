package issuestate

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// TestTransitionTable walks every (state, action, actor) triple — the whole
// product, not just the legal half, plus a stored state the vocabulary does
// not know and an actor it does not know — so a transition added without a
// decision behind it shows up here rather than as an issue that quietly does
// something §5.6 does not describe.
func TestTransitionTable(t *testing.T) {
	type outcome struct {
		next State
		noop bool
	}
	// want[actor][state][action] is the outcome; a missing entry means the
	// triple is illegal and must be a 409.
	humanOrAgent := map[State]map[Action]outcome{
		Open:   {Close: {Closed, false}},
		Closed: {Reopen: {Open, false}},
		// An unknown stored state reads as open.
		"waiting": {Close: {Closed, false}},
	}
	sync := map[State]map[Action]outcome{
		Open: {
			Close:          {Closed, false},
			RemoteClosed:   {Closed, false},
			Reopen:         {Open, true},
			RemoteReopened: {Open, true},
		},
		Closed: {
			Reopen:         {Open, false},
			RemoteReopened: {Open, false},
			Close:          {Closed, true},
			RemoteClosed:   {Closed, true},
		},
		"waiting": {
			Close:          {Closed, false},
			RemoteClosed:   {Closed, false},
			Reopen:         {Open, true},
			RemoteReopened: {Open, true},
		},
	}
	want := map[Actor]map[State]map[Action]outcome{
		Human:     humanOrAgent,
		Agent:     humanOrAgent,
		Sync:      sync,
		"robot":   {},
		Actor(""): {},
	}
	states := append([]State{"waiting"}, States...)
	actions := append([]Action{"merge"}, Actions...)
	for by, table := range want {
		for _, s := range states {
			for _, a := range actions {
				next, noop, ok := Next(s, a, by)
				w, wantOK := table[s][a]
				if ok != wantOK {
					t.Errorf("Next(%s, %s, %s) legal = %v, want %v", s, a, by, ok, wantOK)
					continue
				}
				if ok && (next != w.next || noop != w.noop) {
					t.Errorf("Next(%s, %s, %s) = (%s, noop=%v), want (%s, noop=%v)", s, a, by, next, noop, w.next, w.noop)
				}
				if got := Allowed(s, a, by); got != wantOK {
					t.Errorf("Allowed(%s, %s, %s) = %v, want %v", s, a, by, got, wantOK)
				}
			}
		}
	}
}

func TestNormalize(t *testing.T) {
	for _, s := range States {
		if got := Normalize(s); got != s {
			t.Errorf("Normalize(%s) = %s", s, got)
		}
	}
	for _, s := range []State{"", "waiting", "OPEN", "done"} {
		if got := Normalize(s); got != Open {
			t.Errorf("Normalize(%q) = %s, want open", s, got)
		}
	}
}

func TestResolveReason(t *testing.T) {
	for _, tc := range []struct {
		action  Action
		in      Reason
		want    Reason
		wantErr bool
	}{
		{Close, "", Completed, false},
		{Close, Completed, Completed, false},
		{Close, NotPlanned, NotPlanned, false},
		{Close, Duplicate, Duplicate, false},
		{Close, "wontfix", "", true},
		{RemoteClosed, "", Completed, false},
		{RemoteClosed, NotPlanned, NotPlanned, false},
		{RemoteClosed, "stale", "", true},
		{Reopen, "", "", false},
		{Reopen, Completed, "", true},
		{RemoteReopened, "", "", false},
		{RemoteReopened, Duplicate, "", true},
		{"merge", "", "", true},
	} {
		got, err := ResolveReason(tc.action, tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ResolveReason(%s, %q) = %q, %v; want %q, err=%v", tc.action, tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestValidActorAndReason(t *testing.T) {
	for _, a := range Actors {
		if !ValidActor(a) {
			t.Errorf("ValidActor(%s) = false", a)
		}
	}
	for _, a := range []Actor{"", "Human", "bot"} {
		if ValidActor(a) {
			t.Errorf("ValidActor(%q) = true", a)
		}
	}
	for _, r := range Reasons {
		if !ValidReason(r) {
			t.Errorf("ValidReason(%s) = false", r)
		}
	}
	for _, r := range []Reason{"", "wontfix", "COMPLETED"} {
		if ValidReason(r) {
			t.Errorf("ValidReason(%q) = true", r)
		}
	}
}

func TestHumanActionsFrom(t *testing.T) {
	for _, tc := range []struct {
		s    State
		want []Action
	}{
		{Open, []Action{Close}},
		{Closed, []Action{Reopen}},
		{"waiting", []Action{Close}},
	} {
		if got := HumanActionsFrom(tc.s); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("HumanActionsFrom(%s) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

// TestIsALeaf: the package depends on the standard library alone, so every
// layer — store, service, API, client — can consult it without an import
// cycle. `go list -deps` sees transitive imports, which a parse of this
// package's own files would not.
func TestIsALeaf(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		// The module prefix keeps the standard library's own internal
		// packages (runtime/internal/..., on older toolchains) out of it.
		if strings.HasPrefix(dep, "github.com/lezli01/vincent/") && strings.Contains(dep, "/internal/") &&
			!strings.HasSuffix(dep, "/internal/issuestate") {
			t.Errorf("internal/issuestate depends on %s; it must stay a leaf", dep)
		}
	}
}
