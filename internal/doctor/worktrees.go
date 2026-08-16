package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// WorktreesDirName is the subdirectory of the data dir that holds per-task
// worktrees (§10). It is named here rather than imported from
// internal/worktree so this package keeps its own dependency surface; the two
// spellings are pinned together by a test in internal/api.
const WorktreesDirName = "worktrees"

// inspectStorage measures the data dir and carries in gc's orphan scan.
//
// The disk figures come first and independently of the scan: a full disk is
// one of the answers to "why is nothing running?", and it is exactly the
// condition that also makes the scan fail.
//
// The footprint below is a plain readdir of {data_dir}/worktrees — how many
// worktrees there are and how much they weigh, live ones included. It is
// deliberately *not* a classification: which of them nothing claims is gc's
// question, answered by gc, and asking it twice is how the two commands would
// come to disagree.
func inspectStorage(ctx context.Context, opts Options) Storage {
	root := filepath.Join(opts.Dirs.Data, WorktreesDirName)
	s := Storage{WorktreesDir: root, Orphans: []Orphan{}}

	// Measured against the nearest directory that exists: on a machine where
	// vincent has never run, the data dir itself does not yet exist, and
	// "cannot stat the filesystem" would be a worse answer than the free
	// space of the volume it will be created on.
	if free, total, err := diskUsage(existingAncestor(opts.Dirs.Data)); err != nil {
		s.DiskError = err.Error()
	} else {
		s.DiskFreeBytes, s.DiskTotalBytes = free, total
	}

	if entries, err := os.ReadDir(root); err != nil {
		// No worktrees directory means no worktrees, which is a clean answer
		// rather than a failure to look.
		if !errors.Is(err, os.ErrNotExist) {
			s.ScanError = err.Error()
		}
	} else {
		for _, e := range entries {
			// Only directories: §10 places a worktree at
			// {data_dir}/worktrees/{task_id}, so a stray file there is not a
			// worktree and does not belong in the footprint.
			if !e.IsDir() {
				continue
			}
			s.WorktreeCount++
			s.WorktreeBytes += dirSize(filepath.Join(root, e.Name()))
		}
	}

	if opts.ScanOrphans == nil {
		return s
	}
	s.OrphansKnown = true
	orphans, err := opts.ScanOrphans(ctx)
	if err != nil {
		// A scan that failed is not a scan that found nothing: say so and
		// leave the orphan question unanswered rather than reporting a clean
		// data root the code never actually read.
		s.OrphansKnown = false
		s.ScanError = err.Error()
		return s
	}
	if orphans != nil {
		s.Orphans = orphans
	}
	return s
}

// dirSize sums the file sizes under path. Errors are swallowed on purpose: a
// permission-denied subtree makes the number an underestimate, which is a
// better report than no number at all — and a size is never something a
// decision is made on here.
func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable subtree undercounts rather than failing the report
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// existingAncestor walks up from path until something exists, so a statfs has
// a real directory to answer about.
func existingAncestor(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}
