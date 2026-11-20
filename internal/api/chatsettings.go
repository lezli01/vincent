package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// CodeAgentCannotChangeOnResume refuses a model or effort change on a chat
// whose session has already run, on an adapter with no capture showing that
// a resumed run honours a changed value (task 162 decision 8, §9.1). The
// remedy is nameable — keep the settings, or open a new chat — so it is its
// own code rather than a generic validation failure.
const CodeAgentCannotChangeOnResume = "agent_cannot_change_on_resume"

// CodeAgentCannotRestrictResume refuses `restricted` on a chat whose adapter
// cannot keep a resumed turn restricted (codex, §9.3) or cannot restrict on
// this host at all (cursor on Windows, §9.7). It is task 072 decision 1's
// guard, moved from the absence of a mode field to the place the mode is
// chosen (task 162 decision 1).
const CodeAgentCannotRestrictResume = "agent_cannot_restrict_resume"

// chatSettingsRequest is the settings half of PATCH /v1/chats/{id}, of a send
// body, of a queued message's edit and of POST /v1/chats: each field optional,
// and an absent one changes nothing.
type chatSettingsRequest struct {
	Model          *string `json:"model"`
	Effort         *string `json:"effort"`
	PermissionMode *string `json:"permission_mode"`
}

func (r chatSettingsRequest) empty() bool {
	return r.Model == nil && r.Effort == nil && r.PermissionMode == nil
}

// turnSettings is r as one turn's overrides. An empty string is "no
// override", which is what a per-turn field can only mean.
func (r chatSettingsRequest) turnSettings() store.TurnSettings {
	return store.TurnSettings{
		Model:          strings.TrimSpace(ptrValue(r.Model)),
		Effort:         strings.TrimSpace(ptrValue(r.Effort)),
		PermissionMode: strings.TrimSpace(ptrValue(r.PermissionMode)),
	}
}

// settingsRefusal is a refused settings change: the status, code and message
// to answer with, and the details a 409 carries.
type settingsRefusal struct {
	status  int
	code    string
	message string
	details map[string]string
}

func (f *settingsRefusal) write(w http.ResponseWriter) {
	if f.status == http.StatusConflict {
		writeConflict(w, f.message, f.details)
		return
	}
	writeError(w, f.status, f.code, f.message)
}

// checkChatSettings validates want — the settings a chat's coming turns, or
// one turn, would run with — against chat (task 162 decisions 1, 3, 5 and
// 8). The same check serves PATCH, a per-turn override and a queued
// message's, so none of the three can reach a combination the others refuse.
// It returns the catalog's advisory warnings, as task creation does (T2.11).
func (s *Server) checkChatSettings(
	ctx context.Context, chat *store.Chat, want store.TurnSettings,
) (warnings []string, refusal *settingsRefusal) {
	if s.deps.Agents == nil {
		return nil, &settingsRefusal{
			status: http.StatusInternalServerError, code: CodeInternal,
			message: "no agent registry is configured",
		}
	}
	adapter, ok := s.deps.Agents.Get(chat.Agent)
	if !ok {
		return nil, &settingsRefusal{
			status: http.StatusBadRequest, code: CodeValidationFailed,
			message: fmt.Sprintf("chat %d's agent %q is not registered", chat.ID, chat.Agent),
		}
	}
	if want.PermissionMode == "" {
		want.PermissionMode = string(agent.FullAuto) // the column's default
	}
	switch agent.PermissionMode(want.PermissionMode) {
	case agent.FullAuto:
		restricted, taskID, err := s.linkedTaskRestricted(ctx, chat)
		if err != nil {
			return nil, &settingsRefusal{status: http.StatusInternalServerError, code: CodeInternal, message: err.Error()}
		}
		if restricted {
			// Decision 3: the linked chat's clamp holds, by PATCH, by a
			// per-turn override and by a queued one alike.
			return nil, &settingsRefusal{
				status: http.StatusConflict, message: fmt.Sprintf(
					"chat %d is linked to task %d, which runs restricted; its chat can never run full-auto",
					chat.ID, taskID),
				details: map[string]string{
					"state": string(chat.State), "action": "permission_mode",
					"linked_task_id": strconv.FormatInt(taskID, 10),
				},
			}
		}
	case agent.Restricted:
		if !agent.CanResumeRestricted(adapter) || !s.restrictedPossibleHere(chat.Agent) {
			return nil, &settingsRefusal{
				status: http.StatusBadRequest, code: CodeAgentCannotRestrictResume,
				message: fmt.Sprintf("agent %q cannot keep a resumed chat turn restricted%s; "+
					"vincent refuses this rather than let a later turn run full-auto",
					chat.Agent, restrictHereSuffix(chat.Agent)),
			}
		}
	default:
		return nil, &settingsRefusal{
			status: http.StatusBadRequest, code: CodeValidationFailed,
			message: fmt.Sprintf("permission_mode must be %q or %q", agent.FullAuto, agent.Restricted),
		}
	}
	// A session exists once a turn has started: before the first, there is
	// nothing to resume and any model is the first one (decision 8).
	// UpdateChatSettings judges this again inside its transaction (review
	// F2); this read decides it for a per-turn override.
	modelChanged := want.Model != chat.Model || want.Effort != chat.Effort
	if modelChanged && store.ChatStarted(chat) && !agent.CanChangeOnResume(adapter) {
		return nil, modelFixedRefusal(chat)
	}
	if s.deps.Catalog != nil {
		errs, warns := s.deps.Catalog.Catalogs().Check(agent.Selection{
			Agent: chat.Agent, Model: want.Model, Effort: want.Effort,
		})
		if len(errs) > 0 {
			return nil, &settingsRefusal{
				status: http.StatusBadRequest, code: CodeValidationFailed,
				message: errs[0].Message,
			}
		}
		for _, f := range warns {
			warnings = append(warnings, f.Message)
		}
	}
	return warnings, nil
}

// modelFixedRefusal is decision 8's refusal of a model or effort change on a
// chat whose session has started, for an adapter without the capture.
func modelFixedRefusal(chat *store.Chat) *settingsRefusal {
	return &settingsRefusal{
		status: http.StatusBadRequest, code: CodeAgentCannotChangeOnResume,
		message: fmt.Sprintf("agent %q has no capture showing a resumed session honours a changed model "+
			"or effort, so chat %d keeps the ones its session started with; open a new chat to change them",
			chat.Agent, chat.ID),
	}
}

// restrictedPossibleHere is §9.4's host question for name: false only for a
// positive "cannot restrict on this OS" (cursor on Windows, §9.7).
func (s *Server) restrictedPossibleHere(name string) bool {
	if s.deps.Catalog == nil {
		return true
	}
	return s.deps.Catalog.Catalogs().RestrictedPossible(name)
}

func restrictHereSuffix(name string) string {
	if name == "cursor" && runtime.GOOS == "windows" {
		return " on Windows"
	}
	return ""
}

// linkedTaskRestricted reports whether chat is linked to a task whose turns
// must run restricted — the clamp a linked chat was opened under (task 119,
// 025 decision 7) — and that task's id.
func (s *Server) linkedTaskRestricted(ctx context.Context, chat *store.Chat) (bool, int64, error) {
	if !chat.Linked() {
		return false, 0, nil
	}
	task, err := s.deps.Store.GetTask(ctx, *chat.LinkedTaskID)
	if errors.Is(err, store.ErrNotFound) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	if task.Restricted {
		return true, task.ID, nil
	}
	wf, _, perr := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if perr != nil || wf == nil {
		return false, task.ID, nil
	}
	return wf.ClampedPermissionMode(workflow.Step{}, false) == workflow.PermissionRestricted, task.ID, nil
}

type patchChatRequest struct {
	Title *string `json:"title"`
	chatSettingsRequest
}

// handleChatPatch is PATCH /v1/chats/{id}: a chat's title (task 161
// decision 4), model, effort and permission mode (task 162 decision 5), each
// optional. The agent is fixed (§5.5) — changing it is a new chat.
//
// The two halves keep their own legality. A rename alone is legal in every
// state, terminal ones included — the title is display only — and never
// renames the branch. A settings change is legal on `idle`, `running` and
// `awaiting_input`; a change made during a live turn applies from the next
// one, because a turn runs with what its own row recorded.
func (s *Server) handleChatPatch(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var req patchChatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Title == nil && req.empty() {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"nothing to change: give a title, model, effort or permission_mode")
		return
	}
	if !req.empty() && chatstate.Terminal(chat.State) {
		writeConflict(w, "this chat is "+string(chat.State)+"; its settings can no longer change",
			map[string]string{"state": string(chat.State), "action": "patch"})
		return
	}
	patch := store.ChatSettingsPatch{}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "title must not be empty")
			return
		}
		if n := utf8.RuneCountInString(title); n > ChatTitleMaxRunes {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("title is %d characters; at most %d", n, ChatTitleMaxRunes))
			return
		}
		patch.Title = &title
	}
	if req.empty() {
		// A rename alone (task 161): legal in every state.
		updated, err := s.deps.Store.SetChatTitle(r.Context(), chat.ID, *patch.Title)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
			return
		}
		if err != nil {
			s.internalError(w, "rename chat", err)
			return
		}
		writeJSON(w, http.StatusOK, renderChat(updated))
		return
	}
	var warnings []string
	want := store.TurnSettings{Model: chat.Model, Effort: chat.Effort, PermissionMode: chat.PermissionMode}
	if req.Model != nil {
		want.Model = strings.TrimSpace(*req.Model)
		patch.Model = &want.Model
	}
	if req.Effort != nil {
		want.Effort = strings.TrimSpace(*req.Effort)
		patch.Effort = &want.Effort
	}
	if req.PermissionMode != nil {
		want.PermissionMode = strings.TrimSpace(*req.PermissionMode)
		patch.PermissionMode = &want.PermissionMode
	}
	var refusal *settingsRefusal
	if warnings, refusal = s.checkChatSettings(r.Context(), chat, want); refusal != nil {
		refusal.write(w)
		return
	}
	// The adapter's answer travels with the write, and the store judges
	// "started" from the row it writes: a first turn beginning since the
	// read above cannot carry a model change past decision 8 (review F2).
	if adapter, ok := s.deps.Agents.Get(chat.Agent); ok {
		patch.KeepModelOnceStarted = !agent.CanChangeOnResume(adapter)
	}
	updated, err := s.deps.Store.UpdateChatSettings(r.Context(), chat.ID, patch)
	switch {
	case errors.Is(err, store.ErrChatModelFixed):
		modelFixedRefusal(chat).write(w)
		return
	case errors.Is(err, store.ErrInvalidChatAction):
		writeConflict(w, err.Error(), map[string]string{"state": string(chat.State), "action": "patch"})
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	body := renderChat(updated)
	body.Warnings = warnings
	writeJSON(w, http.StatusOK, body)
}

// checkTurnOverrides validates a send's or a queued message's overrides,
// resolved over the chat's current settings, and writes the refusal itself.
func (s *Server) checkTurnOverrides(
	w http.ResponseWriter, r *http.Request, chat *store.Chat, ov store.TurnSettings,
) bool {
	if ov.Empty() {
		return true
	}
	if _, refusal := s.checkChatSettings(r.Context(), chat, ov.Resolve(chat)); refusal != nil {
		refusal.write(w)
		return false
	}
	return true
}
