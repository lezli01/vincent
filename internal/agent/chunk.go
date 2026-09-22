package agent

// §13.3's live-output chunk shapes, derived from a normalized stream event.
//
// Two runners publish live output — internal/taskrun for a step and
// internal/chatrun for a chat turn — and a client renders both through one
// path, so the mapping lives here rather than in either of them. Putting it in
// the other runner would mean a `chatrun → taskrun` edge, which the one-way
// dependency direction does not allow; duplicating the switch would mean two
// definitions of a wire format whose whole purpose is that a live line and the
// same line refetched from the transcript render identically (task 071).
//
// The payload keys match what internal/api's normalizeLine writes for the same
// event, because that is the other half of the same seam.

// Chunk is one §13.3 live-output chunk: its type and the payload a client
// renders it from. A runner stamps its own identifying fields (a step's
// run_id, a chat's chat_id and turn_id) onto the payload before publishing.
type Chunk struct {
	Type    string
	Payload map[string]any
}

// LiveChunks maps a normalized stream event onto the §13.3 chunks it
// publishes. It returns none for the events that carry nothing a client
// renders as a line: input requests surface as a state change, results,
// errors and unmodeled lines surface as the run's own outcome or as
// `agent.raw` from the transcript route, and an input echo repeats a prompt
// that is already on screen (task 124.2).
//
// One event can become more than one chunk. A codex tool result arrives with
// the body the command printed on the same line, and a claude edit's with its
// hunks (task 110); the transcript route splits each into two records, and
// the live tail publishes the same two, in the same order, because a client
// renders both through one path (task 071 decision 2, task 066 decision 5).
//
// ParentCallID rides on every chunk it is set on, attached here rather than in
// each arm for the reason normalizedEvent gives (task 066).
func LiveChunks(ev Event) []Chunk {
	chunks := liveChunkBody(ev)
	if ev.ParentCallID != "" {
		for _, c := range chunks {
			c.Payload["parent_call_id"] = ev.ParentCallID
		}
	}
	return chunks
}

func liveChunkBody(ev Event) []Chunk {
	one := func(kind string, payload map[string]any) []Chunk {
		return []Chunk{{Type: kind, Payload: payload}}
	}
	switch ev.Type {
	case EventOutput:
		if ev.Text == "" {
			return nil
		}
		return one("agent.output", map[string]any{"text": ev.Text})
	case EventRunHeader:
		// The run header goes live like the rest (task 066): it arrives before
		// the run's first word, so a reader who opens the pane on a running
		// step sees the run's frame early, and does not have to wait for the
		// step to finish to learn what the agent could reach.
		if ev.Header == nil {
			return nil
		}
		return one("agent.run_header", headerChunk(ev.Header))
	case EventToolUse:
		return one("agent.tool_use", map[string]any{"tools": toolChunks(ev.Tools)})
	case EventToolResult:
		chunks := one("agent.tool_result", map[string]any{"results": resultChunks(ev.Results)})
		if ev.Output != nil {
			chunks = append(chunks, Chunk{
				Type: "agent.command_output", Payload: outputChunk(ev.Output),
			})
		}
		// An edit's hunks follow its outcome for the same reason and in the
		// same order the transcript route writes them (task 110).
		if ev.Patch != nil {
			chunks = append(chunks, Chunk{
				Type: "agent.patch", Payload: patchChunk(ev.Patch),
			})
		}
		return chunks
	case EventPlan:
		if ev.Plan == nil {
			return nil
		}
		return one("agent.plan", planChunk(ev.Plan))
	case EventCommandOutput:
		if ev.Output == nil {
			return nil
		}
		return one("agent.command_output", outputChunk(ev.Output))
	case EventThinking:
		// Thinking goes live like everything else (T4.16). §9.7 held it back
		// when it meant one chunk per token; coalescing removed that, and a
		// record that appears only after a step finishes reads as output that
		// went missing while the step was running.
		if ev.Text == "" {
			return nil
		}
		return one("agent.thinking", map[string]any{"text": ev.Text})
	case EventSubagentStarted, EventSubagentProgress, EventSubagentFinished:
		// All three go live (task 109): the pane's rail labels come from the
		// start, and a progress chunk is what stops a live tail from counting
		// those lines as unrecognized.
		if ev.Subagent == nil {
			return nil
		}
		return one("agent."+string(ev.Type), subagentChunk(ev.Subagent))
	case EventSkill:
		// A skill load goes live (task 124.2): it is what replaces the raw
		// `SKILL.md` line a live tail used to show.
		if ev.Skill == nil {
			return nil
		}
		return one("agent.skill", skillChunk(ev.Skill))
	case EventUsage:
		// Usage payloads are adapter-native; the raw line is the honest shape.
		return one("agent.usage", map[string]any{"raw": string(ev.Raw)})
	case EventInputRequest, EventInputCanceled,
		EventResult, EventError, EventUnknown:
		return nil
	case EventInputEcho:
		// The echoed prompt is already on screen — a chat's human message, a
		// step's rendered prompt — so its record goes out with no chunk
		// (task 124 decision 25). It is modeled, so it is not agent.raw either.
		return nil
	case EventConversationReset:
		// A reset goes live, where the echo above does not (task 124.20): the
		// prompt was already drawn, but the agent throwing its memory away is
		// news, and a live tail that showed it only after the turn finished
		// would leave a reader believing the history on screen is the history
		// the agent has. The payload is empty because the record is its type.
		return one("agent.conversation_reset", map[string]any{})
	}
	return nil
}

// skillChunk maps a skill load onto the §13.3 live-chunk shape, matching what
// api.normalizeLine writes for the same event. Every key is omitted when
// unreported, which is what "" means on a SkillInvocation (task 124 decision
// 17).
func skillChunk(s *SkillInvocation) map[string]any {
	chunk := map[string]any{}
	for k, v := range map[string]string{
		"name": s.Name, "args": s.Args, "by": s.By, "call_id": s.CallID, "error": s.Error,
	} {
		if v != "" {
			chunk[k] = v
		}
	}
	if s.Forked {
		chunk["forked"] = true
	}
	return chunk
}

// headerChunk maps a run header onto the §13.3 live-chunk shape, matching
// what api.normalizeLine writes for the same event. The tool list cannot ride
// on `tools`: that key is agent.tool_use's objects.
func headerChunk(h *RunHeader) map[string]any {
	chunk := map[string]any{}
	if h.WorkDir != "" {
		chunk["work_dir"] = h.WorkDir
	}
	if len(h.Tools) > 0 {
		chunk["available_tools"] = h.Tools
	}
	return chunk
}

// subagentChunk maps a subagent's start, progress or finish onto the §13.3
// live-chunk shape, matching what api.normalizeLine writes for the same event.
// Every key is omitted when unreported, so each event type carries only what
// it has.
func subagentChunk(s *Subagent) map[string]any {
	chunk := map[string]any{}
	for k, v := range map[string]string{
		"call_id": s.CallID, "description": s.Description, "subagent_type": s.AgentType,
		"status": s.Status, "summary": s.Summary, "last_tool": s.LastTool,
	} {
		if v != "" {
			chunk[k] = v
		}
	}
	if s.Background {
		chunk["background"] = true
	}
	if s.ToolUses != 0 {
		chunk["tool_uses"] = s.ToolUses
	}
	if s.TotalTokens != 0 {
		chunk["total_tokens"] = s.TotalTokens
	}
	if ms := s.Duration.Milliseconds(); ms != 0 {
		chunk["duration_ms"] = ms
	}
	return chunk
}

// planChunk maps the agent's running plan onto the §13.3 live-chunk shape,
// matching what api.normalizeLine writes for the same event.
func planChunk(p *Plan) map[string]any {
	items := make([]map[string]any, 0, len(p.Items))
	for _, it := range p.Items {
		item := map[string]any{"text": it.Text}
		if it.Completed {
			item["completed"] = true
		}
		items = append(items, item)
	}
	chunk := map[string]any{"items": items}
	if p.CallID != "" {
		chunk["plan_call_id"] = p.CallID
	}
	return chunk
}

// outputChunk maps a command's output body onto the §13.3 live-chunk shape.
// The key is `output` rather than `text`: `text` is agent.output's prose, and
// a client must be able to tell what the agent said from what a command
// printed.
func outputChunk(o *CommandOutput) map[string]any {
	chunk := map[string]any{"output": o.Text}
	if o.CallID != "" {
		chunk["call_id"] = o.CallID
	}
	if o.Name != "" {
		chunk["name"] = o.Name
	}
	if o.Truncated {
		chunk["truncated"] = true
	}
	return chunk
}

// patchChunk maps an edit's hunks onto the §13.3 live-chunk shape, matching
// what api.normalizeLine writes for the same event. The key is `patch`, not
// `output`: a client must be able to tell what an edit changed from what a
// command printed (task 110).
func patchChunk(p *Patch) map[string]any {
	chunk := map[string]any{"patch": p.Text}
	if p.CallID != "" {
		chunk["call_id"] = p.CallID
	}
	if p.Name != "" {
		chunk["name"] = p.Name
	}
	if p.Truncated {
		chunk["truncated"] = true
	}
	return chunk
}

// toolChunks maps tool uses onto the §13.3 live-chunk shape. It must match
// what api.normalizeLine writes for the same event: a client renders the
// live tail and the fetched scrollback through one path, so a difference
// here shows up as output that changes when a step finishes.
func toolChunks(tools []ToolUse) []map[string]string {
	out := make([]map[string]string, 0, len(tools))
	for _, t := range tools {
		chunk := map[string]string{"name": t.Name}
		if t.Summary != "" {
			chunk["summary"] = t.Summary
		}
		if t.CallID != "" {
			chunk["call_id"] = t.CallID
		}
		out = append(out, chunk)
	}
	return out
}

// resultChunks maps tool results onto the §13.3 live-chunk shape, matching
// what api.normalizeLine writes for the same event.
func resultChunks(results []ToolResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		chunk := map[string]any{}
		for k, v := range map[string]string{
			"call_id": r.CallID, "name": r.Name, "summary": r.Summary, "verb": r.Verb,
		} {
			if v != "" {
				chunk[k] = v
			}
		}
		if r.Blocked {
			chunk["blocked"] = true
		}
		if r.IsError {
			chunk["is_error"] = true
		}
		out = append(out, chunk)
	}
	return out
}

// UnmodeledLine reports whether §13.3 has no normalized shape for this event
// and therefore surfaces its verbatim line as `agent.raw`. It is the exact
// complement of what internal/api's normalizeLine calls raw, so a runner that
// publishes a raw chunk for these and nothing else agrees line for line with
// what the transcript route hands back (task 071).
//
// EventResult and EventError are deliberately not here: they do normalize —
// to agent.result and agent.error — and a runner that has no line to publish
// for them simply publishes none, rather than publishing a shape the
// transcript would contradict. EventInputEcho is absent for the same reason
// (task 124.2): it normalizes to agent.input_echo, a record with no live
// chunk, and a raw chunk for it would count cursor's prompt echo as an
// unrecognized line on every turn.
func UnmodeledLine(ev Event) bool {
	switch ev.Type { //nolint:exhaustive // every other type has a normalized shape
	case EventInputRequest, EventInputCanceled, EventUnknown:
		return true
	}
	return false
}
