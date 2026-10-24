package taskrun

import (
	"context"
	"log/slog"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// deliveryAtDone is the delivery a task's `→ done` writes (§5.3, task 137
// decision 3). It runs in Runner.transition, before the store's transaction,
// for the reason mergeBackFor does: the questions it asks are git's, and
// SQLite's write lock is never held across a subprocess (task 134.14-b).
func (r *Runner) deliveryAtDone(ctx context.Context, task *store.Task, log *slog.Logger) *store.Delivery {
	// The actor's copy predates whatever the run's steps recorded through
	// `vincent task deliver`, which writes the row directly; read it back.
	if fresh, err := r.deps.Store.GetTask(ctx, task.ID); err == nil {
		task = fresh
	} else {
		log.Warn("delivery: re-read task", "error", err)
	}
	var d *store.Delivery
	if project, err := r.deps.Store.GetProject(ctx, task.ProjectID); err != nil {
		// Without the repository nothing can be read from git, but the run
		// still finished: record what the task itself says, and say so.
		log.Error("delivery: load project", "error", err)
		d = finishDelivery(task.Delivery.Clone(), task, "", time.Now())
	} else {
		d = buildDelivery(ctx, r.deps.Store, r.deps.Worktrees, project, task, time.Now(), log)
	}
	stampReadiness(ctx, r.deps.Store, task, d, log)
	return d
}

// stampReadiness writes the readiness a task's `→ done` decides (task 137
// decision 13), so every delivery written from here on stores `ready`; only
// a legacy one, which BackfillDeliveries fills and never stamps, reads ready
// by its absence.
//
// The answer is the task's own workflow's hand_off, read from its snapshot —
// the workflow it ran, not whatever the live file says now. A step's
// `vincent task deliver --ready` (stored as ready by task) stands under
// either mode; without one `auto` is ready by the workflow and `explicit` is
// not ready, waiting for a human's `vincent issue ready`. An `auto` workflow
// has no way to say not ready.
//
// A merge-back runs __merge_back, which has no hand_off of its own: it
// delivers its source side task's work, so it carries the source's
// readiness with the rest of the source's delivery, and is ready by the
// workflow when the source has none to give.
func stampReadiness(ctx context.Context, st *store.Store, task *store.Task, d *store.Delivery, log *slog.Logger) {
	ready := func(r bool, by store.ReadyBy) { d.Ready, d.ReadyBy = &r, by }
	if task.MergeSourceTaskID != nil {
		src, err := st.GetTask(ctx, *task.MergeSourceTaskID)
		if err != nil {
			log.Warn("delivery: merge-back source unreadable; ready by the workflow", "error", err)
		}
		if err == nil && src.Delivery != nil && src.Delivery.Ready != nil {
			ready(*src.Delivery.Ready, src.Delivery.ReadyBy)
			return
		}
		ready(true, store.ReadyByWorkflow)
		return
	}
	if task.Delivery != nil && task.Delivery.Ready != nil && *task.Delivery.Ready {
		ready(true, store.ReadyByTask)
		return
	}
	mode := workflow.HandOffAuto
	if wf, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{}); err != nil || wf == nil {
		log.Warn("delivery: workflow snapshot unparseable; handing off as auto", "error", err)
	} else {
		mode = wf.HandOffMode()
	}
	if mode == workflow.HandOffExplicit {
		ready(false, "")
		return
	}
	ready(true, store.ReadyByWorkflow)
}

// buildDelivery is the delivery task finished with: the one its run recorded,
// with what it left empty filled in, or an implicit one built from the task
// and its branch. The engine calls it at `→ done`; BackfillDeliveries calls
// it for tasks that finished before deliveries existed.
//
// A merge-back (task 134.14) delivers its source side task's work. Its own
// title is "Merge task N into issue #M", which says nothing a handoff could
// put in a pull request, so it carries the source's delivery and only the
// commit — the merge — is its own.
func buildDelivery(
	ctx context.Context, st *store.Store, wt *worktree.Manager, project *store.Project,
	task *store.Task, now time.Time, log *slog.Logger,
) *store.Delivery {
	d := task.Delivery.Clone()
	if task.MergeSourceTaskID != nil {
		switch {
		case d == nil:
			d = sourceDelivery(ctx, st, task, log)
		case readinessOnly(d):
			// A human's mark on a legacy merge-back holds no work to deliver:
			// the source's text, with the human's decision kept.
			ready, by := d.Ready, d.ReadyBy
			d = sourceDelivery(ctx, st, task, log)
			d.Ready, d.ReadyBy = ready, by
		}
		// The source's branch and base are the side branch and the issue's
		// main branch. What the issue delivers is the main branch, onto the
		// base the main line was cut from, at the merge commit.
		d.Branch, d.Base, d.HeadSHA = "", issueMainBase(ctx, st, task, project), ""
	}
	tip := ""
	if task.BranchName != "" {
		var err error
		if tip, err = wt.BranchTip(ctx, project.Path, task.BranchName); err != nil {
			log.Warn("delivery: branch tip unreadable", "branch", task.BranchName, "error", err)
			tip = ""
		}
	}
	d = finishDelivery(d, task, tip, now)
	if d.Kind == "" {
		d.Kind = implicitKind(ctx, wt, project.Path, task, d.Base, tip, log)
	}
	return d
}

// sourceDelivery is what a merge-back delivers when its own run recorded
// nothing: a copy of its source side task's delivery, or an implicit one
// titled from the source, or — with the source gone — from the merge-back.
func sourceDelivery(ctx context.Context, st *store.Store, task *store.Task, log *slog.Logger) *store.Delivery {
	src, err := st.GetTask(ctx, *task.MergeSourceTaskID)
	switch {
	case err != nil:
		log.Warn("delivery: merge-back source unreadable; delivering the merge itself", "error", err)
		return &store.Delivery{Implicit: true, Title: task.Title, Body: task.Description}
	case src.Delivery != nil:
		return src.Delivery.Clone()
	default:
		return &store.Delivery{Implicit: true, Title: src.Title, Body: src.Description}
	}
}

// readinessOnly reports whether d is the delivery a human's `vincent issue
// ready`/`unready` writes on a legacy task that had none (task 137 decision
// 13): implicit, with a ready mark and no kind or text. The backfill gives it
// the text its `→ done` would have, and keeps the mark.
func readinessOnly(d *store.Delivery) bool {
	return d != nil && d.Implicit && d.Kind == "" && d.Title == ""
}

// finishDelivery fills what d leaves empty from the task — the title and
// description for an implicit one, the branch, the base and the head — and
// stamps it. A nil d becomes an implicit delivery.
func finishDelivery(d *store.Delivery, task *store.Task, tip string, now time.Time) *store.Delivery {
	if d == nil {
		d = &store.Delivery{Implicit: true, Title: task.Title, Body: task.Description}
	}
	if readinessOnly(d) {
		d.Title, d.Body = task.Title, task.Description
	}
	if d.Branch == "" {
		d.Branch = task.BranchName
	}
	if d.Base == "" {
		d.Base = task.BaseBranch
	}
	if d.HeadSHA == "" {
		d.HeadSHA = tip
	}
	if d.RecordedAt.IsZero() {
		d.RecordedAt = now
	}
	return d
}

// implicitKind is what a run that recorded no kind delivered: `change` when
// its branch holds commits its base does not, `none` otherwise. A side task's
// base is the issue's main branch it forked from (134.13), so its tip equal
// to its recorded base_sha is the same answer without asking git. A branch
// that cannot be read delivered nothing that could be integrated.
func implicitKind(
	ctx context.Context, wt *worktree.Manager, repo string, task *store.Task, base, tip string, log *slog.Logger,
) store.DeliveryKind {
	if tip == "" {
		return store.DeliveryNone
	}
	if task.IssueWorktree == store.IssueWorktreeSide && task.BaseSHA != "" && tip == task.BaseSHA {
		return store.DeliveryNone
	}
	merged, err := wt.BranchMergedInto(ctx, repo, task.BranchName, base)
	if err != nil {
		// An unanswerable question is not evidence of nothing: a branch the
		// base cannot be compared to may still hold the work.
		log.Warn("delivery: could not tell whether the branch is in its base",
			"branch", task.BranchName, "base", base, "error", err)
		return store.DeliveryChange
	}
	if merged {
		return store.DeliveryNone
	}
	return store.DeliveryChange
}

// issueMainBase is the base the issue's main line was cut from: the base
// branch of the first main-role task that carried the merge-back's branch. A
// merge-back's own base_branch is that main branch (134.14), which is the
// wrong answer for anything delivered onward, and the project's default
// branch stands in when no main task is left to ask.
func issueMainBase(ctx context.Context, st *store.Store, task *store.Task, project *store.Project) string {
	if task.IssueID != nil {
		if firstID, _, err := st.IssueMainBranchLine(ctx, *task.IssueID, task.BranchName); err == nil && firstID != 0 {
			if first, err := st.GetTask(ctx, firstID); err == nil && first.BaseBranch != "" &&
				first.MergeSourceTaskID == nil {
				return first.BaseBranch
			}
		}
	}
	return project.DefaultBranch
}

// BackfillDeliveries gives every finished root task of an issue that has no
// delivery the one its `→ done` would have written, had deliveries existed
// (task 137 decision 3). Those are the tasks that finished before migration
// 0045. It is idempotent: the write is guarded on the column still being
// NULL, so a second run changes nothing and a delivery recorded meanwhile is
// never overwritten. It returns how many it filled.
//
// The daemon runs it once at startup, after recovery and in the background:
// it asks git a question or two per task, and nothing waits on the answer.
func BackfillDeliveries(ctx context.Context, deps Deps, log *slog.Logger) (int, error) {
	tasks, err := deps.Store.ListTasksMissingDelivery(ctx)
	if err != nil {
		return 0, err
	}
	projects := map[int64]*store.Project{}
	filled := 0
	for _, t := range tasks {
		if ctx.Err() != nil {
			return filled, ctx.Err()
		}
		p, ok := projects[t.ProjectID]
		if !ok {
			if p, err = deps.Store.GetProject(ctx, t.ProjectID); err != nil {
				log.Warn("delivery backfill: load project", "task_id", t.ID, "error", err)
				continue
			}
			projects[t.ProjectID] = p
		}
		at := time.Now()
		if t.FinishedAt != nil {
			at = *t.FinishedAt
		}
		d := buildDelivery(ctx, deps.Store, deps.Worktrees, p, t, at, log)
		ok, err := deps.Store.FillTaskDelivery(ctx, t.ID, d)
		if err != nil {
			return filled, err
		}
		if ok {
			filled++
		}
	}
	return filled, nil
}
