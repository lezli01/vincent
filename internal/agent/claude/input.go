package claude

// Mid-run input protocol (spec §7.4, §9.2), pinned against claude 2.1.226 —
// the fixtures in testdata/ are captured from real runs. Input mode starts
// the CLI with `--input-format stream-json --permission-prompt-tool stdio`
// (the latter undocumented: it enables the AskUserQuestion tool in -p mode
// and routes permission prompts onto the stream as control_request lines),
// delivers the prompt as one {"type":"user",…} JSONL line, and keeps stdin
// open for control_response writes.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// askUserQuestionTool is the tool whose can_use_tool requests normalize to
// question-kind input requests; every other tool is permission-kind.
const askUserQuestionTool = "AskUserQuestion"

// supportsInput reports whether a detected CLI version is in the
// fixture-verified family [2.1.0, 3.0.0) (PR F decision). Enabling the
// protocol changes invocation for the whole run (prompt-as-JSONL), so a
// major bump degrades visibly to the non-input invocation instead of
// risking every claude step; the gate moves only with re-captured fixtures.
func supportsInput(version string) bool {
	parts := strings.SplitN(versionRe.FindString(version), ".", 3)
	if len(parts) != 3 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return major == 2 && minor >= 1
}

// userMessageLine wraps the prompt as the stream-json user message input
// mode requires in place of raw stdin text.
func userMessageLine(prompt string) ([]byte, error) {
	line, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": prompt}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode prompt message: %w", err)
	}
	return append(line, '\n'), nil
}

// controlLine is the inbound shape of control_request / control_cancel_request.
type controlLine struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Request   *controlPayload `json:"request"`
}

type controlPayload struct {
	Subtype     string          `json:"subtype"`
	ToolName    string          `json:"tool_name"`
	Input       json.RawMessage `json:"input"`
	Description string          `json:"description"`
}

// askInput is the AskUserQuestion tool input inside a can_use_tool request.
type askInput struct {
	Questions []askQuestion `json:"questions"`
}

type askQuestion struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	Options     []askOptionShape `json:"options"`
	MultiSelect bool             `json:"multiSelect"`
}

type askOptionShape struct {
	Label string `json:"label"`
}

// pendingRequest is the adapter-side state needed to translate the answer
// back onto the wire. Requests are serial (spec §7.4): at most one exists.
type pendingRequest struct {
	id       string
	kind     string // "question" | "permission"
	toolName string
	input    json.RawMessage
}

// parseControlRequest normalizes one control_request line. A nil
// pendingRequest with an EventInputRequest event whose Request is nil marks
// a protocol violation the engine must fail, never wait on (spec §18).
func parseControlRequest(raw []byte) (agent.Event, *pendingRequest) {
	protoErr := func(msg string) (agent.Event, *pendingRequest) {
		return agent.Event{Type: agent.EventInputRequest, Message: msg, Raw: raw}, nil
	}
	var line controlLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return protoErr(fmt.Sprintf("unparseable control_request: %v", err))
	}
	if line.RequestID == "" || line.Request == nil {
		return protoErr("control_request without request_id or request")
	}
	if line.Request.Subtype != "can_use_tool" {
		return protoErr(fmt.Sprintf("unknown control_request subtype %q", line.Request.Subtype))
	}
	pend := &pendingRequest{
		id:       line.RequestID,
		toolName: line.Request.ToolName,
		input:    line.Request.Input,
	}
	req := &agent.InputRequest{Raw: append([]byte(nil), raw...)}
	if line.Request.ToolName == askUserQuestionTool {
		var ask askInput
		if err := json.Unmarshal(line.Request.Input, &ask); err != nil || len(ask.Questions) == 0 {
			return protoErr("AskUserQuestion request without parseable questions")
		}
		req.Kind = "question"
		pend.kind = req.Kind
		for _, q := range ask.Questions {
			question := agent.Question{
				Text:        q.Question,
				Header:      q.Header,
				MultiSelect: q.MultiSelect,
			}
			for _, o := range q.Options {
				question.Options = append(question.Options, o.Label)
			}
			req.Questions = append(req.Questions, question)
		}
	} else {
		req.Kind = "permission"
		pend.kind = req.Kind
		summary := line.Request.Description
		if summary == "" {
			summary = line.Request.ToolName
		}
		req.Permission = &agent.PermissionReq{Tool: line.Request.ToolName, Summary: summary}
	}
	return agent.Event{Type: agent.EventInputRequest, Request: req, Raw: raw}, pend
}

// buildControlResponse translates an InputResponse into the control_response
// line the CLI accepts (verified live against 2.1.226):
//
//   - question answers ride updatedInput.answers keyed by question text
//     (arrays for multi-select); a free-text Response rides
//     updatedInput.response (the §7.4 deny-mode canned answer);
//   - permission allow re-sends the original input as updatedInput;
//     permission deny is {"behavior":"deny","message":…}.
func buildControlResponse(pend *pendingRequest, resp agent.InputResponse) ([]byte, error) {
	var inner map[string]any
	switch {
	case pend.kind == "permission" && (resp.Allow == nil || !*resp.Allow):
		msg := resp.Response
		if msg == "" {
			msg = "permission denied"
		}
		inner = map[string]any{"behavior": "deny", "message": msg}
	default:
		updated := map[string]any{}
		if len(pend.input) > 0 {
			if err := json.Unmarshal(pend.input, &updated); err != nil {
				return nil, fmt.Errorf("re-encode request input: %w", err)
			}
		}
		if pend.kind == "question" {
			if resp.Response != "" {
				updated["response"] = resp.Response
			} else {
				answers := map[string]any{}
				for text, vals := range resp.Answers {
					if len(vals) == 1 {
						answers[text] = vals[0]
					} else {
						answers[text] = vals
					}
				}
				updated["answers"] = answers
			}
		}
		inner = map[string]any{"behavior": "allow", "updatedInput": updated}
	}
	line, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": pend.id,
			"response":   inner,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode control response: %w", err)
	}
	return append(line, '\n'), nil
}
