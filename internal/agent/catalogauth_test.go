package agent

import (
	"errors"
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

// TestCatalogAuthTTLRefreshesDetectOnly pins task 026's amendment to §9.6: an
// entry that is otherwise a clean hit has its `logged_in` re-asked after
// authTTL, and only that — the option catalog is a pure function of the
// binary, and the binary has not changed.
func TestCatalogAuthTTLRefreshesDetectOnly(t *testing.T) {
	bin := fakeBinary(t)
	stub := &stubAdapter{
		name: "cursor", path: bin,
		av:   Availability{Found: true, Path: bin, Version: "2026.08.11", LoggedIn: boolPtr(false)},
		opts: cachedOpts("gpt-5.4"),
	}
	c := NewCatalogCache(NewRegistry(stub))
	now := time.Now()
	c.now = func() time.Time { return now }

	if e, _ := c.Entry(t.Context(), "cursor", false); e.Availability.LoggedIn == nil || *e.Availability.LoggedIn {
		t.Fatalf("logged_in = %v, want false from the first probe", e.Availability.LoggedIn)
	}

	// Inside the TTL nothing is spawned at all: a board, a detail view and a
	// new-task form asking in the same second cost one probe between them.
	now = now.Add(authTTL - time.Second)
	if _, ok := c.Entry(t.Context(), "cursor", false); !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if stub.detects != 1 || stub.options != 1 {
		t.Fatalf("detects = %d, options = %d inside authTTL; want 1/1", stub.detects, stub.options)
	}

	// Past it, the user has logged in and the daemon says so.
	now = now.Add(2 * time.Second)
	stub.av.LoggedIn = boolPtr(true)
	e, ok := c.Entry(t.Context(), "cursor", false)
	if !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if stub.detects != 2 {
		t.Fatalf("detects = %d past authTTL, want a Detect-only refresh", stub.detects)
	}
	if stub.options != 1 {
		t.Fatalf("options = %d, want the catalog left alone — binary identity still vouches for it", stub.options)
	}
	if e.Availability.LoggedIn == nil || !*e.Availability.LoggedIn {
		t.Fatalf("logged_in = %v, want true after the refresh", e.Availability.LoggedIn)
	}
}

// TestCatalogAuthTTLSkipsAdaptersThatCannotTell keeps the TTL honest about its
// own purpose: claude reports `logged_in: null` because its CLI has no cheap
// auth surface (§9.5), and spawning a subprocess every five minutes to be told
// nothing again is pure cost.
func TestCatalogAuthTTLSkipsAdaptersThatCannotTell(t *testing.T) {
	bin := fakeBinary(t)
	stub := &stubAdapter{
		name: "claude", path: bin,
		av:   Availability{Found: true, Path: bin, Version: "2.1.241"}, // LoggedIn nil
		opts: cachedOpts("sonnet"),
	}
	c := NewCatalogCache(NewRegistry(stub))
	now := time.Now()
	c.now = func() time.Time { return now }

	if _, ok := c.Entry(t.Context(), "claude", false); !ok {
		t.Fatal("Entry: adapter unknown")
	}
	now = now.Add(100 * authTTL)
	if _, ok := c.Entry(t.Context(), "claude", false); !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if stub.detects != 1 {
		t.Fatalf("detects = %d, want no re-probe for an adapter with no auth state to go stale", stub.detects)
	}
}

// TestCatalogAuthRefreshFailureKeepsPriorLogin is the T4.22 rule applied to
// the field authTTL exists for. A Windows deadline is
// `TerminateProcess(pid, 1)`; reading that as "not authenticated" is a false
// accusation against a logged-in account, and worse than the stale value the
// refresh was trying to improve on.
func TestCatalogAuthRefreshFailureKeepsPriorLogin(t *testing.T) {
	bin := fakeBinary(t)
	stub := &stubAdapter{
		name: "codex", path: bin,
		av:   Availability{Found: true, Path: bin, Version: "0.149.0", LoggedIn: boolPtr(true)},
		opts: cachedOpts("gpt-5.4"),
	}
	c := NewCatalogCache(NewRegistry(stub))
	now := time.Now()
	c.now = func() time.Time { return now }

	if _, ok := c.Entry(t.Context(), "codex", false); !ok {
		t.Fatal("Entry: adapter unknown")
	}

	now = now.Add(authTTL)
	stub.detectErr = errors.New("codex login status: timed out after 20s")
	stub.av = Availability{}
	e, ok := c.Entry(t.Context(), "codex", false)
	if !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if e.Availability.LoggedIn == nil || !*e.Availability.LoggedIn {
		t.Fatalf("logged_in = %v after a failed refresh, want the previous true kept",
			e.Availability.LoggedIn)
	}
	if !e.Availability.Found || e.Availability.Version != "0.149.0" {
		t.Fatalf("availability = %+v, want the previous entry kept intact", e.Availability)
	}
	if e.AuthError == "" {
		t.Error("AuthError is empty; a failed refresh must be recorded, not swallowed")
	}

	// The clock is stamped even on failure, so a persistently failing probe
	// costs one subprocess per authTTL rather than one per request.
	if _, ok := c.Entry(t.Context(), "codex", false); !ok {
		t.Fatal("Entry: adapter unknown")
	}
	if stub.detects != 2 {
		t.Fatalf("detects = %d, want the failed refresh to reset the clock", stub.detects)
	}

	// And when the CLI answers again, the error clears with it.
	now = now.Add(authTTL)
	stub.detectErr = nil
	stub.av = Availability{Found: true, Path: bin, Version: "0.149.0", LoggedIn: boolPtr(false)}
	e, _ = c.Entry(t.Context(), "codex", false)
	if e.AuthError != "" {
		t.Errorf("AuthError = %q, want cleared by a clean refresh", e.AuthError)
	}
	if e.Availability.LoggedIn == nil || *e.Availability.LoggedIn {
		t.Errorf("logged_in = %v, want the fresh false", e.Availability.LoggedIn)
	}
}
