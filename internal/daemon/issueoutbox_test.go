package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issues"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The state write-back drain (task 130.10) against cmd/fakegh: what reached
// GitHub is read off the fake's argv log and its corpus file, which the
// fake's PATCH rewrites the way GitHub would.

type outboxFixture struct {
	*reconcileFixture
	corpus *corpus
	outbox *IssueOutbox
	sleeps []time.Duration
}

func newOutboxFixture(t *testing.T, rows ...map[string]any) *outboxFixture {
	t.Helper()
	f := &outboxFixture{reconcileFixture: newFixture(t, "https://github.com/octo/repo.git")}
	f.corpus = newCorpus(t, rows...)
	f.outbox = f.newOutbox()
	// Import, so the issues exist here with their live remotes.
	f.reconciler().Tick(t.Context())
	if got := len(f.issues(t)); got != len(rows) {
		t.Fatalf("imported %d issues, want %d", got, len(rows))
	}
	f.resetCalls(t)
	return f
}

func (f *outboxFixture) newOutbox() *IssueOutbox {
	o := NewIssueOutbox(f.store, func() config.Config { return f.cfg }, f.client,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.sleep = func(_ context.Context, d time.Duration) bool {
		f.sleeps = append(f.sleeps, d)
		return true
	}
	return o
}

func (f *outboxFixture) resetCalls(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.argvFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// patches is the PATCH lines of the argv log.
func (f *outboxFixture) patches(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, line := range f.apiCalls(t) {
		if strings.Contains(line, "PATCH") {
			out = append(out, line)
		}
	}
	return out
}

// remote is issue n as the fake's corpus file now holds it.
func (f *outboxFixture) remote(t *testing.T, n int) (state, reason string) {
	t.Helper()
	b, err := os.ReadFile(f.corpus.path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if num, _ := row["number"].(float64); int(num) == n {
			r, _ := row["state_reason"].(string)
			return row["state"].(string), r
		}
	}
	t.Fatalf("corpus has no issue %d", n)
	return "", ""
}

func (f *outboxFixture) close(t *testing.T, n int, reason issuestate.Reason) *store.Issue {
	t.Helper()
	iss, err := issues.New(f.store).Close(t.Context(), issuestate.Human, f.issues(t)[n].ID, reason, nil)
	if err != nil {
		t.Fatalf("close #%d: %v", n, err)
	}
	return iss
}

func (f *outboxFixture) write(t *testing.T, n int) *store.IssueOutbox {
	t.Helper()
	iss, err := f.store.GetIssue(t.Context(), f.issues(t)[n].ID)
	if err != nil || iss.Sync == nil {
		t.Fatalf("issue #%d has no write: %+v, %v", n, iss, err)
	}
	return iss.Sync
}

func TestOutboxCloseReachesGitHubOnce(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"))
	f.close(t, 1, issuestate.NotPlanned)
	f.outbox.Drain(t.Context())
	if p := f.patches(t); len(p) != 1 {
		t.Fatalf("drain sent %d PATCHes, want 1:\n%s", len(p), strings.Join(f.apiCalls(t), "\n"))
	}
	if state, reason := f.remote(t, 1); state != "closed" || reason != "not_planned" {
		t.Errorf("GitHub holds %s/%s, want closed/not_planned", state, reason)
	}
	if w := f.write(t, 1); w.Status != store.OutboxDone {
		t.Errorf("write = %+v, want done", w)
	}
	// Another drain sends nothing; the next import sees the echo and moves
	// nothing back.
	f.outbox.Drain(t.Context())
	f.reconciler().Tick(t.Context())
	if p := f.patches(t); len(p) != 1 {
		t.Errorf("after the echo: %d PATCHes, want still 1", len(p))
	}
	if got := f.issues(t)[1]; got.State != issuestate.Closed || got.CloseReason != issuestate.NotPlanned {
		t.Errorf("after the echo the issue is %s/%s", got.State, got.CloseReason)
	}

	// Reopen likewise.
	if _, err := issues.New(f.store).Reopen(t.Context(), issuestate.Human, f.issues(t)[1].ID); err != nil {
		t.Fatal(err)
	}
	f.outbox.Drain(t.Context())
	if state, _ := f.remote(t, 1); state != "open" || len(f.patches(t)) != 2 {
		t.Errorf("after reopen GitHub is %s with %d PATCHes", state, len(f.patches(t)))
	}
}

func TestOutboxImportBeforeDrainKeepsTheLocalChange(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"))
	f.close(t, 1, "")
	// Force a refresh of #1 with GitHub still open.
	f.corpus.edit(1, issueBase.Add(time.Hour), func(row map[string]any) { row["title"] = "renamed" })
	f.reconciler().Tick(t.Context())
	if got := f.issues(t)[1]; got.State != issuestate.Closed || got.Title != "renamed" {
		t.Fatalf("a tick before the drain made the issue %s %q", got.State, got.Title)
	}
	f.outbox.Drain(t.Context())
	if state, _ := f.remote(t, 1); state != "closed" {
		t.Errorf("GitHub is %s, want closed", state)
	}
}

func TestOutboxRemoteAlreadyThereIsDoneWithoutAWrite(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"))
	f.close(t, 1, "")
	f.corpus.edit(1, issueBase.Add(time.Hour), func(row map[string]any) {
		row["state"], row["state_reason"] = "closed", "completed"
	})
	f.outbox.Drain(t.Context())
	if p := f.patches(t); len(p) != 0 {
		t.Errorf("sent %d PATCHes to an issue already closed", len(p))
	}
	if w := f.write(t, 1); w.Status != store.OutboxDone {
		t.Errorf("write = %+v, want done", w)
	}
}

func TestOutboxConflictAdoptsGitHub(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"))
	f.close(t, 1, issuestate.Completed)
	f.outbox.Drain(t.Context())
	// Reopened here (base closed), while a maintainer re-closed it on
	// GitHub as not planned.
	if _, err := issues.New(f.store).Reopen(t.Context(), issuestate.Human, f.issues(t)[1].ID); err != nil {
		t.Fatal(err)
	}
	f.corpus.rows[0]["state"], f.corpus.rows[0]["state_reason"] = "closed", "not_planned"
	f.corpus.save()
	f.resetCalls(t)
	mark := f.lastEventID(t)
	f.outbox.Drain(t.Context())
	if p := f.patches(t); len(p) != 0 {
		t.Errorf("a conflict sent %d PATCHes", len(p))
	}
	got := f.issues(t)[1]
	if got.State != issuestate.Closed || got.CloseReason != issuestate.NotPlanned ||
		got.Sync == nil || got.Sync.Status != store.OutboxConflict {
		t.Errorf("after a conflict: %s/%s sync %+v; want GitHub's closed/not_planned and conflict",
			got.State, got.CloseReason, got.Sync)
	}
	evs := f.eventsAfter(t, mark, store.EventIssueStateChanged)
	if len(evs) != 1 || !strings.Contains(string(evs[0].Payload), `"by":"sync"`) {
		t.Errorf("adoption events = %v, want one state change by sync", evs)
	}
}

func TestOutboxConflictOnTheReasonAdoptsGitHubs(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"))
	f.close(t, 1, issuestate.Completed)
	f.corpus.edit(1, issueBase.Add(time.Hour), func(row map[string]any) {
		row["state"], row["state_reason"] = "closed", "not_planned"
	})
	f.outbox.Drain(t.Context())
	if p := f.patches(t); len(p) != 0 {
		t.Errorf("a conflict sent %d PATCHes", len(p))
	}
	got := f.issues(t)[1]
	if got.CloseReason != issuestate.NotPlanned || got.Sync == nil || got.Sync.Status != store.OutboxConflict {
		t.Errorf("after a conflict: %s/%s sync %+v", got.State, got.CloseReason, got.Sync)
	}
}

func TestOutboxTerminalFailureKeepsTheLocalState(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"))
	f.close(t, 1, "")
	f.corpus.rows = nil
	f.corpus.save()
	f.outbox.Drain(t.Context())
	got := f.issues(t)[1]
	if got.State != issuestate.Closed || got.Sync == nil || got.Sync.Status != store.OutboxFailed ||
		got.Sync.LastReason != github.ReasonNotFound {
		t.Errorf("after not_found: %s sync %+v", got.State, got.Sync)
	}
	c, _ := f.store.CountIssueWrites(t.Context(), f.project.ID)
	if c.Failed != 1 {
		t.Errorf("counts = %+v", c)
	}
}

func TestOutboxDisabledMakesNoCallAndDrainsWhenReenabled(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"))
	f.close(t, 1, "")
	for _, off := range []func(*config.Config){
		func(c *config.Config) { c.GitHub.Enabled = false },
		func(c *config.Config) { c.GitHub.PollInterval = 0 },
	} {
		on := f.cfg
		off(&f.cfg)
		f.outbox.Drain(t.Context())
		if calls := f.ghCalls(t); calls != "" {
			t.Errorf("a disabled drain called gh:\n%s", calls)
		}
		if w := f.write(t, 1); w.Status != store.OutboxPending || w.LastReason != outboxReasonDisabled {
			t.Errorf("disabled write = %+v, want pending/disabled", w)
		}
		f.cfg = on
	}
	f.outbox.Drain(t.Context())
	if state, _ := f.remote(t, 1); state != "closed" {
		t.Errorf("re-enabled drain left GitHub %s", state)
	}
}

// TestOutboxPendingWriteSurvivesARestart: a write the daemon died before
// sending is a row, so a new drain over the reopened store sends it.
func TestOutboxPendingWriteSurvivesARestart(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"))
	f.close(t, 1, "")
	f.cfg.GitHub.Enabled = false
	f.outbox.Drain(t.Context()) // the daemon that dies: nothing sent
	f.cfg.GitHub.Enabled = true
	f.outbox = f.newOutbox()
	f.outbox.Drain(t.Context())
	if state, _ := f.remote(t, 1); state != "closed" || len(f.patches(t)) != 1 {
		t.Errorf("after the restart GitHub is %s with %d PATCHes", state, len(f.patches(t)))
	}
}

func TestOutboxPacesMutativeCalls(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"), issueRow(2, "open"))
	f.close(t, 1, "")
	f.close(t, 2, "")
	f.outbox.Drain(t.Context())
	if len(f.patches(t)) != 2 {
		t.Fatalf("sent %d PATCHes, want 2", len(f.patches(t)))
	}
	if state, _ := f.remote(t, 2); state != "closed" || f.write(t, 2).Status != store.OutboxDone {
		t.Errorf("#2 is %s on GitHub, write %+v", state, f.write(t, 2))
	}
	if len(f.sleeps) != 1 || f.sleeps[0] <= 0 || f.sleeps[0] > outboxPace {
		t.Errorf("pacing waits = %v, want one wait of at most %v", f.sleeps, outboxPace)
	}
}

func TestOutboxRetryClasses(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"))
	f.close(t, 1, "")
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	f.outbox.now = func() time.Time { return now }
	reset := now.Add(17 * time.Minute)
	cases := []struct {
		err  error
		want time.Time
	}{
		{&github.Error{Reason: github.ReasonRateLimited, ResetAt: reset}, reset},
		{&github.Error{Reason: github.ReasonUnreachable}, now.Add(outboxBackoffBase * 2)},
		{&github.Error{Reason: github.ReasonUnauthorized}, now.Add(outboxCredentialWait)},
	}
	for _, c := range cases {
		row := f.write(t, 1)
		f.outbox.retry(t.Context(), row, c.err)
		got := f.write(t, 1)
		if got.Status != store.OutboxPending || !got.NextAttemptAt.Equal(c.want) || got.LastReason != github.ReasonOf(c.err) {
			t.Errorf("%v: %+v, want pending until %v", c.err, got, c.want)
		}
	}
	row := f.write(t, 1)
	f.outbox.retry(t.Context(), row, &github.Error{Reason: github.ReasonNoWriteScope})
	if got := f.write(t, 1); got.Status != store.OutboxFailed || got.LastReason != github.ReasonNoWriteScope {
		t.Errorf("no_write_scope: %+v, want failed", got)
	}
}
