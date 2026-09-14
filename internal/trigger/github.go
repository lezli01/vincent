package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// The GitHub sources (096.3): `type: github_issues` and `type: github_prs`
// are state diffs, not event streams (decision 10). Each trigger keeps the
// last snapshot of what it saw — per number, the fields a transition is read
// from — as JSON in its `trigger_cursors.cursor`, which is opaque by design,
// so a restart is a capped catch-up diff rather than a re-seed (decision 31D).
//
// The daemon owns the fetch. internal/daemon's reconciler lists each project
// that has an armed GitHub trigger once per `github.poll_interval` tick —
// once however many triggers share the project — converts the listing to the
// types below and hands it to JudgeGitHub. This package never reaches the
// network, and never imports internal/github.

// GitHubIssue is one issue as the diff needs it.
type GitHubIssue struct {
	Number    int
	Title     string
	Body      string
	URL       string
	Author    string
	State     string
	Labels    []string
	Assignees []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GitHubPull is one pull request as the diff needs it.
type GitHubPull struct {
	Number             int
	Title              string
	Body               string
	URL                string
	Author             string
	State              string
	HeadRef            string
	BaseRef            string
	Draft              bool
	Merged             bool
	Labels             []string
	RequestedReviewers []string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// GitHubListing is one project's listing for one tick. Err is why there is
// none: github.enabled off, no resolvable repository, or the call failed —
// each of which makes the trigger's poll status failing, never quiet.
type GitHubListing struct {
	Issues []GitHubIssue
	Pulls  []GitHubPull
	Err    error
}

// GitHubWant is what a project's armed GitHub triggers need listed. Since is
// zero when any of them is unseeded, which is a listing of the most recent.
type GitHubWant struct {
	Issues bool
	Pulls  bool
	Since  time.Time
}

// GitHubLister fetches one project's listing.
type GitHubLister func(ctx context.Context, projectID int64, want GitHubWant) GitHubListing

// snapshotRetention bounds the snapshot: a closed item not updated for this
// long is dropped, so a busy repository's cursor does not grow without end.
// It is the ledger's 30 days (decision 13), for the same horizon.
const snapshotRetention = 30 * 24 * time.Hour

type ghSnapshot struct {
	Kind      string            `json:"kind"`
	SeededAt  time.Time         `json:"seeded_at"`
	Watermark time.Time         `json:"watermark"`
	Items     map[string]ghItem `json:"items"`
}

type ghItem struct {
	State     string    `json:"state"`
	Labels    []string  `json:"labels,omitempty"`
	Assignees []string  `json:"assignees,omitempty"`
	Draft     bool      `json:"draft,omitempty"`
	Merged    bool      `json:"merged,omitempty"`
	Reviewers []string  `json:"reviewers,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// decodeSnapshot reads a GitHub trigger's cursor. false is "unseeded": no
// row, a NULL cursor, or one that is not a snapshot (the file changed source
// type while armed), each of which seeds.
func decodeSnapshot(c *store.TriggerCursor) (*ghSnapshot, bool) {
	if c == nil || c.Cursor == nil {
		return &ghSnapshot{Items: map[string]ghItem{}}, false
	}
	var s ghSnapshot
	if err := json.Unmarshal([]byte(*c.Cursor), &s); err != nil || s.Kind == "" {
		return &ghSnapshot{Items: map[string]ghItem{}}, false
	}
	if s.Items == nil {
		s.Items = map[string]ghItem{}
	}
	return &s, true
}

func (s *ghSnapshot) encode() *string {
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	out := string(b)
	return &out
}

func wantFor(d *Definition, snap *ghSnapshot, seeded bool) GitHubWant {
	w := GitHubWant{Issues: d.Source.Type == SourceGitHubIssues, Pulls: d.Source.Type == SourceGitHubPRs}
	if seeded && !snap.Watermark.IsZero() {
		w.Since = snap.Watermark.Add(-githubOverlap)
	}
	return w
}

// GitHubWants returns, per project, what its armed GitHub triggers need
// listed this tick. An empty map is "list nothing".
func (m *Manager) GitHubWants(ctx context.Context) map[int64]GitHubWant {
	out := map[int64]GitHubWant{}
	cursors, err := m.deps.Store.ListTriggerCursors(ctx)
	if err != nil {
		m.log.Warn("trigger cursors not listed", "error", err)
		return out
	}
	unseeded := map[int64]bool{}
	for _, e := range m.deps.Registry.List() {
		if !m.Armed(&e) || !e.Def.IsGitHub() {
			continue
		}
		snap, seeded := decodeSnapshot(cursors[e.ID])
		w := wantFor(e.Def, snap, seeded)
		p := e.Def.Source.Project
		cur, had := out[p]
		cur.Issues = cur.Issues || w.Issues
		cur.Pulls = cur.Pulls || w.Pulls
		switch {
		case !seeded || w.Since.IsZero():
			unseeded[p] = true
		case !had || cur.Since.IsZero() || w.Since.Before(cur.Since):
			cur.Since = w.Since
		}
		out[p] = cur
	}
	for p := range unseeded {
		w := out[p]
		w.Since = time.Time{}
		out[p] = w
	}
	return out
}

// JudgeGitHub judges one project's listing for every armed GitHub trigger on
// it: the reconciler's half of decision 31D.
func (m *Manager) JudgeGitHub(ctx context.Context, projectID int64, listing GitHubListing) {
	m.ghMu.Lock()
	defer m.ghMu.Unlock()
	for _, e := range m.deps.Registry.List() {
		if !m.Armed(&e) || !e.Def.IsGitHub() || e.Def.Source.Project != projectID {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		m.judgeGitHubTrigger(ctx, e.Def, listing)
	}
}

func (m *Manager) judgeGitHubTrigger(ctx context.Context, d *Definition, listing GitHubListing) {
	log := m.log.With("trigger", d.ID)
	prev, err := m.cursorOf(ctx, d.ID)
	if err != nil {
		log.Warn("trigger cursor not read", "error", err)
		return
	}
	now := m.deps.Now()
	next := carry(d.ID, prev, now)
	if listing.Err != nil {
		next.LastPollOK, next.LastPollError = false, listing.Err.Error()
		m.putCursor(ctx, prev, next)
		return
	}
	next.LastPollOK = true
	snap, seeded := decodeSnapshot(prev)
	if !seeded {
		// The first tick after arming stores the snapshot and fires nothing.
		snap = &ghSnapshot{Kind: d.Source.Type, SeededAt: now, Items: map[string]ghItem{}}
		_ = snap.diff(d.Source.Type, listing)
		snap.prune(now)
		next.Cursor = snap.encode()
		log.Info("trigger seeded", "items", len(snap.Items))
		m.putCursor(ctx, prev, next)
		return
	}
	events := m.capEvents(d.ID, snap.diff(d.Source.Type, listing))
	for _, ev := range events {
		del, err := fire(ctx, m.deps.Store, m.deps.Handler, d, ev, now)
		if err != nil {
			next.LastPollOK, next.LastPollError = false, "recording deliveries: "+err.Error()
			log.Error("trigger delivery not recorded", "error", err)
			m.putCursor(ctx, prev, next)
			return
		}
		if del.Outcome == store.DeliveryFired {
			next.LastFireAt = &now
		}
	}
	snap.prune(now)
	next.Cursor = snap.encode()
	m.putCursor(ctx, prev, next)
}

// diff absorbs a listing into the snapshot and returns the events its
// transitions synthesize, oldest change first.
func (s *ghSnapshot) diff(kind string, l GitHubListing) []Event {
	if s.Kind == "" {
		s.Kind = kind
	}
	var out []Event
	if kind == SourceGitHubIssues {
		issues := slices.Clone(l.Issues)
		sort.SliceStable(issues, func(i, j int) bool { return issues[i].UpdatedAt.Before(issues[j].UpdatedAt) })
		for i := range issues {
			out = append(out, s.diffIssue(&issues[i])...)
		}
		return out
	}
	pulls := slices.Clone(l.Pulls)
	sort.SliceStable(pulls, func(i, j int) bool { return pulls[i].UpdatedAt.Before(pulls[j].UpdatedAt) })
	for i := range pulls {
		out = append(out, s.diffPull(&pulls[i])...)
	}
	return out
}

// isNew reports whether an item the snapshot has never seen was created after
// the seed. An older unseen item — past the seed listing's limit — is taken as
// a baseline, because there is nothing to diff it against and "opened" would
// be a lie.
func (s *ghSnapshot) isNew(created time.Time) bool {
	return !created.IsZero() && created.After(s.SeededAt)
}

func (s *ghSnapshot) absorb(key string, it ghItem) (ghItem, bool) {
	old, known := s.Items[key]
	s.Items[key] = it
	if it.UpdatedAt.After(s.Watermark) {
		s.Watermark = it.UpdatedAt
	}
	return old, known
}

func (s *ghSnapshot) diffIssue(is *GitHubIssue) []Event {
	state := strings.ToLower(is.State)
	cur := ghItem{State: state, Labels: sortedCopy(is.Labels), Assignees: sortedCopy(is.Assignees), UpdatedAt: is.UpdatedAt}
	old, known := s.absorb(strconv.Itoa(is.Number), cur)
	issue := map[string]any{
		"Number": is.Number, "Title": is.Title, "Body": is.Body, "URL": is.URL, "Author": is.Author,
		"State": state, "Labels": anyList(is.Labels), "Assignees": anyList(is.Assignees),
		"UpdatedAt": is.UpdatedAt.UTC().Format(time.RFC3339),
	}
	mk := func(action string, extra map[string]any) Event {
		ev := Event{
			"id":     fmt.Sprintf("github:issue:%d:%s:%d", is.Number, action, is.UpdatedAt.Unix()),
			"action": action, "author": is.Author, "state": state, "number": is.Number, "Issue": issue,
		}
		for k, v := range extra {
			ev[k] = v
		}
		return ev
	}
	if !known {
		if s.isNew(is.CreatedAt) {
			return []Event{mk("opened", nil)}
		}
		return nil
	}
	var out []Event
	if old.State != cur.State {
		if cur.State == "closed" {
			out = append(out, mk("closed", nil))
		} else {
			out = append(out, mk("reopened", nil))
		}
	}
	added, removed := setDiff(old.Labels, cur.Labels)
	if len(added) > 0 {
		out = append(out, mk("labeled", map[string]any{"labels": anyList(added)}))
	}
	if len(removed) > 0 {
		out = append(out, mk("unlabeled", map[string]any{"labels": anyList(removed)}))
	}
	if assigned, _ := setDiff(old.Assignees, cur.Assignees); len(assigned) > 0 {
		out = append(out, mk("assigned", map[string]any{"assignees": anyList(assigned)}))
	}
	return out
}

func (s *ghSnapshot) diffPull(p *GitHubPull) []Event {
	state := strings.ToLower(p.State)
	merged := p.Merged
	if state == "merged" {
		// gh spells a merged pull request's state MERGED; REST says closed
		// with merged true. One vocabulary reaches templates: closed + merged.
		state, merged = "closed", true
	}
	cur := ghItem{
		State: state, Labels: sortedCopy(p.Labels), Draft: p.Draft, Merged: merged,
		Reviewers: sortedCopy(p.RequestedReviewers), UpdatedAt: p.UpdatedAt,
	}
	old, known := s.absorb(strconv.Itoa(p.Number), cur)
	pull := map[string]any{
		"Number": p.Number, "Title": p.Title, "Body": p.Body, "URL": p.URL, "Author": p.Author,
		"State": state, "HeadRef": p.HeadRef, "BaseRef": p.BaseRef, "Draft": p.Draft, "Merged": merged,
		"Labels": anyList(p.Labels), "RequestedReviewers": anyList(p.RequestedReviewers),
		"UpdatedAt": p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	mk := func(action string, extra map[string]any) Event {
		ev := Event{
			"id":     fmt.Sprintf("github:pull:%d:%s:%d", p.Number, action, p.UpdatedAt.Unix()),
			"action": action, "author": p.Author, "state": state, "number": p.Number, "Pull": pull,
		}
		for k, v := range extra {
			ev[k] = v
		}
		return ev
	}
	if !known {
		if s.isNew(p.CreatedAt) && state == "open" {
			out := []Event{mk("opened", nil)}
			if len(cur.Reviewers) > 0 {
				out = append(out, mk("review_requested", map[string]any{"reviewer": anyList(cur.Reviewers)}))
			}
			return out
		}
		return nil
	}
	var out []Event
	if old.Draft && !cur.Draft && cur.State == "open" {
		out = append(out, mk("ready_for_review", nil))
	}
	if requested, _ := setDiff(old.Reviewers, cur.Reviewers); len(requested) > 0 {
		out = append(out, mk("review_requested", map[string]any{"reviewer": anyList(requested)}))
	}
	if old.State != "closed" && cur.State == "closed" {
		if cur.Merged {
			out = append(out, mk("merged", nil))
		} else {
			out = append(out, mk("closed", nil))
		}
	}
	return out
}

func (s *ghSnapshot) prune(now time.Time) {
	for k, it := range s.Items {
		if it.State == "closed" && now.Sub(it.UpdatedAt) > snapshotRetention {
			delete(s.Items, k)
		}
	}
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// setDiff returns what cur has that old lacks, and the reverse.
func setDiff(old, cur []string) (added, removed []string) {
	for _, c := range cur {
		if !slices.Contains(old, c) {
			added = append(added, c)
		}
	}
	for _, o := range old {
		if !slices.Contains(cur, o) {
			removed = append(removed, o)
		}
	}
	return added, removed
}

// anyList is a []string as JSON decoding would have produced it, which is
// the shape `match:` and templates walk.
func anyList(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}
