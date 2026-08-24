package taskrun

import (
	"log/slog"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// This file is the engine's half of task 026: making the quota stop it already
// recognizes (task 003) outlive the hold it causes.
//
// It writes nothing the engine did not already learn first-hand — there is no
// probe, and `agent.Adapter` gains no method, because no CLI vincent ships has
// a non-interactive quota surface (§9.2, §9.3, §9.7). Nothing here touches
// admission: `internal/scheduler` keeps both caps and its walk unchanged, and
// a near-spent agent is displayed, never withheld.

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
