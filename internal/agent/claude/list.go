package claude

// Skill listing (spec §9.1, §9.2, task 124.7): the stream-json `initialize`
// control request, the one pre-turn, zero-token source claude has for the
// entries a person can invoke by name, with their descriptions and argument
// hints. Captured against 2.1.277 in testdata/initialize_*_2.1.277.jsonl.
//
// The control_request framing is documented by the Agent SDK
// (`SDKControlInitializeResponse`, `SlashCommand`), not by the CLI — the same
// standing as the §7.4 channel input.go speaks, and gated the same way.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

var _ agent.SkillLister = (*Adapter)(nil)

// skillListingFloor is the first build whose initialize reply marks claude's
// own rows `builtin` (task 124 decision 40). Below it only description text
// tells a built-in command from a skill, and decision 7 forbids listing
// `/clear` as one. It is also the only build captured; like supportsInput's
// family, it moves only with re-captured fixtures.
const skillListingFloor = "2.1.277"

// skillListTimeout bounds the whole initialize exchange: spawn, reply and
// exit. The reply takes well under a second; the rest is the cold-logon
// margin versionTimeout carries. A var so a test can make it short.
var skillListTimeout = versionTimeout

// listRequestID is the request_id of the one initialize request a listing
// sends. The reply is matched on it and every other line is skipped.
const listRequestID = "vincent-list-skills"

// supportsSkillListing reports whether a detected CLI version can list its
// skills: the §7.4 input family, for the control channel, from
// skillListingFloor on (task 124 decision 40). A version outside the family
// — a 3.x build as much as a 1.x one — is a positive no, the way
// InputVerdictWith reads supportsInput.
func supportsSkillListing(version string) bool {
	if !supportsInput(version) {
		return false
	}
	parts := strings.SplitN(versionRe.FindString(version), ".", 3)
	minor, _ := strconv.Atoi(parts[1]) // supportsInput parsed both already
	patch, _ := strconv.Atoi(parts[2])
	return minor > 1 || patch >= 277
}

// listSkillsArgs is the listing's whole argv (§9.2, task 124 decision 41).
// Hooks and MCP servers are suppressed, so the probe runs no user code; the
// measured list is identical without them. It never carries `--resume`,
// which would write to the session's `.jsonl`, or `--bare`, which drops user
// and plugin skills — and none of a run's model, effort or permission flags,
// which do not change the list.
func listSkillsArgs() []string {
	return []string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--strict-mcp-config", "--settings", `{"disableAllHooks":true}`,
	}
}

// initializeLine is the one line a listing writes to the CLI's stdin.
func initializeLine() []byte {
	return []byte(`{"type":"control_request","request_id":"` + listRequestID +
		`","request":{"subtype":"initialize"}}` + "\n")
}

// initializeReply is the matched control_response, decoded no further than
// `commands`: the reply also carries the account's email and organization,
// the model list and the agents, and none of that may reach a Go value.
type initializeReply struct {
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
		Response  struct {
			// Commands is a pointer so a missing or null key, which is no
			// answer, stays apart from `[]`, which is an empty list.
			Commands *[]slashCommand `json:"commands"`
		} `json:"response"`
	} `json:"response"`
}

// slashCommand is the SDK's `SlashCommand`.
type slashCommand struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	ArgumentHint string   `json:"argumentHint"`
	Aliases      []string `json:"aliases"`
	Builtin      bool     `json:"builtin"`
}

// ListSkills implements agent.SkillLister (§9.1, §9.2, task 124.7). It spawns
// the CLI in q.WorkDir through q.Launcher, asks `initialize`, and returns the
// reply's commands in the CLI's order with every `builtin` row dropped (task
// 124 decision 7). Nothing is synthesized: a description keeps its display
// label, a repeated name stays repeated, and Scope, Plugin and Path stay "".
//
// A build outside [2.1.277, 3.0.0) answers agent.ErrSkillsUnsupported. Every
// other failure — no binary, no version, a timeout, an error reply, a reply
// without `commands`, an exit before the reply — is an ordinary error, the
// "nobody can say" of decision 16.
func (a *Adapter) ListSkills(ctx context.Context, q agent.SkillQuery) (agent.SkillList, error) {
	path, err := a.resolvePathWith(q.Launcher)
	if err != nil {
		return agent.SkillList{}, err
	}
	version, err := probeVersion(ctx, q.Launcher, path)
	if err != nil {
		return agent.SkillList{}, err
	}
	if !supportsSkillListing(version) {
		return agent.SkillList{}, fmt.Errorf("claude %s: listing needs a 2.x build from %s on: %w",
			version, skillListingFloor, agent.ErrSkillsUnsupported)
	}
	commands, err := initialize(ctx, q, path)
	if err != nil {
		return agent.SkillList{}, err
	}
	return skillsFrom(commands), nil
}

// skillsFrom maps the reply's commands onto a SkillList, in order, dropping
// every `builtin` row: claude marks its own commands and its bundled skills
// alike, and decision 7 lists neither. Scope, Plugin and Path stay "" —
// claude reports scope only inside the description, which is never parsed.
func skillsFrom(commands []slashCommand) agent.SkillList {
	list := agent.SkillList{Skills: make([]agent.Skill, 0, len(commands))}
	for _, c := range commands {
		if c.Builtin {
			continue
		}
		list.Skills = append(list.Skills, agent.Skill{
			Name:         c.Name,
			Description:  c.Description,
			ArgumentHint: c.ArgumentHint,
			Aliases:      c.Aliases,
		})
	}
	return list
}

// initialize runs the exchange: one request line, then stdout until the
// matching reply. The reply is the answer. Once it is in hand stdin closes
// and the CLI exits on its own; one that outlives skillListTimeout is killed
// and the answer still stands, as does a non-zero exit after it.
func initialize(ctx context.Context, q agent.SkillQuery, path string) ([]slashCommand, error) {
	ctx, cancel := context.WithTimeout(ctx, skillListTimeout)
	defer cancel()
	stderr := &tailWriter{max: 64 * 1024}
	proc, err := agent.Launch(q.Launcher, agent.Command{
		Path:      path,
		Args:      listSkillsArgs(),
		Dir:       q.WorkDir,
		Env:       q.Env,
		StdinPipe: true,
		Stderr:    stderr,
	})
	if err != nil {
		return nil, fmt.Errorf("start claude skill listing: %w", err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = proc.Kill()
		case <-done:
		}
	}()
	stdin, stdout := proc.Stdin(), proc.Stdout()
	var commands []slashCommand
	if _, err = stdin.Write(initializeLine()); err != nil {
		err = fmt.Errorf("write initialize request: %w", err)
	} else {
		commands, err = readInitialize(stdout, listRequestID)
	}
	_ = stdin.Close()
	if err != nil && !errors.Is(err, errNoReply) {
		// Nothing the CLI does next can change the answer, and a reader
		// that stopped early would leave it blocked on a full pipe. A
		// stream that ended is a CLI already exiting, whose code is kept.
		_ = proc.Kill()
	}
	_, _ = io.Copy(io.Discard, stdout)
	exitCode, _ := proc.Wait()
	close(done)
	proc.Release()
	if err == nil {
		return commands, nil
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("no initialize reply within %s: %w", skillListTimeout, ctx.Err())
	case ctx.Err() != nil:
		err = fmt.Errorf("skill listing canceled: %w", ctx.Err())
	case errors.Is(err, errNoReply):
		err = fmt.Errorf("%w (exit %d)", err, exitCode)
	}
	if tail := stderr.String(); tail != "" {
		err = fmt.Errorf("%w: %s", err, tail)
	}
	return nil, fmt.Errorf("claude skill listing: %w", err)
}

// errNoReply is a stream that ended before the matching reply arrived.
var errNoReply = errors.New("claude exited before answering initialize")

// readInitialize reads stream-json lines until the control_response for id
// and decodes its commands. Every other line — a `system/hook_*`, another
// request's response, a type this build invented — is skipped. A line that
// is not JSON at all is an error, and so is a matching reply that is an
// error, is not a success, or carries no `commands`.
func readInitialize(r io.Reader, id string) ([]slashCommand, error) {
	sc := bufio.NewScanner(r)
	// The reply is one line holding every command plus the models, agents
	// and account: far past bufio's default 64 KiB token.
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var head struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
			} `json:"response"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			var syntax *json.SyntaxError
			if errors.As(err, &syntax) {
				return nil, fmt.Errorf("malformed line on claude's stream: %w", err)
			}
			continue // JSON of some other shape: not the reply
		}
		if head.Type != "control_response" || head.Response.RequestID != id {
			continue
		}
		var reply initializeReply
		if err := json.Unmarshal(line, &reply); err != nil {
			return nil, fmt.Errorf("decode initialize reply: %w", err)
		}
		switch resp := reply.Response; {
		case resp.Subtype == "error":
			return nil, fmt.Errorf("claude refused initialize: %s", resp.Error)
		case resp.Subtype != "success":
			return nil, fmt.Errorf("initialize reply has subtype %q", resp.Subtype)
		case resp.Response.Commands == nil:
			return nil, errors.New("initialize reply carries no commands")
		}
		return *reply.Response.Response.Commands, nil
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read claude's stream: %w", err)
	}
	return nil, errNoReply
}
