package store

import (
	"context"
	"sync"
)

// The issue-source gate orders an importer pass against a rebind of the
// same project (task 142.6 decision 4, review F1). A pass reads the project
// once and then spends seconds on provider calls before it writes; a rebind
// that committed in between would be undone by those writes — an upsert
// resets the remotes it marked missing to live, and the pass's final
// PutIssueSyncState brings back the sync row it deleted.
//
// One transaction cannot cover a pass, so the gate is held instead: a pass
// holds its project's gate from before it re-reads the project to its last
// write, and a rebind takes the gate for its transaction. A rebind does not
// wait out a pass's network calls — it cancels the pass's context first, so
// the pass stops at its next call or write. A pass that takes the gate after
// a rebind re-reads the rebound project and does not act on its stale copy.
//
// The gate is in-memory, which is enough because only the daemon opens the
// database and both halves run in its process.

type issueSourceGates struct {
	mu sync.Mutex
	m  map[int64]*issueSourceGate
}

type issueSourceGate struct {
	// sem is the gate itself, a one-slot semaphore so a wait can honour a
	// context.
	sem chan struct{}
	// cancel stops the pass holding the gate; nil when none does or a
	// rebind holds it. Guarded by issueSourceGates.mu.
	cancel context.CancelFunc
}

func (g *issueSourceGates) gate(projectID int64) *issueSourceGate {
	if g.m == nil {
		g.m = make(map[int64]*issueSourceGate)
	}
	gt, ok := g.m[projectID]
	if !ok {
		gt = &issueSourceGate{sem: make(chan struct{}, 1)}
		g.m[projectID] = gt
	}
	return gt
}

// BeginIssueSourcePass takes projectID's issue-source gate for one importer
// pass, waiting while another pass or a rebind holds it. The returned
// context is cancelled when a rebind of the project arrives; the pass must
// make every call and write under it, re-read the project after this
// returns, and call done when its last write is made.
func (s *Store) BeginIssueSourcePass(ctx context.Context, projectID int64) (passCtx context.Context, done func(), err error) {
	s.sourceGates.mu.Lock()
	gt := s.sourceGates.gate(projectID)
	s.sourceGates.mu.Unlock()
	select {
	case gt.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	passCtx, cancel := context.WithCancel(ctx)
	s.sourceGates.mu.Lock()
	gt.cancel = cancel
	s.sourceGates.mu.Unlock()
	return passCtx, func() {
		s.sourceGates.mu.Lock()
		gt.cancel = nil
		s.sourceGates.mu.Unlock()
		cancel()
		<-gt.sem
	}, nil
}

// lockIssueSourceForRebind takes projectID's gate for a rebind, cancelling
// the pass that holds it so the wait is for the pass to stop, not finish.
func (s *Store) lockIssueSourceForRebind(ctx context.Context, projectID int64) (unlock func(), err error) {
	s.sourceGates.mu.Lock()
	gt := s.sourceGates.gate(projectID)
	if gt.cancel != nil {
		gt.cancel()
	}
	s.sourceGates.mu.Unlock()
	select {
	case gt.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return func() { <-gt.sem }, nil
}
