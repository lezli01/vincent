package taskrun

// Interactive input requests (spec §7.4, T2.12): the engine-side half.
// The adapter surfaces EventInputRequest; this file owns the normalized
// pending-input shape persisted on the task, the strict /answer validation,
// and the delivery of a human answer into the parked actor.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// Canned §7.4 deny-mode responses.
const (
	cannedQuestionResponse   = "no user is available; decide with your best judgment"
	cannedPermissionResponse = "no user is available; permission denied"
)

// Run-context cancel causes: they tell classifyAgent why the process died.
var (
	errStepTimeout     = errors.New("step timeout")
	errInputTimeout    = errors.New("input timeout")
	errInputProtocol   = errors.New("input protocol error")
	errTranscriptLimit = errors.New("transcript limit")
	errTranscriptIO    = errors.New("transcript i/o error")
)

// PendingInput is the normalized input request as persisted in
// tasks.pending_input_json and served to clients on the task (§13.2).
type PendingInput struct {
	Kind       string             `json:"kind"` // "question" | "permission"
	Questions  []PendingQuestion  `json:"questions,omitempty"`
	Permission *PendingPermission `json:"permission,omitempty"`
	// Raw is the adapter-native request line, passed through untranslated.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// PendingQuestion is one structured question awaiting an answer.
type PendingQuestion struct {
	Text        string   `json:"text"`
	Header      string   `json:"header,omitempty"`
	Options     []string `json:"options,omitempty"` // suggestions; free text is always accepted
	MultiSelect bool     `json:"multi_select,omitempty"`
}

// PendingPermission describes a permission-kind request.
type PendingPermission struct {
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
}

// normalizePending maps the adapter's request onto the persisted shape.
func normalizePending(req *agent.InputRequest) PendingInput {
	p := PendingInput{Kind: req.Kind, Raw: req.Raw}
	for _, q := range req.Questions {
		p.Questions = append(p.Questions, PendingQuestion{
			Text: q.Text, Header: q.Header, Options: q.Options, MultiSelect: q.MultiSelect,
		})
	}
	if req.Permission != nil {
		p.Permission = &PendingPermission{Tool: req.Permission.Tool, Summary: req.Permission.Summary}
	}
	return p
}

// summarize renders the one-line summary the state-change event carries
// (§13.3): enough for an alert, not the full request.
func summarize(req *agent.InputRequest) string {
	if req.Permission != nil {
		return "permission: " + req.Permission.Tool + " — " + req.Permission.Summary
	}
	if len(req.Questions) > 0 {
		s := req.Questions[0].Text
		if len(req.Questions) > 1 {
			s = fmt.Sprintf("%s (+%d more)", s, len(req.Questions)-1)
		}
		return truncate(s, 200)
	}
	return req.Kind
}

// AnswerInput is the decoded body of POST /v1/tasks/{id}/answer (§13.2).
type AnswerInput struct {
	Answers map[string][]string
	Allow   *bool
}

// AnswerValidationError reports an answer that does not fit the pending
// request; the API answers 400 (§13.2, PR F decision: strict validation —
// a mismatched answer is untranslatable and would cost the live session).
type AnswerValidationError struct{ Msg string }

func (e *AnswerValidationError) Error() string { return e.Msg }

// AsAnswerValidation extracts an *AnswerValidationError from err.
func AsAnswerValidation(err error) (*AnswerValidationError, bool) {
	var e *AnswerValidationError
	ok := errors.As(err, &e)
	return e, ok
}

// validateAnswer checks in against the pending request and builds the
// adapter response. Values are free text (options are suggestions, §7.4);
// only the structure is validated.
func validateAnswer(pending PendingInput, in AnswerInput) (agent.InputResponse, error) {
	fail := func(format string, args ...any) (agent.InputResponse, error) {
		return agent.InputResponse{}, &AnswerValidationError{Msg: fmt.Sprintf(format, args...)}
	}
	switch pending.Kind {
	case "question":
		if in.Allow != nil {
			return fail("allow is only valid for a permission request")
		}
		known := map[string]bool{}
		for _, q := range pending.Questions {
			known[q.Text] = true
			vals := in.Answers[q.Text]
			switch {
			case len(vals) == 0:
				return fail("question %q has no answer", q.Text)
			case !q.MultiSelect && len(vals) != 1:
				return fail("question %q takes exactly one answer, got %d", q.Text, len(vals))
			}
			for _, v := range vals {
				if v == "" {
					return fail("question %q has an empty answer", q.Text)
				}
			}
		}
		for text := range in.Answers {
			if !known[text] {
				return fail("answer %q matches no pending question", text)
			}
		}
		return agent.InputResponse{Answers: in.Answers}, nil
	case "permission":
		if len(in.Answers) > 0 {
			return fail("answers are only valid for a question request")
		}
		if in.Allow == nil {
			return fail("allow is required for a permission request")
		}
		return agent.InputResponse{Allow: in.Allow}, nil
	default:
		return fail("pending request has unknown kind %q", pending.Kind)
	}
}

// Answer delivers a human answer to a task's pending input request (§6,
// §7.4): validate against the pending request, CAS awaiting_input → running
// (which clears pending_input and, per the PR C rule, a pending pause), then
// hand the response to the parked actor — the only goroutine that touches
// the live RunHandle.
func (r *Runner) Answer(ctx context.Context, id int64, in AnswerInput) (*store.Task, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if !taskstate.Can(task.State, taskstate.Answer) {
		return nil, &InvalidActionError{TaskID: id, Action: taskstate.Answer, State: task.State}
	}
	if task.PendingInputJSON == "" {
		return nil, fmt.Errorf("task %d is awaiting input but no pending request is recorded", id)
	}
	var pending PendingInput
	if err := json.Unmarshal([]byte(task.PendingInputJSON), &pending); err != nil {
		return nil, fmt.Errorf("pending_input_json: %w", err)
	}
	resp, err := validateAnswer(pending, in)
	if err != nil {
		return nil, err
	}
	updated, err := r.transitionFrom(ctx, task, taskstate.Answer, store.TaskChange{})
	if err != nil {
		return nil, err
	}
	if lr, ok := r.lookupRun(id); ok {
		// The CAS cleared the persisted pause flag; keep the live mirror
		// honest so the actor does not park on a request the answer revoked.
		lr.mu.Lock()
		lr.pauseRequested = false
		lr.mu.Unlock()
		select {
		case lr.answers <- resp:
		default:
			// Cannot happen while requests are serial (one pending answer at
			// a time, guarded by the CAS); log rather than block a handler.
			r.deps.Logger.Error("answer channel full; response dropped", "task", id)
		}
	} else {
		// awaiting_input implies a live actor; its absence means a crash won
		// the race. Recovery will re-queue the task and the fresh run re-asks.
		r.deps.Logger.Error("answered task has no live actor", "task", id)
	}
	return updated, nil
}

// autoDeny answers an input request under on_input: deny (§7.4): questions
// get the canned best-judgment response, permissions are denied. The task
// never leaves running; request and answer are transcripted.
func (r *Runner) autoDeny(env *stepEnv, handle agent.RunHandle, req *agent.InputRequest, tr *transcript) {
	resp := agent.InputResponse{Response: cannedQuestionResponse}
	if req.Kind == "permission" {
		deny := false
		resp = agent.InputResponse{Allow: &deny, Response: cannedPermissionResponse}
	}
	tr.Note("input_request", map[string]any{
		"kind": req.Kind, "summary": summarize(req), "policy": string(agent.InputDeny),
	})
	if err := handle.Respond(resp); err != nil {
		env.log.Warn("auto-deny input request", "error", err)
		tr.Note("input_response_failed", map[string]any{"error": err.Error()})
		return
	}
	tr.Note("input_response", map[string]any{
		"source": "auto_deny", "allow": resp.Allow, "response": resp.Response,
	})
	env.log.Info("input request auto-answered", "kind", req.Kind, "policy", "deny")
}

// stepClock is the §7.4 step-timeout clock. It is actor-managed rather than
// a context deadline because the clock must pause while the task waits in
// awaiting_input — it measures agent work, not human latency. Firing
// cancels the run context with errStepTimeout. pause/resume are called only
// by the actor goroutine.
type stepClock struct {
	timer     *time.Timer
	remaining time.Duration
	started   time.Time
	now       func() time.Time
	running   bool
}

func newStepClock(d time.Duration, now func() time.Time, fire func()) *stepClock {
	return &stepClock{
		timer: time.AfterFunc(d, fire), remaining: d, started: now(), now: now, running: true,
	}
}

func (c *stepClock) pause() {
	if !c.running {
		return
	}
	c.timer.Stop()
	c.remaining -= c.now().Sub(c.started)
	c.running = false
}

func (c *stepClock) resume() {
	if c.running {
		return
	}
	if c.remaining < 0 {
		c.remaining = 0
	}
	c.started = c.now()
	c.timer.Reset(c.remaining)
	c.running = true
}

func (c *stepClock) stop() { c.timer.Stop() }

// encodePending marshals the normalized request for the task column.
func encodePending(req *agent.InputRequest) (string, error) {
	b, err := json.Marshal(normalizePending(req))
	if err != nil {
		return "", fmt.Errorf("marshal pending input: %w", err)
	}
	return string(b), nil
}

// causeIs reports whether the run context was canceled with the given cause.
func causeIs(runCtx context.Context, target error) bool {
	return errors.Is(context.Cause(runCtx), target)
}
