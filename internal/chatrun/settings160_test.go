package chatrun

import (
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/store"
)

// flagValue is the argument after flag in args, or "" when flag is absent.
func flagValue(args []string, flag string) string {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

// TestTurnSettingsChangeBetweenTurns is task 160 decisions 5–7 through the
// runner and a real fakeagent: a turn after a settings change runs with the
// new argv, a per-turn override runs that turn only and the next reverts,
// and every turn row records what it ran with.
func TestTurnSettingsChangeBetweenTurns(t *testing.T) {
	h := newHarness(t)
	rec := &agenttest.RecordingLauncher{}
	c := h.linkedChatOn(t, "claude", rec)
	ctx := t.Context()

	h.sendAndWait(t, c.ID, "one")
	h.waitIdle(t, c.ID)
	model, effort := "sonnet", "high"
	if _, err := h.store.UpdateChatSettings(ctx, c.ID, store.ChatSettingsPatch{Model: &model, Effort: &effort}); err != nil {
		t.Fatalf("UpdateChatSettings: %v", err)
	}
	h.sendAndWait(t, c.ID, "two")
	h.waitIdle(t, c.ID)
	turn, _, err := h.runner.SubmitWith(ctx, c.ID, "three", store.TurnSettings{Model: "opus"})
	if err != nil {
		t.Fatalf("SubmitWith: %v", err)
	}
	h.waitTurn(t, turn.ID)
	h.waitIdle(t, c.ID)
	h.sendAndWait(t, c.ID, "four")
	h.waitIdle(t, c.ID)

	launches := rec.Launches()
	if len(launches) != 4 {
		t.Fatalf("launches = %d, want 4", len(launches))
	}
	want := []struct{ model, effort string }{{"", ""}, {"sonnet", "high"}, {"opus", "high"}, {"sonnet", "high"}}
	turns, err := h.store.ListChatTurns(ctx, c.ID)
	if err != nil || len(turns) != 4 {
		t.Fatalf("turns = %d, %v; want 4", len(turns), err)
	}
	for i, w := range want {
		args := launches[i].Command.Args
		if got := flagValue(args, "--model"); got != w.model {
			t.Errorf("turn %d argv --model = %q, want %q (%v)", i+1, got, w.model, args)
		}
		if got := flagValue(args, "--effort"); got != w.effort {
			t.Errorf("turn %d argv --effort = %q, want %q", i+1, got, w.effort)
		}
		if i > 0 && flagValue(args, "--resume") == "" {
			t.Errorf("turn %d did not resume: %v", i+1, args)
		}
		if turns[i].Model != w.model || turns[i].Effort != w.effort || turns[i].PermissionMode != "full-auto" {
			t.Errorf("turn %d recorded %q/%q/%q, want %q/%q/full-auto", i+1,
				turns[i].Model, turns[i].Effort, turns[i].PermissionMode, w.model, w.effort)
		}
	}
}

// TestQueuedOverridesSurviveToTheDrain: a message queued behind a live turn
// with its own overrides runs with them when it drains (task 160 decision 6),
// and the chat's own settings are untouched.
func TestQueuedOverridesSurviveToTheDrain(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKEAGENT_DELAY_MS", "600")
	rec := &agenttest.RecordingLauncher{}
	c := h.linkedChatOn(t, "claude", rec)
	ctx := t.Context()

	first, err := h.runner.Send(ctx, c.ID, "first")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, q, err := h.runner.SubmitWith(ctx, c.ID, "second", store.TurnSettings{Model: "haiku", Effort: "low"})
	if err != nil || q == nil {
		t.Fatalf("SubmitWith on a live chat = %v, %v; want it queued", q, err)
	}
	if q.Settings.Model != "haiku" || q.Settings.Effort != "low" {
		t.Errorf("queued settings = %+v, want haiku/low", q.Settings)
	}
	h.waitTurn(t, first.ID)
	waitFor(t, func() bool {
		turns, err := h.store.ListChatTurns(ctx, c.ID)
		return err == nil && len(turns) == 2 && turns[1].EndedAt != nil
	})
	h.waitIdle(t, c.ID)
	turns, _ := h.store.ListChatTurns(ctx, c.ID)
	if turns[1].Model != "haiku" || turns[1].Effort != "low" {
		t.Errorf("drained turn recorded %q/%q, want haiku/low", turns[1].Model, turns[1].Effort)
	}
	launches := rec.Launches()
	if len(launches) != 2 || flagValue(launches[1].Command.Args, "--model") != "haiku" {
		t.Errorf("drained turn's argv does not carry --model haiku")
	}
	got, _ := h.store.GetChat(ctx, c.ID)
	if got.Model != "" || got.Effort != "" {
		t.Errorf("chat settings = %q/%q after an override; want them untouched", got.Model, got.Effort)
	}
}
