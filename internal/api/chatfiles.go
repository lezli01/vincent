package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/worktree"
)

// chatFilesMax is the most rows GET /v1/chats/{id}/files will ever serve, and
// the ceiling `?limit=` may lower but never raise (task 126 decision 36).
//
// It is workflow.MaxSourceBytes' reasoning applied to a listing: a bound the
// daemon chose beats an allocation it did not, and a `limit` that could raise
// the ceiling would hand that choice back to the caller. The number is a
// judgement — larger than any repository worth calling normal, about 3 MB of
// JSON — and `truncated` is what makes a wrong guess visible rather than
// silent.
const chatFilesMax = 50000

// chatFilesBody is GET /v1/chats/{id}/files (§5.5, §13.2, task 126.6, #550):
// the files of the directory the chat's next turn would start in, each with
// the exact text that mentions it.
//
// The mention facts are flat siblings, never nested under a `mention` object
// (task 126 decision 37). That is §9.6's rule, the one chatSkillsBody already
// follows with InvokeSigil and InvokePosition beside each other, and task
// 041's reasoning against nesting one facet while its siblings stay flat
// applies unchanged. It also leaves the key `mention` meaning exactly one
// thing here: a row's ready-to-insert text.
type chatFilesBody struct {
	ChatID int64  `json:"chat_id"`
	Agent  string `json:"agent"`
	// WorkDir is the directory the chat's next turn would start in, and so
	// the one the listing is about (chatrun.Runner.Workspace).
	//
	// It is a **host** path, including for a linked chat on a containerized
	// task, and that is a stated consequence of enumerating on the host
	// (task 126 decision 40). It happens to equal the container path,
	// because containerMounts bind-mounts the worktree at its own absolute
	// host path so the repository resolves with zero translation (task 061
	// decision 2) — where chatSkillsBody.WorkDir is a *container* path in
	// that same case. The two agree by construction, not by contract: if the
	// bind-mount invariant ever changes — a remote container runtime, say,
	// since container.Runtime shells out and holds no daemon connection —
	// these paths break silently, with nothing failing at compile time.
	WorkDir string `json:"work_dir"`
	// MentionSigil and MentionPosition are the chat adapter's static
	// FileMentionSyntax, and MentionExpands whether its CLI puts the file in
	// front of the model itself rather than leaving the model to read it
	// with a tool (§9.1).
	//
	// An adapter that cannot mention files — unregistered, or registered and
	// not a FileMentioner — is an empty sigil rather than a refusal (task 126
	// decision 38): "" is the skills route's own spelling for "this adapter
	// cannot", and it is the client's signal not to offer the picker. No
	// verdict vocabulary is imported, because a file listing has no axis on
	// which nobody can say: git either answered or errored.
	MentionSigil    string `json:"mention_sigil"`
	MentionPosition string `json:"mention_position"`
	MentionExpands  bool   `json:"mention_expands"`
	// Files is git's own rows in git's own order, and is an array even when
	// it is empty, never null.
	Files []chatFileBody `json:"files"`
	// Truncated says rows were cut, by either bound: the daemon's ceiling or
	// a lower `?limit=`.
	Truncated bool `json:"truncated"`
}

// chatFileBody is one file on the wire. It is an object rather than a bare
// string so `tracked` (task 126 decision 14), a `kind` or a size can be added
// without a breaking change — the call branchBody made.
type chatFileBody struct {
	// Path is workspace-relative and forward-slash-separated on every
	// platform: git's own bytes, unsorted and uncased (task 126 decision 21).
	Path string `json:"path"`
	// Mention is the adapter's FileMentioner.FileMention for Path, so no
	// client builds the insert text and the `@"…"`-on-a-space quoting rule
	// has one definition, in the adapter (task 126 decision 1) — the way
	// chatSkillBody.Invocation comes from SkillInvoker.Invocation. "" when
	// the adapter cannot mention files.
	Mention string `json:"mention"`
}

// handleChatFiles lists the files a chat's next turn could be pointed at, and
// how a message names one (§5.5, §13.2, task 126.6, #550).
//
// The directory is chatrun.Runner.Workspace, the resolver the next turn
// itself uses, so the list and the turn cannot disagree about what `@` is
// relative to. It refuses only on state: a listing is information, so a chat
// with a next turn is listed whether it is idle, running or awaiting input,
// and it takes no §11 slot — one short-lived `git ls-files`, no adapter
// deadline, no chat or turn row touched.
//
// **A containerized task is listed on the host** (task 126 decision 3), which
// is a scoped departure from task 124 decision 11 — the decision
// handleChatSkills cites as 124.17 decision 1 — and not a hole in it. That
// decision is about the CLI's own *skill* directories, which live in the
// image: listing those on the host would name the skills of a machine the
// agent never runs on, which is §9's emulation. Files do not transfer.
// containerMounts bind-mounts the project repository and the task's worktree
// each at its own absolute host path (internal/taskrun/container.go),
// specifically so the repository resolves with zero translation, so a host
// `git ls-files` reads the identical directory the agent reads at the
// identical path the agent would have to type — there is nothing to emulate.
// Enumerating inside is also barely possible: it would need git on the
// image's PATH, and what §16 requires of an image is the agent CLI. So
// Workspace is the placement here and SkillPlace is not: Workspace answers
// the directory alone and never asks the container runtime, which is why this
// route has no taskrun.ErrTaskContainerMissing leg and no
// missingContainerSkillsReason twin.
//
// **There is no server cache, and therefore no `?refresh=`** (task 126
// decision 5). The skill cache exists because an agent CLI probe is a process
// spawn measured in seconds; caching a 30 ms git call would buy latency
// nobody is waiting on and cost a second invalidation surface. Recorded here
// so a later cache is a decision rather than a drift.
func (s *Server) handleChatFiles(w http.ResponseWriter, r *http.Request) {
	// The tolerated-nil convention handleChatSkills opens with: a server
	// built without any of the three answers 500 rather than guessing at a
	// directory or an adapter.
	if s.deps.Chats == nil || s.deps.Worktrees == nil || s.deps.Agents == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal,
			"no chat runner, worktree manager or agent registry is wired")
		return
	}
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	// Before limit is parsed and before any git runs, exactly the skills
	// route's ordering: a terminal chat has no next turn, and so no
	// directory a listing could be about.
	if chatstate.Terminal(chat.State) {
		writeConflict(w, fmt.Sprintf("this chat is %s, so it has no next turn to list files for", chat.State),
			map[string]string{"state": string(chat.State), "action": "files"})
		return
	}
	limit, ok := chatFilesLimit(r.URL.Query().Get("limit"))
	if !ok {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("limit must be a positive integer, got %q", r.URL.Query().Get("limit")))
		return
	}
	dir, err := s.deps.Chats.Workspace(r.Context(), chat)
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
	paths, dropped, err := s.deps.Worktrees.ListFiles(r.Context(), dir)
	if err != nil {
		// A workspace that has gone missing under the daemon is the
		// caller's repository problem, the mapping handleProjectBranches
		// already performs for a missing project path. Every other git
		// failure is the daemon's.
		if worktree.ReasonOf(err) == worktree.ReasonWorkspacePathMissing {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
			return
		}
		s.internalError(w, "list chat files", err)
		return
	}
	if dropped > 0 {
		// The daemon log and not the wire (task 126 decision 39, closing
		// 126.5 decision 18's deferral): the rows are undrawable by
		// definition, so a client can do nothing with the number, and the
		// operator still has the record when a path is missing from a
		// picker.
		s.deps.Logger.Warn("chat file listing: dropped undrawable paths",
			"chat_id", chat.ID, "dir", dir, "dropped", dropped)
	}
	body := chatFilesBody{
		ChatID: chat.ID, Agent: chat.Agent, WorkDir: dir,
		Files: make([]chatFileBody, 0, min(len(paths), limit)),
	}
	var mentioner agent.FileMentioner
	if a, registered := s.deps.Agents.Get(chat.Agent); registered && agent.CanMentionFiles(a) {
		mentioner = a.(agent.FileMentioner)
		syntax := mentioner.FileMentionSyntax()
		body.MentionSigil, body.MentionPosition = syntax.Sigil, string(syntax.Position)
		body.MentionExpands = syntax.Expands
	}
	if len(paths) > limit {
		paths, body.Truncated = paths[:limit], true
	}
	for _, p := range paths {
		row := chatFileBody{Path: p}
		if mentioner != nil {
			row.Mention = mentioner.FileMention(p)
		}
		body.Files = append(body.Files, row)
	}
	writeJSON(w, http.StatusOK, body)
}

// chatFilesLimit reads `?limit=`, which may only *lower* the daemon's own
// ceiling (task 126 decision 36). An absent or empty value is the ceiling
// itself; anything that is not a positive integer is the 400 that
// handleProjectGitHubIssues spells the same way, reported by ok being false.
func chatFilesLimit(raw string) (limit int, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return chatFilesMax, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, false
	}
	return min(n, chatFilesMax), true
}
