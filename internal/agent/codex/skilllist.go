package codex

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lezli01/vincent/internal/agent"
)

// This file is codex's answer to "which skills would you load here?" (§9.1,
// §9.3, task 124.8): the app-server's own `skills/list`, asked over the same
// JSON-RPC exchange the §9.6 quota reader uses (appserver.go).
//
// It asks the CLI and never scans (task 124 decision 1), and every field it
// returns is codex's own word (decisions 8 and 17). Verified against
// codex-cli 0.154.0 and captured as testdata/app_server_skills_0.154.0.json.
// `skills/list` itself arrived in rust-v0.73.0 (openai/codex#7914), which
// every verified build postdates; there is no listing floor (decision 29), so
// a build that refuses the method fails like any probe — an ordinary error,
// never agent.ErrSkillsUnsupported.

// skillsListMethod is the request that answers with the skills codex would
// load for a set of working directories.
const skillsListMethod = "skills/list"

// The chat skills route finds this adapter by type assertion, so a rename
// that silently dropped the interface would cost a listing rather than a
// build.
var _ agent.SkillLister = (*Adapter)(nil)

// skillsListParams is `SkillsListParams` from `codex app-server
// generate-ts`. forceReload is set because the docs allow the server to
// reuse a cached result per cwd; a freshly spawned server has no cache, so
// it costs nothing and keeps the answer honest if that ever changes.
type skillsListParams struct {
	Cwds        []string `json:"cwds"`
	ForceReload bool     `json:"forceReload"`
}

// ListSkills implements agent.SkillLister (§9.3, task 124.8): one app-server
// exchange asking `skills/list` for q.WorkDir.
//
// The binary is resolved and spawned where q.Launcher says, as Start does
// for a run, with the listing's directory and environment; nil is the host
// and the daemon's environment. No login is needed — codex lists without
// one. Every failure is an ordinary error, which the caller reports as
// `list_verdict: unknown` (decision 16): a missing binary, a spawn that
// fails, a timeout, a JSON-RPC error reply, and an answer that does not
// parse or carries the wrong number of entries.
func (a *Adapter) ListSkills(ctx context.Context, q agent.SkillQuery) (agent.SkillList, error) {
	path, err := a.resolvePathWith(q.Launcher)
	if err != nil {
		return agent.SkillList{}, err
	}
	params, err := json.Marshal(skillsListParams{Cwds: []string{q.WorkDir}, ForceReload: true})
	if err != nil {
		return agent.SkillList{}, fmt.Errorf("encode %s: %w", skillsListMethod, err)
	}
	result, err := callAppServer(ctx, q.Launcher, agent.Command{Path: path, Dir: q.WorkDir, Env: q.Env}, skillsListMethod, params)
	if err != nil {
		return agent.SkillList{}, fmt.Errorf("codex %s: %w", skillsListMethod, err)
	}
	return parseSkillsList(result)
}

// skillsListResult is the part of `SkillsListResponse` this reads.
// `shortDescription`, `interface` and `dependencies` are deliberately absent:
// nothing in agent.Skill carries them, and the invocation is built from the
// name alone.
type skillsListResult struct {
	Data []struct {
		Skills []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Path        string `json:"path"`
			Scope       string `json:"scope"`
			// Enabled is a pointer so a row that does not say is kept:
			// only codex's own `enabled: false` drops one.
			Enabled  *bool   `json:"enabled"`
			PluginID *string `json:"pluginId"`
		} `json:"skills"`
		Errors []struct {
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"data"`
}

// parseSkillsList maps one `skills/list` result onto an agent.SkillList.
//
// Exactly one cwd was sent, so exactly one `data` entry is expected, and it
// is taken whatever its echoed `cwd` says (decision 33): codex may normalize
// the path it echoes, on Windows especially, and comparing it would turn a
// correct answer into a failed one. Zero or several entries are a malformed
// answer.
//
// Rows codex reports `enabled: false` are dropped (decision 32): codex will
// not load them, and its own name counting (`name_counts.rs`) excludes them,
// so dropping them keeps Invocation's `among` counting the set codex counts.
// A load error is not dropped — each `errors[]` item becomes a Problem
// (decision 17). A well-formed answer naming no skill is an empty list, not
// an error.
func parseSkillsList(result json.RawMessage) (agent.SkillList, error) {
	var r skillsListResult
	if err := json.Unmarshal(result, &r); err != nil {
		return agent.SkillList{}, fmt.Errorf("parse codex skills: %w", err)
	}
	if len(r.Data) != 1 {
		return agent.SkillList{}, fmt.Errorf("parse codex skills: %d entries for one cwd, want 1", len(r.Data))
	}
	entry := r.Data[0]
	var list agent.SkillList
	for _, s := range entry.Skills {
		if s.Enabled != nil && !*s.Enabled {
			continue
		}
		skill := agent.Skill{
			Name:        s.Name,
			Description: s.Description,
			Scope:       s.Scope,
			Path:        s.Path,
		}
		if s.PluginID != nil {
			skill.Plugin = *s.PluginID
		}
		list.Skills = append(list.Skills, skill)
	}
	for _, e := range entry.Errors {
		list.Problems = append(list.Problems, agent.SkillProblem{Path: e.Path, Message: e.Message})
	}
	return list, nil
}
