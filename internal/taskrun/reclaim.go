package taskrun

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// Orphan kinds — which data root the entry was found under (§10, §17).
const (
	KindWorktree   = "worktree"
	KindTranscript = "transcript"
)

// SkipNotADirectory is why a stray *file* sitting directly under a data root
// is reported and never removed (task 005). vincent only ever creates
// directories there, so a file is something else's — an editor swap file, a
// `.DS_Store`, a note somebody left — and deleting it would exceed what gc
// was asked to do.
const SkipNotADirectory = "not_a_directory"

// Orphan is one entry directly under a data root that no task row claims.
type Orphan struct {
	// Path is absolute and always inside one of the two data roots.
	Path string
	Kind string
	// TaskID is the id the entry is named after, or 0 when the name is not
	// an id at all. It is informational: the claim decides, not the name.
	TaskID int64
	Bytes  int64
	// Skip is why this orphan was left alone — worktree_dirty,
	// dirty_unknown, not_a_directory — or "" when it is eligible.
	Skip string
	// Error is a removal that was attempted and failed (a file locked by
	// another process on Windows, a permissions problem). The orphan stays
	// reported and the totals count only what actually went.
	Error string
	// Removed is true when this run deleted it. A dry run leaves it false on
	// every entry, which is the whole difference between the two reports.
	Removed bool
}

// Mismatch is the reverse of an orphan: a task row whose worktree_path names
// a directory that is not there (§18's `worktree_missing` shape). It is
// report-only — there is nothing to delete, and gc modifies no row.
type Mismatch struct {
	TaskID int64
	Path   string
	State  string
}

// Report is what a scan or a reclaim run found and did.
type Report struct {
	Orphans    []Orphan
	Mismatches []Mismatch
	// Bytes is every orphan's size, eligible or not: what a full `--force`
	// run would reclaim.
	Bytes int64
	// Reclaimed and ReclaimedBytes count only entries actually removed, so a
	// failed removal never inflates the figure the user is shown.
	Reclaimed      int
	ReclaimedBytes int64
	DryRun         bool
	Force          bool
}

// Reclaimer finds and removes data-root directories no task row claims
// (task 005; spec §10, §17).
//
// It lives beside the transcript pruner because it needs exactly the same
// dependencies — Store, DataDir, Worktrees, Logger — and reclaims the same
// two trees. The producers it exists for are the ones nothing else
// reconciles: `DELETE /v1/projects/{id}`, whose worktree removal is
// best-effort by the T1.5 decision while the cascade drops the rows
// regardless, and a crash between `git worktree add` and the claim write.
//
// Removal is never automatic. The startup pass only reports (§18's "never
// auto-deletes" posture); deletion happens when a human runs `vincent gc`.
type Reclaimer struct {
	deps Deps
}

// NewReclaimer returns a reclaimer over the runner's dependencies.
func NewReclaimer(deps Deps) *Reclaimer { return &Reclaimer{deps: deps} }

// Scan reports what gc would consider, removing nothing.
func (rc *Reclaimer) Scan(ctx context.Context) (Report, error) {
	return rc.run(ctx, false, true)
}

// Reclaim removes every eligible orphan and reports what went. Without force,
// a worktree git calls dirty — or cannot judge at all — is skipped with its
// reason. A dry run removes nothing and returns the identical report.
func (rc *Reclaimer) Reclaim(ctx context.Context, force, dryRun bool) (Report, error) {
	return rc.run(ctx, force, dryRun)
}

// Count is the size-free orphan count for GET /v1/info: a readdir of each
// root plus the id queries, with no size walk and no git, so the endpoint
// stays cheap enough to serve per request and can never be stale after a gc
// run.
func (rc *Reclaimer) Count(ctx context.Context) (int, error) {
	claimed, transcripts, err := rc.claimSets(ctx)
	if err != nil {
		return 0, err
	}
	return len(rc.strays(rc.worktreeRoot(), claimed)) +
		len(rc.strays(rc.transcriptRoot(), transcripts)), nil
}

func (rc *Reclaimer) worktreeRoot() string   { return rc.deps.Worktrees.Root() }
func (rc *Reclaimer) transcriptRoot() string { return filepath.Join(rc.deps.DataDir, "transcripts") }

// run is the one implementation behind Scan and Reclaim: classify under the
// manager's exclusive claim lock, then remove what is eligible without
// releasing it. Holding the lock across the removal is what closes the
// create-and-claim window — a task admitted mid-run waits rather than racing.
func (rc *Reclaimer) run(ctx context.Context, force, dryRun bool) (Report, error) {
	var rep Report
	err := rc.deps.Worktrees.WithReclaimLock(func() error {
		var err error
		rep, err = rc.classify(ctx, force, dryRun)
		if err != nil {
			return err
		}
		if dryRun {
			return nil
		}
		for i := range rep.Orphans {
			o := &rep.Orphans[i]
			if o.Skip != "" {
				continue
			}
			if err := rc.remove(*o); err != nil {
				// One undeletable entry must not strand every orphan behind
				// it — the same rule the transcript pruner already follows,
				// and the Windows locked-file case this feature exists for.
				o.Error = err.Error()
				rc.deps.Logger.Warn("reclaim orphan", "path", o.Path, "error", err)
				continue
			}
			o.Removed = true
			rep.Reclaimed++
			rep.ReclaimedBytes += o.Bytes
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	return rep, nil
}

// remove deletes one orphan. Worktrees go through the manager so its
// containment check applies; transcripts get the identical check against
// their own root, so neither kind can reach outside the data dir whatever a
// claim says.
func (rc *Reclaimer) remove(o Orphan) error {
	if o.Kind == KindWorktree {
		return rc.deps.Worktrees.Reclaim(o.Path)
	}
	if err := containedIn(rc.transcriptRoot(), o.Path); err != nil {
		return err
	}
	if err := os.RemoveAll(o.Path); err != nil {
		return fmt.Errorf("remove %s: %w", o.Path, err)
	}
	return nil
}

// classify diffs both roots against the claim set and decides what each
// entry is. It is the whole definition of an orphan in one place.
func (rc *Reclaimer) classify(ctx context.Context, force, dryRun bool) (Report, error) {
	claims, err := rc.deps.Store.ListWorktreeClaims(ctx)
	if err != nil {
		return Report{}, err
	}
	ids, err := rc.deps.Store.ListTaskIDs(ctx)
	if err != nil {
		return Report{}, err
	}
	rep := Report{DryRun: dryRun, Force: force}

	wtRoot := rc.worktreeRoot()
	for _, e := range rc.strays(wtRoot, claimedPaths(claims)) {
		o := rc.measure(e, wtRoot, KindWorktree)
		if o.Skip == "" && !force {
			o.Skip = rc.dirtiness(ctx, o.Path)
		}
		rep.Orphans = append(rep.Orphans, o)
	}
	tsRoot := rc.transcriptRoot()
	for _, e := range rc.strays(tsRoot, claimedTranscripts(tsRoot, ids)) {
		// No dirty check: a transcript directory is vincent's own output,
		// not a working tree, so there is nothing for git to have an opinion
		// about.
		rep.Orphans = append(rep.Orphans, rc.measure(e, tsRoot, KindTranscript))
	}
	sortOrphans(rep.Orphans)

	for _, c := range claims {
		if c.Path == "" {
			continue
		}
		if _, err := os.Stat(c.Path); errors.Is(err, fs.ErrNotExist) {
			rep.Mismatches = append(rep.Mismatches, Mismatch{
				TaskID: c.TaskID, Path: c.Path, State: string(c.State),
			})
		}
	}
	for _, o := range rep.Orphans {
		rep.Bytes += o.Bytes
	}
	return rep, nil
}

// measure fills in an entry's size and the reason a non-directory is left
// alone. A size that cannot be read is not a reason to hide the orphan: the
// entry is reported with whatever bytes could be accounted for.
func (rc *Reclaimer) measure(e os.DirEntry, root, kind string) Orphan {
	o := Orphan{Path: filepath.Join(root, e.Name()), Kind: kind}
	if id, err := strconv.ParseInt(e.Name(), 10, 64); err == nil {
		o.TaskID = id
	}
	if !e.IsDir() {
		o.Skip = SkipNotADirectory
		if info, err := e.Info(); err == nil {
			o.Bytes = info.Size()
		}
		return o
	}
	size, err := dirSize(o.Path)
	if err != nil {
		rc.deps.Logger.Warn("measure orphan", "path", o.Path, "error", err)
	}
	o.Bytes = size
	return o
}

// dirtiness is the without-force gate on a worktree orphan. git failing
// outright is the *common* answer here, not the exceptional one: an orphan's
// `.git` file points at `{repo}/.git/worktrees/{n}`, and the repo being gone —
// or `git worktree prune` having run in it — makes `git status` fail. That is
// reported as dirty_unknown rather than folded into worktree_dirty, because
// "you have uncommitted work" and "nobody can tell" are different facts and
// only the first is about work that could be lost.
func (rc *Reclaimer) dirtiness(ctx context.Context, path string) string {
	dirty, err := rc.deps.Worktrees.IsDirty(ctx, path)
	switch {
	case err != nil:
		return worktree.ReasonDirtyUnknown
	case dirty:
		return worktree.ReasonWorktreeDirty
	default:
		return ""
	}
}

// claimSets builds both roots' claim sets in one pair of queries.
func (rc *Reclaimer) claimSets(ctx context.Context) (worktrees, transcripts map[string]bool, err error) {
	claims, err := rc.deps.Store.ListWorktreeClaims(ctx)
	if err != nil {
		return nil, nil, err
	}
	ids, err := rc.deps.Store.ListTaskIDs(ctx)
	if err != nil {
		return nil, nil, err
	}
	return claimedPaths(claims), claimedTranscripts(rc.transcriptRoot(), ids), nil
}

// strays lists the entries directly under root that claimed does not name. A
// root that is not there yet has no strays and is not an error — a daemon
// that has never run a task has no worktrees directory.
func (rc *Reclaimer) strays(root string, claimed map[string]bool) []os.DirEntry {
	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			rc.deps.Logger.Warn("read data root", "path", root, "error", err)
		}
		return nil
	}
	out := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if claimed[filepath.Clean(filepath.Join(root, e.Name()))] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// claimedPaths is the set of directories task rows claim.
//
// An empty claim is deliberately *not* in the set. That is the crash window:
// `git worktree add` succeeded, the row's worktree_path was never written, and
// the directory on disk belongs to nobody — which is exactly what makes the
// task's next admission fail `worktree_path_occupied` and tell the user to
// remove a directory by hand. A name-based definition of "orphan" would call
// that directory claimed and leave the block in place forever.
//
// A claim pointing outside the worktree root stays in the set: it still means
// a row owns it, and the containment check is what refuses to act on it.
func claimedPaths(claims []store.WorktreeClaim) map[string]bool {
	out := make(map[string]bool, len(claims))
	for _, c := range claims {
		if c.Path == "" {
			continue
		}
		out[filepath.Clean(c.Path)] = true
	}
	return out
}

// claimedTranscripts is the transcript root's equivalent: a transcript
// directory belongs to its task id for as long as the row exists at all.
// §17 retention decides when an *archived* row's transcripts go; only a row
// that no longer exists is gc's.
func claimedTranscripts(root string, ids []int64) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[filepath.Clean(filepath.Join(root, strconv.FormatInt(id, 10)))] = true
	}
	return out
}

// containedIn refuses a path outside root — the same rule, and the same
// reason, as the worktree manager's own containment check.
func containedIn(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to delete %s: outside %s", path, root)
	}
	return nil
}

// sortOrphans gives the report a stable order — worktrees first, then by
// path. A report a human compares between two runs must not reorder itself,
// and readdir promises nothing.
func sortOrphans(o []Orphan) {
	sort.SliceStable(o, func(i, j int) bool {
		if o[i].Kind != o[j].Kind {
			return o[i].Kind == KindWorktree
		}
		return o[i].Path < o[j].Path
	})
}
