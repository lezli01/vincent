package taskrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// handoffFileName is $VINCENT_HANDOFF_FILE's name inside the worktree's own
// git directory, beside issueFileName and placed for the same reasons
// (task 130 decision 23.3, task 137.8).
const handoffFileName = "vincent-handoff.json"

// handoffFile is $VINCENT_HANDOFF_FILE's shape (§8.5, task 137.8). Each
// item's `issue` is exactly issueFile, so a workflow's per-issue jq carries
// over from $VINCENT_ISSUE_FILE unchanged; each item marshalled compact is
// one line of `.Handoff.ItemLines`.
type handoffFile struct {
	ID    int64             `json:"id"`
	Grant []string          `json:"grant"`
	Items []handoffFileItem `json:"items"`
}

type handoffFileItem struct {
	Issue    issueFile           `json:"issue"`
	Delivery handoffFileDelivery `json:"delivery"`
	TaskID   *int64              `json:"task_id"`
	Branch   string              `json:"branch"`
	HeadSHA  string              `json:"head_sha"`
}

type handoffFileDelivery struct {
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Summary     string `json:"summary"`
	HeadSHA     string `json:"head_sha"`
	Branch      string `json:"branch"`
	Base        string `json:"base"`
	CloseReason string `json:"close_reason"`
	Proof       string `json:"proof"`
	Ready       bool   `json:"ready"`
}

// handoffData is a handoff task's `.Handoff` and the file that says the same
// thing, built together from one read of the rows.
type handoffData struct {
	ctx  workflow.HandoffContext
	file []byte
}

// loadHandoff reads the handoff whose task is task and builds both shapes;
// ok is false for every task that is not a handoff task. It reads rows only:
// an item's issue is its claim-time snapshot (migration 0048), and an item
// claimed before that migration falls back to the issue row as it is now.
func loadHandoff(ctx context.Context, st *store.Store, task *store.Task) (handoffData, bool, error) {
	if task == nil || task.ParentTaskID != nil {
		return handoffData{}, false, nil
	}
	h, err := st.GetHandoffByTask(ctx, task.ID)
	if errors.Is(err, store.ErrNotFound) {
		return handoffData{}, false, nil
	}
	if err != nil {
		return handoffData{}, false, fmt.Errorf("read handoff: %w", err)
	}
	items, _, err := st.ListHandoffItems(ctx, h.ID)
	if err != nil {
		return handoffData{}, false, fmt.Errorf("read handoff items: %w", err)
	}
	if err := fillHandoffIssues(ctx, st, items); err != nil {
		return handoffData{}, false, err
	}
	d, err := buildHandoff(h, items)
	return d, err == nil, err
}

// fillHandoffIssues gives an item claimed before migration 0048 — which has
// no snapshot — the issue row as it is now.
func fillHandoffIssues(ctx context.Context, st *store.Store, items []*store.HandoffItem) error {
	for _, it := range items {
		if it.Issue != nil {
			continue
		}
		iss, err := st.GetIssue(ctx, it.IssueID)
		if err != nil {
			return fmt.Errorf("read handoff issue: %w", err)
		}
		it.Issue = store.NewIssueSnapshot(iss, time.Now())
	}
	return nil
}

// HandoffFile is $VINCENT_HANDOFF_FILE's content for a handoff and its items,
// byte for byte what the engine writes before an attempt. GET
// /v1/handoffs/{id} carries it, so `vincent workflow render --task` binds a
// handoff task's real `.Handoff` through the file the run would read.
func HandoffFile(ctx context.Context, st *store.Store, h *store.Handoff, items []*store.HandoffItem) ([]byte, error) {
	if err := fillHandoffIssues(ctx, st, items); err != nil {
		return nil, err
	}
	d, err := buildHandoff(h, items)
	if err != nil {
		return nil, err
	}
	return d.file, nil
}

// buildHandoff maps a handoff and its items onto `.Handoff` and the file.
// Every item must carry its issue snapshot.
func buildHandoff(h *store.Handoff, items []*store.HandoffItem) (handoffData, error) {
	grant := make([]string, 0)
	for _, c := range h.Grant() {
		grant = append(grant, string(c))
	}
	f := handoffFile{ID: h.ID, Grant: grant, Items: make([]handoffFileItem, 0, len(items))}
	hc := workflow.HandoffContext{ID: h.ID, Grant: grant}
	raw := make([]json.RawMessage, 0, len(items))
	for _, it := range items {
		issue, _ := snapshotFile(it.Issue)
		fi := handoffFileItem{Issue: issue, TaskID: cloneInt64(it.DeliveryTaskID)}
		ci := workflow.HandoffItemContext{Issue: snapshotContext(it.Issue)}
		if d := it.Delivery; d != nil {
			fi.Delivery = handoffFileDelivery{
				Kind: string(d.Kind), Title: d.Title, Body: d.Body, Summary: d.Summary, HeadSHA: d.HeadSHA,
				Branch: d.Branch, Base: d.Base, CloseReason: d.CloseReason, Proof: d.Proof, Ready: d.IsReady(),
			}
			fi.Branch, fi.HeadSHA = d.Branch, d.HeadSHA
		}
		fd := fi.Delivery
		ci.Delivery = workflow.DeliveryContext{
			Kind: fd.Kind, Title: fd.Title, Body: fd.Body, Summary: fd.Summary, HeadSHA: fd.HeadSHA,
			Branch: fd.Branch, Base: fd.Base, CloseReason: fd.CloseReason, Proof: fd.Proof, Ready: fd.Ready,
		}
		ci.Branch, ci.HeadSHA = fi.Branch, fi.HeadSHA
		if fi.TaskID != nil {
			ci.TaskID = *fi.TaskID
		}
		b, err := json.Marshal(fi)
		if err != nil {
			return handoffData{}, fmt.Errorf("marshal handoff item: %w", err)
		}
		raw = append(raw, b)
		f.Items = append(f.Items, fi)
		hc.Items = append(hc.Items, ci)
	}
	lines, err := workflow.HandoffItemLines(raw)
	if err != nil {
		return handoffData{}, err
	}
	hc.ItemLines = lines
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return handoffData{}, fmt.Errorf("marshal handoff file: %w", err)
	}
	return handoffData{ctx: hc, file: append(b, '\n')}, nil
}

// writeHandoffFile writes a handoff task's file into its worktree's git
// directory before an attempt and returns VINCENT_HANDOFF_FILE's input; the
// zero HandoffEnv for every other task. It follows writeIssueFile exactly:
// rewritten before every attempt as a pure function of the rows, and left
// alone when it already holds the same bytes.
func writeHandoffFile(task *store.Task, d handoffData, ok bool) (workflow.HandoffEnv, error) {
	if !ok {
		return workflow.HandoffEnv{}, nil
	}
	gitDir, err := worktreeGitDir(task.WorktreePath)
	if err != nil {
		return workflow.HandoffEnv{}, err
	}
	path := filepath.Join(gitDir, handoffFileName)
	//nolint:gosec // G304: handoffFileName in the git dir of the task's own worktree
	if cur, rErr := os.ReadFile(path); rErr != nil || !bytes.Equal(cur, d.file) {
		if err := writeFileAtomic(path, d.file); err != nil {
			return workflow.HandoffEnv{}, err
		}
	}
	return workflow.HandoffEnv{File: path}, nil
}
