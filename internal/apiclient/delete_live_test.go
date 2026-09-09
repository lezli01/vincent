package apiclient_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// TestDeleteRoundTrip wires the client to the real handlers and asserts the
// permanent delete survives the wire — the response shape, the refusal's
// `details.reason`, and the two new listing bounds. The server DTOs are
// unexported, so nothing but a live round-trip catches `delete_branch` or
// `archived_since` renamed on one side.
func TestDeleteRoundTrip(t *testing.T) {
	h := newCreateHarness(t)
	ctx := t.Context()

	mk := func(title string, state store.TaskState) *store.Task {
		task := &store.Task{
			ProjectID:        h.projectID,
			Title:            title,
			WorkflowName:     "adhoc",
			WorkflowSnapshot: "steps: []",
			BaseBranch:       "main",
			BranchName:       "vincent/" + title,
			State:            state,
		}
		if err := h.store.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask(%s): %v", title, err)
		}
		return task
	}
	live := mk("live", store.TaskDone)
	gone := mk("gone", store.TaskDone)
	if _, _, err := h.store.TransitionTask(ctx, gone.ID,
		store.TaskDone, store.TaskArchived, store.TaskChange{}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// The archived listing, and the two bounds that narrow it.
	archived, err := h.client.ListTasks(ctx, apiclient.ListTasksOptions{Archived: apiclient.ArchivedOnly})
	if err != nil {
		t.Fatalf("ListTasks archived: %v", err)
	}
	if len(archived) != 1 || archived[0].ID != gone.ID {
		t.Fatalf("archived listing: %v, want just task %d", archived, gone.ID)
	}
	none, err := h.client.ListTasks(ctx, apiclient.ListTasksOptions{
		Archived: apiclient.ArchivedOnly, ArchivedBefore: time.Now().AddDate(-1, 0, 0),
	})
	if err != nil {
		t.Fatalf("ListTasks archived_before: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("archived_before a year ago returned %d rows", len(none))
	}
	since, err := h.client.ListTasks(ctx, apiclient.ListTasksOptions{
		Archived: apiclient.ArchivedOnly, ArchivedSince: time.Now().AddDate(-1, 0, 0),
	})
	if err != nil {
		t.Fatalf("ListTasks archived_since: %v", err)
	}
	if len(since) != 1 {
		t.Fatalf("archived_since a year ago returned %d rows, want 1", len(since))
	}

	// The refusal, decoded as the typed 409 a client branches on.
	if _, err := h.client.DeleteTask(ctx, live.ID, false); err == nil {
		t.Fatal("deleting a done task succeeded")
	} else {
		reason, ok := apiclient.DeleteRefused(err)
		if !ok {
			t.Fatalf("the refusal did not decode as one: %v", err)
		}
		if reason != store.DeleteRefusedNotArchived {
			t.Fatalf("reason %q, want %q", reason, store.DeleteRefusedNotArchived)
		}
	}

	// The delete itself. The task never ran, so no branch was ever cut and
	// the outcome is the zero value rather than a judgement.
	branch, err := h.client.DeleteTask(ctx, gone.ID, true)
	if err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if branch.Result != "" {
		t.Fatalf("a task with no branch of its own reported %q", branch.Result)
	}
	if _, err := h.store.GetTask(ctx, gone.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the task survived the delete: %v", err)
	}
}

// TestListChatsOptionsRoundTrip is the chat half: the options struct that
// replaced two bare arguments has to reach the daemon as the same four
// parameters GET /v1/tasks takes.
func TestListChatsOptionsRoundTrip(t *testing.T) {
	h := newCreateHarness(t)
	ctx := t.Context()

	c := &store.Chat{
		ProjectID: h.projectID, Title: "ended", Agent: "claude",
		BaseBranch: "main", Branch: "vincent/chat-1", PermissionMode: "full_auto",
	}
	if err := h.store.CreateChat(ctx, c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	live, err := h.client.ListChats(ctx, apiclient.ListChatsOptions{})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("the default listing returned %d chats, want 1", len(live))
	}
	paged, err := h.client.ListChats(ctx, apiclient.ListChatsOptions{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("ListChats paged: %v", err)
	}
	if len(paged) != 0 {
		t.Fatalf("offset 1 of a one-row listing returned %d chats", len(paged))
	}
	bounded, err := h.client.ListChats(ctx, apiclient.ListChatsOptions{
		ArchivedSince: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("ListChats archived_since: %v", err)
	}
	if len(bounded) != 0 {
		t.Fatalf("archived_since an hour from now returned %d chats", len(bounded))
	}
	// A live chat cannot be deleted, and the refusal decodes.
	if _, err := h.client.DeleteChat(ctx, c.ID, false); err == nil {
		t.Fatal("deleting a live chat succeeded")
	} else if reason, ok := apiclient.DeleteRefused(err); !ok || reason != store.DeleteRefusedNotArchived {
		t.Fatalf("reason %q (typed: %v), want %q", reason, ok, store.DeleteRefusedNotArchived)
	}
}
