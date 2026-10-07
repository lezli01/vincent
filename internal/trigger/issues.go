package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// The `type: issues` source (task 130.15): vincent's own durable issue.*
// events, mapped to the action vocabulary github_issues has — minus
// `assigned`, since a vincent issue has no assignee (decision 4).
//
// It is not a state diff (decision 2). The store writes the delta into each
// event's payload inside the transaction that makes the change — the labels
// added and removed, the state moved from and to — so the mapper is pure and
// the trigger's only state is the id of the last event it handled, kept in
// `trigger_cursors.cursor` (decision 7). Delivery is at-least-once from the
// events table: the manager wakes on the post-commit broker, reads
// everything after the cursor, and a restart resumes where it left off, so
// an event committed while the daemon was down is still delivered. Arming
// seeds the cursor at the newest event and fires nothing for history, which
// is the rule every other source follows (decision 6); disarming drops it.
//
// One action is vincent's own (task 134.7): `lane_changed`, mapped from the
// issue.lane_changed event task 134.6 writes, carrying the board lane moved
// to and from as `lane` and `from_lane` — never `from`/`to`, which already
// mean the issue state on closed/reopened. A move a task caused reads
// `by: task`, local and so trusted. It is opt-in: a trigger whose
// match.action does not name it never sees it (issueEventsFor), so an armed
// action-less trigger neither starts firing on every task start and finish
// nor fires twice, closed and lane_changed, on every close.

// issueEventTypes are the issue.* events the source reads. Edits, comments,
// deletes and issue.sync_changed fire nothing, so they are not read.
var issueEventTypes = []string{
	store.EventIssueCreated, store.EventIssueStateChanged,
	store.EventIssueLabelsChanged, store.EventIssueUpdated,
	store.EventIssueLaneChanged,
}

// issuesPage bounds one read of the events table.
const issuesPage = 100

// issuePayload is the part of an issue.* event payload the mapper reads.
// Payloads carry ids, names and states only, never text (§13.3).
type issuePayload struct {
	ID            int64    `json:"id"`
	By            string   `json:"by"`
	From          string   `json:"from"`
	To            string   `json:"to"`
	Reason        string   `json:"reason"`
	LabelsAdded   []string `json:"labels_added"`
	LabelsRemoved []string `json:"labels_removed"`
	TaskID        *int64   `json:"task_id"`
}

// issueChange is one trigger event a store event yields, before the issue
// is read to fill it in.
type issueChange struct {
	action string
	labels []string
	state  bool
	lane   bool
}

// mapIssueEvent is the pure half of the source: the trigger events one
// issue.* event yields, in github_issues' order — state, then labels added,
// then labels removed (github.go's diffIssue). One sync refresh can yield
// several.
func mapIssueEvent(typ string, p *issuePayload) []issueChange {
	var out []issueChange
	switch typ {
	case store.EventIssueCreated:
		return []issueChange{{action: "opened"}}
	case store.EventIssueStateChanged, store.EventIssueUpdated:
		if p.To != "" && p.To != p.From {
			action := "reopened"
			if p.To == "closed" {
				action = "closed"
			}
			out = append(out, issueChange{action: action, state: true})
		}
	case store.EventIssueLaneChanged:
		if p.To == "" || p.To == p.From {
			return nil
		}
		return []issueChange{{action: ActionLaneChanged, lane: true}}
	case store.EventIssueLabelsChanged:
	default:
		return nil
	}
	if typ == store.EventIssueStateChanged {
		return out
	}
	if len(p.LabelsAdded) > 0 {
		out = append(out, issueChange{action: "labeled", labels: p.LabelsAdded})
	}
	if len(p.LabelsRemoved) > 0 {
		out = append(out, issueChange{action: "unlabeled", labels: p.LabelsRemoved})
	}
	return out
}

// issueEvents turns one stored event into the trigger events it yields. The
// issue is read now, at judge time, because the event carries no text; an
// issue deleted since yields nothing — there is nothing left to act on.
func issueEvents(ctx context.Context, st Store, e *store.Event) ([]Event, error) {
	var p issuePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil || p.ID <= 0 {
		return nil, nil
	}
	changes := mapIssueEvent(e.Type, &p)
	if len(changes) == 0 {
		return nil, nil
	}
	iss, err := st.GetIssue(ctx, p.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read issue %d: %w", p.ID, err)
	}
	issue := issueMap(iss)
	by := p.By
	if by == "" && p.TaskID != nil {
		// A lane move a root task's write caused has no human, agent or
		// sync actor: the payload names the task instead (task 134.7
		// decision 2).
		by = ByTask
	}
	out := make([]Event, 0, len(changes))
	for _, c := range changes {
		ev := Event{
			"id":         "issue:" + strconv.FormatInt(iss.ID, 10) + ":" + c.action + ":" + strconv.FormatInt(e.ID, 10),
			"action":     c.action,
			"by":         by,
			"author":     iss.Author,
			"state":      string(iss.State),
			"issue_id":   iss.ID,
			"project_id": iss.ProjectID,
			"Issue":      issue,
		}
		if c.labels != nil {
			ev["labels"] = anyList(c.labels)
		}
		if c.state {
			ev["from"], ev["to"], ev["reason"] = p.From, p.To, p.Reason
		}
		if c.lane {
			// From the payload, never a judge-time read, so a burst of
			// moves reports each one as it happened.
			ev["lane"], ev["from_lane"] = p.To, p.From
			if p.TaskID != nil {
				ev["task_id"] = *p.TaskID
			}
		}
		out = append(out, ev)
	}
	return out, nil
}

// issueEventsFor is issueEvents as trigger d sees it: an opt-in action
// (task 134.7 decision 3) is dropped unless d's match.action names it. It
// is the source's one notion of "every action", shared with the load-time
// trust check through defaultEvents.
func issueEventsFor(ctx context.Context, st Store, d *Definition, e *store.Event) ([]Event, error) {
	evs, err := issueEvents(ctx, st, e)
	if err != nil || len(evs) == 0 {
		return evs, err
	}
	out := evs[:0]
	for _, ev := range evs {
		if a, _ := ev["action"].(string); d.wantsAction(a) {
			out = append(out, ev)
		}
	}
	return out, nil
}

// issueMap is an issue in the `.Issue` shape a task's template sees (task
// 130.4), so a trigger and the workflow it starts spell a field the same
// way. Number is the vincent issue id; an imported issue's provider
// reference is Source.
func issueMap(iss *store.Issue) map[string]any {
	snap := store.NewIssueSnapshot(iss, time.Time{})
	source := map[string]any{"Provider": "", "Repo": "", "Number": 0, "URL": "", "State": ""}
	assignee, milestone, milestoneNumber := "", "", 0
	if r := snap.Remote; r != nil {
		source = map[string]any{"Provider": r.Provider, "Repo": r.Repo, "Number": r.Number, "URL": r.URL, "State": r.State}
		if len(r.Assignees) > 0 {
			assignee = strings.Join(r.Assignees, ",")
		}
		milestone, milestoneNumber = r.Milestone, r.MilestoneNumber
	}
	return map[string]any{
		"Number": iss.ID, "Title": iss.Title, "Body": iss.Body, "State": string(iss.State),
		"Labels": anyList(iss.Labels), "Kind": iss.Kind, "Priority": iss.Priority, "Author": iss.Author,
		"Assignee": assignee, "Milestone": milestone, "MilestoneNumber": milestoneNumber,
		"Source": source, "Repo": source["Repo"], "URL": source["URL"],
	}
}

// issueCursor reads the last handled event id out of the cursor column. An
// unparseable value is unseeded, which seeds at the newest event and fires
// nothing — the safe direction, as scheduleAnchor's is.
func issueCursor(prev *store.TriggerCursor) (int64, bool) {
	if prev == nil || prev.Cursor == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(*prev.Cursor, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func formatIssueCursor(id int64) *string {
	s := strconv.FormatInt(id, 10)
	return &s
}

// issuesLoop runs the source: a pass on every issue.* commit the broker
// publishes, and on the reconcile tick as a backstop.
func (m *Manager) issuesLoop(ctx context.Context) {
	ticker := time.NewTicker(reconcileEvery)
	defer ticker.Stop()
	for {
		m.pollIssues(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.issueWake:
		case <-ticker.C:
		}
	}
}

// wakeIssues asks for a pass now.
func (m *Manager) wakeIssues() {
	select {
	case m.issueWake <- struct{}{}:
	default:
	}
}

// pollIssues is one pass over every armed `type: issues` trigger.
func (m *Manager) pollIssues(ctx context.Context) {
	for _, e := range m.deps.Registry.List() {
		if ctx.Err() != nil {
			return
		}
		if !m.Armed(&e) || !e.Def.IsIssues() {
			continue
		}
		func() {
			unlock := m.lockTrigger(e.Def.ID)
			defer unlock()
			if more := m.pollIssuesTrigger(ctx, e.Def); more {
				m.wakeIssues()
			}
		}()
	}
}

// pollIssuesTrigger delivers what one trigger has not yet handled, and
// reports whether more is waiting. A pass judges at most MaxEventsPerPoll
// trigger events (decision 13) — but unlike a command source's catch-up it
// drops nothing past the cap: the cursor stops at the last event handled,
// and the next pass, which this one asks for, carries on from there.
func (m *Manager) pollIssuesTrigger(ctx context.Context, d *Definition) bool {
	log := m.log.With("trigger", d.ID)
	prev, err := m.cursorOf(ctx, d.ID)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("trigger cursor not read", "error", err)
		}
		return false
	}
	now := m.deps.Now()
	next := carry(d.ID, prev, now)
	cursor, seeded := issueCursor(prev)
	if !seeded {
		// Arming seeds at the newest event and fires nothing for history.
		top, err := m.deps.Store.MaxEventID(ctx)
		if err != nil {
			next.LastPollOK, next.LastPollError = false, "reading events: "+err.Error()
			m.putCursor(ctx, prev, next)
			return false
		}
		next.Cursor, next.LastPollOK = formatIssueCursor(top), true
		log.Info("trigger seeded", "event_id", top)
		m.putCursor(ctx, prev, next)
		return false
	}
	evs, err := m.deps.Store.ListEvents(ctx, store.EventFilter{
		AfterID: cursor, Types: issueEventTypes, ProjectID: d.Source.Project, Limit: issuesPage,
	})
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		next.LastPollOK, next.LastPollError = false, "reading events: "+err.Error()
		m.putCursor(ctx, prev, next)
		return false
	}
	if len(evs) == 0 {
		// Nothing new: the row is written only when health moves, so an
		// idle trigger costs a read per pass and no write.
		if prev != nil && prev.LastPollOK {
			return false
		}
		next.LastPollOK = true
		m.putCursor(ctx, prev, next)
		return false
	}
	next.LastPollOK = true
	judged, more := 0, len(evs) == issuesPage
	for i := range evs {
		e := &evs[i]
		mapped, err := issueEventsFor(ctx, m.deps.Store, d, e)
		if err != nil {
			next.LastPollOK, next.LastPollError = false, err.Error()
			log.Warn("trigger issue event not read", "event_id", e.ID, "error", err)
			break
		}
		if judged > 0 && judged+len(mapped) > MaxEventsPerPoll {
			more = true
			break
		}
		failed := false
		for _, ev := range mapped {
			del, err := fire(ctx, m.deps.Store, m.deps.Handler, d, ev, now)
			if err != nil {
				// The cursor stays before this event, so it is judged again;
				// the ledger dedupes what was recorded.
				next.LastPollOK, next.LastPollError = false, "recording deliveries: "+err.Error()
				log.Error("trigger delivery not recorded", "error", err)
				failed = true
				break
			}
			if del.Outcome == store.DeliveryFired {
				next.LastFireAt = &now
			}
		}
		if failed {
			more = false
			break
		}
		judged += len(mapped)
		next.Cursor = formatIssueCursor(e.ID)
	}
	m.putCursor(ctx, prev, next)
	return more
}

// dryIssues is the live-poll dry run of a `type: issues` trigger: what the
// next pass would judge, read without moving the cursor. true is "a real
// pass now would seed and fire nothing".
func (m *Manager) dryIssues(ctx context.Context, d *Definition, prev *store.TriggerCursor) ([]Event, bool, error) {
	cursor, seeded := issueCursor(prev)
	if !seeded {
		return nil, true, nil
	}
	evs, err := m.deps.Store.ListEvents(ctx, store.EventFilter{
		AfterID: cursor, Types: issueEventTypes, ProjectID: d.Source.Project, Limit: issuesPage,
	})
	if err != nil {
		return nil, false, err
	}
	var out []Event
	for i := range evs {
		mapped, err := issueEventsFor(ctx, m.deps.Store, d, &evs[i])
		if err != nil {
			return nil, false, err
		}
		out = append(out, mapped...)
	}
	return out, false, nil
}
