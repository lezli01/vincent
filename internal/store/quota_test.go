package store

import (
	"encoding/json"
	"testing"
	"time"
)

func observation(agent string, observed, resets time.Time, reported bool) *AgentQuota {
	return &AgentQuota{
		Agent:            agent,
		ObservedAt:       observed,
		ResetsAt:         resets,
		ResetsAtReported: reported,
		Source:           QuotaSourceObserved,
	}
}

// TestAgentQuotaRoundTrips proves the 0011 table exists on a store the migrator
// opened and that both timestamps survive store.timeFormat unchanged — the
// nanosecond-width format the whole schema uses, not RFC3339Nano.
func TestAgentQuotaRoundTrips(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	if got, err := s.ListAgentQuota(ctx); err != nil || len(got) != 0 {
		t.Fatalf("ListAgentQuota on a fresh store = %v, %v; want empty", got, err)
	}

	observed := time.Now().UTC().Truncate(time.Nanosecond)
	resets := observed.Add(15 * time.Minute)
	changed, err := s.UpsertAgentQuota(ctx, observation("claude", observed, resets, true))
	if err != nil {
		t.Fatalf("UpsertAgentQuota: %v", err)
	}
	if !changed {
		t.Error("changed = false for the first observation of an adapter")
	}

	got, err := s.GetAgentQuota(ctx, "claude")
	if err != nil {
		t.Fatalf("GetAgentQuota: %v", err)
	}
	if !got.ObservedAt.Equal(observed) || !got.ResetsAt.Equal(resets) {
		t.Errorf("times = %s / %s, want %s / %s",
			got.ObservedAt, got.ResetsAt, observed, resets)
	}
	if !got.ResetsAtReported {
		t.Error("resets_at_reported = false; the CLI named this reset")
	}
	if got.Source != QuotaSourceObserved {
		t.Errorf("source = %q, want %q", got.Source, QuotaSourceObserved)
	}
	if !got.Spent(observed) || got.Spent(resets.Add(time.Second)) {
		t.Error("Spent must be true inside the window and false past the reset")
	}
}

// TestAgentQuotaUpsertIsMonotonic is the guard against two actors hitting the
// same wall in the same second: an observation older than the stored one is
// discarded rather than applied, so the state cannot go backwards.
func TestAgentQuotaUpsertIsMonotonic(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	newer := time.Now().UTC()
	if _, err := s.UpsertAgentQuota(ctx,
		observation("claude", newer, newer.Add(time.Hour), true)); err != nil {
		t.Fatalf("UpsertAgentQuota: %v", err)
	}

	older := newer.Add(-time.Minute)
	changed, err := s.UpsertAgentQuota(ctx,
		observation("claude", older, older.Add(time.Minute), false))
	if err != nil {
		t.Fatalf("UpsertAgentQuota (straggler): %v", err)
	}
	if changed {
		t.Error("changed = true for a stale observation; the newer one must win")
	}
	got, err := s.GetAgentQuota(ctx, "claude")
	if err != nil {
		t.Fatalf("GetAgentQuota: %v", err)
	}
	if !got.ResetsAt.Equal(newer.Add(time.Hour)) || !got.ResetsAtReported {
		t.Errorf("stored = %+v; a straggler overwrote the newer observation", got)
	}
}

// TestAgentQuotaClearOnlyRetiresOlderObservations pins the clear's one
// condition: a successful run disproves an observation it postdates, and
// nothing else. A run that started before a fresh wall must not erase the wall
// it never saw.
func TestAgentQuotaClearOnlyRetiresOlderObservations(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	observed := time.Now().UTC()
	if _, err := s.UpsertAgentQuota(ctx,
		observation("claude", observed, observed.Add(15*time.Minute), false)); err != nil {
		t.Fatalf("UpsertAgentQuota: %v", err)
	}

	// A run that finished before the observation proves nothing about it.
	cleared, err := s.ClearAgentQuota(ctx, "claude", observed.Add(-time.Second))
	if err != nil {
		t.Fatalf("ClearAgentQuota: %v", err)
	}
	if cleared {
		t.Error("cleared = true for a run that predates the observation")
	}
	if _, err := s.GetAgentQuota(ctx, "claude"); err != nil {
		t.Fatalf("the observation was deleted by a run that predates it: %v", err)
	}

	// A different adapter's success retires that adapter's window and only
	// that one: the row is keyed on the agent, not on "somebody ran out".
	if _, err := s.UpsertAgentQuota(ctx,
		observation("codex", observed, observed.Add(15*time.Minute), false)); err != nil {
		t.Fatalf("UpsertAgentQuota(codex): %v", err)
	}
	if cleared, err = s.ClearAgentQuota(ctx, "codex", observed.Add(time.Minute)); err != nil {
		t.Fatalf("ClearAgentQuota: %v", err)
	}
	if !cleared {
		t.Error("cleared = false for codex's own postdating run")
	}
	if _, err := s.GetAgentQuota(ctx, "claude"); err != nil {
		t.Fatalf("codex's success deleted claude's observation: %v", err)
	}

	// A run that finished after it is first-hand evidence the window reopened.
	if cleared, err = s.ClearAgentQuota(ctx, "claude", observed.Add(time.Minute)); err != nil {
		t.Fatalf("ClearAgentQuota: %v", err)
	}
	if !cleared {
		t.Fatal("cleared = false for a run that postdates the observation")
	}
	rows, err := s.ListAgentQuota(ctx)
	if err != nil {
		t.Fatalf("ListAgentQuota: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %+v after the clear, want none", rows)
	}
}

// TestAgentQuotaEventsOnlyOnChange is decision 6: the durable event is
// appended when the upsert actually changed a value or the clear actually
// deleted one — never on a re-observation identical to what is stored. A board
// that refetches on the event must not be woken by news it already has.
func TestAgentQuotaEventsOnlyOnChange(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	var seen []Event
	s.SetEventHook(func(e *Event) { seen = append(seen, *e) })

	observed := time.Now().UTC()
	resets := observed.Add(15 * time.Minute)
	if _, err := s.UpsertAgentQuota(ctx, observation("claude", observed, resets, true)); err != nil {
		t.Fatalf("UpsertAgentQuota: %v", err)
	}
	if len(seen) != 1 || seen[0].Type != EventAgentQuotaChanged {
		t.Fatalf("events = %+v, want one %s", seen, EventAgentQuotaChanged)
	}
	if seen[0].TaskID != nil || seen[0].ProjectID != nil {
		t.Error("agent.quota_changed carries a task or project id; the fact is about an adapter")
	}
	var payload struct {
		Agent    string  `json:"agent"`
		Spent    bool    `json:"spent"`
		ResetsAt *string `json:"resets_at"`
		Source   *string `json:"source"`
	}
	if err := json.Unmarshal(seen[0].Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Agent != "claude" || !payload.Spent || payload.ResetsAt == nil ||
		payload.Source == nil || *payload.Source != QuotaSourceObserved {
		t.Errorf("payload = %+v, want the spent window named", payload)
	}

	// The same wall, seen again a minute later by another actor: the row's
	// observed_at moves, nothing a client renders does, so no event.
	if _, err := s.UpsertAgentQuota(ctx,
		observation("claude", observed.Add(time.Minute), resets, true)); err != nil {
		t.Fatalf("UpsertAgentQuota (re-observation): %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("events = %d after an identical re-observation, want 1", len(seen))
	}

	// A different reset is news.
	if _, err := s.UpsertAgentQuota(ctx,
		observation("claude", observed.Add(2*time.Minute), resets.Add(time.Hour), true)); err != nil {
		t.Fatalf("UpsertAgentQuota (new window): %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("events = %d after a new reset, want 2", len(seen))
	}

	// A clear that deletes nothing is likewise silent; one that deletes is not.
	if _, err := s.ClearAgentQuota(ctx, "codex", observed.Add(time.Hour)); err != nil {
		t.Fatalf("ClearAgentQuota: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("events = %d after a no-op clear, want 2", len(seen))
	}
	if _, err := s.ClearAgentQuota(ctx, "claude", observed.Add(3*time.Minute)); err != nil {
		t.Fatalf("ClearAgentQuota: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("events = %d after a real clear, want 3", len(seen))
	}
	if err := json.Unmarshal(seen[2].Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Spent || payload.ResetsAt != nil || payload.Source != nil {
		t.Errorf("clear payload = %+v, want spent:false with null resets_at and source", payload)
	}
}
