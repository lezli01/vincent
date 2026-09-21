package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Chat is one conversation (spec §5.5, §13.2). The wire types live here, in
// the one client both the TUI and the CLI consume, so client and server
// cannot drift without a *live_test.go noticing.
type Chat struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Agent     string `json:"agent"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Branch    string `json:"branch"`
	// AdoptedBranch says the chat runs on a branch vincent did not cut
	// (task 125).
	AdoptedBranch bool            `json:"adopted_branch"`
	BaseBranch    string          `json:"base_branch"`
	BaseSHA       string          `json:"base_sha,omitempty"`
	BaseRefresh   *BaseRefresh    `json:"base_refresh"`
	WorktreePath  string          `json:"worktree_path,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	PendingInput  json.RawMessage `json:"pending_input,omitempty"`
	// HandoffTaskID is the task this chat's worktree and branch were handed
	// to (task 074). Set exactly in `handed_off`.
	HandoffTaskID *int64 `json:"handoff_task_id,omitempty"`
	// LinkedTaskID is the task this chat was opened on (task 119). Such a
	// chat works in that task's worktree and branch rather than its own, and
	// while it is open the task is locked; it ends in `closed`, never in
	// `archived` or `handed_off`.
	LinkedTaskID *int64    `json:"linked_task_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ChatSkills is GET /v1/chats/{id}/skills: the skills the chat's agent CLI
// would load in the chat's directory, and how a message invokes one (§5.5,
// §13.2, task 124.9). Listing and invoking are separate verdicts, each
// "supported", "unsupported" or "unknown" with agent.InputVerdict's meaning;
// an empty Skills is "none" only when ListVerdict is "supported".
type ChatSkills struct {
	ChatID int64  `json:"chat_id"`
	Agent  string `json:"agent"`
	// WorkDir is the directory the chat's next turn would start in — its own
	// worktree, or its linked task's — and so the one the list is about.
	WorkDir     string `json:"work_dir"`
	ListVerdict string `json:"list_verdict"`
	// UnavailableReason says why there is no list; "" when there is one or
	// the answer is only unknown.
	UnavailableReason string `json:"unavailable_reason"`
	// ProbeError is the latest probe's failure; nil when it answered. Beside
	// a "supported" list it means the list is an earlier one the daemon kept.
	ProbeError *string `json:"probe_error"`
	// ProbedAt is when the served list was obtained; nil when there is none.
	ProbedAt       *time.Time `json:"probed_at"`
	InvokeVerdict  string     `json:"invoke_verdict"`
	InvokeSigil    string     `json:"invoke_sigil"`
	InvokePosition string     `json:"invoke_position"`
	// BuiltinSkills says what became of the rows the CLI marks as its own
	// (task 124.16): "listed" when the skills it bundles are among Skills,
	// flagged Builtin; "after_first_turn" when it has such rows and no turn
	// has yet said which of them are skills rather than built-in commands, so
	// they are all withheld; "" when the question does not arise.
	BuiltinSkills string `json:"builtin_skills"`
	// Skills is in the CLI's order, and names may repeat: key nothing by
	// name.
	Skills   []ChatSkill        `json:"skills"`
	Problems []ChatSkillProblem `json:"problems"`
}

// ChatSkill is one skill as the chat's agent CLI reported it. Every field is
// the CLI's own word, "" where it said nothing (task 124 decision 8).
type ChatSkill struct {
	Name string `json:"name"`
	// Invocation is the exact text to insert to invoke this skill, built by
	// the daemon's adapter; a client never builds its own. "" when the
	// adapter cannot invoke skills.
	Invocation   string   `json:"invocation"`
	Description  string   `json:"description"`
	ArgumentHint string   `json:"argument_hint"`
	Aliases      []string `json:"aliases"`
	Scope        string   `json:"scope"`
	Plugin       string   `json:"plugin"`
	Path         string   `json:"path"`
	// Builtin is a skill the CLI ships itself, served only once a turn has
	// proven it is a skill and not a built-in command (task 124.16).
	Builtin bool `json:"builtin"`
}

// ChatSkillProblem is an entry the chat's agent CLI found and could not load.
type ChatSkillProblem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ChatFiles is GET /v1/chats/{id}/files: the files of the directory the
// chat's next turn would start in, each with the exact text that mentions it
// (§5.5, §13.2, task 126.6).
//
// The mention facts are flat siblings rather than a nested object, the way
// ChatSkills carries InvokeSigil and InvokePosition (§9.6's rule, task 126
// decision 37), which leaves `Mention` on a row meaning one thing: its
// ready-to-insert text.
type ChatFiles struct {
	ChatID int64  `json:"chat_id"`
	Agent  string `json:"agent"`
	// WorkDir is the directory the listing is about — the chat's own
	// worktree, or its linked task's. It is a host path even when the task
	// runs in a container, where ChatSkills.WorkDir is a container path; the
	// two agree only because the worktree is bind-mounted at its own path
	// (task 126 decision 40).
	WorkDir string `json:"work_dir"`
	// MentionSigil and MentionPosition are the chat adapter's static mention
	// syntax, and MentionExpands whether its CLI puts the file in front of
	// the model itself rather than leaving the model to read it with a tool.
	//
	// An empty MentionSigil means this adapter cannot mention files, and is
	// the signal not to offer a picker: the paths are still served, because
	// they are true regardless of who reads them (task 126 decision 38).
	MentionSigil    string `json:"mention_sigil"`
	MentionPosition string `json:"mention_position"`
	MentionExpands  bool   `json:"mention_expands"`
	// Files is git's own order — workspace-relative, forward-slash on every
	// platform, unsorted — and is an array even when empty, never null.
	Files []ChatFile `json:"files"`
	// Truncated says rows were cut, by the daemon's ceiling or by a lower
	// limit the caller asked for.
	Truncated bool `json:"truncated"`
}

// ChatFile is one file of the chat's workspace.
type ChatFile struct {
	Path string `json:"path"`
	// Mention is the exact text to insert to mention this path, built by the
	// daemon's adapter; a client never builds its own, because the quoting
	// rule has one definition and it lives there. "" when the adapter cannot
	// mention files.
	Mention string `json:"mention"`
}

// ChatTurn is one exchange in a chat.
type ChatTurn struct {
	ID           int64      `json:"id"`
	ChatID       int64      `json:"chat_id"`
	Seq          int        `json:"seq"`
	Prompt       string     `json:"prompt"`
	State        string     `json:"state"`
	FailReason   string     `json:"fail_reason,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	ResultText   string     `json:"result_text,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	CostUSD      *float64   `json:"cost_usd"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	DurationMS   *int64     `json:"duration_ms,omitempty"`
}

// CreateChatRequest is the POST /v1/chats body. An omitted agent resolves
// server-side to one that can resume a session (§9.1); an omitted base_branch
// to the project's default.
type CreateChatRequest struct {
	ProjectID  int64  `json:"project_id"`
	Title      string `json:"title"`
	Agent      string `json:"agent,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
	// BranchName and ExistingBranch run the chat on a branch that already
	// exists instead of cutting one (§10, task 125). Neither is meaningful
	// without the other.
	BranchName     string `json:"branch_name,omitempty"`
	ExistingBranch bool   `json:"existing_branch,omitempty"`
}

// CreateChat starts a chat: a title, a project, an agent, and a worktree and
// branch of its own.
func (c *Client) CreateChat(ctx context.Context, req CreateChatRequest) (*Chat, error) {
	var out Chat
	if err := c.post(ctx, "/v1/chats", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListChatsOptions narrows GET /v1/chats. It is the shape ListTasksOptions
// has, with the same parameter names on the wire (§13.2, task 092): issue #298
// settled that one vocabulary covers both entities, and the archived board
// pages and date-bounds chats exactly as it does tasks.
//
// It replaced two bare arguments. A third and a fourth would have been two
// more call sites to touch for every parameter after them.
type ListChatsOptions struct {
	ProjectID int64
	// TaskID narrows to the chats opened on one task (task 119) — at most
	// one of them open, and any number closed. Closed is terminal, so the
	// closed ones come back only with Archived set, as on any other listing.
	TaskID int64
	// Archived selects how terminal chats are treated, and reuses ListTasks'
	// ArchivedScope because the wire parameter is the same one (§13.2,
	// amended 2026-09-01). The zero value — ArchivedExclude — leaves the
	// parameter off and takes the server's default, which is to hide every
	// terminal state: `archived`, `handed_off` and `closed`.
	Archived ArchivedScope
	Limit    int
	Offset   int
	// ArchivedBefore and ArchivedSince bound when the chat ended. The daemon
	// measures them over `updated_at`, which for a terminal chat *is* when it
	// ended (task 074 decision 6, task 079 decision 2).
	ArchivedBefore time.Time
	ArchivedSince  time.Time
}

func (o ListChatsOptions) query() string {
	q := url.Values{}
	if o.ProjectID > 0 {
		q.Set("project_id", strconv.FormatInt(o.ProjectID, 10))
	}
	if o.TaskID > 0 {
		q.Set("task_id", strconv.FormatInt(o.TaskID, 10))
	}
	if o.Archived != ArchivedExclude {
		q.Set("archived", string(o.Archived))
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	if !o.ArchivedBefore.IsZero() {
		q.Set("archived_before", o.ArchivedBefore.UTC().Format(time.RFC3339))
	}
	if !o.ArchivedSince.IsZero() {
		q.Set("archived_since", o.ArchivedSince.UTC().Format(time.RFC3339))
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// ListChats fetches chats, optionally narrowed to one project. Tasks never
// appear here, and chats never appear in ListTasks.
func (c *Client) ListChats(ctx context.Context, opts ListChatsOptions) ([]Chat, error) {
	path := "/v1/chats" + opts.query()
	var out struct {
		Chats []Chat `json:"chats"`
	}
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Chats, nil
}

// GetChat fetches one chat with its whole conversation, in order.
func (c *Client) GetChat(ctx context.Context, id int64) (*Chat, []ChatTurn, error) {
	var out struct {
		Chat  Chat       `json:"chat"`
		Turns []ChatTurn `json:"turns"`
	}
	if err := c.get(ctx, fmt.Sprintf("/v1/chats/%d", id), &out); err != nil {
		return nil, nil, err
	}
	return &out.Chat, out.Turns, nil
}

// ChatSkills fetches the skills a chat's agent CLI would load in the chat's
// directory (task 124.9). refresh asks the daemon to probe again rather than
// answer from its cache.
//
// Every call is held to the probe deadline, refresh or not: a cold cache
// spawns the agent CLI to answer, which is not loopback-fast.
func (c *Client) ChatSkills(ctx context.Context, id int64, refresh bool) (*ChatSkills, error) {
	path := fmt.Sprintf("/v1/chats/%d/skills", id)
	if refresh {
		path += "?refresh=true"
	}
	var out ChatSkills
	if err := c.getVia(ctx, c.probeClient(true), path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChatFiles fetches the files of the directory the chat's next turn would
// start in, each with its ready-to-insert mention (task 126.6). A limit above
// zero lowers the daemon's own ceiling; it can never raise it, and zero or
// less means "the daemon's ceiling", which is what an unset limit sends.
//
// It uses the plain REST client and not the probe deadline ChatSkills takes:
// nothing here spawns an agent CLI, only one short-lived `git ls-files`.
// That leaves a known gap standing — requestTimeout is 10s while the
// server-side query is bounded at gitx.QueryTimeout, 30s — so a worktree on a
// cold network filesystem would have this client give up first. Borrowing the
// probe's three minutes to close it would mean a picker that can hang for
// three minutes, which is worse.
func (c *Client) ChatFiles(ctx context.Context, id int64, limit int) (*ChatFiles, error) {
	path := fmt.Sprintf("/v1/chats/%d/files", id)
	if limit > 0 {
		path += fmt.Sprintf("?limit=%d", limit)
	}
	var out ChatFiles
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendChat starts a turn. A 409 with code `chat_cap_reached` means
// `max_parallel_chats` chats are already running — the send is refused, not
// queued (§11).
func (c *Client) SendChat(ctx context.Context, id int64, message string) (*ChatTurn, error) {
	var out ChatTurn
	body := map[string]string{"message": message}
	if err := c.post(ctx, fmt.Sprintf("/v1/chats/%d/send", id), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AnswerChat answers a chat's pending §7.4 input request.
func (c *Client) AnswerChat(ctx context.Context, id int64, resp InputResponse) error {
	return c.post(ctx, fmt.Sprintf("/v1/chats/%d/answer", id), resp, nil)
}

// CancelChat stops a chat's live turn.
func (c *Client) CancelChat(ctx context.Context, id int64) error {
	return c.post(ctx, fmt.Sprintf("/v1/chats/%d/cancel", id), nil, nil)
}

// ArchiveChat archives a chat, removing its worktree and taking an empty
// branch with it. force is the way past a dirty worktree, as on a task.
func (c *Client) ArchiveChat(ctx context.Context, id int64, force bool) (*Chat, error) {
	path := fmt.Sprintf("/v1/chats/%d/archive", id)
	if force {
		path += "?force=true"
	}
	var out Chat
	if err := c.post(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CloseChat ends a chat opened on a task (task 119): a live turn is cancelled
// first, the chat moves to `closed`, and the task's lock lifts. The worktree
// and branch are the task's and are left exactly as they are.
//
// It returns the chat as it now stands. A free chat is a 409 — archive is how
// one of those ends — and so is a chat already closed.
func (c *Client) CloseChat(ctx context.Context, id int64) (*Chat, error) {
	var out Chat
	if err := c.post(ctx, fmt.Sprintf("/v1/chats/%d/close", id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HandoffChat hands the chat's worktree and branch to a new task (task 074).
// The request is a task-create body: `project_id`, `base_branch` and
// `branch_name` are the chat's and are ignored.
//
// It returns the created task and the chat as it now stands — terminal, and
// naming the task. A 409 means the chat was not idle, had nothing to hand
// over, or its worktree is mid-merge or mid-rebase; a 400 means the task
// itself did not validate, and in every case the chat is untouched.
func (c *Client) HandoffChat(ctx context.Context, id int64, req CreateTaskRequest) (TaskDetail, Chat, error) {
	var out struct {
		Task TaskDetail `json:"task"`
		Chat Chat       `json:"chat"`
	}
	if err := c.post(ctx, "/v1/chats/"+strconv.FormatInt(id, 10)+"/handoff", req, &out); err != nil {
		return TaskDetail{}, Chat{}, err
	}
	return out.Task, out.Chat, nil
}

// StreamChat subscribes to GET /v1/chats/{id}/events: that chat's durable
// events interleaved with its live output (§13.3). It is the per-task stream's
// twin — Last-Event-ID resumes the durable events, live output is ephemeral
// and never replayed — so a reconnect catches up by re-fetching the running
// turn's transcript and discarding chunks at or before its NextOffset.
func (c *Client) StreamChat(ctx context.Context, chatID int64, opts StreamOptions) <-chan Note {
	ch := make(chan Note)
	go c.streamLoop(ctx, "/v1/chats/"+strconv.FormatInt(chatID, 10)+"/events", opts, ch)
	return ch
}

// ChatTurnTranscript fetches one turn's transcript in normalized form,
// returning the records and the offset to resume from. The turn is named by
// its 1-based seq, not by a run id: a chat turn is its own run.
func (c *Client) ChatTurnTranscript(
	ctx context.Context, chatID int64, seq int, opts TranscriptOptions,
) (records []TranscriptRecord, nextOffset int64, err error) {
	path := fmt.Sprintf("/v1/chats/%d/turns/%d/transcript%s", chatID, seq, opts.query("normalized"))
	resp, nextOffset, err := c.transcriptAt(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	records, err = decodeTranscript(resp.Body)
	return records, nextOffset, err
}

// ChatTurnTranscriptRaw fetches the same byte range in the agent's own
// dialect, returning the file's bytes unaltered plus the offset to resume
// from. Like TranscriptRaw, and for the same reason, it never goes through
// decodeTranscript: that decoder drops a line it cannot parse, and the caller
// asked for what the file says.
func (c *Client) ChatTurnTranscriptRaw(
	ctx context.Context, chatID int64, seq int, opts TranscriptOptions,
) (data []byte, nextOffset int64, err error) {
	return c.transcriptRawAt(ctx,
		fmt.Sprintf("/v1/chats/%d/turns/%d/transcript%s", chatID, seq, opts.query("raw")))
}
