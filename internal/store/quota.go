package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Quota sources as `GET /v1/agents` reports them (spec §9.6, task 026).
const (
	// QuotaSourceObserved is a window vincent watched close: an agent step
	// the CLI stopped with `usage_limit`. It is the only source this table
	// stores — a *reported* reading lives in the catalog cache and never
	// reaches SQLite (task 082 decision 4), so the column's job is to mark
	// which rows the §14 retirement rule applies to, and it applies to these.
	QuotaSourceObserved = "observed"
)

// AgentQuota is what the daemon knows about one adapter's usage window: the
// last quota stop it saw first-hand, and the reset the engine acted on
// (task 026, §14). One row per adapter — this is current state, not history.
type AgentQuota struct {
	Agent      string
	ObservedAt time.Time
	ResetsAt   time.Time
	// ResetsAtReported distinguishes a fact from an estimate: true when the
	// CLI named the reset, false when `usage_limit_recheck_interval` (§12.3)
	// supplied it. It is the difference between rendering `→ 14:20` and
	// `≈ 14:20`, and a computed 15-minute guess must not be shown as
	// something the CLI stated.
	ResetsAtReported bool
	Source           string
}

const agentQuotaColumns = `agent, observed_at, resets_at, resets_at_reported, source`

// Spent reports whether the observed window is still shut as of now. It is
// derived rather than stored: a lapsed reset does not delete the row, so the
// observation survives as "last spent at" context after the window reopens.
func (q AgentQuota) Spent(now time.Time) bool { return now.Before(q.ResetsAt) }

// UpsertAgentQuota records an observation of a spent window, writing the
// durable `agent.quota_changed` event in the same transaction when — and only
// when — something actually changed (§13.3). The bool reports that.
//
// The upsert is **monotonic**: an observation older than the stored one is
// discarded rather than applied, so two actors hitting the same wall in the
// same second cannot make the state go backwards. A re-observation identical
// to what is stored writes no event; a board that refetches on the event must
// not be woken by news it already has.
func (s *Store) UpsertAgentQuota(ctx context.Context, q *AgentQuota) (bool, error) {
	var ev *Event
	changed := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		prev, err := getAgentQuotaTx(ctx, tx, q.Agent)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if prev != nil {
			if q.ObservedAt.Before(prev.ObservedAt) {
				return nil // a straggler; the newer observation wins
			}
			if prev.ResetsAt.Equal(q.ResetsAt) &&
				prev.ResetsAtReported == q.ResetsAtReported &&
				prev.Source == q.Source {
				// Same window, same provenance: the row's observed_at moves
				// but nothing a client renders does, so no event.
				_, err := tx.ExecContext(ctx,
					`UPDATE agent_quota SET observed_at = ? WHERE agent = ?`,
					formatTime(q.ObservedAt), q.Agent)
				if err != nil {
					return fmt.Errorf("touch agent quota: %w", err)
				}
				return nil
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_quota (`+agentQuotaColumns+`) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(agent) DO UPDATE SET
				observed_at = excluded.observed_at,
				resets_at = excluded.resets_at,
				resets_at_reported = excluded.resets_at_reported,
				source = excluded.source`,
			q.Agent, formatTime(q.ObservedAt), formatTime(q.ResetsAt),
			q.ResetsAtReported, q.Source); err != nil {
			return fmt.Errorf("upsert agent quota: %w", err)
		}
		changed = true
		resets := q.ResetsAt.UTC().Format(time.RFC3339)
		ev, err = agentQuotaEvent(q.Agent, q.Spent(q.ObservedAt), &resets, &q.Source)
		if err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return false, err
	}
	if ev != nil {
		s.notify(ev)
	}
	return changed, nil
}

// ClearAgentQuota deletes the adapter's *observation* when it predates before,
// writing `agent.quota_changed` in the same transaction. The bool reports
// whether a row went away.
//
// Only `source = observed` rows are touched (task 082 decision 5). §14's rule
// is that an observation is retired by evidence — the next successful agent
// step on that adapter deletes it — and that argument is about observations
// alone: a step completing proves the wall vincent watched has come down, and
// proves nothing whatever about a percentage a vendor reported. A reported
// reading is retired by a fresher reading or by its own reset, never by work
// succeeding.
//
// before is the moment a run on that adapter *succeeded*. A hold with no
// CLI-reported reset is an estimate — `now + usage_limit_recheck_interval` —
// and a step that completes five minutes in is proof the window reopened.
// Without the clear, the board would claim "spent until 14:20" about an
// adapter it can watch doing work, which is a worse lie than saying nothing.
//
// An observation *newer* than the run survives: a run that started before a
// fresh wall must not erase the wall it never saw.
func (s *Store) ClearAgentQuota(ctx context.Context, agent string, before time.Time) (bool, error) {
	var ev *Event
	cleared := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM agent_quota WHERE agent = ? AND observed_at < ? AND source = ?`,
			agent, formatTime(before), QuotaSourceObserved)
		if err != nil {
			return fmt.Errorf("clear agent quota: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("clear agent quota: %w", err)
		}
		if n == 0 {
			return nil
		}
		cleared = true
		ev, err = agentQuotaEvent(agent, false, nil, nil)
		if err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return false, err
	}
	if ev != nil {
		s.notify(ev)
	}
	return cleared, nil
}

// ListAgentQuota returns every recorded observation, ordered by adapter name.
func (s *Store) ListAgentQuota(ctx context.Context) ([]AgentQuota, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentQuotaColumns+` FROM agent_quota ORDER BY agent`)
	if err != nil {
		return nil, fmt.Errorf("list agent quota: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AgentQuota
	for rows.Next() {
		q, err := scanAgentQuota(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agent quota: %w", err)
	}
	return out, nil
}

// GetAgentQuota returns one adapter's observation, or ErrNotFound.
func (s *Store) GetAgentQuota(ctx context.Context, agent string) (*AgentQuota, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+agentQuotaColumns+` FROM agent_quota WHERE agent = ?`, agent)
	q, err := scanAgentQuota(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("agent quota %s: %w", agent, ErrNotFound)
	}
	return q, err
}

func getAgentQuotaTx(ctx context.Context, tx *sql.Tx, agent string) (*AgentQuota, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+agentQuotaColumns+` FROM agent_quota WHERE agent = ?`, agent)
	q, err := scanAgentQuota(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("agent quota %s: %w", agent, ErrNotFound)
	}
	return q, err
}

func scanAgentQuota(r rowScanner) (*AgentQuota, error) {
	var (
		q                    AgentQuota
		observedAt, resetsAt string
	)
	if err := r.Scan(&q.Agent, &observedAt, &resetsAt, &q.ResetsAtReported, &q.Source); err != nil {
		return nil, err
	}
	var err error
	if q.ObservedAt, err = parseTime(observedAt); err != nil {
		return nil, fmt.Errorf("agent quota %s: observed_at: %w", q.Agent, err)
	}
	if q.ResetsAt, err = parseTime(resetsAt); err != nil {
		return nil, fmt.Errorf("agent quota %s: resets_at: %w", q.Agent, err)
	}
	return &q, nil
}

// agentQuotaEvent builds the daemon-level `agent.quota_changed` event
// (§13.3). It carries no task_id and no project_id — the fact is about an
// adapter, not a task — which is `workflow.registry_changed`'s shape.
// resets and source are nil on a clear, and render as JSON null so the
// payload has one shape whichever way it changed.
func agentQuotaEvent(agent string, spent bool, resets, source *string) (*Event, error) {
	payload, err := json.Marshal(map[string]any{
		"agent": agent, "spent": spent, "resets_at": resets, "source": source,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventAgentQuotaChanged, err)
	}
	return &Event{Type: EventAgentQuotaChanged, Payload: payload}, nil
}
