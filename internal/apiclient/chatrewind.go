package apiclient

// Rewind and fork a chat from a turn's checkpoint (task 167, §5.5, §13.2).

import (
	"context"
	"fmt"
	"net/http"
)

// RewindRequest is the POST /v1/chats/{id}/rewind body. Turn rewinds to the
// state before that turn ran, in Mode "code", "conversation" or "both" (the
// daemon's default when Mode is empty). Rewound instead goes forward to the
// k-th saved pre-rewind state, which restores code only. Exactly one of Turn
// and Rewound is set; the daemon validates which.
type RewindRequest struct {
	Turn    int    `json:"turn,omitempty"`
	Rewound int    `json:"rewound,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

// RewindResult is a rewind's answer: the chat as it now stands, the id that
// groups the turns it marked rewound, and the `rewound-<k>` ref it saved the
// pre-rewind state under.
//
// Skipped lists the files the pre-rewind snapshot could not read (task 167
// decision D): the restore left them in place rather than delete what it had
// not saved, so a client must name them. Seeded says the chat's next turn
// starts a fresh session given the earlier turns, rather than resuming.
type RewindResult struct {
	Chat         Chat     `json:"chat"`
	RewindID     string   `json:"rewind_id"`
	Ref          string   `json:"ref"`
	Skipped      []string `json:"skipped"`
	Seeded       bool     `json:"seeded"`
	RewoundTurns []int    `json:"rewound_turns"`
}

// RewindChat posts a rewind. It never runs an agent: a conversation rewind
// takes effect on the chat's next turn. A 409's code says why it was
// refused — chat_turn_live, chat_checkpoint_unavailable,
// chat_rewind_foreign_commits, chat_turn_rewound or chat_terminal.
//
// The daemon snapshots and restores the whole worktree before it answers —
// work bounded by the worktree's size, as making one is — so the call is
// held to worktreeTimeout, not the loopback deadline.
func (c *Client) RewindChat(ctx context.Context, id int64, req RewindRequest) (*RewindResult, error) {
	var out RewindResult
	path := fmt.Sprintf("/v1/chats/%d/rewind", id)
	if err := c.sendVia(ctx, c.worktrees, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ForkRequest is the POST /v1/chats/{id}/fork body: the turn whose
// checkpoint the new chat starts from, and an optional title.
type ForkRequest struct {
	Turn  int    `json:"turn"`
	Title string `json:"title,omitempty"`
	// Agent forks onto another agent (task 171); the fork is then always
	// seeded, and Model and Effort are the new agent's ("" for its
	// defaults). Empty is the source's agent.
	Agent  string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// PermissionMode overrides the mode copied from the source.
	PermissionMode string `json:"permission_mode,omitempty"`
}

// ForkResult is a fork's answer: the new, idle chat, the files its
// checkpoint could not carry over, and whether its first turn will be
// seeded rather than resumed.
type ForkResult struct {
	Chat    Chat     `json:"chat"`
	Skipped []string `json:"skipped"`
	Seeded  bool     `json:"seeded"`
}

// ForkChat creates a new free chat from one turn's checkpoint (task 167
// decision 11). It starts no turn. It is refused as a rewind is, and also by
// the create path's restricted-mode checks (task 162). Like CreateChat it
// makes a worktree before it answers, so it is held to worktreeTimeout.
func (c *Client) ForkChat(ctx context.Context, id int64, req ForkRequest) (*ForkResult, error) {
	var out ForkResult
	path := fmt.Sprintf("/v1/chats/%d/fork", id)
	if err := c.sendVia(ctx, c.worktrees, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
