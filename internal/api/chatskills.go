package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
)

// containerSkillsReason is what a container-run linked chat is told in place
// of a list (task 124 decision 11, 124.9, #505). Listing on the host would
// name skills the agent in the container never loads, which is emulation;
// probing through the container is task 124.17 (#513), and this string goes
// when that lands.
const containerSkillsReason = "this chat's task runs in a container, and listing skills " +
	"inside the task's container is not supported yet"

// chatSkillsBody is GET /v1/chats/{id}/skills (§5.5, §13.2, task 124.9,
// #505). Its fields are flat siblings, §9.6's rule for GET /v1/agents: a
// verdict, its reason and the probe's health side by side rather than nested
// under a status object.
//
// Listing and invoking are separate verdicts because they are separate
// capabilities: an adapter may invoke skills it cannot list (task 124
// decision 2).
type chatSkillsBody struct {
	ChatID int64  `json:"chat_id"`
	Agent  string `json:"agent"`
	// WorkDir is the directory the chat's next turn would start in, and so
	// the one the list is about (chatrun.Runner.Workspace).
	WorkDir string `json:"work_dir"`
	// ListVerdict means what agent.InputVerdict means (task 124 decision 4):
	// "supported" is a real list, "unsupported" a positive no, and
	// "unknown" nobody can say. An empty Skills is "none" only under
	// "supported".
	ListVerdict string `json:"list_verdict"`
	// UnavailableReason says why there is no list: "" when there is one or
	// the answer is only unknown.
	UnavailableReason string `json:"unavailable_reason"`
	// ProbeError is the latest probe's failure, null when it answered. It may
	// sit beside a "supported" list, which is then the earlier one the cache
	// kept (T4.22).
	ProbeError *string `json:"probe_error"`
	// ProbedAt is when the served list was obtained, RFC3339 UTC; null when
	// there is none.
	ProbedAt *string `json:"probed_at"`
	// InvokeVerdict is agent.CanInvokeSkills as a verdict: "unknown" only
	// when the chat's adapter is not registered.
	InvokeVerdict  string            `json:"invoke_verdict"`
	InvokeSigil    string            `json:"invoke_sigil"`
	InvokePosition string            `json:"invoke_position"`
	Skills         []chatSkillBody   `json:"skills"`
	Problems       []chatProblemBody `json:"problems"`
}

// chatSkillBody is one agent.Skill on the wire. Every field is the CLI's own
// word, verbatim (task 124 decisions 8 and 17): no kind, no normalized scope.
type chatSkillBody struct {
	Name string `json:"name"`
	// Invocation is the adapter's SkillInvoker.Invocation, so no client ever
	// builds one (task 124 decision 9); "" when the adapter cannot invoke.
	Invocation   string   `json:"invocation"`
	Description  string   `json:"description"`
	ArgumentHint string   `json:"argument_hint"`
	Aliases      []string `json:"aliases"`
	Scope        string   `json:"scope"`
	Plugin       string   `json:"plugin"`
	Path         string   `json:"path"`
}

// chatProblemBody is one agent.SkillProblem: an entry the CLI found and could
// not load.
type chatProblemBody struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// handleChatSkills answers which skills the chat's agent CLI would load in the
// chat's directory, and how a message invokes one (§5.5, §13.2, task 124.9,
// #505).
//
// It answers 200 with verdicts and refuses only on state (task 124 decision
// 4): a read about a capability is information, not a refused action. The
// cache owns the TTLs, single flight and the list kept across a failed probe
// (§9.6); this handler only picks the directory and maps the answer. A probe
// is not a turn — it takes no `max_parallel_chats` slot — so a running or
// awaiting chat is listed like an idle one.
func (s *Server) handleChatSkills(w http.ResponseWriter, r *http.Request) {
	// The tolerated-nil convention: a server built without any of the three
	// answers 500 rather than guessing at a directory or an adapter.
	if s.deps.Skills == nil || s.deps.Chats == nil || s.deps.Agents == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal,
			"no skill cache, chat runner or agent registry is wired")
		return
	}
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	// Before refresh is read, so a terminal chat never costs a probe: it has
	// no next turn, and so no directory a list could be about.
	if chatstate.Terminal(chat.State) {
		writeConflict(w, fmt.Sprintf("this chat is %s, so it has no next turn to list skills for", chat.State),
			map[string]string{"state": string(chat.State), "action": "skills"})
		return
	}
	refresh := r.URL.Query().Has("refresh") &&
		r.URL.Query().Get("refresh") != "false" && r.URL.Query().Get("refresh") != "0"
	// The turn's own placement, so the list and the next turn cannot disagree
	// about where the CLI starts.
	dir, inContainer, err := s.deps.Chats.Workspace(r.Context(), chat)
	if errors.Is(err, chatrun.ErrLinkedTaskNoWorktree) {
		writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
			Code: CodeTaskHasNoWorktree, Message: err.Error(),
			Details: map[string]string{"task_id": fmt.Sprint(*chat.LinkedTaskID)},
		}})
		return
	}
	if err != nil {
		s.internalError(w, "chat workspace", err)
		return
	}
	body := chatSkillsBody{
		ChatID: chat.ID, Agent: chat.Agent, WorkDir: dir,
		Skills: []chatSkillBody{}, Problems: []chatProblemBody{},
	}
	a, registered := s.deps.Agents.Get(chat.Agent)
	var invoker agent.SkillInvoker
	switch {
	case !registered:
		body.InvokeVerdict = string(agent.InputUnknown)
	case agent.CanInvokeSkills(a):
		invoker = a.(agent.SkillInvoker)
		syntax := invoker.SkillSyntax()
		body.InvokeVerdict = string(agent.InputSupported)
		body.InvokeSigil, body.InvokePosition = syntax.Sigil, string(syntax.Position)
	default:
		body.InvokeVerdict = string(agent.InputUnsupported)
	}
	switch {
	case inContainer:
		// Checked ahead of the adapter, and the cache is never called: a
		// host probe would list the wrong skills (task 124 decision 11). A
		// container that is configured but gone lands here too, and still
		// never falls back to the host.
		body.ListVerdict = string(agent.InputUnknown)
		body.UnavailableReason = containerSkillsReason
	case !registered:
		// A chat whose adapter is no longer registered is "nobody can say",
		// never a no (task 124 decision 58): the name may come back.
		body.ListVerdict = string(agent.InputUnknown)
		pe := fmt.Sprintf("no adapter named %q", chat.Agent)
		body.ProbeError = &pe
	case !agent.CanListSkills(a):
		// A fact about the adapter, so the route words it and asks nothing
		// (task 124 decision 57).
		body.ListVerdict = string(agent.InputUnsupported)
		body.UnavailableReason = fmt.Sprintf("%s does not report the skills it loads", chat.Agent)
	default:
		renderSkillAnswer(&body, s.deps.Skills.Lookup(r.Context(), a, dir, refresh), invoker)
	}
	writeJSON(w, http.StatusOK, body)
}

// renderSkillAnswer maps the cache's answer onto body. Order and duplicate
// names are the CLI's and are kept; every slice renders as an array, never
// null, so "none" has one spelling on the wire.
func renderSkillAnswer(body *chatSkillsBody, ans agent.SkillAnswer, invoker agent.SkillInvoker) {
	body.ListVerdict = string(ans.Verdict)
	if ans.Verdict == agent.InputUnsupported {
		body.UnavailableReason = ans.Reason
	}
	if ans.ProbeError != "" {
		pe := ans.ProbeError
		body.ProbeError = &pe
	}
	if !ans.ProbedAt.IsZero() {
		at := ans.ProbedAt.UTC().Format(time.RFC3339)
		body.ProbedAt = &at
	}
	for _, sk := range ans.Skills {
		row := chatSkillBody{
			Name: sk.Name, Description: sk.Description, ArgumentHint: sk.ArgumentHint,
			Aliases: sk.Aliases, Scope: sk.Scope, Plugin: sk.Plugin, Path: sk.Path,
		}
		if row.Aliases == nil {
			row.Aliases = []string{}
		}
		if invoker != nil {
			row.Invocation = invoker.Invocation(sk, ans.Skills)
		}
		body.Skills = append(body.Skills, row)
	}
	for _, p := range ans.Problems {
		body.Problems = append(body.Problems, chatProblemBody{Path: p.Path, Message: p.Message})
	}
}
