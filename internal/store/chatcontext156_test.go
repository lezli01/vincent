package store

import "testing"

// TestChatTurnContextRoundTrip is migration 0059 (task 156): the four new
// columns and the two session cache totals survive a write and a read, and a
// reported zero stays distinct from an unreported nil.
func TestChatTurnContextRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)

	turn, err := s.CreateChatTurn(ctx, c.ID, "hello")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	if turn.CacheReadTokens != nil || turn.ContextTokens != nil || turn.ContextWindow != nil {
		t.Fatalf("a new turn reports context figures: %+v", turn)
	}
	zero, cr, ctxTok, win := int64(0), int64(900), int64(1200), int64(200000)
	turn.CacheReadTokens, turn.CacheCreationTokens = &cr, &zero
	turn.ContextTokens, turn.ContextWindow = &ctxTok, &win
	turn.SessionCacheReadTokens, turn.SessionCacheCreationTokens = &cr, &zero
	if err := s.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	got, err := s.GetChatTurn(ctx, turn.ID)
	if err != nil {
		t.Fatalf("GetChatTurn: %v", err)
	}
	for name, pair := range map[string][2]*int64{
		"cache_read":            {got.CacheReadTokens, &cr},
		"cache_creation":        {got.CacheCreationTokens, &zero},
		"context_tokens":        {got.ContextTokens, &ctxTok},
		"context_window":        {got.ContextWindow, &win},
		"session_cache_read":    {got.SessionCacheReadTokens, &cr},
		"session_cache_created": {got.SessionCacheCreationTokens, &zero},
	} {
		if pair[0] == nil || *pair[0] != *pair[1] {
			t.Errorf("%s = %v, want %d", name, pair[0], *pair[1])
		}
	}

	got.ContextWindow = nil
	if err := s.UpdateChatTurn(ctx, got); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	again, err := s.GetChatTurn(ctx, turn.ID)
	if err != nil {
		t.Fatalf("GetChatTurn: %v", err)
	}
	if again.ContextWindow != nil {
		t.Errorf("context_window = %d, want nil after clearing it", *again.ContextWindow)
	}
	if again.CacheCreationTokens == nil {
		t.Error("a reported zero cache_creation read back as nil")
	}
}

// TestPreviousSessionUsageCarriesCacheTotals: the share subtraction reads
// the cache totals beside the input and output, and a turn written without
// them (before migration 0059) answers zero for them.
func TestPreviousSessionUsageCarriesCacheTotals(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := testChat(t, s, p.ID)
	turn, err := s.CreateChatTurn(ctx, c.ID, "hello")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	in, out := int64(100), int64(40)
	turn.SessionID = "s1"
	turn.SessionInputTokens, turn.SessionOutputTokens = &in, &out
	if err := s.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	u, ok, err := s.PreviousSessionUsage(ctx, c.ID, turn.Seq+1, "s1")
	if err != nil || !ok {
		t.Fatalf("PreviousSessionUsage = %v, %v", ok, err)
	}
	if u != (SessionUsage{InputTokens: 100, OutputTokens: 40}) {
		t.Errorf("usage = %+v, want 100/40 and no cache", u)
	}
	cr, cw := int64(700), int64(7)
	turn.SessionCacheReadTokens, turn.SessionCacheCreationTokens = &cr, &cw
	if err := s.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	u, _, _ = s.PreviousSessionUsage(ctx, c.ID, turn.Seq+1, "s1")
	if u.CacheReadTokens != 700 || u.CacheCreationTokens != 7 {
		t.Errorf("cache totals = %d/%d, want 700/7", u.CacheReadTokens, u.CacheCreationTokens)
	}
}
