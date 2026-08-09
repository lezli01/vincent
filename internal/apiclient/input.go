package apiclient

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Input request kinds (§7.4).
const (
	InputKindQuestion   = "question"
	InputKindPermission = "permission"
)

// InputRequest is the pending §7.4 request a task is parked on, decoded from
// the task's pending_input. The client owns this struct for the same reason
// it owns Task: the server's DTO stays unexported and the two are
// integration-tested together.
type InputRequest struct {
	Kind       string           `json:"kind"`
	Questions  []InputQuestion  `json:"questions,omitempty"`
	Permission *InputPermission `json:"permission,omitempty"`
}

// InputQuestion is one structured question. Options are suggestions, never an
// enum — §7.4 accepts free text for any question.
type InputQuestion struct {
	Text        string   `json:"text"`
	Header      string   `json:"header,omitempty"`
	Options     []string `json:"options,omitempty"`
	MultiSelect bool     `json:"multi_select,omitempty"`
}

// InputPermission describes a permission-kind request.
type InputPermission struct {
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
}

// PendingRequest decodes the task's pending input request. ok is false when
// the task is not waiting on one, which is every state but awaiting_input.
func (t TaskDetail) PendingRequest() (req InputRequest, ok bool, err error) {
	if len(t.PendingInput) == 0 {
		return InputRequest{}, false, nil
	}
	if err := json.Unmarshal(t.PendingInput, &req); err != nil {
		return InputRequest{}, false, fmt.Errorf("decode pending_input: %w", err)
	}
	return req, req.Kind != "", nil
}

// InputResponse answers an InputRequest. Answers is keyed by question text;
// Allow decides a permission request.
type InputResponse struct {
	Answers map[string][]string
	Allow   *bool
}

// body renders the §13.2 answer payload. Values always ride as arrays: the
// server accepts a bare string too, and one shape on the wire is one shape to
// test.
func (r InputResponse) body() any {
	out := struct {
		Answers map[string][]string `json:"answers,omitempty"`
		Allow   *bool               `json:"allow,omitempty"`
	}{Answers: r.Answers, Allow: r.Allow}
	return out
}

// Validate checks an answer against the request it answers, using §7.4's
// rules: every question answered, exactly one value for a single-select, at
// least one for a multi-select, and allow (never answers) for a permission.
//
// The daemon enforces this too and stays the authority — this exists so a
// form can disable its own submit with a reason instead of spending a round
// trip to be told the obvious.
func (r InputRequest) Validate(resp InputResponse) error {
	if r.Kind == InputKindPermission {
		if resp.Allow == nil {
			return errors.New("allow or deny the request")
		}
		if len(resp.Answers) > 0 {
			return errors.New("a permission request takes no answers")
		}
		return nil
	}
	if resp.Allow != nil {
		return errors.New("a question takes answers, not allow")
	}
	for _, q := range r.Questions {
		values := resp.Answers[q.Text]
		if len(values) == 0 {
			return fmt.Errorf("answer %q", short(q.Text))
		}
		if !q.MultiSelect && len(values) != 1 {
			return fmt.Errorf("%q takes exactly one answer", short(q.Text))
		}
		for _, v := range values {
			if v == "" {
				return fmt.Errorf("%q has an empty answer", short(q.Text))
			}
		}
	}
	for text := range resp.Answers {
		if !r.asks(text) {
			return fmt.Errorf("%q is not a pending question", short(text))
		}
	}
	return nil
}

// asks reports whether the request contains a question with this text.
func (r InputRequest) asks(text string) bool {
	for _, q := range r.Questions {
		if q.Text == text {
			return true
		}
	}
	return false
}

// short trims a question down to something that fits on one status line.
func short(s string) string {
	const limit = 40
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit-1]) + "…"
}
