package issues

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The forge-write guard (task 137 decision 6, task 137.7). Every write
// vincent makes to a forge on a caller's behalf — an imported issue's close,
// reopen or comment, and each pull-request write route — asks ForgeWrite
// first, so "who may write to GitHub" has one definition. A human may. An
// agent may only as a live handoff's task, under a grant a human gave that
// names the capability, and only on a target in the handoff's scope.
// Everything else is ErrForgeWriteNeedsHuman.
//
// Like the marker it reads, it is best-effort and not a privilege boundary
// (spec §16, task 130 decision 17): a full-auto agent has a shell and its
// own credentials. It keeps the honest path — a step's `vincent issue
// close`, an MCP tool call — inside what a human granted.

// Caller is who asks for a forge write: the actor the request reads as, and
// the task it says it is made by (the MCP creator task or the step marker),
// nil when it says none — a chat agent's marker, a plain MCP call, a human.
type Caller struct {
	By     issuestate.Actor
	TaskID *int64
}

// Target is what a forge write is aimed at: an issue for the issue
// capabilities, or the task whose linked pull request is written for the
// pull ones. Exactly one is set.
type Target struct {
	IssueID int64
	TaskID  int64
}

// Verdict is the guard's answer when it allows a write. By is the actor the
// write is attributed to — the caller's own for a human, issuestate.Handoff
// for a granted agent — and HandoffID the granting handoff, 0 otherwise.
type Verdict struct {
	By        issuestate.Actor
	HandoffID int64
}

// ForgeWrite decides whether c may make a forge write of capability on t.
// A non-agent caller is allowed as itself. An agent is allowed as
// issuestate.Handoff only when all three hold: its task is the task of a
// handoff in store.HandoffLive; that handoff's grant names capability; and
// t is in scope — an item's issue for an issue capability, the handoff task
// itself or an item's delivering task for a pull one. Any other agent is
// refused ErrForgeWriteNeedsHuman. It does not ask whether the target writes
// back at all; IssueWriteAs does, for the issue routes, where a local
// issue needs no grant.
func (s *Service) ForgeWrite(ctx context.Context, c Caller, capability store.Capability, t Target) (Verdict, error) {
	if c.By != issuestate.Agent {
		return Verdict{By: c.By}, nil
	}
	refuse := func(why string) (Verdict, error) {
		return Verdict{}, fmt.Errorf("%s on %s: %s: %w", capability, grantee(t), why, ErrForgeWriteNeedsHuman)
	}
	if c.TaskID == nil {
		return refuse("the caller is not a handoff's task")
	}
	h, err := s.st.GetHandoffByTask(ctx, *c.TaskID)
	if errors.Is(err, store.ErrNotFound) {
		return refuse(fmt.Sprintf("task %d is not a handoff's task", *c.TaskID))
	}
	if err != nil {
		return Verdict{}, err
	}
	if h.State != store.HandoffLive {
		return refuse(fmt.Sprintf("handoff %d is %s, not live", h.ID, h.State))
	}
	if !slices.Contains(h.Grant(), capability) {
		return refuse(fmt.Sprintf("handoff %d was not granted %s", h.ID, capability))
	}
	items, _, err := s.st.ListHandoffItems(ctx, h.ID)
	if err != nil {
		return Verdict{}, err
	}
	if !inScope(h, items, capability, t) {
		return refuse(fmt.Sprintf("%s is outside handoff %d", grantee(t), h.ID))
	}
	return Verdict{By: issuestate.Handoff, HandoffID: h.ID}, nil
}

// IssueWriteAs is ForgeWrite for the issue routes: it resolves the actor a
// close, reopen (capability store.CapIssueClose) or comment
// (store.CapIssueComment) on issueID is made as, and the context to make it
// with. Only an agent's write that would reach the forge consults the grant
// — a state change of an issue that writes back, or a comment on a
// live-remote thread. Any other write keeps the caller's actor, so a local
// issue needs no grant and an ungranted agent's write to one stays `agent`.
// A granted write is made as issuestate.Handoff, with the handoff's id on
// ctx for the issue event (store.WithIssueHandoff).
func (s *Service) IssueWriteAs(ctx context.Context, c Caller, capability store.Capability, issueID int64) (issuestate.Actor, context.Context, error) {
	if c.By != issuestate.Agent {
		return c.By, ctx, nil
	}
	iss, err := s.st.GetIssue(ctx, issueID)
	if err != nil {
		return "", ctx, err
	}
	reaches := WritesBack(iss)
	if capability == store.CapIssueComment {
		reaches = ThreadMirrored(iss)
	}
	if !reaches {
		return c.By, ctx, nil
	}
	v, err := s.ForgeWrite(ctx, c, capability, Target{IssueID: issueID})
	if err != nil {
		return "", ctx, err
	}
	return v.By, store.WithIssueHandoff(ctx, v.HandoffID), nil
}

// inScope reports whether t is a target handoff h's items cover for
// capability.
func inScope(h *store.Handoff, items []*store.HandoffItem, capability store.Capability, t Target) bool {
	issueCap := capability == store.CapIssueClose || capability == store.CapIssueComment
	if issueCap {
		return t.IssueID != 0 && slices.ContainsFunc(items, func(it *store.HandoffItem) bool {
			return it.IssueID == t.IssueID
		})
	}
	if t.TaskID == 0 {
		return false
	}
	return t.TaskID == h.TaskID || slices.ContainsFunc(items, func(it *store.HandoffItem) bool {
		return it.DeliveryTaskID != nil && *it.DeliveryTaskID == t.TaskID
	})
}

func grantee(t Target) string {
	if t.IssueID != 0 {
		return fmt.Sprintf("issue %d", t.IssueID)
	}
	return fmt.Sprintf("task %d's pull request", t.TaskID)
}
