package taskrun

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// This file is the engine's half of task 026: making the quota stop it already
// recognizes (task 003) outlive the hold it causes.
//
// It writes nothing the engine did not already learn first-hand: everything
// here is an *observation*, a window this daemon watched close. Reported
// readings — the ones an adapter's own surface can now answer with (§9.6,
// task 082) — do not pass through the engine at all; they live in the catalog
// cache, and `agent.Adapter` still gains no method for them, because an
// adapter that has no quota surface must not grow a stub saying so.
//
// An observation is also a brake, since task 106: usageWall is what an agent
// spawn consults first, so every other task on an adapter whose window vincent
// watched close waits the window out instead of spending a process spawn
// rediscovering it. The brake lives here and not in `internal/scheduler`,
// which keeps both caps and its walk unchanged and never parses a snapshot
// (task 081): a queued task has no stored adapter, and only the engine, at the
// spawn, knows which one the step at the cursor resolves to (task 106
// decision 1). Reported readings are still displayed, never withheld.

// recordUsageLimit stores the reset a quota stop just taught us, so the fact
// outlives the hold it causes. Before task 026 the reset lived only in the
// held task's `admit_not_before`, which any transition out of `queued` clears
// (task 003 decision 1) — so the board went quiet exactly when the window was
// still shut.
//
// A failed write is logged, never fatal, for the same reason emit's is: losing
// a display fact must not lose the work. An empty adapter name is a no-op —
// an aggregated outcome from a `parallel` group whose interrupted lane is not
// an agent step has nothing to attribute.
func (r *Runner) recordUsageLimit(agentName string, resetsAt time.Time, reported bool, log *slog.Logger) {
	if agentName == "" {
		return
	}
	q := &store.AgentQuota{
		Agent:            agentName,
		ObservedAt:       r.now().UTC(),
		ResetsAt:         resetsAt.UTC(),
		ResetsAtReported: reported,
		Source:           store.QuotaSourceObserved,
	}
	changed, err := r.deps.Store.UpsertAgentQuota(r.persistCtx(), q)
	if err != nil {
		log.Error("record agent quota", "agent", agentName, "error", err)
		return
	}
	if changed {
		log.Info("agent usage window recorded as spent",
			"agent", agentName,
			"resets_at", q.ResetsAt.Format(time.RFC3339),
			"reported_by_cli", reported)
	}
}

// clearUsageLimit retires an observation a successful run has just disproved.
//
// It matters most where the reset was never reported: a hold of
// `now + usage_limit_recheck_interval` is an estimate, and an agent step that
// completes five minutes in is first-hand evidence the window reopened. The
// store only deletes an observation *older* than ranAt, so a run that started
// before a fresh wall cannot erase the wall it never saw.
//
// The evidence is narrow and so is the retirement (task 082 decision 5): a
// completed step disproves a wall vincent watched, and says nothing about a
// percentage a vendor reported. The store deletes `observed` rows only.
func (r *Runner) clearUsageLimit(agentName string, ranAt time.Time, log *slog.Logger) {
	if agentName == "" {
		return
	}
	cleared, err := r.deps.Store.ClearAgentQuota(r.persistCtx(), agentName, ranAt.UTC())
	if err != nil {
		log.Error("clear agent quota", "agent", agentName, "error", err)
		return
	}
	if cleared {
		log.Info("agent usage window reopened; observation cleared", "agent", agentName)
	}
}

// usageWall returns the observed window an agent spawn on agentName would walk
// straight into, or nil when the spawn should go ahead (task 106).
//
// Three filters, each a decision rather than a convenience:
//
//   - The mode is read here, at the check, never cached, so a hot reload
//     (§12.3) reaches the next spawn (task 091 decision 6). It applies only
//     where the stop itself would wait: `never` holds nothing, and
//     `reported_only` holds only on a reset the CLI named. An operator who
//     does not trust the classifier must not have one wrong match spread to
//     every task on the adapter — and letting the next task spawn is what
//     retires a wrong observation (task 026 decision 3). Decision 2.
//   - Only `observed` rows count, even though the table holds nothing else
//     today, so a future writer of reported readings cannot change admission
//     behaviour by accident (task 082, decision 3).
//   - A read that fails is logged and the spawn goes ahead. A display table's
//     read failure must not stall work, which is recordUsageLimit's rule for
//     its write (decision 4).
func (r *Runner) usageWall(ctx context.Context, agentName string, log *slog.Logger) *store.AgentQuota {
	if agentName == "" {
		return nil
	}
	mode := r.deps.Config().UsageLimitAutoContinue
	if mode == config.UsageLimitNever {
		return nil
	}
	q, err := r.deps.Store.GetAgentQuota(ctx, agentName)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		log.Error("read agent quota; spawning anyway", "agent", agentName, "error", err)
		return nil
	}
	if q.Source != store.QuotaSourceObserved || !q.Spent(r.now()) {
		return nil
	}
	if mode == config.UsageLimitReportedOnly && !q.ResetsAtReported {
		return nil
	}
	return q
}
