package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The issue writes (task 130.12) against the **real** API handlers over
// httptest: create, edit, a stale edit, close, reopen and delete, each from
// the screen a person would use.

func TestIssueWritesAgainstTheRealAPI(t *testing.T) {
	h := newNewTaskLiveHarness(t)
	ctx := context.Background()
	list := issuesListView(t, h)
	detail := issueDetailView(t, h)

	seed, err := h.st.CreateIssue(ctx, store.NewIssue{
		ProjectID: h.projectID, Title: "Seed", Kind: "task", Author: "human",
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	h.send(selectViewMsg{id: viewIssues})
	h.p.until(10*time.Second, "the issues list", func() bool { return list.loaded && len(list.rows()) == 1 })

	// n, every row filled, ctrl+s: the create carries an Idempotency-Key and
	// the new issue opens on its own screen.
	h.sendKey(keyPress("n"))
	f := list.w.form
	if f == nil || f.projectID != h.projectID {
		t.Fatalf("n did not open a create form on the row's project: %+v", f)
	}
	h.p.until(10*time.Second, "the form's project list", func() bool { return len(f.projects) > 0 })
	h.sendKey(keyPress("enter"))
	h.typeText("Crash on start")
	h.sendKey(keyPress("enter"))
	f.body.SetValue("It crashes.")
	f.labels = []string{"p1"}
	f.kind, f.priority = "bug", 2
	key := f.idemKey
	h.sendKey(keyPress("ctrl+s"))
	h.p.until(10*time.Second, "the new issue's screen", func() bool {
		return h.m.active == viewIssue && detail.loaded && detail.issue.Title == "Crash on start"
	})
	created := detail.issue
	if created.Body != "It crashes." || !slices.Equal(created.Labels, []string{"p1"}) ||
		created.Kind != "bug" || created.Priority != 2 {
		t.Fatalf("created %+v, want every row the form filled", created)
	}
	if k, err := h.st.GetIssueIdempotencyKey(ctx, "POST", "/v1/issues", key); err != nil || k == nil {
		t.Fatalf("the create's Idempotency-Key %q was not recorded: %v", key, err)
	}

	// i on the detail: an edit of the title alone leaves every other field
	// as the daemon has it, and bumps the version once.
	h.sendKey(keyPress("i"))
	ef := detail.w.form
	if ef == nil || ef.creating() {
		t.Fatal("i did not open the edit form")
	}
	if p := ef.patch(); p.Version != created.Version || p.Title != nil || p.Kind != nil {
		t.Fatalf("an untouched form's patch is %+v", p)
	}
	ef.title.SetValue("Crash on start (macOS)")
	if p := ef.patch(); p.Title == nil || p.Body != nil || p.Kind != nil || p.Priority != nil ||
		p.AddLabels != nil || p.RemoveLabels != nil {
		t.Fatalf("patch = %+v, want the title alone", p)
	}
	h.sendKey(keyPress("ctrl+s"))
	h.p.until(10*time.Second, "the saved title", func() bool {
		return detail.w.form == nil && detail.issue.Title == "Crash on start (macOS)"
	})
	got, err := h.st.GetIssue(ctx, created.ID)
	if err != nil || got.Kind != "bug" || got.Body != "It crashes." || got.Version != created.Version+1 {
		t.Fatalf("after the edit: %+v, %v", got, err)
	}

	// A stale edit: someone else changes the kind under an open form. The
	// 409 is shown, R rebases onto the current issue, and the user's title
	// survives while the other writer's kind is kept.
	h.sendKey(keyPress("i"))
	ef = detail.w.form
	ef.title.SetValue("Crash on every start")
	other := "chore"
	if _, err := h.st.UpdateIssue(ctx, created.ID, got.Version, store.IssuePatch{Kind: &other}, issuestate.Human); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	h.sendKey(keyPress("ctrl+s"))
	h.p.until(10*time.Second, "the conflict", func() bool { return ef.stale && !ef.saving })
	if !strings.Contains(ef.err, "changed since") {
		t.Fatalf("the conflict says %q", ef.err)
	}
	h.sendKey(keyPress("R"))
	h.p.until(10*time.Second, "the rebase", func() bool { return !ef.stale && ef.original.Kind == "chore" })
	if ef.title.Value() != "Crash on every start" || ef.kind != "chore" {
		t.Fatalf("after R: title %q kind %q", ef.title.Value(), ef.kind)
	}
	h.sendKey(keyPress("ctrl+s"))
	h.p.until(10*time.Second, "the rebased save", func() bool {
		return detail.w.form == nil && detail.issue.Title == "Crash on every start" && detail.issue.Kind == "chore"
	})

	// X on the detail: close as a duplicate of the seed issue.
	h.p.until(10*time.Second, "the detail's actions", func() bool {
		return slices.Contains(detail.issue.AvailableActions, "close")
	})
	h.sendKey(keyPress("X"))
	if detail.w.act == nil || detail.w.act.stage != iaReason {
		t.Fatal("X did not ask for a close reason")
	}
	h.sendKey(keyPress("down"))
	h.sendKey(keyPress("down"))
	h.sendKey(keyPress("enter"))
	act := detail.w.act
	h.p.until(10*time.Second, "the duplicate targets", func() bool { return act.pick != nil })
	h.sendKey(keyPress("down"))
	h.sendKey(keyPress("enter"))
	h.p.until(10*time.Second, "the close", func() bool { return detail.issue.State == "closed" })
	if detail.issue.CloseReason != "duplicate" || detail.issue.DuplicateOf == nil || *detail.issue.DuplicateOf != seed.ID {
		t.Fatalf("closed as %q of %v, want duplicate of #%d", detail.issue.CloseReason, detail.issue.DuplicateOf, seed.ID)
	}

	// X again: reopen, the one action a closed issue offers.
	h.p.until(10*time.Second, "the reopen action", func() bool {
		return slices.Equal(detail.issue.AvailableActions, []string{"reopen"})
	})
	h.sendKey(keyPress("X"))
	// The prompt too: its result is routed to the active view, and landing
	// on the list after the switch below would close the list's own prompt.
	h.p.until(10*time.Second, "the reopen", func() bool { return detail.issue.State == "open" && detail.w.act == nil })

	// selectCreated waits for the list to show the issue as the store last
	// wrote it, then selects it. Every write moves an issue to the top and a
	// re-list puts the cursor back on `selected`, so a list still showing an
	// earlier write, or a cursor set without `selected`, lands a key on the
	// other issue once the late re-list arrives.
	selectCreated := func() {
		t.Helper()
		cur, err := h.st.GetIssue(ctx, created.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		h.p.until(10*time.Second, "the list", func() bool {
			if h.m.active != viewIssues || list.w.act != nil || len(list.rows()) != 2 {
				return false
			}
			for _, row := range list.rows() {
				if row.issue.ID == created.ID && row.issue.Version == cur.Version {
					return true
				}
			}
			return false
		})
		for i, row := range list.rows() {
			if row.issue.ID == created.ID {
				list.cursor, list.selected = i, row.issue.ID
			}
		}
	}

	// Close as not planned and as a duplicate with no target, from the list:
	// X there reads the issue for its available_actions first.
	for _, c := range []struct {
		downs  int
		reason string
	}{{1, "not_planned"}, {2, "duplicate"}, {0, "completed"}} {
		h.send(selectViewMsg{id: viewIssues})
		selectCreated()
		h.sendKey(keyPress("X"))
		h.p.until(10*time.Second, "the reason prompt", func() bool { return list.w.act != nil && list.w.act.stage == iaReason })
		for range c.downs {
			h.sendKey(keyPress("down"))
		}
		h.sendKey(keyPress("enter"))
		if c.reason == "duplicate" {
			a := list.w.act
			h.p.until(10*time.Second, "the duplicate targets", func() bool { return a.pick != nil })
			h.sendKey(keyPress("enter")) // (no target)
		}
		var cur *store.Issue
		h.p.until(10*time.Second, "the close as "+c.reason, func() bool {
			cur, _ = h.st.GetIssue(ctx, created.ID)
			return cur != nil && string(cur.State) == "closed" && list.w.act == nil
		})
		if string(cur.CloseReason) != c.reason || cur.DuplicateOfIssueID != nil {
			t.Fatalf("closed as %q of %v, want %s with no target", cur.CloseReason, cur.DuplicateOfIssueID, c.reason)
		}
		if _, err := h.st.TransitionIssue(ctx, created.ID, issuestate.Reopen, "", nil, issuestate.Human); err != nil {
			t.Fatalf("reopen: %v", err)
		}
	}

	// D on the list asks, then deletes.
	selectCreated()
	h.sendKey(keyPress("D"))
	h.sendKey(keyPress("y"))
	h.p.until(10*time.Second, "the delete", func() bool { return len(list.rows()) == 1 })
	if _, err := h.st.GetIssue(ctx, created.ID); err == nil {
		t.Fatal("the issue is still in the store")
	}
}

// An imported issue's form takes its read-only rows from the DTO's
// `editable`: title, description and labels are marked mirrored and refuse
// to edit, while kind and priority save. Its close says it is written to
// GitHub (130.10). Not parallel: a step's environment would mark the client
// an agent, and an agent's close of an imported issue is refused.
func TestImportedIssueFormAgainstTheRealAPI(t *testing.T) {
	t.Setenv("VINCENT_TASK_ID", "")
	t.Setenv(apiclient.EnvChatID, "")
	h := newNewTaskLiveHarness(t)
	ctx := context.Background()
	imported, _, err := h.st.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: h.projectID, Provider: "github", RemoteKey: "I_41", Repo: "octo/web", Number: 41,
		URL: "https://github.com/octo/web/issues/41", RemoteJSON: `{"state":"open"}`,
		Title: "Dark mode", Author: "octocat", State: issuestate.Open,
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	detail := issueDetailView(t, h)
	h.send(openIssueMsg{id: imported.ID})
	h.p.until(10*time.Second, "the imported issue", func() bool { return detail.loaded && detail.issue.ID == imported.ID })

	h.sendKey(keyPress("i"))
	f := detail.w.form
	if f == nil {
		t.Fatal("i did not open the form")
	}
	if out := detail.render(160, 40); strings.Count(out, "mirrored from GitHub") != 3 {
		t.Fatalf("want three mirrored rows:\n%s", out)
	}
	h.sendKey(keyPress("enter")) // the title row
	if f.editing {
		t.Fatal("a mirrored title opened for editing")
	}
	f.kind, f.priority = "bug", 1
	h.sendKey(keyPress("ctrl+s"))
	h.p.until(10*time.Second, "the saved kind and priority", func() bool {
		return detail.w.form == nil && detail.issue.Kind == "bug" && detail.issue.Priority == 1
	})

	h.p.until(10*time.Second, "the close action", func() bool {
		return slices.Contains(detail.issue.AvailableActions, "close")
	})
	h.sendKey(keyPress("X"))
	h.sendKey(keyPress("enter")) // completed
	out := strings.Join(detail.w.act.lines(400), "\n")
	if !strings.Contains(out, issueWritesBack) {
		t.Fatalf("the imported close does not say it is written to GitHub:\n%s", out)
	}
	h.sendKey(keyPress("y"))
	h.p.until(10*time.Second, "the close", func() bool { return detail.issue.State == "closed" })
}
