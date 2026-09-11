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
}

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

// Judgement is what the pipeline decided about one event. A dry run returns
// it without having written anything.
type Judgement struct {
	EventID string `json:"event_id"`
	// Matched is the `match:` result; MatchMiss names the first key that
	// failed.
	Matched   bool   `json:"matched"`
	MatchMiss string `json:"match_miss,omitempty"`
	// If is the guard's verdict, nil when it was not reached or there is no
	// guard; IfRendered is what it rendered to.
	If         *bool  `json:"if,omitempty"`
	IfRendered string `json:"if_rendered,omitempty"`
	// DedupeKey is the rendered key; WouldDedupe says the ledger already
	// holds a `fired` row for it.
	DedupeKey   string `json:"dedupe_key,omitempty"`
	WouldDedupe bool   `json:"would_dedupe"`
	// Action is the rendered request body, nil when rendering was not
	// reached or failed.
	Action *CreateBody `json:"action,omitempty"`
	// Outcome is the ledger outcome this event gets (or, in a dry run,
	// would get short of the replay: a dry run that reaches the replay
	// reports "fired" meaning "would be replayed").
	Outcome string `json:"outcome"`
	// Error is a render failure's message.
	Error string `json:"error,omitempty"`
}

// judge runs steps 1–5 of the pipeline — match, if, dedupe, rate limit,
// render — and writes nothing. fire and Test both start here, which is what
// makes the dry run the real pipeline rather than a re-derivation of it.
func judge(ctx context.Context, st Store, d *Definition, ev Event, now time.Time) (*Judgement, error) {
	j := &Judgement{EventID: ev.ID()}
	data := renderData{Event: ev}

	j.Matched, j.MatchMiss = matchEvent(d.Match, ev)
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

	body, err := renderAction(d, data)
	if err != nil {
		j.Outcome, j.Error = store.DeliveryError, err.Error()
		return j, nil
	}
	j.Action = body
	j.Outcome = store.DeliveryFired
	return j, nil
}

func dedupeKey(d *Definition, ev Event, data renderData) (string, error) {
	if d.DedupeKey == "" {
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

// renderAction renders a create_task action into the body it replays.
func renderAction(d *Definition, data renderData) (*CreateBody, error) {
	a := d.Action
	render := func(field, text string) (string, error) {
		if text == "" {
			return "", nil
		}
		return workflow.RenderWith("action."+field, text, data)
	}
	body := &CreateBody{
		ProjectID:  d.Source.Project,
		Paused:     d.EffectiveOnFire() != OnFireCreate,
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
	return body, nil
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

// replayResult is what the in-process POST /v1/tasks answered.
type replayResult struct {
	status int
	body   []byte
}

// replay sends a create body into the daemon's handler in-process, the way
// internal/mcp replays a tool call (decision 1, decision record row 28), so
// the route's validation, bounds, the task 041 creation gate and
// Idempotency-Key apply by construction.
func replay(ctx context.Context, h http.Handler, key string, body *CreateBody) (*replayResult, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode create body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, createPath, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("build replay: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.ContentLength = int64(len(b))
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
	Detail     string `json:"detail,omitempty"`
}

// fire runs the whole pipeline for one event and records its ledger row.
func fire(ctx context.Context, st Store, h http.Handler, d *Definition, ev Event, now time.Time) (*Delivery, error) {
	j, err := judge(ctx, st, d, ev, now)
	if err != nil {
		return nil, err
	}
	del := &Delivery{Judgement: *j, Detail: j.Error}
	if j.Outcome == store.DeliveryFired {
		res, rerr := replay(ctx, h, IdempotencyKey(d.ID, j.DedupeKey), j.Action)
		switch {
		case rerr != nil:
			del.Outcome, del.Detail = store.DeliveryError, rerr.Error()
		case res.status >= 200 && res.status < 300:
			var created struct {
				ID int64 `json:"id"`
			}
			if uerr := json.Unmarshal(res.body, &created); uerr != nil || created.ID == 0 {
				del.Outcome, del.Detail = store.DeliveryError, "create answered "+strconv.Itoa(res.status)+" without a task id"
			} else {
				del.TaskID = &created.ID
			}
		case res.status >= 400 && res.status < 500:
			del.Outcome, del.Detail = store.DeliveryRefused, string(res.body)
		default:
			del.Outcome, del.Detail = store.DeliveryError, strconv.Itoa(res.status)+": "+string(res.body)
		}
	}
	row, err := st.RecordTriggerDelivery(ctx, &store.TriggerDelivery{
		TriggerID: d.ID, EventID: del.EventID, DedupeKey: del.DedupeKey,
		Outcome: del.Outcome, TaskID: del.TaskID, Detail: del.Detail, CreatedAt: now,
	})
	if err != nil {
		return nil, err
	}
	del.DeliveryID = row.ID
	return del, nil
}
