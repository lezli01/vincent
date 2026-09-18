package trigger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// Store is what the pipeline and the poller need from internal/store. It is
// an interface so the package's dependency on the store is exactly this
// list; the tests use the real store over a temp database.
type Store interface {
	GetTriggerCursor(ctx context.Context, triggerID string) (*store.TriggerCursor, error)
	ListTriggerCursors(ctx context.Context) (map[string]*store.TriggerCursor, error)
	PutTriggerCursor(ctx context.Context, c *store.TriggerCursor) error
	DeleteTriggerCursor(ctx context.Context, triggerID string) error
	RecordTriggerDelivery(ctx context.Context, d *store.TriggerDelivery) (*store.TriggerDelivery, error)
	TriggerKeyDelivered(ctx context.Context, triggerID, dedupeKey string) (bool, error)
	CountTriggerFiredSince(ctx context.Context, triggerID string, since time.Time) (int, error)
	FindTaskForBranch(ctx context.Context, projectID int64, branch string) (*store.Task, error)
	AppendEvent(ctx context.Context, e *store.Event) error
	// The overrun group and its backlog (task 121).
	TriggerGroupInFlight(ctx context.Context, triggerID, key string) ([]int64, error)
	TaskInFlight(ctx context.Context, id int64) (bool, error)
	AppendTriggerBacklog(ctx context.Context, b *store.TriggerBacklogItem) (*store.TriggerBacklogItem, error)
	ListTriggerBacklog(ctx context.Context, triggerID, key string) ([]store.TriggerBacklogItem, error)
	ListTriggerBacklogFor(ctx context.Context, triggerID string) ([]store.TriggerBacklogItem, error)
	ListTriggerBacklogGroups(ctx context.Context) ([]store.TriggerBacklogGroup, error)
	DeleteTriggerBacklog(ctx context.Context, ids ...int64) error
}

// MaxBacklogPerTrigger caps held events per trigger (task 121). At the cap
// the *oldest* pending event is dropped and recorded `superseded`, which is
// right for queue_coalesce and acceptable for queue_serial: a backlog that
// deep is one whose group is not draining, and the recorded drop is what makes
// that explicable rather than silent.
const MaxBacklogPerTrigger = 100

// MaxEventsPerPoll caps catch-up (decision 13): at most this many events of
// one poll are judged, and the rest are dropped with a warning. Twenty, fixed,
// because the consequence of a storm is not a hundred toasts but a hundred
// agent processes and worktrees (decision 6).
const MaxEventsPerPoll = 20

// createPath is the route a create_task action replays.
const createPath = "/v1/tasks"

// renderData is the one root trigger templates see (decision 11).
type renderData struct {
	Event map[string]any
}

// CreateBody is the POST /v1/tasks body a create_task action replays. The
// omitempty on every widening field is deliberate: a body without them is
// byte-for-byte the one a person's client sends, so the idempotency digest
// and the §13.1 bounds see nothing new unless the trigger asked for it.
type CreateBody struct {
	ProjectID      int64             `json:"project_id"`
	Workflow       string            `json:"workflow,omitempty"`
	Title          string            `json:"title"`
	Description    string            `json:"description,omitempty"`
	Fields         map[string]string `json:"fields,omitempty"`
	GitHubIssue    *int              `json:"github_issue,omitempty"`
	GitHubPull     *int              `json:"github_pull,omitempty"`
	Paused         bool              `json:"paused,omitempty"`
	Restricted     bool              `json:"restricted,omitempty"`
	MaxTaskCostUSD *float64          `json:"max_task_cost_usd,omitempty"`
}

// FollowUpBody is the POST /v1/tasks/{id}/follow_up body a follow_up
// replays; Paused is propose (decision 31C).
type FollowUpBody struct {
	Prompt string `json:"prompt"`
	Paused bool   `json:"paused,omitempty"`
}

// RetryBody is the POST /v1/tasks/{id}/retry body a retry replays.
type RetryBody struct {
	PromptOverride string `json:"prompt_override,omitempty"`
	Paused         bool   `json:"paused,omitempty"`
}

// Replay is a rendered action: the request the pipeline sends, and for a
// reaction the task it resolved. A dry run returns it unsent.
type Replay struct {
	Type   string `json:"type"`
	Method string `json:"method"`
	Path   string `json:"path"`
	// Branch is a reaction's rendered branch; TaskID the task it names.
	Branch string `json:"branch,omitempty"`
	TaskID *int64 `json:"task_id,omitempty"`
	// Body is one of CreateBody, FollowUpBody or RetryBody, nil for cancel.
	Body any `json:"body,omitempty"`
}

// Judgement is what the pipeline decided about one event. A dry run returns
// it without having written anything.
type Judgement struct {
	EventID string `json:"event_id"`
	// Matched is the `match:` result; MatchMiss names the first key that
	// failed, or `allowed_actors` for an author the list does not name.
	Matched   bool   `json:"matched"`
	MatchMiss string `json:"match_miss,omitempty"`
	// If is the guard's verdict, nil when it was not reached or there is no
	// guard; IfRendered is what it rendered to.
	If         *bool  `json:"if,omitempty"`
	IfRendered string `json:"if_rendered,omitempty"`
	// DedupeKey is the rendered key; WouldDedupe says the ledger already
	// holds a `fired` or `seeded` row for it.
	DedupeKey   string `json:"dedupe_key,omitempty"`
	WouldDedupe bool   `json:"would_dedupe"`
	// Overrun is the mode consulted, "" when the trigger takes the `parallel`
	// default and no group was looked at; ConcurrencyKey the rendered group;
	// InFlight the group's unsettled tasks. The three Would* flags are the
	// decision, and sit beside WouldDedupe for the same reason: a dry run has
	// to be able to show a skip coming.
	Overrun        string  `json:"overrun,omitempty"`
	ConcurrencyKey string  `json:"concurrency_key,omitempty"`
	InFlight       []int64 `json:"in_flight,omitempty"`
	WouldSkip      bool    `json:"would_skip,omitempty"`
	WouldCancel    bool    `json:"would_cancel,omitempty"`
	WouldQueue     bool    `json:"would_queue,omitempty"`
	// Action is the rendered request, nil when rendering was not reached or
	// failed.
	Action *Replay `json:"action,omitempty"`
	// Outcome is the ledger outcome this event gets (or, in a dry run,
	// would get short of the replay: a dry run that reaches the replay
	// reports "fired" meaning "would be replayed").
	Outcome string `json:"outcome"`
	// Error is a render failure's or an unresolved target's message.
	Error string `json:"error,omitempty"`
}

// plan is what a drain knows that a freshly arrived event does not: the event
// came off the backlog under a group already decided, and the overrun step
// must not run again on it (task 121 decision 6 — a drained event is
// re-judged in full *minus* the overrun step, or it would queue itself
// forever).
type plan struct {
	drained bool
	group   string
}

// judge runs the pipeline's read-only steps — match, if, dedupe, rate limit,
// render, overrun — and writes nothing. fire and the dry runs all start here,
// which is what makes a dry run the real pipeline rather than a
// re-derivation of it.
func judge(ctx context.Context, st Store, d *Definition, ev Event, now time.Time) (*Judgement, error) {
	return judgePlan(ctx, st, d, ev, now, plan{})
}

func judgePlan(ctx context.Context, st Store, d *Definition, ev Event, now time.Time, p plan) (*Judgement, error) {
	j := &Judgement{EventID: ev.ID()}
	data := renderData{Event: ev}

	j.Matched, j.MatchMiss = matchEvent(d.Match, ev)
	if j.Matched && len(d.AllowedActors) > 0 && !actorAllowed(d.AllowedActors, ev) {
		j.Matched, j.MatchMiss = false, "allowed_actors"
	}
	if !j.Matched {
		j.Outcome = store.DeliveryFiltered
		return j, nil
	}
	if d.If != "" {
		ok, rendered, err := workflow.EvaluateWith("if", d.If, data)
		j.IfRendered = rendered
		if err != nil {
			j.Outcome, j.Error = store.DeliveryError, err.Error()
			return j, nil
		}
		j.If = &ok
		if !ok {
			j.Outcome = store.DeliveryFiltered
			return j, nil
		}
	}

	key, err := dedupeKey(d, ev, data)
	if err != nil {
		j.Outcome, j.Error = store.DeliveryError, err.Error()
		return j, nil
	}
	j.DedupeKey = key
	delivered, err := st.TriggerKeyDelivered(ctx, d.ID, key)
	if err != nil {
		return nil, err
	}
	j.WouldDedupe = delivered
	if delivered {
		j.Outcome = store.DeliveryDeduped
		return j, nil
	}

	if d.Limits.MaxPerHour > 0 {
		n, err := st.CountTriggerFiredSince(ctx, d.ID, now.Add(-time.Hour))
		if err != nil {
			return nil, err
		}
		if n >= d.Limits.MaxPerHour {
			j.Outcome = store.DeliveryRateLimited
			return j, nil
		}
	}

	rp, err := renderAction(d, data)
	if err != nil {
		j.Outcome, j.Error = store.DeliveryError, err.Error()
		return j, nil
	}
	j.Action = rp
	if rp.Branch != "" {
		task, err := st.FindTaskForBranch(ctx, d.Source.Project, rp.Branch)
		if errors.Is(err, store.ErrNotFound) {
			// Decision 31C: nothing to act on is the route's own refusal in
			// spirit — a 404 for a task that is not there — so it is
			// `refused`, and the detail names the branch that was looked for.
			j.Outcome = store.DeliveryRefused
			j.Error = fmt.Sprintf("no unarchived task in project %d is on branch %q", d.Source.Project, rp.Branch)
			return j, nil
		}
		if err != nil {
			return nil, err
		}
		id := task.ID
		rp.TaskID = &id
		rp.Path = strings.Replace(rp.Path, "{id}", strconv.FormatInt(id, 10), 1)
	}
	// The overrun check is the last step, after render and after a reaction's
	// target resolution (decision 7): a reaction cannot know its group before
	// the target exists, and one position keeps one definition. It reads only,
	// so POST /v1/triggers/{id}/test reports it exactly as it reports
	// would_dedupe and writes nothing.
	if err := applyOverrun(ctx, st, d, j, data, p); err != nil {
		return nil, err
	}
	if j.Outcome != "" {
		return j, nil
	}
	j.Outcome = store.DeliveryFired
	return j, nil
}

// applyOverrun is the group-liveness step. It sets j.Outcome only when the
// event is suppressed or held; a `cancel_previous` that found work leaves the
// outcome open — it still fires, having cancelled first.
func applyOverrun(ctx context.Context, st Store, d *Definition, j *Judgement, data renderData, p plan) error {
	mode := d.EffectiveOverrun()
	if mode == OverrunParallel {
		return nil
	}
	key := p.group
	if !p.drained {
		rendered, err := concurrencyKey(d, j, data)
		if err != nil {
			j.Outcome, j.Error = store.DeliveryError, err.Error()
			return nil
		}
		key = rendered
	}
	j.Overrun, j.ConcurrencyKey = mode, key
	if p.drained {
		// The group was empty when the drain picked this event up, and the
		// drain is serialized with every other firing path for this trigger.
		return nil
	}
	inFlight, err := st.TriggerGroupInFlight(ctx, d.ID, key)
	if err != nil {
		return err
	}
	// A reaction's group is its resolved target's own state, ledger or no
	// ledger (decision 2): on the first follow-up that task has never
	// appeared in this trigger's ledger, and the ledger cannot answer for it.
	if j.Action != nil && j.Action.TaskID != nil && !slices.Contains(inFlight, *j.Action.TaskID) {
		live, err := st.TaskInFlight(ctx, *j.Action.TaskID)
		if err != nil {
			return err
		}
		if live {
			inFlight = append([]int64{*j.Action.TaskID}, inFlight...)
		}
	}
	j.InFlight = inFlight
	if len(inFlight) == 0 {
		return nil
	}
	switch mode {
	case OverrunSkip:
		j.WouldSkip, j.Outcome = true, store.DeliverySuperseded
	case OverrunCancelPrevious:
		j.WouldCancel = true
	default:
		j.WouldQueue, j.Outcome = true, store.DeliveryQueued
	}
	return nil
}

// concurrencyKey renders `concurrency_key:`, or supplies the absent default:
// the resolved target task for a reaction, the trigger id for a create_task
// (decision 2). The reaction default is namespaced so it cannot collide with
// a trigger whose id is a number.
func concurrencyKey(d *Definition, j *Judgement, data renderData) (string, error) {
	if d.ConcurrencyKey == "" {
		if j.Action != nil && j.Action.TaskID != nil {
			return "task:" + strconv.FormatInt(*j.Action.TaskID, 10), nil
		}
		return d.ID, nil
	}
	key, err := workflow.RenderWith("concurrency_key", d.ConcurrencyKey, data)
	if err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("concurrency_key rendered to an empty string")
	}
	return key, nil
}

// actorAllowed matches allowed_actors against the event's author — on a
// GitHub source, the issue's or pull request's (decision 31F).
func actorAllowed(allowed []string, ev Event) bool {
	author, _ := ev["author"].(string)
	for _, a := range allowed {
		if author != "" && strings.EqualFold(a, author) {
			return true
		}
	}
	return false
}

func dedupeKey(d *Definition, ev Event, data renderData) (string, error) {
	if d.DedupeKey == "" {
		if ev.ID() == "" {
			return "", errors.New("the event has no id and the trigger declares no dedupe_key")
		}
		return ev.ID(), nil
	}
	key, err := workflow.RenderWith("dedupe_key", d.DedupeKey, data)
	if err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("dedupe_key rendered to an empty string")
	}
	return key, nil
}

// renderAction renders an action into the request it replays.
func renderAction(d *Definition, data renderData) (*Replay, error) {
	a := d.Action
	render := func(field, text string) (string, error) {
		if text == "" {
			return "", nil
		}
		return workflow.RenderWith("action."+field, text, data)
	}
	propose := d.EffectiveOnFire() != OnFireCreate
	switch a.Type {
	case ActionFollowUp, ActionRetry, ActionCancel:
		branch, err := render("branch", a.Branch)
		if err != nil {
			return nil, err
		}
		branch = strings.TrimSpace(branch)
		if branch == "" {
			return nil, errors.New("action.branch rendered to an empty string")
		}
		prompt, err := render("prompt", a.Prompt)
		if err != nil {
			return nil, err
		}
		rp := &Replay{Type: a.Type, Method: http.MethodPost, Branch: branch}
		switch a.Type {
		case ActionFollowUp:
			rp.Path = "/v1/tasks/{id}/follow_up"
			rp.Body = &FollowUpBody{Prompt: prompt, Paused: propose}
		case ActionRetry:
			rp.Path = "/v1/tasks/{id}/retry"
			rp.Body = &RetryBody{PromptOverride: prompt, Paused: propose}
		default:
			rp.Path = "/v1/tasks/{id}/cancel"
		}
		return rp, nil
	}
	body := &CreateBody{
		ProjectID:  d.Source.Project,
		Paused:     propose,
		Restricted: d.EffectivePermission() != PermissionWorkflow,
	}
	var err error
	if body.Workflow, err = render("workflow", a.Workflow); err != nil {
		return nil, err
	}
	body.Workflow = strings.TrimSpace(body.Workflow)
	if body.Title, err = render("title", a.Title); err != nil {
		return nil, err
	}
	body.Title = strings.TrimSpace(body.Title)
	if body.Description, err = render("description", a.Description); err != nil {
		return nil, err
	}
	for k, v := range a.Fields {
		out, err := render("fields."+k, v)
		if err != nil {
			return nil, err
		}
		if body.Fields == nil {
			body.Fields = map[string]string{}
		}
		body.Fields[k] = out
	}
	if body.GitHubIssue, err = renderNumber("github_issue", a.GitHubIssue, data); err != nil {
		return nil, err
	}
	if body.GitHubPull, err = renderNumber("github_pull", a.GitHubPull, data); err != nil {
		return nil, err
	}
	if d.Limits.MaxTaskCostUSD > 0 {
		c := d.Limits.MaxTaskCostUSD
		body.MaxTaskCostUSD = &c
	}
	return &Replay{Type: ActionCreateTask, Method: http.MethodPost, Path: createPath, Body: body}, nil
}

// renderNumber renders an issue or pull-request template: a positive number,
// or nothing at all.
func renderNumber(field, text string, data renderData) (*int, error) {
	if text == "" {
		return nil, nil
	}
	out, err := workflow.RenderWith("action."+field, text, data)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(out, "#"))
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("action.%s rendered %q, not a positive number", field, out)
	}
	return &n, nil
}

// IdempotencyKey is the header a trigger's replay carries for a dedupe key.
//
// It is namespaced and hashed rather than the rendered key verbatim. The
// API's key scope is (method, path, key) across *every* client, so two
// triggers — or a trigger and a CI job pushing in with its build id (096.1)
// — rendering the same string would otherwise replay each other's tasks or
// collide into 409 idempotency_key_reused. The hash also keeps the header
// inside §13.1's 255-byte, printable-ASCII bound whatever the key's text is.
// The ledger keeps the rendered key itself.
func IdempotencyKey(triggerID, dedupeKey string) string {
	sum := sha256.Sum256([]byte(dedupeKey))
	return "trigger:" + triggerID + ":" + hex.EncodeToString(sum[:16])
}

// replayResult is what the in-process route answered.
type replayResult struct {
	status int
	body   []byte
}

// replay sends a rendered action into the daemon's handler in-process, the
// way internal/mcp replays a tool call (decision 1, decision record row 28),
// so the route's validation, bounds, the task 041 creation gate, the §6 FSM's
// 409 and Idempotency-Key apply by construction.
func replay(ctx context.Context, h http.Handler, key string, rp *Replay) (*replayResult, error) {
	var body []byte
	if rp.Body != nil {
		b, err := json.Marshal(rp.Body)
		if err != nil {
			return nil, fmt.Errorf("encode %s body: %w", rp.Type, err)
		}
		body = b
	}
	req, err := http.NewRequestWithContext(ctx, rp.Method, rp.Path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build replay: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(len(body))
	}
	// Only the create route honours §13.1's key; a reaction's dedupe is the
	// ledger's alone, and a header its route ignores would be a claim.
	if rp.Type == ActionCreateTask {
		req.Header.Set("Idempotency-Key", key)
	}
	// The inner mux is reached below the listener's bearer and Host checks,
	// as MCP's replay is; these are set so a handler that reads them sees a
	// loopback caller rather than an empty one.
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:0"
	rec := &recorder{limit: 256 << 10}
	h.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return &replayResult{status: rec.status, body: rec.buf.Bytes()}, nil
}

// recorder is a bounded in-memory ResponseWriter.
type recorder struct {
	status int
	header http.Header
	buf    bytes.Buffer
	limit  int
}

func (r *recorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if room := r.limit - r.buf.Len(); room < len(p) {
		if room > 0 {
			r.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return r.buf.Write(p) //nolint:wrapcheck // bytes.Buffer.Write never errors
}

// Delivery is one event's recorded outcome.
type Delivery struct {
	Judgement
	DeliveryID int64  `json:"delivery_id"`
	TaskID     *int64 `json:"task_id,omitempty"`
	// SupersededTaskID is the task a `cancel_previous` fire replaced.
	SupersededTaskID *int64 `json:"superseded_task_id,omitempty"`
	Detail           string `json:"detail,omitempty"`
}

// hold writes a held event to the backlog and records its `queued` ledger
// row, dropping the trigger's oldest held event first when the cap is met.
func hold(ctx context.Context, st Store, d *Definition, ev Event, del *Delivery, now time.Time) (*Delivery, error) {
	payload, err := json.Marshal(map[string]any(ev))
	if err != nil {
		return nil, fmt.Errorf("encode held event: %w", err)
	}
	// An event this group already holds is not held twice. Decision 8 keeps a
	// `queued` row out of the dedupe lookup so a *repeat* event reaches the
	// overrun step rather than being swallowed — but a source that re-shows
	// its whole window every poll (one that keeps no cursor, which is the
	// shape decision 31B's `seeded` rows exist for) would otherwise append the
	// same event once a second until it hit the cap, and queue_serial would
	// fire it once per copy.
	group, err := st.ListTriggerBacklog(ctx, d.ID, del.ConcurrencyKey)
	if err != nil {
		return nil, err
	}
	for _, it := range group {
		if it.EventID != "" && it.EventID == del.EventID {
			del.Outcome, del.Detail = store.DeliverySuperseded, "already held for this group"
			row, err := st.RecordTriggerDelivery(ctx, &store.TriggerDelivery{
				TriggerID: d.ID, EventID: del.EventID, DedupeKey: del.DedupeKey,
				ConcurrencyKey: del.ConcurrencyKey, Outcome: del.Outcome,
				Detail: del.Detail, CreatedAt: now,
			})
			if err != nil {
				return nil, err
			}
			del.DeliveryID = row.ID
			return del, nil
		}
	}
	held, err := st.ListTriggerBacklogFor(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	for i := 0; len(held)-i >= MaxBacklogPerTrigger; i++ {
		old := held[i]
		if err := st.DeleteTriggerBacklog(ctx, old.ID); err != nil {
			return nil, err
		}
		if _, err := st.RecordTriggerDelivery(ctx, &store.TriggerDelivery{
			TriggerID: d.ID, EventID: old.EventID, ConcurrencyKey: old.ConcurrencyKey,
			Outcome:   store.DeliverySuperseded,
			Detail:    fmt.Sprintf("dropped: the backlog reached its cap of %d held events", MaxBacklogPerTrigger),
			CreatedAt: now,
		}); err != nil {
			return nil, err
		}
	}
	if _, err := st.AppendTriggerBacklog(ctx, &store.TriggerBacklogItem{
		TriggerID: d.ID, ConcurrencyKey: del.ConcurrencyKey, EventID: del.EventID,
		EventJSON: payload, CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	row, err := st.RecordTriggerDelivery(ctx, &store.TriggerDelivery{
		TriggerID: d.ID, EventID: del.EventID, DedupeKey: del.DedupeKey,
		ConcurrencyKey: del.ConcurrencyKey, Outcome: store.DeliveryQueued,
		Detail: del.Detail, CreatedAt: now,
	})
	if err != nil {
		return nil, err
	}
	del.DeliveryID = row.ID
	return del, nil
}

// fire runs the whole pipeline for one event, records its ledger row and, for
// a `fired` delivery, publishes trigger.fired post-commit.
func fire(ctx context.Context, st Store, h http.Handler, d *Definition, ev Event, now time.Time) (*Delivery, error) {
	return firePlan(ctx, st, h, d, ev, now, plan{})
}

func firePlan(ctx context.Context, st Store, h http.Handler, d *Definition, ev Event, now time.Time, p plan) (*Delivery, error) {
	j, err := judgePlan(ctx, st, d, ev, now, p)
	if err != nil {
		return nil, err
	}
	del := &Delivery{Judgement: *j, Detail: j.Error}
	if j.Outcome == store.DeliveryQueued {
		return hold(ctx, st, d, ev, del, now)
	}
	if j.WouldCancel {
		// Cancel every task in the group before creating the new one, and
		// record the newest as the one this delivery superseded (decision 4).
		// A cancel that the FSM refuses is not this delivery's failure: the
		// task settled between the group read and here, which is the outcome
		// the cancel was for.
		for _, id := range j.InFlight {
			rp := &Replay{
				Type: ActionCancel, Method: http.MethodPost,
				Path: "/v1/tasks/" + strconv.FormatInt(id, 10) + "/cancel",
			}
			if _, cerr := replay(ctx, h, "", rp); cerr != nil {
				del.Outcome, del.Detail = store.DeliveryError, cerr.Error()
				break
			}
		}
		if del.Outcome == store.DeliveryFired {
			superseded := j.InFlight[0]
			del.SupersededTaskID = &superseded
		}
	}
	if j.Outcome == store.DeliveryFired && del.Outcome == store.DeliveryFired {
		res, rerr := replay(ctx, h, IdempotencyKey(d.ID, j.DedupeKey), j.Action)
		switch {
		case rerr != nil:
			del.Outcome, del.Detail = store.DeliveryError, rerr.Error()
		case res.status >= 200 && res.status < 300:
			if j.Action.TaskID != nil {
				// A reaction's ledger task is the task it acted on (#362:
				// "created or acted on").
				del.TaskID = j.Action.TaskID
				break
			}
			var created struct {
				ID int64 `json:"id"`
			}
			if uerr := json.Unmarshal(res.body, &created); uerr != nil || created.ID == 0 {
				del.Outcome, del.Detail = store.DeliveryError, "create answered "+strconv.Itoa(res.status)+" without a task id"
			} else {
				del.TaskID = &created.ID
			}
		case res.status >= 400 && res.status < 500:
			// A 409 from an FSM-invalid target lands here too: appendix B
			// example 5's "the same 409 a client would get".
			del.Outcome, del.Detail = store.DeliveryRefused, string(res.body)
			del.TaskID = j.Action.TaskID
		default:
			del.Outcome, del.Detail = store.DeliveryError, strconv.Itoa(res.status)+": "+string(res.body)
		}
	}
	row, err := st.RecordTriggerDelivery(ctx, &store.TriggerDelivery{
		TriggerID: d.ID, EventID: del.EventID, DedupeKey: del.DedupeKey,
		ConcurrencyKey: del.ConcurrencyKey, Outcome: del.Outcome, TaskID: del.TaskID,
		SupersededTaskID: del.SupersededTaskID, Detail: del.Detail, CreatedAt: now,
	})
	if err != nil {
		return nil, err
	}
	del.DeliveryID = row.ID
	if del.Outcome == store.DeliveryFired {
		publishFired(ctx, st, d, del)
	}
	return del, nil
}

// publishFired appends trigger.fired. A failure is not the delivery's: the
// ledger row is the durable record, the event only its announcement.
func publishFired(ctx context.Context, st Store, d *Definition, del *Delivery) {
	payload, err := json.Marshal(map[string]any{
		"trigger_id": d.ID, "delivery_id": del.DeliveryID, "action": d.Action.Type,
	})
	if err != nil {
		return
	}
	project := d.Source.Project
	_ = st.AppendEvent(ctx, &store.Event{
		Type: store.EventTriggerFired, ProjectID: &project, TaskID: del.TaskID, Payload: payload,
	})
}
