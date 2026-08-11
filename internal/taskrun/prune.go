package taskrun

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// PruneInterval is how often the daemon re-runs retention pruning.
//
// Pruning also runs once at startup, and that alone used to be the whole
// plan. It is not enough once the daemon survives reboots (T4.1 service
// install): a daemon that runs for months would prune only on the restarts
// it no longer has (PR V decision).
const PruneInterval = 24 * time.Hour

// TranscriptPruner deletes transcripts of tasks archived longer ago than the
// retention window (§17). It owns no state: each pass reads the current
// config, so a retention change takes effect on the next tick without a
// restart.
type TranscriptPruner struct {
	deps Deps
}

// NewTranscriptPruner returns a pruner over the runner's dependencies.
func NewTranscriptPruner(deps Deps) *TranscriptPruner { return &TranscriptPruner{deps: deps} }

// Run prunes once, then on every tick until ctx is done. It is meant to be
// started in a goroutine by the daemon.
func (p *TranscriptPruner) Run(ctx context.Context) {
	p.once(ctx)
	t := time.NewTicker(PruneInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.once(ctx)
		}
	}
}

// once runs a single pruning pass, logging what it did. A failure is logged
// and dropped: retention is housekeeping, and a daemon that refuses to serve
// because it could not delete an old file has its priorities backwards.
func (p *TranscriptPruner) once(ctx context.Context) {
	removed, freed, err := p.Prune(ctx, time.Now())
	switch {
	case err != nil:
		p.deps.Logger.Error("prune transcripts", "error", err)
	case removed > 0:
		p.deps.Logger.Info("pruned transcripts",
			"tasks", removed, "freed_bytes", freed,
			"retention_days", p.deps.Config().TranscriptRetentionDays)
	default:
		p.deps.Logger.Debug("prune transcripts: nothing to do")
	}
}

// Prune deletes the transcript directory of every task archived before
// now-retention, returning how many task directories went and how many bytes
// that freed. A retention of zero or less disables pruning entirely, which is
// how an operator keeps everything.
//
// now is a parameter so tests can age data without sleeping.
func (p *TranscriptPruner) Prune(ctx context.Context, now time.Time) (removed int, freed int64, err error) {
	days := p.deps.Config().TranscriptRetentionDays
	if days <= 0 {
		return 0, 0, nil
	}
	cutoff := now.AddDate(0, 0, -days)
	ids, err := p.deps.Store.ArchivedTaskIDsBefore(ctx, cutoff)
	if err != nil {
		return 0, 0, err
	}
	root := filepath.Join(p.deps.DataDir, "transcripts")
	for _, id := range ids {
		dir := filepath.Join(root, strconv.FormatInt(id, 10))
		size, err := dirSize(dir)
		if err != nil {
			// Already gone is the common case — a task pruned on an earlier
			// pass, or one that never produced a transcript at all. Neither
			// is worth a log line, let alone an error. errors.Is rather than
			// os.IsNotExist: dirSize wraps, and os.IsNotExist does not
			// unwrap, so the check silently never matched and every pass
			// re-counted deleted tasks as freshly removed.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			p.deps.Logger.Warn("measure transcript dir", "task", id, "error", err)
		}
		if err := os.RemoveAll(dir); err != nil {
			// Report the first real failure but keep going: one undeletable
			// directory must not strand every task behind it.
			p.deps.Logger.Warn("prune transcript dir", "task", id, "error", err)
			continue
		}
		removed++
		freed += size
	}
	return removed, freed, nil
}

// dirSize sums the regular files under dir.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walk %s: %w", dir, err)
	}
	return total, nil
}
