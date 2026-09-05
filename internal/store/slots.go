package store

import (
	"context"
	"fmt"
)

// SlotCounts is the breakdown of the tasks holding a concurrency slot (§11).
//
// It is the reporting half of the two admission counters beside it in
// tasks.go: CountSlotHolders and CountSlotHoldersByProject answer "may one
// more task start", this answers "what is running right now, and why". Both
// halves read the same slotStates, so a client's header and the scheduler's
// cap can never disagree about what a slot is — which is the bug issue #324
// was: every client counted `state == "running"` over root tasks only, and
// each one had to be fixed separately.
type SlotCounts struct {
	Total         int
	Lanes         int
	AwaitingInput int
}

// SlotCounts returns how many tasks occupy a concurrency slot, how many of
// those are fan-out lanes, and how many are parked on a question (§11).
//
// One statement rather than three: the three figures are subsets of the same
// row set, and a client polling /v1/info would otherwise pay three round
// trips for a number that must be internally consistent. SUM over zero rows
// is NULL in SQLite, hence the COALESCE — an empty tasks table is a healthy
// one, not a scan error.
func (s *Store) SlotCounts(ctx context.Context) (SlotCounts, error) {
	// slotPlaceholders is placeholders() — package-internal and value-free.
	// The states themselves bind as arguments.
	q := `SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN parent_task_id IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = ? THEN 1 ELSE 0 END), 0)
		FROM tasks WHERE state IN ` + slotPlaceholders
	args := append([]any{string(TaskAwaitingInput)}, slotStates...)
	var out SlotCounts
	if err := s.db.QueryRowContext(ctx, q, args...).
		Scan(&out.Total, &out.Lanes, &out.AwaitingInput); err != nil {
		return SlotCounts{}, fmt.Errorf("count slots: %w", err)
	}
	return out, nil
}

// SlotCountsByProject returns each project's slot-holder count, keyed by
// project id (§11).
//
// A project holding no slot is absent from the map rather than present as 0:
// GROUP BY produces no row for it, and a caller reading a missing key as 0 is
// the same answer. It exists so a list endpoint can fill the per-project
// figure for every row from one statement instead of one query per row.
func (s *Store) SlotCountsByProject(ctx context.Context) (map[int64]int, error) {
	// As above: slotPlaceholders is placeholders(), and the states bind.
	q := `SELECT project_id, COUNT(*) FROM tasks
		WHERE state IN ` + slotPlaceholders + ` GROUP BY project_id`
	rows, err := s.db.QueryContext(ctx, q, slotStates...)
	if err != nil {
		return nil, fmt.Errorf("count slots by project: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]int)
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("count slots by project: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count slots by project: %w", err)
	}
	return out, nil
}
