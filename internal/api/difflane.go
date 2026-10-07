package api

// Attributing a fan-out parent's diff to the lanes that produced it
// (§7.6 fan_out, §13.2 the REST API).
//
// After the join, a parent's branch is its own commits interleaved with one
// `--no-ff` merge per lane, and `GET /v1/tasks/{id}/diff` flattens all of it
// into one wall of hunks. Nothing in that wall says which lane wrote what,
// which is the first question a reviewer of a fan-out has. `?by=lane` answers
// it by walking the merges the parent itself made and cutting the diff along
// them.
//
// It is done here rather than in a client for two reasons. A client would need
// one request per lane plus a refetch on every `task.children_changed`, on a
// surface that refreshes live (the task 051 cost objection). And a lane's own
// `/diff` is taken against *its* base, so under `needs:` a lane that merged a
// dependency reports the dependency's work as its own.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// laneMergeSubject matches the message `internal/worktree.MergeLane` writes,
// which §7.6 fixes: `Merge lane '{lane_id}' of task {child_id}`.
//
// That message is a **contract**, not a convenience — this is the only record
// of which commits came from which lane, so changing the wording breaks
// attribution for every branch already on disk. The match is deliberately
// strict (anchored, the id must parse as an integer) and is only ever applied
// to a commit that is both a merge and on the parent's own first-parent chain,
// so an agent that happens to write the same sentence in a commit message of
// its own cannot be mistaken for a lane.
//
// The lane id is captured greedily so that an id which itself contains
// `' of task 123` still resolves to the outermost match — the one git wrote.
var laneMergeSubject = regexp.MustCompile(`^Merge lane '(.+)' of task (\d+)$`)

// laneLogSep separates the fields of one `git log` record. A unit separator
// cannot occur in a sha or in a decimal task id, and a commit subject
// containing one is not a lane merge by the rule above.
const laneLogSep = "\x1f"

// diffLaneSection is one section of a `?by=lane` response: what a single lane
// contributed, or what is left over.
type diffLaneSection struct {
	// LaneID and ChildTaskID name the lane, read out of the merge commit's
	// message. Both are zero on the remainder section.
	LaneID      string `json:"lane_id"`
	ChildTaskID int64  `json:"child_task_id"`
	// MergeCommit is the merge this section was cut from — the thing a reader
	// can go look at themselves. Empty on the remainder section.
	MergeCommit string `json:"merge_commit"`
	// Remainder marks the one section that is not a lane: the parent task's
	// own commits and its uncommitted work.
	Remainder bool `json:"remainder"`
	// Diff is a unified diff in git's own bytes, formatted exactly as the
	// unsectioned endpoint formats its body.
	Diff string `json:"diff"`
}

// diffLanesResponse is the `?by=lane` body.
type diffLanesResponse struct {
	Sections []diffLaneSection `json:"sections"`
}

// laneDiffSections cuts the range mergeBase..worktree into one section per
// lane merge plus a remainder.
//
// The walk is over the parent's **first-parent** chain, which is precisely the
// commits the parent made: a merge inside a lane — its own nested fan_out, or
// the dependency a `needs:` lane merged into itself — is on a second-parent
// side and never appears here, so a lane is credited once and only once.
//
// A lane's section is `diff <merge>^1 <merge>`: what that merge introduced to
// the parent's branch. Diffing the merge's two *parents* against each other
// looks equivalent and is not — lanes all branch from the parent's tip before
// any of them is joined, so `^1` already carries every earlier lane's work
// while `^2` does not, and every lane after the first would report its
// predecessors as deletions.
//
// The remainder is every stretch of the chain no lane merge covers, plus
// `git diff <last anchor>` for the working tree — which is what makes a task
// with no lanes at all come back as one remainder section holding the whole
// diff, byte for byte what the unsectioned endpoint serves.
//
// end is "" for a task holding its worktree, and the range then runs to HEAD
// and the working tree as above. A main task that handed the issue's
// worktree on (task 134.12) passes its end_sha with dir the project
// repository: the range stops at that commit, and the tail is
// `git diff <last anchor> <end>`, since there is no working tree of its own
// left to read.
func (s *Server) laneDiffSections(ctx context.Context, dir, mergeBase, end string) ([]diffLaneSection, error) {
	tip := "HEAD"
	if end != "" {
		tip = end
	}
	log, err := s.firstParentLog(ctx, dir, mergeBase+".."+tip)
	if err != nil {
		return nil, err
	}

	var (
		sections  []diffLaneSection
		remainder []string
		// anchor is the newest commit already accounted for; prev is the
		// commit before the one being read, which on a first-parent chain is
		// exactly its `^1`.
		anchor = mergeBase
		prev   = mergeBase
	)
	for _, rec := range log {
		sha := rec.SHA
		laneID, childID, ok := parseLaneMerge(rec)
		if !ok {
			prev = sha
			continue
		}
		// Whatever the parent committed itself since the last lane merge is
		// remainder, and has to be taken before the anchor moves past it.
		if anchor != prev {
			d, dErr := s.git(ctx, dir, "diff", anchor, prev)
			if dErr != nil {
				return nil, fmt.Errorf("diff the task's own commits: %w", dErr)
			}
			remainder = append(remainder, d)
		}
		d, dErr := s.git(ctx, dir, "diff", prev, sha)
		if dErr != nil {
			return nil, fmt.Errorf("diff lane %q: %w", laneID, dErr)
		}
		sections = append(sections, diffLaneSection{
			LaneID:      laneID,
			ChildTaskID: childID,
			MergeCommit: sha,
			Diff:        formatDiffBody(d),
		})
		anchor, prev = sha, sha
	}

	// The tail is taken against the working tree rather than against HEAD, so
	// the parent's uncommitted work lands in the remainder instead of
	// vanishing — the unsectioned endpoint shows it, and the sections must add
	// up to the same change.
	tailArgs := []string{"diff", anchor}
	if end != "" {
		tailArgs = append(tailArgs, end)
	}
	tail, err := s.git(ctx, dir, tailArgs...)
	if err != nil {
		return nil, fmt.Errorf("diff the task's own work: %w", err)
	}
	remainder = append(remainder, tail)

	return append(sections, diffLaneSection{
		Remainder: true,
		Diff:      formatDiffBody(strings.Join(nonEmpty(remainder), "\n")),
	}), nil
}

// laneLogRecord is one commit of a task's first-parent chain, as
// firstParentLog read it.
type laneLogRecord struct {
	SHA string
	// Parents is git's `%P`: the parent shas, space-separated. Two or more
	// make the commit a merge.
	Parents string
	// AuthorTime is the author date in Unix seconds, as git's `%at` spells it.
	AuthorTime string
	Subject    string
}

// firstParentLog reads the first-parent chain of revRange, oldest first — the
// commits a task made itself (see laneDiffSections for why the first-parent
// walk is what credits each lane exactly once). It is the one reader of that
// chain: the lane-sectioned diff and the commit list both walk it, so a lane
// merge is recognised the same way by both.
func (s *Server) firstParentLog(ctx context.Context, dir, revRange string) ([]laneLogRecord, error) {
	// The subject is last so that nothing it contains can shift a field.
	format := "--format=%H" + laneLogSep + "%P" + laneLogSep + "%at" + laneLogSep + "%s"
	log, err := s.git(ctx, dir, "log", "--first-parent", "--reverse", format, revRange)
	if err != nil {
		return nil, fmt.Errorf("list the task's own commits: %w", err)
	}
	var out []laneLogRecord
	for _, line := range strings.Split(log, "\n") {
		// git writes LF here on every platform, but a commit message that
		// itself ended a line with CR would leave one on the subject and the
		// anchored lane match would then never fire — on Windows only, which
		// is the worst place to find out.
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		f := strings.SplitN(line, laneLogSep, 4)
		for len(f) < 4 {
			f = append(f, "")
		}
		out = append(out, laneLogRecord{SHA: f[0], Parents: f[1], AuthorTime: f[2], Subject: f[3]})
	}
	return out, nil
}

// parseLaneMerge reports the lane only for a commit that is both a merge and
// carries the contract message.
func parseLaneMerge(rec laneLogRecord) (laneID string, childID int64, ok bool) {
	if len(strings.Fields(rec.Parents)) < 2 {
		return "", 0, false
	}
	m := laneMergeSubject.FindStringSubmatch(rec.Subject)
	if m == nil {
		return "", 0, false
	}
	id, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		// A task id too large to be one. Not a lane merge vincent wrote.
		return "", 0, false
	}
	return m[1], id, true
}

// formatDiffBody spells a section's diff the way the unsectioned endpoint
// spells its body: git's own bytes with a single trailing newline, and the
// empty string for no change at all.
func formatDiffBody(diff string) string {
	if diff == "" {
		return ""
	}
	return diff + "\n"
}

func nonEmpty(in []string) []string {
	out := in[:0:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
