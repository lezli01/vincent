package jiramap

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func status(name, key string) *Status {
	return &Status{Name: name, StatusCategory: StatusCategory{Key: key}}
}

func res(name string) *Named { return &Named{Name: name} }

func TestFromJira(t *testing.T) {
	custom := Config{
		DuplicateResolutions:  []string{"Same as"},
		NotPlannedResolutions: []string{"Nope"},
	}
	type want struct {
		state  issuestate.State
		reason issuestate.Reason
	}
	open := want{issuestate.Open, ""}
	closed := func(r issuestate.Reason) want { return want{issuestate.Closed, r} }
	resolutions := []struct {
		name string
		r    *Named
	}{
		{"absent", nil},
		{"Done", res("Done")},
		{"Duplicate", res("Duplicate")},
		{"Won't do", res("Won't do")},
		{"custom", res("Fixed in prod")},
		{"custom-dup", res("Same as")},
		{"custom-np", res("Nope")},
	}
	// The non-done categories, including an unknown key, are open whatever
	// the resolution says.
	for _, key := range []string{CategoryNew, CategoryIndeterminate, CategoryUndefined, "in-flight", "completed", ""} {
		for _, r := range resolutions {
			for _, c := range []Config{{}, custom} {
				s, reason, dup := FromJira(Fields{Status: status("Whatever", key), Resolution: r.r}, c)
				if (want{s, reason}) != open || dup != "" {
					t.Errorf("category %q resolution %s = (%s, %q, %q), want open", key, r.name, s, reason, dup)
				}
			}
		}
	}
	if s, _, _ := FromJira(Fields{}, Config{}); s != issuestate.Open {
		t.Errorf("no status = %s, want open", s)
	}

	for _, tc := range []struct {
		status string
		r      *Named
		c      Config
		want   want
	}{
		{"Done", nil, Config{}, closed(issuestate.Completed)},
		{"Done", res("Done"), Config{}, closed(issuestate.Completed)},
		{"Done", res("Duplicate"), Config{}, closed(issuestate.Duplicate)},
		{"Done", res("Won't do"), Config{}, closed(issuestate.NotPlanned)},
		{"Done", res("  won't FIX "), Config{}, closed(issuestate.NotPlanned)},
		{"Done", res("Cannot Reproduce"), Config{}, closed(issuestate.NotPlanned)},
		{"Done", res("Incomplete"), Config{}, closed(issuestate.NotPlanned)},
		{"Done", res("Declined"), Config{}, closed(issuestate.NotPlanned)},
		{"Done", res("Fixed in prod"), Config{}, closed(issuestate.Completed)},
		{"Done", res(""), Config{}, closed(issuestate.Completed)},
		// A custom set replaces the default rather than adding to it.
		{"Done", res("Same as"), custom, closed(issuestate.Duplicate)},
		{"Done", res("Nope"), custom, closed(issuestate.NotPlanned)},
		{"Done", res("Duplicate"), custom, closed(issuestate.Completed)},
		{"Done", res("Won't do"), custom, closed(issuestate.Completed)},
		// The null-resolution status-name fallback.
		{"Cancelled", nil, Config{}, closed(issuestate.NotPlanned)},
		{"canceled", nil, Config{}, closed(issuestate.NotPlanned)},
		{"Won't do", nil, Config{}, closed(issuestate.NotPlanned)},
		{"Nope", nil, custom, closed(issuestate.NotPlanned)},
		{"Won't do", nil, custom, closed(issuestate.Completed)},
		{"Cancelled", nil, custom, closed(issuestate.NotPlanned)},
		// A set resolution always wins over the status name.
		{"Cancelled", res("Done"), Config{}, closed(issuestate.Completed)},
		{"Cancelled", res("Duplicate"), Config{}, closed(issuestate.Duplicate)},
	} {
		s, r, _ := FromJira(Fields{Status: status(tc.status, CategoryDone), Resolution: tc.r}, tc.c)
		if (want{s, r}) != tc.want {
			name := "<nil>"
			if tc.r != nil {
				name = tc.r.Name
			}
			t.Errorf("done %q resolution %q = (%s, %s), want %v", tc.status, name, s, r, tc.want)
		}
	}
}

func TestDuplicateOfKey(t *testing.T) {
	dup := func(links ...IssueLink) string {
		_, _, k := FromJira(Fields{Status: status("Done", CategoryDone), Resolution: res("Duplicate"), IssueLinks: links}, Config{})
		return k
	}
	outward := IssueLink{Type: LinkType{Name: "Duplicate", Inward: "is duplicated by", Outward: "duplicates"}, OutwardIssue: &IssueRef{Key: "ABC-1"}}
	inward := IssueLink{Type: LinkType{Name: "Duplicate"}, InwardIssue: &IssueRef{Key: "ABC-2"}}
	blocks := IssueLink{Type: LinkType{Name: "Blocks"}, OutwardIssue: &IssueRef{Key: "ABC-3"}}
	if got := dup(outward); got != "ABC-1" {
		t.Errorf("outward duplicate = %q, want ABC-1", got)
	}
	if got := dup(inward, blocks, outward); got != "ABC-1" {
		t.Errorf("mixed links = %q, want ABC-1", got)
	}
	if got := dup(inward); got != "" {
		t.Errorf("inward duplicate = %q, want empty", got)
	}
	if got := dup(blocks); got != "" {
		t.Errorf("blocks link = %q, want empty", got)
	}
	// A non-duplicate close names no target even with the link present.
	if _, _, k := FromJira(Fields{Status: status("Done", CategoryDone), Resolution: res("Done"), IssueLinks: []IssueLink{outward}}, Config{}); k != "" {
		t.Errorf("completed with duplicate link = %q, want empty", k)
	}
}

func TestKindSeed(t *testing.T) {
	for in, want := range map[string]string{
		"Bug":                        "bug",
		"Task":                       "task",
		"Story":                      "story",
		"Epic":                       "epic",
		"Sub-task":                   "sub-task",
		"New Feature":                "new-feature",
		"  Tech   Debt ":             "tech-debt",
		"1Thing":                     "",
		"Ä":                          "",
		"":                           "",
		strings.Repeat("a", 40):      "",
		strings.Repeat("a", 32):      strings.Repeat("a", 32),
		"Bug/Defect":                 "",
		"under_score":                "under_score",
		"Improvement (customer req)": "",
	} {
		if got := KindSeed(in); got != want {
			t.Errorf("KindSeed(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabels(t *testing.T) {
	ok := strings.Repeat("a", 64)
	long := strings.Repeat("b", 65)
	kept, dropped := Labels([]string{"x", ok, long, "y", long})
	if !reflect.DeepEqual(kept, []string{"x", ok, "y"}) || dropped != 2 {
		t.Errorf("Labels = %v, %d; want [x <64> y], 2", kept, dropped)
	}
	if kept, dropped := Labels(nil); kept != nil || dropped != 0 {
		t.Errorf("Labels(nil) = %v, %d", kept, dropped)
	}
}

func fullFields() Fields {
	return Fields{
		Status:     status("Won't do", CategoryDone),
		Resolution: res("Won't do"),
		IssueType:  res("Bug"),
		Priority:   res("High"),
		Assignee:   &User{DisplayName: "Ada Lovelace", AccountID: "5b10a2844c20165700ede21g"},
		Reporter:   &User{DisplayName: "Charles Babbage", AccountID: "5b10ac8d82e05b22cc7d4ef5"},
		Parent:     &IssueRef{Key: "ABC-10"},
		IssueLinks: []IssueLink{
			{Type: LinkType{Name: "Duplicate"}, OutwardIssue: &IssueRef{Key: "ABC-1"}},
			{Type: LinkType{Name: "Blocks"}, InwardIssue: &IssueRef{Key: "ABC-7"}},
		},
		Components:     []Named{{Name: "api"}, {Name: "web"}},
		Labels:         []string{"triage"},
		Created:        "2024-03-05T10:11:12.345+0100",
		ResolutionDate: "2024-03-06T08:00:00.000+0000",
	}
}

func TestRemoteJSONGolden(t *testing.T) {
	got := RemoteJSON("ABC-42", fullFields())
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, got, "", "  "); err != nil {
		t.Fatalf("RemoteJSON is not JSON: %v", err)
	}
	pretty.WriteByte('\n')
	path := filepath.Join("testdata", "remote_json.golden")
	if *update {
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Git may check the golden out with CRLF on Windows.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(pretty.Bytes(), want) {
		t.Errorf("RemoteJSON mismatch; run with -update to see the diff.\ngot:\n%s\nwant:\n%s", pretty.Bytes(), want)
	}
	if again := RemoteJSON("ABC-42", fullFields()); !bytes.Equal(again, got) {
		t.Error("RemoteJSON is not deterministic")
	}
}

// TestRemoteJSONSnapshotKeys decodes the record the way
// internal/store/issuesnapshot.go's remoteDetail does — the same tags,
// restated because this package does not import the store — and checks the
// GitHub-named keys land.
func TestRemoteJSONSnapshotKeys(t *testing.T) {
	var detail struct {
		Key       string    `json:"key"`
		State     string    `json:"state"`
		Assignee  string    `json:"assignee"`
		Assignees []string  `json:"assignees"`
		CreatedAt time.Time `json:"created_at"`
	}
	raw := RemoteJSON("ABC-42", fullFields())
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("snapshot decode: %v", err)
	}
	// store.RemotePayloadKey reads the display key from here (review F3).
	if detail.Key != "ABC-42" {
		t.Errorf("key = %q, want ABC-42", detail.Key)
	}
	if detail.State != "closed" {
		t.Errorf("state = %q, want closed", detail.State)
	}
	if !reflect.DeepEqual(detail.Assignees, []string{"Ada Lovelace"}) || detail.Assignee != "Ada Lovelace" {
		t.Errorf("assignee(s) = %q, %v", detail.Assignee, detail.Assignees)
	}
	if want := time.Date(2024, 3, 5, 9, 11, 12, 345e6, time.UTC); !detail.CreatedAt.Equal(want) {
		t.Errorf("created_at = %v, want %v", detail.CreatedAt, want)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"key", "state", "created_at", "assignees"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("remote_json lacks %q", k)
		}
	}

	// An open, unassigned issue with an unparsable date still writes
	// state and omits what it cannot say.
	raw = RemoteJSON("ABC-43", Fields{Status: status("To Do", CategoryNew), Created: "yesterday"})
	keys = nil
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if string(keys["key"]) != `"ABC-43"` {
		t.Errorf("key = %s, want \"ABC-43\"", keys["key"])
	}
	if string(keys["state"]) != `"open"` {
		t.Errorf("state = %s, want \"open\"", keys["state"])
	}
	for _, k := range []string{"created_at", "assignees", "assignee"} {
		if _, ok := keys[k]; ok {
			t.Errorf("open record carries %q", k)
		}
	}
}

// Transition fixtures.

var stockResolutions = []Named{{Name: "Done"}, {Name: "Won't Do"}, {Name: "Duplicate"}, {Name: "Cannot Reproduce"}}

func tr(id, name, to, key string, fields map[string]FieldMeta) Transition {
	return Transition{ID: id, Name: name, To: *status(to, key), HasScreen: len(fields) > 0, Fields: fields}
}

func resolutionScreen(required bool, allowed ...Named) map[string]FieldMeta {
	return map[string]FieldMeta{"resolution": {Required: required, AllowedValues: allowed}}
}

// companyOpen is the sample transitions_open_issue.json shape: "Done" with a
// required resolution, and "Won't do" to Cancelled (done category) with one.
var companyOpen = []Transition{
	tr("11", "To Do", "To Do", CategoryNew, nil),
	tr("21", "In Progress", "In Progress", CategoryIndeterminate, nil),
	tr("31", "Done", "Done", CategoryDone, resolutionScreen(true, stockResolutions...)),
	tr("41", "Won't do", "Cancelled", CategoryDone, resolutionScreen(true, stockResolutions...)),
}

// teamOpen is a team-managed workflow: no resolution field anywhere.
var teamOpen = []Transition{
	tr("11", "To Do", "To Do", CategoryNew, nil),
	tr("21", "In Progress", "In Progress", CategoryIndeterminate, nil),
	tr("31", "Done", "Done", CategoryDone, nil),
	tr("41", "Won't do", "Cancelled", CategoryDone, nil),
}

func TestChooseTransition(t *testing.T) {
	const (
		closeA  = issuestate.Close
		reopenA = issuestate.Reopen
	)
	for _, tc := range []struct {
		name    string
		action  issuestate.Action
		reason  issuestate.Reason
		ts      []Transition
		c       Config
		wantID  string
		wantRes string
		code    string
	}{
		{"company completed", closeA, issuestate.Completed, companyOpen, Config{}, "31", "Done", ""},
		{"company default reason", closeA, "", companyOpen, Config{}, "31", "Done", ""},
		{"company not planned", closeA, issuestate.NotPlanned, companyOpen, Config{}, "41", "Won't Do", ""},
		{"company duplicate", closeA, issuestate.Duplicate, companyOpen, Config{}, "31", "Duplicate", ""},
		{"team completed", closeA, issuestate.Completed, teamOpen, Config{}, "31", "", ""},
		{"team not planned", closeA, issuestate.NotPlanned, teamOpen, Config{}, "41", "", ""},
		{"team duplicate never degrades", closeA, issuestate.Duplicate, teamOpen, Config{}, "", "", CodeNoTransition},
		{
			"not planned with no achievable resolution", closeA, issuestate.NotPlanned,
			[]Transition{tr("31", "Done", "Done", CategoryDone, resolutionScreen(true, Named{Name: "Done"}))},
			Config{},
			"", "", CodeNoTransition,
		},
		{
			"duplicate with no achievable resolution", closeA, issuestate.Duplicate,
			[]Transition{tr("31", "Done", "Done", CategoryDone, resolutionScreen(true, Named{Name: "Done"}, Named{Name: "Won't Do"}))},
			Config{},
			"", "", CodeNoTransition,
		},
		{
			"resolution offered without Done", closeA, issuestate.Completed,
			[]Transition{tr("31", "Close", "Closed", CategoryDone, resolutionScreen(false, Named{Name: "Won't Do"}, Named{Name: "Fixed"}))},
			Config{},
			"31", "Fixed", "",
		},
		{
			"two equal Done", closeA, issuestate.Completed,
			[]Transition{tr("31", "Done", "Done", CategoryDone, nil), tr("32", "Done", "Done", CategoryDone, nil)},
			Config{},
			"", "", CodeAmbiguousTransition,
		},
		{
			"required custom field without default", closeA, issuestate.Completed,
			[]Transition{tr("31", "Done", "Done", CategoryDone, map[string]FieldMeta{"customfield_10010": {Required: true}})},
			Config{},
			"", "", CodeTransitionNeedsFields,
		},
		{
			"required custom field with default", closeA, issuestate.Completed,
			[]Transition{tr("31", "Done", "Done", CategoryDone, map[string]FieldMeta{"customfield_10010": {Required: true, HasDefaultValue: true}})},
			Config{},
			"31", "", "",
		},
		{
			"needs-fields transition excluded before counting", closeA, issuestate.Completed,
			[]Transition{
				tr("31", "Done", "Done", CategoryDone, map[string]FieldMeta{"customfield_10010": {Required: true}}),
				tr("32", "Resolve", "Resolved", CategoryDone, nil),
			},
			Config{},
			"32", "", "",
		},
		{"no done transition", closeA, issuestate.Completed, companyOpen[:2], Config{}, "", "", CodeNoTransition},
		{"no transitions at all", closeA, issuestate.Completed, nil, Config{}, "", "", CodeNoTransition},
		{
			"reopen with resolution on screen sets none", reopenA, "",
			[]Transition{tr("51", "Reopen", "To Do", CategoryNew, resolutionScreen(false, stockResolutions...))},
			Config{},
			"51", "", "",
		},
		{
			"reopen with required resolution needs fields", reopenA, "",
			[]Transition{tr("51", "Reopen", "To Do", CategoryNew, resolutionScreen(true, stockResolutions...))},
			Config{},
			"", "", CodeTransitionNeedsFields,
		},
		{
			"reopen prefers new over indeterminate", reopenA, "",
			[]Transition{
				tr("61", "Back to work", "In Progress", CategoryIndeterminate, nil),
				tr("51", "Reopen", "To Do", CategoryNew, nil),
			},
			Config{},
			"51", "", "",
		},
		{
			"reopen falls back to indeterminate", reopenA, "",
			[]Transition{tr("61", "Back to work", "In Progress", CategoryIndeterminate, nil), tr("31", "Done", "Done", CategoryDone, nil)},
			Config{},
			"61", "", "",
		},
		{
			"reopen with only done", reopenA, "",
			[]Transition{tr("31", "Done", "Done", CategoryDone, nil)},
			Config{},
			"", "", CodeNoTransition,
		},
		{
			"override wins over the heuristic", closeA, issuestate.Completed,
			append([]Transition{tr("71", "Ship it", "Released", CategoryDone, nil)}, teamOpen...),
			Config{Overrides: Overrides{CloseCompleted: "ship IT"}},
			"71", "", "",
		},
		{
			"override by target status name", closeA, issuestate.Completed,
			append([]Transition{tr("71", "Ship it", "Released", CategoryDone, nil)}, teamOpen...),
			Config{Overrides: Overrides{CloseCompleted: "released"}},
			"71", "", "",
		},
		{
			"override unavailable never falls back", closeA, issuestate.Completed, teamOpen,
			Config{Overrides: Overrides{CloseCompleted: "Ship it"}},
			"", "", CodeNoTransition,
		},
		{
			"override matching two", closeA, issuestate.Completed,
			[]Transition{tr("71", "Ship it", "Released", CategoryDone, nil), tr("72", "Ship it", "Released", CategoryDone, nil)},
			Config{Overrides: Overrides{CloseCompleted: "Ship it"}},
			"", "", CodeAmbiguousTransition,
		},
		{
			"override cannot make a not-done status close", closeA, issuestate.Completed, teamOpen,
			Config{Overrides: Overrides{CloseCompleted: "In Progress"}},
			"", "", CodeNoTransition,
		},
		{
			"reopen override", reopenA, "",
			[]Transition{tr("51", "Reopen", "To Do", CategoryNew, nil), tr("61", "Back to work", "In Progress", CategoryIndeterminate, nil)},
			Config{Overrides: Overrides{Reopen: "Back to work"}},
			"61", "", "",
		},
		{
			"not planned override", closeA, issuestate.NotPlanned, companyOpen,
			Config{Overrides: Overrides{CloseNotPlanned: "Done"}},
			"31", "Won't Do", "",
		},
		{
			"duplicate override", closeA, issuestate.Duplicate, companyOpen,
			Config{Overrides: Overrides{CloseDuplicate: "Cancelled"}},
			"41", "Duplicate", "",
		},
		{
			"custom not-planned set", closeA, issuestate.NotPlanned,
			[]Transition{tr("31", "Done", "Done", CategoryDone, resolutionScreen(true, Named{Name: "Done"}, Named{Name: "Nope"}))},
			Config{NotPlannedResolutions: []string{"Nope"}},
			"31", "Nope", "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ChooseTransition(tc.action, tc.reason, tc.ts, tc.c)
			if tc.code != "" {
				var ce *ChooseError
				if !errors.As(err, &ce) || ce.Code != tc.code {
					t.Fatalf("err = %v, want code %s", err, tc.code)
				}
				sentinel := map[string]error{
					CodeNoTransition:          ErrNoTransition,
					CodeAmbiguousTransition:   ErrAmbiguousTransition,
					CodeTransitionNeedsFields: ErrTransitionNeedsFields,
				}[tc.code]
				if !errors.Is(err, sentinel) {
					t.Errorf("err %v does not wrap %v", err, sentinel)
				}
				if ce.Action != tc.action {
					t.Errorf("ChooseError.Action = %s, want %s", ce.Action, tc.action)
				}
				return
			}
			if err != nil {
				t.Fatalf("ChooseTransition: %v", err)
			}
			if got.TransitionID != tc.wantID || got.Resolution != tc.wantRes {
				t.Fatalf("choice = %+v, want id %s resolution %q", got, tc.wantID, tc.wantRes)
			}
			// The invariant: Achieved equals the request, and equals what
			// FromJira reads back after the transition.
			want := Outcome{State: issuestate.Open}
			if tc.action == issuestate.Close {
				r, _ := issuestate.ResolveReason(tc.action, tc.reason)
				want = Outcome{State: issuestate.Closed, Reason: r}
			}
			if got.Achieved != want {
				t.Errorf("Achieved = %+v, want the request %+v", got.Achieved, want)
			}
			var chosen Transition
			for _, tt := range tc.ts {
				if tt.ID == got.TransitionID {
					chosen = tt
				}
			}
			post := Fields{Status: &Status{Name: chosen.To.Name, StatusCategory: chosen.To.StatusCategory}}
			if got.Resolution != "" {
				post.Resolution = res(got.Resolution)
			}
			if s, r, _ := FromJira(post, tc.c); (Outcome{s, r}) != got.Achieved {
				t.Errorf("FromJira(post) = (%s, %s), Achieved = %+v", s, r, got.Achieved)
			}
		})
	}
}

func TestChooseTransitionUnknownInput(t *testing.T) {
	for _, tc := range []struct {
		a issuestate.Action
		r issuestate.Reason
	}{
		{issuestate.Close, "wontfix"},
		{issuestate.Reopen, issuestate.Completed},
		{"merge", ""},
	} {
		_, want := issuestate.ResolveReason(tc.a, tc.r)
		_, err := ChooseTransition(tc.a, tc.r, companyOpen, Config{})
		if err == nil || want == nil || err.Error() != want.Error() {
			t.Errorf("ChooseTransition(%s, %q) err = %v, want ResolveReason's %v", tc.a, tc.r, err, want)
		}
		var ce *ChooseError
		if errors.As(err, &ce) {
			t.Errorf("ChooseTransition(%s, %q) returned a ChooseError for bad input", tc.a, tc.r)
		}
	}
	if _, err := ChooseTransition(issuestate.RemoteClosed, "", companyOpen, Config{}); err == nil {
		t.Error("remote_closed was accepted as a write")
	}
}

func TestChooseErrorMessage(t *testing.T) {
	_, err := ChooseTransition(issuestate.Close, issuestate.Duplicate, teamOpen, Config{})
	if err == nil || !strings.Contains(err.Error(), "close as duplicate") {
		t.Errorf("err = %v", err)
	}
}

// TestIsALeaf: the package depends on the standard library and
// internal/issuestate alone, so the Jira client and the outbox can both
// import it without a cycle. `go list -deps` sees transitive imports.
func TestIsALeaf(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if !strings.HasPrefix(dep, "github.com/lezli01/vincent/") {
			continue
		}
		if !strings.HasSuffix(dep, "/internal/issuestate") && !strings.HasSuffix(dep, "/internal/jira/jiramap") {
			t.Errorf("internal/jira/jiramap depends on %s; it may import only internal/issuestate", dep)
		}
	}
}
