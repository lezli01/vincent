package apiclient

import "context"

// §8.6 precedence levels, as POST /v1/resolve reports them.
const (
	SourceStep     = "step"
	SourceTask     = "task"
	SourceWorkflow = "workflow"
	SourceAdapter  = "adapter"
)

// ResolveRequest asks what a workflow's agent steps would run under a
// candidate task-level override. Every field but Workflow is optional; an
// empty override triple answers "as written".
type ResolveRequest struct {
	Workflow  string `json:"workflow"`
	ProjectID *int64 `json:"project_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	// The draft's branch inputs, so the daemon can preview the resolved name
	// (task 001). Sent by the new-task form as the user types; resolution stays
	// server-side, which is the PR L decision this reuses rather than works
	// around.
	Title      string            `json:"title,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
	BaseBranch string            `json:"base_branch,omitempty"`
	BranchName string            `json:"branch_name,omitempty"`
}

// ResolvedField is one §8.6 value and the level that supplied it. Value is
// empty only for a level-4 answer the adapter itself does not name, which
// means the CLI picks at run time — display that, never a guessed model.
type ResolvedField struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// FromAdapter reports whether this field fell all the way through to §8.6
// level 4, which is the case a form has to phrase carefully.
func (f ResolvedField) FromAdapter() bool { return f.Source == SourceAdapter }

// ResolvedStep is one step's resolution. Agent, Model and Effort are nil for
// non-agent steps, which keep their index so a resolution lines up with the
// registry's own step list.
type ResolvedStep struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Agent  *ResolvedField `json:"agent"`
	Model  *ResolvedField `json:"model"`
	Effort *ResolvedField `json:"effort"`
}

// Resolution is POST /v1/resolve's answer for one workflow.
type Resolution struct {
	Workflow string         `json:"workflow"`
	Steps    []ResolvedStep `json:"steps"`
	// Branch previews the name a draft task would get. Nil when the request
	// named no project, since the project template is part of the chain.
	Branch *ResolvedBranch `json:"branch"`
}

// Branch-naming levels, as POST /v1/resolve reports them (task 001).
const (
	BranchSourceDefault = "default"
	BranchSourceConfig  = "config"
	BranchSourceProject = "project"
	BranchSourceTask    = "task"
)

// ResolvedBranch is the previewed branch name for a draft task.
type ResolvedBranch struct {
	Value  string `json:"value"`
	Source string `json:"source"`
	// Placeholder reports that Value carries a literal `<id>` where the task id
	// will go, because the id does not exist until the task is created. Display
	// it as-is: the daemon deliberately does not guess the next id.
	Placeholder bool `json:"placeholder"`
}

// Explain names the level that decided the branch, for a form's hint line.
func (b ResolvedBranch) Explain() string {
	switch b.Source {
	case BranchSourceTask:
		return "as typed"
	case BranchSourceProject:
		return "from the project template"
	case BranchSourceConfig:
		return "from config.yaml"
	default:
		return "vincent default"
	}
}

// Agents lists the distinct resolved agents across the agent steps, in step
// order. It is what a summary line names instead of "(workflow default)".
func (r Resolution) Agents() []string {
	var out []string
	for _, s := range r.Steps {
		if s.Agent == nil || s.Agent.Value == "" {
			continue
		}
		if !containsString(out, s.Agent.Value) {
			out = append(out, s.Agent.Value)
		}
	}
	return out
}

// Values lists the distinct resolved values of one field across the agent
// steps, in step order. unnamed reports whether some step resolved to a
// level-4 answer the adapter does not name — the caller phrases that, since
// "the CLI decides" reads differently per field.
func (r Resolution) Values(field func(ResolvedStep) *ResolvedField) (values []string, unnamed bool) {
	for _, s := range r.Steps {
		f := field(s)
		if f == nil {
			continue
		}
		if f.Value == "" {
			unnamed = true
			continue
		}
		if !containsString(values, f.Value) {
			values = append(values, f.Value)
		}
	}
	return values, unnamed
}

// ModelOf is the Values selector for the model field; it exists so callers
// name a field without writing a closure at every call site.
func ModelOf(s ResolvedStep) *ResolvedField { return s.Model }

// EffortOf is the Values selector for the effort field.
func EffortOf(s ResolvedStep) *ResolvedField { return s.Effort }

// Resolve applies §8.6 to every step of a workflow under the given override.
// The daemon owns the precedence; this is the only way a client learns what
// an unset override actually resolves to (T4.7).
func (c *Client) Resolve(ctx context.Context, req ResolveRequest) (Resolution, error) {
	var out Resolution
	if err := c.post(ctx, "/v1/resolve", req, &out); err != nil {
		return Resolution{}, err
	}
	return out, nil
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
