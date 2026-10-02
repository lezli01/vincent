package issues

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// newService opens a real store on a temp database, with one project, and
// returns the service over it and the project's id.
func newService(t *testing.T) (*Service, *store.Store, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "vincent.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &store.Project{Name: "p1", Path: "/p1", DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return New(st), st, p.ID
}

func mustCreate(t *testing.T, svc *Service, in CreateInput) *store.Issue {
	t.Helper()
	iss, err := svc.Create(t.Context(), issuestate.Human, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return iss
}

// wantField asserts err is a *ValidationError naming field.
func wantField(t *testing.T, err error, field string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError on %q", err, field)
	}
	if ve.Field != field {
		t.Fatalf("field = %q (%v), want %q", ve.Field, ve, field)
	}
}

func eventCount(t *testing.T, st *store.Store) int {
	t.Helper()
	evs, err := st.ListEvents(t.Context(), store.EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return len(evs)
}

func TestCreateValidation(t *testing.T) {
	svc, _, pid := newService(t)
	base := func() CreateInput { return CreateInput{ProjectID: pid, Title: "t"} }
	label64 := strings.Repeat("l", MaxLabelBytes)
	kind32 := "k" + strings.Repeat("9", MaxKindBytes-1)

	ok := []struct {
		name string
		mod  func(*CreateInput)
	}{
		{"title at bound", func(in *CreateInput) { in.Title = strings.Repeat("a", MaxTitleBytes) }},
		{"title trimmed to bound", func(in *CreateInput) { in.Title = "  " + strings.Repeat("a", MaxTitleBytes) + "\n" }},
		{"label at bound", func(in *CreateInput) { in.Labels = []string{label64} }},
		{"no kind", func(in *CreateInput) { in.Kind = "" }},
		{"kind at bound", func(in *CreateInput) { in.Kind = kind32 }},
		{"kind with separators", func(in *CreateInput) { in.Kind = "a-b_c9" }},
		{"min priority", func(in *CreateInput) { in.Priority = MinPriority }},
		{"max priority", func(in *CreateInput) { in.Priority = MaxPriority }},
	}
	for _, c := range ok {
		in := base()
		c.mod(&in)
		if _, err := svc.Create(t.Context(), issuestate.Human, in); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}

	bad := []struct {
		name, field string
		mod         func(*CreateInput)
	}{
		{"empty title", "title", func(in *CreateInput) { in.Title = " \t\n" }},
		{"title past bound", "title", func(in *CreateInput) { in.Title = strings.Repeat("a", MaxTitleBytes+1) }},
		{"label past bound", "labels", func(in *CreateInput) { in.Labels = []string{label64 + "l"} }},
		{"empty label", "labels", func(in *CreateInput) { in.Labels = []string{"ok", "  "} }},
		{"kind past bound", "kind", func(in *CreateInput) { in.Kind = kind32 + "9" }},
		{"uppercase kind", "kind", func(in *CreateInput) { in.Kind = "Bug" }},
		{"kind leading digit", "kind", func(in *CreateInput) { in.Kind = "1x" }},
		{"kind with space", "kind", func(in *CreateInput) { in.Kind = "a b" }},
		{"priority below", "priority", func(in *CreateInput) { in.Priority = MinPriority - 1 }},
		{"priority above", "priority", func(in *CreateInput) { in.Priority = MaxPriority + 1 }},
		{"unknown actor", "by", nil},
	}
	for _, c := range bad {
		in := base()
		by := issuestate.Human
		if c.mod != nil {
			c.mod(&in)
		} else {
			by = "robot"
		}
		_, err := svc.Create(t.Context(), by, in)
		t.Run(c.name, func(t *testing.T) { wantField(t, err, c.field) })
	}
}

func TestCreateRoundTrip(t *testing.T) {
	svc, _, pid := newService(t)
	iss := mustCreate(t, svc, CreateInput{
		ProjectID: pid, Title: "  Crash on start ", Body: "trace", Kind: "bug",
		Author: "ann", Priority: 2, Labels: []string{" zeta ", "Alpha", "ALPHA", "alpha "},
	})
	got, err := svc.Get(t.Context(), iss.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "Crash on start" || got.Body != "trace" || got.Kind != "bug" ||
		got.Author != "ann" || got.Priority != 2 || got.State != issuestate.Open {
		t.Errorf("issue = %+v", got)
	}
	if want := []string{"Alpha", "zeta"}; !reflect.DeepEqual(got.Labels, want) {
		t.Errorf("labels = %v, want %v (first spelling kept)", got.Labels, want)
	}
	list, err := svc.List(t.Context(), store.IssueFilter{ProjectID: pid})
	if err != nil || len(list) != 1 || list[0].ID != iss.ID {
		t.Errorf("List = %v, %v", list, err)
	}
}

func TestUpdate(t *testing.T) {
	svc, _, pid := newService(t)
	ctx := t.Context()
	iss := mustCreate(t, svc, CreateInput{ProjectID: pid, Title: "old"})

	title, kind, prio, body := "  new  ", "feature", MaxPriority, "b"
	up, err := svc.Update(ctx, issuestate.Agent, iss.ID, iss.Version,
		store.IssuePatch{Title: &title, Kind: &kind, Priority: &prio, Body: &body})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if up.Title != "new" || up.Kind != "feature" || up.Priority != MaxPriority || up.Body != "b" || up.Version != iss.Version+1 {
		t.Errorf("updated = %+v", up)
	}

	// A stale version is the store's compare-and-set refusal.
	if _, err := svc.Update(ctx, issuestate.Human, iss.ID, iss.Version, store.IssuePatch{Body: &body}); !errors.Is(err, store.ErrIssueChanged) {
		t.Errorf("stale update err = %v, want ErrIssueChanged", err)
	}
	if _, err := svc.Update(ctx, issuestate.Human, 9999, 1, store.IssuePatch{Body: &body}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing issue err = %v, want ErrNotFound", err)
	}

	blank, long := " ", strings.Repeat("a", MaxTitleBytes+1)
	badKind, low, high := "Nope", MinPriority-1, MaxPriority+1
	for field, p := range map[string][]store.IssuePatch{
		"title":    {{Title: &blank}, {Title: &long}},
		"kind":     {{Kind: &badKind}},
		"priority": {{Priority: &low}, {Priority: &high}},
	} {
		for _, patch := range p {
			_, err := svc.Update(ctx, issuestate.Human, iss.ID, up.Version, patch)
			wantField(t, err, field)
		}
	}
	empty := ""
	if _, err := svc.Update(ctx, issuestate.Human, iss.ID, up.Version, store.IssuePatch{Kind: &empty}); err != nil {
		t.Errorf("clearing kind: %v", err)
	}
}

func TestCloseReopen(t *testing.T) {
	svc, _, pid := newService(t)
	ctx := t.Context()
	iss := mustCreate(t, svc, CreateInput{ProjectID: pid, Title: "t"})

	closed, err := svc.Close(ctx, issuestate.Human, iss.ID, "", nil)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closed.State != issuestate.Closed || closed.CloseReason != issuestate.Completed || closed.ClosedAt == nil {
		t.Errorf("closed = %+v", closed)
	}
	// A human asking for the state the issue is already in is refused…
	if _, err := svc.Close(ctx, issuestate.Human, iss.ID, "", nil); !errors.Is(err, store.ErrInvalidIssueAction) {
		t.Errorf("human double close err = %v, want ErrInvalidIssueAction", err)
	}
	// …while sync's is a no-op.
	again, err := svc.Close(ctx, issuestate.Sync, iss.ID, "", nil)
	if err != nil {
		t.Fatalf("sync double close: %v", err)
	}
	if again.State != issuestate.Closed || again.Version != closed.Version {
		t.Errorf("sync double close changed the issue: %+v", again)
	}

	reopened, err := svc.Reopen(ctx, issuestate.Agent, iss.ID)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if reopened.State != issuestate.Open || reopened.CloseReason != "" || reopened.ClosedAt != nil {
		t.Errorf("reopened = %+v", reopened)
	}
	if _, err := svc.Reopen(ctx, issuestate.Human, iss.ID); !errors.Is(err, store.ErrInvalidIssueAction) {
		t.Errorf("double reopen err = %v, want ErrInvalidIssueAction", err)
	}

	np, err := svc.Close(ctx, issuestate.Human, iss.ID, issuestate.NotPlanned, nil)
	if err != nil || np.CloseReason != issuestate.NotPlanned {
		t.Errorf("close not_planned = %+v, %v", np, err)
	}
	if _, err := svc.Transition(ctx, issuestate.Sync, iss.ID, issuestate.RemoteReopened, ""); err != nil {
		t.Errorf("sync remote_reopened: %v", err)
	}
	if _, err := svc.Transition(ctx, issuestate.Human, iss.ID, issuestate.RemoteClosed, ""); !errors.Is(err, store.ErrInvalidIssueAction) {
		t.Errorf("human remote_closed err = %v, want ErrInvalidIssueAction", err)
	}
	if _, err := svc.Close(ctx, issuestate.Human, 9999, "", nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing issue err = %v, want ErrNotFound", err)
	}

	_, err = svc.Close(ctx, issuestate.Human, iss.ID, "wontfix", nil)
	wantField(t, err, "reason")
	_, err = svc.Transition(ctx, issuestate.Human, iss.ID, issuestate.Reopen, issuestate.Duplicate)
	wantField(t, err, "reason")
	_, err = svc.Transition(ctx, issuestate.Human, iss.ID, "archive", "")
	wantField(t, err, "action")
}

func TestLabelsCommentsDelete(t *testing.T) {
	svc, _, pid := newService(t)
	ctx := t.Context()
	iss := mustCreate(t, svc, CreateInput{ProjectID: pid, Title: "t", Labels: []string{"old"}})

	got, err := svc.SetLabels(ctx, issuestate.Human, iss.ID, []string{" Bug ", "bug", "ui", strings.Repeat("l", MaxLabelBytes)})
	if err != nil {
		t.Fatalf("SetLabels: %v", err)
	}
	if want := []string{"Bug", strings.Repeat("l", MaxLabelBytes), "ui"}; !reflect.DeepEqual(got.Labels, want) {
		t.Errorf("labels = %v, want %v", got.Labels, want)
	}
	_, err = svc.SetLabels(ctx, issuestate.Human, iss.ID, []string{strings.Repeat("l", MaxLabelBytes+1)})
	wantField(t, err, "labels")
	_, err = svc.SetLabels(ctx, issuestate.Human, iss.ID, []string{""})
	wantField(t, err, "labels")

	c, err := svc.Comment(ctx, issuestate.Human, iss.ID, "ann", "  looks right \n")
	if err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if c.Body != "looks right" || c.Author != "ann" || c.IssueID != iss.ID {
		t.Errorf("comment = %+v", c)
	}
	_, err = svc.Comment(ctx, issuestate.Human, iss.ID, "ann", " \n ")
	wantField(t, err, "body")
	cs, err := svc.Comments(ctx, iss.ID)
	if err != nil || len(cs) != 1 || cs[0].ID != c.ID {
		t.Errorf("Comments = %v, %v", cs, err)
	}

	if err := svc.Delete(ctx, issuestate.Human, iss.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, iss.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after delete err = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, issuestate.Human, iss.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete err = %v, want ErrNotFound", err)
	}
}

// TestUnknownActorTouchesNothing: an invalid actor is refused by every write
// before the store is reached, so no event is appended.
func TestUnknownActorTouchesNothing(t *testing.T) {
	svc, st, pid := newService(t)
	iss := mustCreate(t, svc, CreateInput{ProjectID: pid, Title: "t"})
	before := eventCount(t, st)
	title := "x"
	const by issuestate.Actor = "robot"

	writes := map[string]func(ctx context.Context) error{
		"create": func(ctx context.Context) error {
			_, err := svc.Create(ctx, by, CreateInput{ProjectID: pid, Title: "t"})
			return err
		},
		"update": func(ctx context.Context) error {
			_, err := svc.Update(ctx, by, iss.ID, iss.Version, store.IssuePatch{Title: &title})
			return err
		},
		"close": func(ctx context.Context) error {
			_, err := svc.Close(ctx, by, iss.ID, "", nil)
			return err
		},
		"reopen": func(ctx context.Context) error {
			_, err := svc.Reopen(ctx, by, iss.ID)
			return err
		},
		"transition": func(ctx context.Context) error {
			_, err := svc.Transition(ctx, by, iss.ID, issuestate.RemoteClosed, "")
			return err
		},
		"labels": func(ctx context.Context) error {
			_, err := svc.SetLabels(ctx, by, iss.ID, []string{"a"})
			return err
		},
		"comment": func(ctx context.Context) error {
			_, err := svc.Comment(ctx, by, iss.ID, "ann", "hi")
			return err
		},
		"delete": func(ctx context.Context) error { return svc.Delete(ctx, by, iss.ID) },
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) { wantField(t, write(t.Context()), "by") })
	}
	if after := eventCount(t, st); after != before {
		t.Errorf("events %d -> %d: a refused actor reached the store", before, after)
	}
	if got, err := svc.Get(t.Context(), iss.ID); err != nil || got.Version != iss.Version || got.Title != "t" {
		t.Errorf("issue changed: %+v, %v", got, err)
	}
}

// TestMirroredContentIsSyncs: an imported issue's title, body and labels
// belong to sync (task 130.3 decision 2); a human or an agent may still set
// kind and priority, and a local issue is editable throughout.
func TestMirroredContentIsSyncs(t *testing.T) {
	svc, st, pid := newService(t)
	ctx := t.Context()
	imported, _, err := st.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: pid, Provider: "github", RemoteKey: "I_1", Title: "remote", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatal(err)
	}
	if !Mirrored(imported) || !reflect.DeepEqual(Editable(imported), []string{"kind", "priority"}) {
		t.Errorf("imported: mirrored %v editable %v", Mirrored(imported), Editable(imported))
	}
	title := "mine"
	for _, by := range []issuestate.Actor{issuestate.Human, issuestate.Agent} {
		if _, err := svc.Update(ctx, by, imported.ID, imported.Version, store.IssuePatch{Title: &title}); !errors.Is(err, ErrMirrored) {
			t.Errorf("%s title edit = %v, want ErrMirrored", by, err)
		}
		if _, err := svc.SetLabels(ctx, by, imported.ID, []string{"x"}); !errors.Is(err, ErrMirrored) {
			t.Errorf("%s label edit = %v, want ErrMirrored", by, err)
		}
	}
	kind := "bug"
	if _, err := svc.Update(ctx, issuestate.Human, imported.ID, imported.Version, store.IssuePatch{Kind: &kind}); err != nil {
		t.Errorf("kind edit = %v", err)
	}
	if _, err := svc.SetLabels(ctx, issuestate.Sync, imported.ID, []string{"synced"}); err != nil {
		t.Errorf("sync label write = %v", err)
	}

	local := mustCreate(t, svc, CreateInput{ProjectID: pid, Title: "local"})
	if Mirrored(local) || len(Editable(local)) != 5 {
		t.Errorf("local: mirrored %v editable %v", Mirrored(local), Editable(local))
	}
	labels := []string{"a"}
	_, err = svc.Update(ctx, issuestate.Human, local.ID, local.Version, store.IssuePatch{Labels: &labels, AddLabels: []string{"b"}})
	wantField(t, err, "labels")
}

// TestCloseDuplicateOf: duplicate_of needs reason duplicate and another issue
// in the same project, and each refusal is a validation error.
func TestCloseDuplicateOf(t *testing.T) {
	svc, st, pid := newService(t)
	ctx := t.Context()
	a := mustCreate(t, svc, CreateInput{ProjectID: pid, Title: "a"})
	b := mustCreate(t, svc, CreateInput{ProjectID: pid, Title: "b"})
	other := &store.Project{Name: "p2", Path: "/p2", DefaultBranch: "main"}
	if err := st.CreateProject(ctx, other); err != nil {
		t.Fatal(err)
	}
	c := mustCreate(t, svc, CreateInput{ProjectID: other.ID, Title: "c"})
	_, err := svc.Close(ctx, issuestate.Human, a.ID, "", &b.ID)
	wantField(t, err, "duplicate_of")
	_, err = svc.Close(ctx, issuestate.Human, a.ID, issuestate.Duplicate, &a.ID)
	wantField(t, err, "duplicate_of")
	_, err = svc.Close(ctx, issuestate.Human, a.ID, issuestate.Duplicate, &c.ID)
	wantField(t, err, "duplicate_of")
	closed, err := svc.Close(ctx, issuestate.Human, a.ID, issuestate.Duplicate, &b.ID)
	if err != nil || closed.DuplicateOfIssueID == nil || *closed.DuplicateOfIssueID != b.ID {
		t.Errorf("close = %+v, %v", closed, err)
	}
}
