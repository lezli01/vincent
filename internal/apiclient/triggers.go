package apiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// The client half of the trigger routes (§13.2, task 096). Like workflows,
// the wire carries edit operations, never YAML: the daemon renders a create's
// starter and applies a form's ops to the file's own bytes, so comments
// survive and a second writer is caught by the version token.

// Delivery outcomes (task 096 *Observability*, decision 31B).
const (
	TriggerFired       = "fired"
	TriggerSeeded      = "seeded"
	TriggerDeduped     = "deduped"
	TriggerFiltered    = "filtered"
	TriggerRateLimited = "rate_limited"
	// TriggerSuperseded is an event `overrun:` dropped, and TriggerQueued one
	// it is holding until the group empties (task 122).
	TriggerSuperseded = "superseded"
	TriggerQueued     = "queued"
	TriggerRefused    = "refused"
	TriggerError      = "error"
)

// Trigger events on §13.3's stream.
const (
	EventTriggerFired       = "trigger.fired"
	EventTriggerPollChanged = "trigger.poll_changed"
)

// TriggerPoll is one trigger's poll health.
type TriggerPoll struct {
	// Seeded says the next poll judges rather than seeds.
	Seeded     bool    `json:"seeded"`
	LastPollAt *string `json:"last_poll_at"`
	OK         bool    `json:"ok"`
	Error      string  `json:"error,omitempty"`
	LastFireAt *string `json:"last_fire_at"`
}

// TriggerSummary is one row of the trigger list.
type TriggerSummary struct {
	ID             string            `json:"id"`
	File           string            `json:"file"`
	Version        string            `json:"version"`
	Valid          bool              `json:"valid"`
	Errors         []WorkflowFinding `json:"errors"`
	Enabled        bool              `json:"enabled"`
	Armed          bool              `json:"armed"`
	DisarmedReason string            `json:"disarmed_reason,omitempty"`
	SourceType     string            `json:"source_type,omitempty"`
	ActionType     string            `json:"action_type,omitempty"`
	ProjectID      int64             `json:"project_id,omitempty"`
	OnFire         string            `json:"on_fire,omitempty"`
	Permission     string            `json:"permission,omitempty"`
	Poll           TriggerPoll       `json:"poll"`
}

// TriggerList is GET /v1/triggers. Enabled is `triggers.enabled`.
type TriggerList struct {
	Enabled  bool             `json:"enabled"`
	Dir      string           `json:"dir"`
	Triggers []TriggerSummary `json:"triggers"`
}

// TriggerDetail is GET /v1/triggers/{id}. Definition is the parsed document
// as generic JSON, nil when the file does not validate: a form reads values
// out of it by the served schema's field names.
type TriggerDetail struct {
	TriggerSummary
	Source     string         `json:"source"`
	Definition map[string]any `json:"definition"`
}

// TriggerSchemaField is one editable row of the served trigger schema.
type TriggerSchemaField struct {
	Name      string                  `json:"name"`
	Control   string                  `json:"control"`
	Values    []string                `json:"values,omitempty"`
	Required  bool                    `json:"required,omitempty"`
	Default   string                  `json:"default,omitempty"`
	Help      string                  `json:"help,omitempty"`
	Dangerous []TriggerDangerousValue `json:"dangerous,omitempty"`
}

// TriggerDangerousValue is a value a client confirms before committing
// (decision 19).
type TriggerDangerousValue struct {
	Value   string `json:"value"`
	Warning string `json:"warning"`
}

// TriggerSchemaVariant is one source or action type.
type TriggerSchemaVariant struct {
	Type    string               `json:"type"`
	Fields  []TriggerSchemaField `json:"fields"`
	Help    string               `json:"help,omitempty"`
	Events  []string             `json:"events,omitempty"`
	Trusted []string             `json:"trusted,omitempty"`
}

// TriggerSchema is GET /v1/triggers/schema.
type TriggerSchema struct {
	TopLevel  []TriggerSchemaField   `json:"top_level"`
	Sources   []TriggerSchemaVariant `json:"sources"`
	Actions   []TriggerSchemaVariant `json:"actions"`
	Limits    []TriggerSchemaField   `json:"limits"`
	Signature []TriggerSchemaField   `json:"signature"`
}

// Trigger schema controls beyond the workflow vocabulary.
const (
	TriggerControlSource    = "source"
	TriggerControlAction    = "action"
	TriggerControlLimits    = "limits"
	TriggerControlSignature = "signature"
	TriggerControlMatch     = "match"
	TriggerControlProject   = "project"
	TriggerControlNumber    = "number"
)

// CreateTriggerRequest is POST /v1/triggers: the starter's inputs.
type CreateTriggerRequest struct {
	ID           string   `json:"id"`
	ProjectID    int64    `json:"project_id"`
	PollInterval string   `json:"poll_interval,omitempty"`
	Command      []string `json:"command,omitempty"`
	Workflow     string   `json:"workflow,omitempty"`
	Title        string   `json:"title,omitempty"`
}

// TriggerWriteResult answers every write.
type TriggerWriteResult struct {
	ID      string            `json:"id"`
	File    string            `json:"file"`
	Version string            `json:"version"`
	Errors  []WorkflowFinding `json:"errors"`
}

// TriggerValidation is POST /v1/triggers/validate.
type TriggerValidation struct {
	Valid  bool              `json:"valid"`
	Errors []WorkflowFinding `json:"errors"`
}

// TriggerReplay is a rendered action.
type TriggerReplay struct {
	Type   string          `json:"type"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Branch string          `json:"branch,omitempty"`
	TaskID *int64          `json:"task_id,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// TriggerJudgement is what the pipeline decided about one event.
type TriggerJudgement struct {
	EventID     string `json:"event_id"`
	Matched     bool   `json:"matched"`
	MatchMiss   string `json:"match_miss,omitempty"`
	If          *bool  `json:"if,omitempty"`
	IfRendered  string `json:"if_rendered,omitempty"`
	DedupeKey   string `json:"dedupe_key,omitempty"`
	WouldDedupe bool   `json:"would_dedupe"`
	// Overrun is the `overrun:` mode consulted, "" for the parallel default;
	// ConcurrencyKey the rendered group, InFlight its unsettled tasks, and
	// the three Would* flags the decision (task 122).
	Overrun        string         `json:"overrun,omitempty"`
	ConcurrencyKey string         `json:"concurrency_key,omitempty"`
	InFlight       []int64        `json:"in_flight,omitempty"`
	WouldSkip      bool           `json:"would_skip,omitempty"`
	WouldCancel    bool           `json:"would_cancel,omitempty"`
	WouldQueue     bool           `json:"would_queue,omitempty"`
	Action         *TriggerReplay `json:"action,omitempty"`
	Outcome        string         `json:"outcome"`
	Error          string         `json:"error,omitempty"`
}

// TriggerDryPoll is POST /v1/triggers/{id}/poll.
type TriggerDryPoll struct {
	Seed      bool               `json:"seed"`
	Events    []TriggerJudgement `json:"events"`
	Truncated int                `json:"truncated"`
	Refused   int                `json:"refused"`
	Cursor    *string            `json:"cursor,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// TriggerDelivery is one ledger row.
type TriggerDelivery struct {
	ID               int64  `json:"id"`
	TriggerID        string `json:"trigger_id"`
	EventID          string `json:"event_id"`
	DedupeKey        string `json:"dedupe_key"`
	ConcurrencyKey   string `json:"concurrency_key,omitempty"`
	Outcome          string `json:"outcome"`
	TaskID           *int64 `json:"task_id"`
	SupersededTaskID *int64 `json:"superseded_task_id,omitempty"`
	Detail           string `json:"detail,omitempty"`
	CreatedAt        string `json:"created_at"`
}

// Triggers lists the trigger files and whether triggers are on globally.
func (c *Client) Triggers(ctx context.Context) (TriggerList, error) {
	var out TriggerList
	err := c.get(ctx, "/v1/triggers", &out)
	return out, err
}

// Trigger fetches one trigger with its source and parsed definition.
func (c *Client) Trigger(ctx context.Context, id string) (TriggerDetail, error) {
	var out TriggerDetail
	err := c.get(ctx, "/v1/triggers/"+url.PathEscape(id), &out)
	return out, err
}

// TriggerSchema fetches the served descriptor.
func (c *Client) TriggerSchema(ctx context.Context) (TriggerSchema, error) {
	var out TriggerSchema
	err := c.get(ctx, "/v1/triggers/schema", &out)
	return out, err
}

// ValidateTrigger validates a trigger document; id, when set, is the file
// stem its id must match.
func (c *Client) ValidateTrigger(ctx context.Context, source, id string) (TriggerValidation, error) {
	var out TriggerValidation
	err := c.send(ctx, http.MethodPost, "/v1/triggers/validate",
		map[string]string{"source": source, "id": id}, &out)
	return out, err
}

// CreateTrigger writes a new, disabled trigger from the starter.
func (c *Client) CreateTrigger(ctx context.Context, req CreateTriggerRequest) (TriggerWriteResult, error) {
	var out TriggerWriteResult
	err := c.send(ctx, http.MethodPost, "/v1/triggers", req, &out)
	return out, err
}

// PatchTrigger applies edit operations to a trigger file whose version still
// matches. A stale version is a 409 whose details carry the current one.
func (c *Client) PatchTrigger(ctx context.Context, id, version string, ops []WorkflowOp) (TriggerWriteResult, error) {
	var out TriggerWriteResult
	err := c.send(ctx, http.MethodPatch, "/v1/triggers/"+url.PathEscape(id),
		map[string]any{"version": version, "ops": ops}, &out)
	return out, err
}

// DeleteTrigger removes a trigger file whose version still matches. Its
// ledger is kept.
func (c *Client) DeleteTrigger(ctx context.Context, id, version string) error {
	return c.send(ctx, http.MethodDelete,
		"/v1/triggers/"+url.PathEscape(id)+"?version="+url.QueryEscape(version), nil, nil)
}

// TestTrigger judges a sample event and writes nothing.
func (c *Client) TestTrigger(ctx context.Context, id string, event map[string]any) (TriggerJudgement, error) {
	var out TriggerJudgement
	err := c.send(ctx, http.MethodPost, "/v1/triggers/"+url.PathEscape(id)+"/test",
		map[string]any{"event": event}, &out)
	return out, err
}

// PollTrigger runs the source once and judges it, recording nothing.
func (c *Client) PollTrigger(ctx context.Context, id string) (TriggerDryPoll, error) {
	var out TriggerDryPoll
	err := c.send(ctx, http.MethodPost, "/v1/triggers/"+url.PathEscape(id)+"/poll", nil, &out)
	return out, err
}

// TriggerDeliveries reads a trigger's ledger, newest first.
func (c *Client) TriggerDeliveries(ctx context.Context, id string, limit int) ([]TriggerDelivery, error) {
	path := "/v1/triggers/" + url.PathEscape(id) + "/deliveries"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out struct {
		Deliveries []TriggerDelivery `json:"deliveries"`
	}
	err := c.get(ctx, path, &out)
	return out.Deliveries, err
}
