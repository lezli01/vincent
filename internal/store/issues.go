package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// Issue event types (spec §5.6, §13.3, task 130). Like the chat family they
// ride the one durable event table with task_id NULL and the issue's id in
// the payload, and no payload ever carries a title, body or comment text: a
// client re-fetches what it renders. `scheduler.WakeOn` is false for every
// one of them — nothing about admission depends on an issue.
const (
	EventIssueCreated       = "issue.created"
	EventIssueUpdated       = "issue.updated"
	EventIssueStateChanged  = "issue.state_changed"
	EventIssueLabelsChanged = "issue.labels_changed"
	EventIssueCommentAdded  = "issue.comment_added"
	EventIssueDeleted       = "issue.deleted"
	// EventIssueCommentUpdated is a mirrored comment edited on GitHub and
	// updated in place by the sync (task 130.16, decision 24): payload
	// {id, comment_id, by}, never the text.
	EventIssueCommentUpdated = "issue.comment_updated"
	// EventIssueSyncChanged is a project's issue sync turning failing or
	// recovering (task 130.8): payload {project_id, ok, reason?}, written
	// only on a transition, never on each attempt.
	EventIssueSyncChanged = "issue.sync_changed"
	// EventIssueLaneChanged is an issue moving between issuestate lanes
	// (task 134.6): payload {id, from, to, task_id?, by?}. It is written in
	// the commit that moved the lane, right after the event recording the
	// cause, and only when the lane before and after differ. task_id names
	// the root task whose create, transition, delete or restore moved it; by
	// is the actor of a close or reopen. Neither version nor updated_at
	// moves: the lane is derived, never stored (task 134 decision 1).
	EventIssueLaneChanged = "issue.lane_changed"
)

// ErrIssueChanged is UpdateIssue's compare-and-set refusal: the version the
// caller read is no longer the stored one.
var ErrIssueChanged = errors.New("issue changed since it was read")

// ErrInvalidIssueAction is a transition issuestate refuses in the issue's
// current state — the issue analogue of a task's 409.
var ErrInvalidIssueAction = errors.New("action not allowed in this issue state")

// ErrInvalidDuplicateOf is a close whose duplicate_of names no issue in the
// closing issue's project (task 130.3 decision 3).
var ErrInvalidDuplicateOf = errors.New("duplicate_of must name another issue in the same project")

// Issue is a vincent-owned issue (spec §5.6, task 130). Labels, Remote,
// Active, TaskCount, Lane and Attention are read with it; the last four are
// derived from its root tasks and never stored (task 130 decision 3, task
// 134 decision 1).
type Issue struct {
	ID, ProjectID      int64
	Title, Body        string
	State              issuestate.State
	CloseReason        issuestate.Reason // "" when open
	DuplicateOfIssueID *int64
	Kind               string
	Priority           int
	Author             string
	ParentIssueID      *int64 // seam, never written in v1
	// CreatedByTaskID is the task whose agent step created the issue over
	// MCP (§13.4); nil for every other creator.
	CreatedByTaskID *int64
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ClosedAt        *time.Time
	Labels          []string     // names, sorted case-insensitively
	Remote          *IssueRemote // nil for a local issue
	// Sync is the issue's newest state write-back that was not superseded
	// (task 130.10); nil when it never had one.
	Sync *IssueOutbox
	// Active is whether any root task created from the issue is not
	// taskstate.Settled; TaskCount is how many root tasks there are. Lanes
	// never count (decision 5): a twenty-lane tree is one piece of work.
	Active    bool
	TaskCount int
	// Lane is the issue's board lane (task 134 decision 2), always set:
	// `done` when closed, else what its root tasks give it —
	// issuestate.LaneOf. For an open issue Active is exactly
	// Lane == LaneInProgress.
	Lane issuestate.Lane
	// Attention is whether some root task is waiting on a person
	// (taskstate.NeedsHuman, decision 3). It is computed for a closed issue
	// too: closing does not answer a task's question.
	Attention bool
	// MainWorktree is the issue's main branch and its occupant (task 134
	// decisions 2, 8), derived from its main-role tasks in the same query
	// and never stored (task 130 decision 3).
	MainWorktree IssueMainWorktree
}

// IssueRemote is an imported issue's link to its source (decision 2). A nil
// IssueID is a tombstone: the issue was deleted, and sync must not import
// the key again (decision 6).
type IssueRemote struct {
	ID                        int64
	IssueID                   *int64 // nil = tombstone
	ProjectID                 int64
	Provider, RemoteKey, Repo string
	Number                    int
	URL, RemoteJSON           string
	RemoteUpdatedAt, SyncedAt *time.Time
	Suppressed                bool
	// Status is what the last sweep learned of the remote (task 130.8):
	// RemoteStatusLive, RemoteStatusMoved or RemoteStatusMissing.
	Status string
}

// Label is one entry of a project's label catalogue (decision 4). Source is
// "local" or the provider it was imported from.
type Label struct {
	ID, ProjectID                    int64
	Name, Color, Description, Source string
	// IssueCount is how many issues carry the label; ListLabels fills it.
	IssueCount int
}

// IssueComment is a comment on an issue. RemoteKey is the provider's id for
// an imported comment, "" for a local one.
type IssueComment struct {
	ID, IssueID             int64
	Author, Body, RemoteKey string
	CreatedAt, UpdatedAt    time.Time
}

// NewIssue is what CreateIssue inserts. The store validates none of the
// text; internal/issues owns those rules.
type NewIssue struct {
	ProjectID                 int64
	Title, Body, Kind, Author string
	Priority                  int
	Labels                    []string // upserted into the project's catalogue, source "local"
	CreatedByTaskID           *int64
}

// IssuePatch is UpdateIssue's change set; nil means unchanged. Labels
// replaces the whole set; AddLabels and RemoveLabels are a delta over the
// current one, applied after Labels when both are given.
type IssuePatch struct {
	Title, Body, Kind       *string
	Priority                *int
	Labels                  *[]string
	AddLabels, RemoveLabels []string
}

// touchesLabels reports whether p asks for any label change.
func (p IssuePatch) touchesLabels() bool {
	return p.Labels != nil || len(p.AddLabels) > 0 || len(p.RemoveLabels) > 0
}

// Issue list orders.
const (
	IssueSortUpdated = "updated" // most recently updated first; the default
	IssueSortCreated = "created" // newest first
)

// IssueFilter narrows ListIssues. Zero values mean "no filter".
type IssueFilter struct {
	ProjectID int64              // 0 = every project
	States    []issuestate.State // empty = any
	Lanes     []issuestate.Lane  // empty = any; applied in SQL, so Limit/Offset page the filtered set
	Label     string             // case-insensitive name, "" = any
	Labels    []string           // every one must be carried (AND), case-insensitively
	Kind      string             // "" = any
	Source    string             // "" any, "local" no live remote row, else a remote row with that provider
	Text      string             // LIKE match over title and body, "" = any
	Sort      string             // IssueSortUpdated (default) or IssueSortCreated
	Limit     int                // 0 = no limit
	Offset    int                // rows to skip, after the sort
	IDs       []int64            // empty = any; else only these issues
}

// labelSourceLocal is the source of a label a human or an agent created.
const labelSourceLocal = "local"

// issueQuerier is the read subset of *sql.DB and *sql.Tx, so the same read
// serves a plain GetIssue and the in-transaction reads of every write.
type issueQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// issueSelect reads an issue with its live remote row and its derived
// counts, lane, attention (issuelane.go) and main worktree (tasks.go). The
// remote join is on issue_id, so a tombstone never joins. Its bind
// arguments — the task-state sets the derived columns use — come first, from
// issueSelectArgs, which is the one place their order is written.
func issueSelect() string {
	return `SELECT i.id, i.project_id, i.title, i.body, i.state, i.close_reason, i.duplicate_of_issue_id,
		i.kind, i.priority, i.author, i.parent_issue_id, i.created_by_task_id, i.version, i.created_at,
		i.updated_at, i.closed_at,
		(SELECT COUNT(*) FROM tasks t WHERE t.issue_id = i.id AND t.parent_task_id IS NULL),
		` + activeExpr().sql + `,
		` + laneExpr().sql + `,
		` + attentionExpr().sql + `,
		(` + issueMainBranchSQL("i.id") + `),
		(` + issueMainOccupantSQL("i.id", "") + `),
		r.id, r.issue_id, r.project_id, r.provider, r.remote_key, r.repo, r.number, r.url,
		r.remote_json, r.remote_updated_at, r.synced_at, r.suppressed, r.remote_status
	FROM issues i LEFT JOIN issue_remotes r ON r.issue_id = i.id`
}

// The derived columns bind in text order: Active, Lane and Attention's
// fragments, then the main worktree occupant's own arguments.
func issueSelectArgs() []any {
	var args []any
	for _, f := range []sqlFrag{activeExpr(), laneExpr(), attentionExpr()} {
		args = append(args, f.args...)
	}
	return append(args, issueMainOccupantArgs()...)
}

func scanIssue(r rowScanner) (*Issue, error) {
	var (
		iss                                 Issue
		state                               string
		closeReason, closedAt               sql.NullString
		dupOf, parent, createdBy            sql.NullInt64
		created, updated                    string
		active, attention                   bool
		lane                                string
		mainBranch                          sql.NullString
		mainOccupant                        sql.NullInt64
		rID, rIssueID, rProjectID, rNumber  sql.NullInt64
		rProvider, rKey, rRepo, rURL, rJSON sql.NullString
		rRemoteUpdated, rSynced             sql.NullString
		rSuppressed                         sql.NullBool
		rStatus                             sql.NullString
	)
	if err := r.Scan(&iss.ID, &iss.ProjectID, &iss.Title, &iss.Body, &state, &closeReason, &dupOf,
		&iss.Kind, &iss.Priority, &iss.Author, &parent, &createdBy, &iss.Version, &created, &updated, &closedAt,
		&iss.TaskCount, &active, &lane, &attention, &mainBranch, &mainOccupant,
		&rID, &rIssueID, &rProjectID, &rProvider, &rKey, &rRepo, &rNumber, &rURL,
		&rJSON, &rRemoteUpdated, &rSynced, &rSuppressed, &rStatus); err != nil {
		return nil, err
	}
	iss.State = issuestate.State(state)
	iss.CloseReason = issuestate.Reason(closeReason.String)
	iss.Active = active
	iss.Lane = issuestate.Lane(lane)
	iss.Attention = attention
	iss.MainWorktree.Branch = mainBranch.String
	if mainOccupant.Valid {
		id := mainOccupant.Int64
		iss.MainWorktree.OccupantTaskID = &id
	}
	if dupOf.Valid {
		iss.DuplicateOfIssueID = &dupOf.Int64
	}
	if parent.Valid {
		iss.ParentIssueID = &parent.Int64
	}
	if createdBy.Valid {
		iss.CreatedByTaskID = &createdBy.Int64
	}
	var err error
	if iss.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if iss.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, err
	}
	if iss.ClosedAt, err = parseTimePtr(closedAt); err != nil {
		return nil, err
	}
	if rID.Valid {
		rem := &IssueRemote{
			ID:         rID.Int64,
			ProjectID:  rProjectID.Int64,
			Provider:   rProvider.String,
			RemoteKey:  rKey.String,
			Repo:       rRepo.String,
			Number:     int(rNumber.Int64),
			URL:        rURL.String,
			RemoteJSON: rJSON.String,
			Suppressed: rSuppressed.Bool,
			Status:     rStatus.String,
		}
		if rIssueID.Valid {
			rem.IssueID = &rIssueID.Int64
		}
		if rem.RemoteUpdatedAt, err = parseTimePtr(rRemoteUpdated); err != nil {
			return nil, err
		}
		if rem.SyncedAt, err = parseTimePtr(rSynced); err != nil {
			return nil, err
		}
		iss.Remote = rem
	}
	return &iss, nil
}

// GetIssue returns the issue with the given id, or ErrNotFound.
func (s *Store) GetIssue(ctx context.Context, id int64) (*Issue, error) {
	return getIssue(ctx, s.db, id)
}

func getIssue(ctx context.Context, q issueQuerier, id int64) (*Issue, error) {
	args := append(issueSelectArgs(), id)
	iss, err := scanIssue(q.QueryRowContext(ctx, issueSelect()+` WHERE i.id = ?`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("issue %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get issue %d: %w", id, err)
	}
	if err := loadIssueLabels(ctx, q, []*Issue{iss}); err != nil {
		return nil, err
	}
	if err := loadIssueSync(ctx, q, []*Issue{iss}); err != nil {
		return nil, err
	}
	return iss, nil
}

// ListIssues returns the issues matching f, most recently updated first.
func (s *Store) ListIssues(ctx context.Context, f IssueFilter) ([]*Issue, error) {
	//nolint:gosec // G202: issueSelect is a constant query with placeholders() bind markers; every value binds
	q := issueSelect() + ` WHERE 1 = 1`
	args := issueSelectArgs()
	if f.ProjectID != 0 {
		q += ` AND i.project_id = ?`
		args = append(args, f.ProjectID)
	}
	if len(f.States) > 0 {
		q += ` AND i.state IN ` + placeholders(len(f.States))
		for _, st := range f.States {
			args = append(args, string(st))
		}
	}
	if len(f.Lanes) > 0 {
		lane := laneExpr()
		q += ` AND ` + lane.sql + ` IN ` + placeholders(len(f.Lanes))
		args = append(args, lane.args...)
		for _, l := range f.Lanes {
			args = append(args, string(l))
		}
	}
	for _, name := range append([]string{f.Label}, f.Labels...) {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// labels.name is COLLATE NOCASE, so `=` is case-insensitive. One
		// EXISTS per name is the AND.
		q += ` AND EXISTS (SELECT 1 FROM issue_labels il JOIN labels l ON l.id = il.label_id
			WHERE il.issue_id = i.id AND l.name = ?)`
		args = append(args, name)
	}
	if len(f.IDs) > 0 {
		q += ` AND i.id IN ` + placeholders(len(f.IDs))
		for _, id := range f.IDs {
			args = append(args, id)
		}
	}
	if f.Kind != "" {
		q += ` AND i.kind = ?`
		args = append(args, f.Kind)
	}
	switch f.Source {
	case "":
	case labelSourceLocal:
		q += ` AND r.id IS NULL`
	default:
		q += ` AND r.provider = ?`
		args = append(args, f.Source)
	}
	if f.Text != "" {
		like := "%" + escapeLike(f.Text) + "%"
		q += ` AND (i.title LIKE ? ESCAPE '\' OR i.body LIKE ? ESCAPE '\')`
		args = append(args, like, like)
	}
	if f.Sort == IssueSortCreated {
		q += ` ORDER BY i.created_at DESC, i.id DESC`
	} else {
		q += ` ORDER BY i.updated_at DESC, i.id DESC`
	}
	switch {
	case f.Limit > 0:
		q += ` LIMIT ? OFFSET ?`
		args = append(args, f.Limit, max(f.Offset, 0))
	case f.Offset > 0:
		// SQLite has no OFFSET without LIMIT; -1 is its "no limit".
		q += ` LIMIT -1 OFFSET ?`
		args = append(args, f.Offset)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Issue
	for rows.Next() {
		iss, err := scanIssue(rows)
		if err != nil {
			return nil, fmt.Errorf("scan issue: %w", err)
		}
		out = append(out, iss)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	if err := loadIssueLabels(ctx, s.db, out); err != nil {
		return nil, err
	}
	if err := loadIssueSync(ctx, s.db, out); err != nil {
		return nil, err
	}
	return out, nil
}

// escapeLike escapes LIKE's wildcards so a search for "100%" means the text.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// loadIssueLabels fills Labels on every issue in one query.
func loadIssueLabels(ctx context.Context, q issueQuerier, issues []*Issue) error {
	if len(issues) == 0 {
		return nil
	}
	byID := make(map[int64]*Issue, len(issues))
	args := make([]any, 0, len(issues))
	for _, iss := range issues {
		byID[iss.ID] = iss
		args = append(args, iss.ID)
	}
	rows, err := q.QueryContext(ctx, `SELECT il.issue_id, l.name FROM issue_labels il
		JOIN labels l ON l.id = il.label_id WHERE il.issue_id IN `+placeholders(len(args)), args...)
	if err != nil {
		return fmt.Errorf("load issue labels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			id   int64
			name string
		)
		if err := rows.Scan(&id, &name); err != nil {
			return fmt.Errorf("scan issue label: %w", err)
		}
		byID[id].Labels = append(byID[id].Labels, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load issue labels: %w", err)
	}
	for _, iss := range issues {
		sortLabelNames(iss.Labels)
	}
	return nil
}

// sortLabelNames orders names case-insensitively, ties broken by the raw
// spelling so the order is total.
func sortLabelNames(names []string) {
	slices.SortFunc(names, func(a, b string) int {
		if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
}

// normalizeLabelNames trims, drops empties and de-duplicates
// case-insensitively, keeping the first spelling.
func normalizeLabelNames(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	return out
}

// issueEvent builds an issue.* event: the project id on the row, task_id
// NULL — an issue is not a task, and a per-task stream must never deliver
// one — and the issue id plus the actor in the payload.
func issueEvent(evType string, projectID, issueID int64, by issuestate.Actor, extra map[string]any) (*Event, error) {
	body := map[string]any{"id": issueID, "by": string(by)}
	for k, v := range extra {
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", evType, err)
	}
	pid := projectID
	return &Event{Type: evType, ProjectID: &pid, Payload: payload}, nil
}

// laneChangedEvent builds an issue.lane_changed event. Like issueEvent it
// carries the project id and a NULL task_id — the task that moved the lane
// rides the payload, so a per-task stream never delivers it.
func laneChangedEvent(projectID, issueID int64, from, to issuestate.Lane, taskID *int64, by issuestate.Actor) (*Event, error) {
	body := map[string]any{"id": issueID, "from": string(from), "to": string(to)}
	if taskID != nil {
		body["task_id"] = *taskID
	}
	if by != "" {
		body["by"] = string(by)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventIssueLaneChanged, err)
	}
	pid := projectID
	return &Event{Type: EventIssueLaneChanged, ProjectID: &pid, Payload: payload}, nil
}

func checkActor(by issuestate.Actor) error {
	if !issuestate.ValidActor(by) {
		return fmt.Errorf("unknown issue actor %q", by)
	}
	return nil
}

// writeIssue runs fn in one transaction and publishes the event it returns
// after the commit. fn returns a nil event for a write that changed nothing;
// then nothing is published, which is notify's own rule.
func (s *Store) writeIssue(ctx context.Context, fn func(*sql.Tx) (*Event, error)) error {
	return s.writeIssueEvents(ctx, func(tx *sql.Tx) ([]*Event, error) {
		e, err := fn(tx)
		return []*Event{e}, err
	})
}

// writeIssueEvents is writeIssue for a write that announces more than one
// thing — a patch that edits fields and labels at once. Nil entries are
// skipped.
func (s *Store) writeIssueEvents(ctx context.Context, fn func(*sql.Tx) ([]*Event, error)) error {
	var evs []*Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		es, err := fn(tx)
		if err != nil {
			return err
		}
		for _, e := range es {
			if e == nil {
				continue
			}
			if err := appendEventTx(ctx, tx, e); err != nil {
				return err
			}
			evs = append(evs, e)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, e := range evs {
		s.notify(e)
	}
	return nil
}

// CreateIssue inserts a local issue with its labels and writes issue.created
// in the same transaction.
func (s *Store) CreateIssue(ctx context.Context, in NewIssue, by issuestate.Actor) (*Issue, error) {
	return s.CreateIssueWithKey(ctx, in, by, nil)
}

// CreateIssueWithKey is CreateIssue recording key, when non-nil, in the same
// transaction (§13.1, task 130.3): the issue and its key commit together or
// not at all. A key another request already recorded is
// ErrIdempotencyKeyExists, and nothing is created.
func (s *Store) CreateIssueWithKey(ctx context.Context, in NewIssue, by issuestate.Actor, key *IdempotencyKey) (*Issue, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	var id int64
	err := s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		if err := projectExistsTx(ctx, tx, in.ProjectID); err != nil {
			return nil, err
		}
		now := time.Now()
		var err error
		id, err = insertIssueTx(ctx, tx, in, issuestate.Open, "", now)
		if err != nil {
			return nil, err
		}
		if err := replaceIssueLabelsTx(ctx, tx, in.ProjectID, id, in.Labels, labelSourceLocal); err != nil {
			return nil, err
		}
		if key != nil {
			if err := insertIssueIdempotencyKeyTx(ctx, tx, key, id, now); err != nil {
				return nil, err
			}
		}
		return issueEvent(EventIssueCreated, in.ProjectID, id, by, nil)
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

func projectExistsTx(ctx context.Context, tx *sql.Tx, projectID int64) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, projectID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("project %d: %w", projectID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("read project %d: %w", projectID, err)
	}
	return nil
}

func insertIssueTx(ctx context.Context, tx *sql.Tx, in NewIssue, state issuestate.State, reason issuestate.Reason, now time.Time) (int64, error) {
	var closedAt any
	if state == issuestate.Closed {
		closedAt = formatTime(now)
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO issues (project_id, title, body, state, close_reason, kind, priority, author,
			created_by_task_id, created_at, updated_at, closed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ProjectID, in.Title, in.Body, string(state), nullString(string(reason)), in.Kind, in.Priority,
		in.Author, in.CreatedByTaskID, formatTime(now), formatTime(now), closedAt)
	if err != nil {
		return 0, fmt.Errorf("insert issue: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert issue: %w", err)
	}
	return id, nil
}

// UpdateIssue applies p as a compare-and-set on version: a caller holding a
// stale version gets ErrIssueChanged and changes nothing. Field edits and
// label changes land in one transaction under that one check and bump the
// version once (task 130.3), announcing issue.updated for the fields and
// issue.labels_changed for the labels — each only when it moved. A patch
// that changes nothing writes nothing — no version bump, no event.
func (s *Store) UpdateIssue(ctx context.Context, id, version int64, p IssuePatch, by issuestate.Actor) (*Issue, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	err := s.writeIssueEvents(ctx, func(tx *sql.Tx) ([]*Event, error) {
		cur, err := getIssue(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if cur.Version != version {
			return nil, fmt.Errorf("issue %d at version %d, read at %d: %w", id, cur.Version, version, ErrIssueChanged)
		}
		next := *cur
		var changed []string
		if p.Title != nil && *p.Title != cur.Title {
			next.Title = *p.Title
			changed = append(changed, "title")
		}
		if p.Body != nil && *p.Body != cur.Body {
			next.Body = *p.Body
			changed = append(changed, "body")
		}
		if p.Kind != nil && *p.Kind != cur.Kind {
			next.Kind = *p.Kind
			changed = append(changed, "kind")
		}
		if p.Priority != nil && *p.Priority != cur.Priority {
			next.Priority = *p.Priority
			changed = append(changed, "priority")
		}
		var labels []string
		labelsMoved := false
		if p.touchesLabels() {
			labels = applyLabelPatch(cur.Labels, p)
			labelsMoved = !sameLabelSet(cur.Labels, labels)
		}
		if len(changed) == 0 && !labelsMoved {
			return nil, nil
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE issues SET title = ?, body = ?, kind = ?, priority = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?`,
			next.Title, next.Body, next.Kind, next.Priority, formatTime(time.Now()), id, version)
		if err != nil {
			return nil, fmt.Errorf("update issue %d: %w", id, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, fmt.Errorf("update issue %d: %w", id, err)
		} else if n == 0 {
			return nil, fmt.Errorf("issue %d: %w", id, ErrIssueChanged)
		}
		var evs []*Event
		if len(changed) > 0 {
			slices.Sort(changed)
			ev, err := issueEvent(EventIssueUpdated, cur.ProjectID, id, by, map[string]any{"changed": changed})
			if err != nil {
				return nil, err
			}
			evs = append(evs, ev)
		}
		if labelsMoved {
			ev, err := replaceLabelsAndAnnounceTx(ctx, tx, cur, labels, by)
			if err != nil {
				return nil, err
			}
			evs = append(evs, ev)
		}
		return evs, nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// applyLabelPatch returns the label set p leaves: Labels replaces cur when
// set, then AddLabels joins and RemoveLabels leaves, case-insensitively.
func applyLabelPatch(cur []string, p IssuePatch) []string {
	base := cur
	if p.Labels != nil {
		base = *p.Labels
	}
	out := normalizeLabelNames(append(slices.Clone(base), p.AddLabels...))
	if len(p.RemoveLabels) == 0 {
		return out
	}
	drop := make(map[string]bool, len(p.RemoveLabels))
	for _, n := range p.RemoveLabels {
		drop[strings.ToLower(strings.TrimSpace(n))] = true
	}
	return slices.DeleteFunc(out, func(n string) bool { return drop[strings.ToLower(n)] })
}

// TransitionIssue moves an issue through issuestate inside one transaction:
// the state is read, normalized and judged there, so two racing transitions
// cannot both pass. Closing records the reason and the time, and — for a
// duplicate — the issue it duplicates, which must be another issue in the
// same project (ErrInvalidDuplicateOf); reopening clears all three. A sync
// no-op returns the issue unchanged and announces nothing.
//
// A human's or an agent's move of an issue with a live GitHub remote also
// enqueues its write-back in the same transaction (task 130.10), so the
// change, its event and the promise to send it commit together; the
// OnIssueOutboxEnqueued callback is told after the commit.
func (s *Store) TransitionIssue(ctx context.Context, id int64, action issuestate.Action, reason issuestate.Reason, duplicateOf *int64, by issuestate.Actor) (*Issue, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	enqueued := false
	err := s.writeIssueEvents(ctx, func(tx *sql.Tx) ([]*Event, error) {
		cur, err := getIssue(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		from := issuestate.Normalize(cur.State)
		to, noop, ok := issuestate.Next(from, action, by)
		if !ok {
			return nil, fmt.Errorf("issue %d: %s from %s by %s: %w", id, action, from, by, ErrInvalidIssueAction)
		}
		resolved, err := issuestate.ResolveReason(action, reason)
		if err != nil {
			return nil, fmt.Errorf("issue %d: %w", id, err)
		}
		if duplicateOf != nil {
			if to != issuestate.Closed || resolved != issuestate.Duplicate {
				return nil, fmt.Errorf("issue %d: duplicate_of without a duplicate close: %w", id, ErrInvalidDuplicateOf)
			}
			if err := checkDuplicateOfTx(ctx, tx, cur, *duplicateOf); err != nil {
				return nil, err
			}
		}
		if noop {
			return nil, nil
		}
		watch, err := watchIssueLaneTx(ctx, tx, id, cur.ProjectID)
		if err != nil {
			return nil, err
		}
		now := formatTime(time.Now())
		if to == issuestate.Closed {
			_, err = tx.ExecContext(ctx, `
				UPDATE issues SET state = ?, close_reason = ?, closed_at = ?, duplicate_of_issue_id = ?,
					version = version + 1, updated_at = ?
				WHERE id = ?`, string(to), string(resolved), now, duplicateOf, now, id)
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE issues SET state = ?, close_reason = NULL, closed_at = NULL, duplicate_of_issue_id = NULL,
					version = version + 1, updated_at = ?
				WHERE id = ?`, string(to), now, id)
		}
		if err != nil {
			return nil, fmt.Errorf("transition issue %d: %w", id, err)
		}
		if enqueued, err = enqueueStateWriteTx(ctx, tx, cur, to, resolved, duplicateOf, by, time.Now()); err != nil {
			return nil, err
		}
		extra := map[string]any{"from": string(from), "to": string(to), "reason": string(resolved)}
		if duplicateOf != nil {
			extra["duplicate_of"] = *duplicateOf
		}
		ev, err := issueEvent(EventIssueStateChanged, cur.ProjectID, id, by, extra)
		if err != nil {
			return nil, err
		}
		// After the state event, in the same commit (task 134.6 decision 4).
		lane, err := watch.changedTx(ctx, tx, nil, by)
		if err != nil {
			return nil, err
		}
		return []*Event{ev, lane}, nil
	})
	if err != nil {
		return nil, err
	}
	if enqueued {
		s.kickOutbox()
	}
	return s.GetIssue(ctx, id)
}

// checkDuplicateOfTx refuses a duplicate_of that names the issue itself, no
// issue, or an issue in another project.
func checkDuplicateOfTx(ctx context.Context, tx *sql.Tx, cur *Issue, target int64) error {
	if target == cur.ID {
		return fmt.Errorf("issue %d cannot duplicate itself: %w", cur.ID, ErrInvalidDuplicateOf)
	}
	var projectID int64
	err := tx.QueryRowContext(ctx, `SELECT project_id FROM issues WHERE id = ?`, target).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("issue %d does not exist: %w", target, ErrInvalidDuplicateOf)
	}
	if err != nil {
		return fmt.Errorf("read issue %d: %w", target, err)
	}
	if projectID != cur.ProjectID {
		return fmt.Errorf("issue %d is in project %d, not %d: %w", target, projectID, cur.ProjectID, ErrInvalidDuplicateOf)
	}
	return nil
}

// ActiveIssueTaskIDs returns the ids of the issue's root tasks that are not
// settled, lowest first — what Issue.Active summarizes (decision 5).
func (s *Store) ActiveIssueTaskIDs(ctx context.Context, issueID int64) ([]int64, error) {
	cond := unsettledCond()
	//nolint:gosec // G202: the fragment emits bind markers only; every value binds
	q := `SELECT t.id FROM tasks t WHERE t.issue_id = ? AND t.parent_task_id IS NULL
		AND ` + cond.sql + ` ORDER BY t.id`
	rows, err := s.db.QueryContext(ctx, q, append([]any{issueID}, cond.args...)...)
	if err != nil {
		return nil, fmt.Errorf("list issue %d tasks: %w", issueID, err)
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan issue task: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list issue %d tasks: %w", issueID, err)
	}
	return out, nil
}

// DeleteIssue deletes an issue in any state (decision 6). Its remote row
// becomes a tombstone so sync never imports the key again, its tasks keep
// their snapshots with issue_id NULL, and its labels and comments go with it.
func (s *Store) DeleteIssue(ctx context.Context, id int64, by issuestate.Actor) error {
	if err := checkActor(by); err != nil {
		return err
	}
	return s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		var projectID int64
		err := tx.QueryRowContext(ctx, `SELECT project_id FROM issues WHERE id = ?`, id).Scan(&projectID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("issue %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("read issue %d: %w", id, err)
		}
		// Refused before any write while a main task is unsettled (task
		// 134.12): it works in, or waits for, the issue's main worktree, and
		// the issue is what names that line of work. Settled and archived
		// main tasks are history and do not hold the delete.
		live, err := liveIssueMainTaskTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if live != 0 {
			return nil, &IssueHasLiveMainTaskError{IssueID: id, TaskID: live}
		}
		// The FK's SET NULL would do this too; it is spelled out because the
		// tombstone is the point, not a side effect.
		if _, err := tx.ExecContext(ctx, `UPDATE issue_remotes SET issue_id = NULL WHERE issue_id = ?`, id); err != nil {
			return nil, fmt.Errorf("tombstone issue %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, id); err != nil {
			return nil, fmt.Errorf("delete issue %d: %w", id, err)
		}
		return issueEvent(EventIssueDeleted, projectID, id, by, nil)
	})
}

// AddIssueComment appends a comment. A comment is not an edit of the issue,
// so it bumps neither version nor updated_at.
func (s *Store) AddIssueComment(ctx context.Context, issueID int64, author, body, remoteKey string, by issuestate.Actor) (*IssueComment, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	now := time.Now()
	c := &IssueComment{IssueID: issueID, Author: author, Body: body, RemoteKey: remoteKey, CreatedAt: now, UpdatedAt: now}
	err := s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		var projectID int64
		err := tx.QueryRowContext(ctx, `SELECT project_id FROM issues WHERE id = ?`, issueID).Scan(&projectID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("issue %d: %w", issueID, ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("read issue %d: %w", issueID, err)
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO issue_comments (issue_id, author, body, remote_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			issueID, author, body, nullString(remoteKey), formatTime(now), formatTime(now))
		if err != nil {
			return nil, fmt.Errorf("insert issue comment: %w", err)
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			return nil, fmt.Errorf("insert issue comment: %w", err)
		}
		return issueEvent(EventIssueCommentAdded, projectID, issueID, by, map[string]any{"comment_id": c.ID})
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListIssueComments returns an issue's comments, oldest first.
func (s *Store) ListIssueComments(ctx context.Context, issueID int64) ([]*IssueComment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, issue_id, author, body, remote_key, created_at, updated_at
		FROM issue_comments WHERE issue_id = ? ORDER BY created_at ASC, id ASC`, issueID)
	if err != nil {
		return nil, fmt.Errorf("list issue comments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*IssueComment
	for rows.Next() {
		var (
			c                IssueComment
			remoteKey        sql.NullString
			created, updated string
		)
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &remoteKey, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan issue comment: %w", err)
		}
		c.RemoteKey = remoteKey.String
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if c.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list issue comments: %w", err)
	}
	return out, nil
}
