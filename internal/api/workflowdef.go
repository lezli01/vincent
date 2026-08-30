package api

import (
	"net/http"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/workflow"
)

// The definition endpoint (task 017 decisions 4, 10-12) serves one workflow's
// whole structure, which the registry list deliberately discards: the list's
// step DTO carries id, name, type and agent, and a graph needs nested steps,
// fan-out lanes, merges, guards and loop drivers.
//
// Three properties of this contract are decisions, not accidents:
//
//   - The name travels in the query string, not the path (decision 10). A
//     registry name is neither URL-safe nor unique: a file that fails to parse
//     is still listed, under a name that may be its unvalidated `name:` field
//     or its base filename, and the loser of a duplicate name is listed beside
//     the winner. The endpoint serves what Registry.Lookup serves — the
//     shadowing winner — and reports the File and Scope it came from so a
//     client can tell which entry it got.
//   - A workflow that does not parse is a 200 with findings and a null
//     definition (decision 11), the same way the list shows a broken file
//     rather than hiding it. 404 means no entry of that name at all.
//   - Steps are reported **as authored** (decision 12). Workflow defaults stay
//     in their own block and are never folded into the steps that inherit
//     them: `agent: claude` written on a step and the same value inherited
//     from defaults are different facts, which is the distinction §8.6 exists
//     to make and the one a workflow builder has to round-trip. The resolved
//     answer has its own endpoint, POST /v1/resolve.
//
// These types deliberately restate internal/workflow rather than embedding it,
// so the parser's model can change without silently changing the wire.

// workflowDefinitionResponse is GET /v1/workflows/definition. It repeats the
// list's derived fields because the graph is opened against one workflow and
// must not have to join against a list row the client may not hold.
type workflowDefinitionResponse struct {
	Name              string   `json:"name"`
	Scope             string   `json:"scope"`
	ProjectID         *int64   `json:"project_id"`
	File              string   `json:"file,omitempty"`
	Platforms         []string `json:"platforms,omitempty"`
	PlatformSupported bool     `json:"platform_supported"`
	RequiresInput     bool     `json:"requires_input"`
	// Version is the token a PATCH of this entry must carry (task 065
	// decision 4). Empty for a built-in, which has no file.
	Version  string           `json:"version,omitempty"`
	Errors   []workflow.Error `json:"errors,omitempty"`
	Warnings []workflow.Error `json:"warnings,omitempty"`
	Error    *string          `json:"error"`
	// Definition is null when the file did not parse. Its findings are in
	// Errors, which is the whole reason this is a 200.
	Definition *workflowDefinition `json:"definition"`
}

// workflowDefinition is the workflow itself, as the file declares it.
type workflowDefinition struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description,omitempty"`
	Platforms   []string                `json:"platforms,omitempty"`
	Fields      []workflowFieldResponse `json:"fields"`
	Defaults    workflowDefaults        `json:"defaults"`
	Steps       []workflowStepDef       `json:"steps"`
}

// workflowDefaults is the §8.1 defaults block, kept separate from the steps
// that inherit it (decision 12). Every field is omitempty: an absent field
// means the file set nothing and the daemon's own default applies, which is
// not the API's answer to give here.
type workflowDefaults struct {
	Agent          string  `json:"agent,omitempty"`
	Model          string  `json:"model,omitempty"`
	Effort         string  `json:"effort,omitempty"`
	PermissionMode string  `json:"permission_mode,omitempty"`
	OnInput        string  `json:"on_input,omitempty"`
	InputTimeout   *string `json:"input_timeout,omitempty"`
	MaxRetries     *int    `json:"max_retries,omitempty"`
	RetryBackoff   *string `json:"retry_backoff,omitempty"`
	Timeout        *string `json:"timeout,omitempty"`
	// Container is the §16 containerization override, absent when the
	// workflow sets none. It is on the wire because it changes where every
	// step of a task runs, which a graph editor and the workflow detail view
	// both have to be able to say (task 065).
	Container *workflowContainerDef `json:"container,omitempty"`
}

// workflowContainerDef is `defaults.container:` on the wire. Every field is a
// pointer for the reason the YAML's are: `image: ""` in a workflow means "run
// this one on the host" and must stay distinguishable from an absent key.
type workflowContainerDef struct {
	Image            *string  `json:"image,omitempty"`
	Runtime          *string  `json:"runtime,omitempty"`
	MountAgentConfig *bool    `json:"mount_agent_config,omitempty"`
	Network          *bool    `json:"network,omitempty"`
	ExtraMounts      []string `json:"extra_mounts,omitempty"`
}

func toWorkflowContainerDef(c *config.ContainerOverride) *workflowContainerDef {
	if c == nil {
		return nil
	}
	return &workflowContainerDef{
		Image:            c.Image,
		Runtime:          c.Runtime,
		MountAgentConfig: c.MountAgentConfig,
		Network:          c.Network,
		ExtraMounts:      c.ExtraMounts,
	}
}

// workflowStepDef is one step, flat across every type the way the YAML is
// (§8.2). Fields that do not belong to a step's type are absent rather than
// zero, so a client can tell a `run:` of "" from a step that has no `run:`.
//
// Name is the authored `name:` and may be empty; the display label is
// name-or-id, which every client derives the same way. The list endpoint sends
// the derived label instead, because a registry row has no use for the
// difference and a graph editor does.
type workflowStepDef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Type string `json:"type"`

	MaxRetries   *int    `json:"max_retries,omitempty"`
	RetryBackoff *string `json:"retry_backoff,omitempty"`
	Timeout      *string `json:"timeout,omitempty"`
	If           string  `json:"if,omitempty"`
	AllowFailure bool    `json:"allow_failure,omitempty"`

	// agent steps
	Prompt         string  `json:"prompt,omitempty"`
	Agent          string  `json:"agent,omitempty"`
	Model          string  `json:"model,omitempty"`
	Effort         string  `json:"effort,omitempty"`
	PermissionMode string  `json:"permission_mode,omitempty"`
	OnInput        string  `json:"on_input,omitempty"`
	InputTimeout   *string `json:"input_timeout,omitempty"`

	// agent and command steps
	Check        string  `json:"check,omitempty"`
	CheckTimeout *string `json:"check_timeout,omitempty"`

	// command steps
	Run   string            `json:"run,omitempty"`
	Shell string            `json:"shell,omitempty"`
	Env   map[string]string `json:"env,omitempty"`

	// manual steps
	Instructions string `json:"instructions,omitempty"`

	// parallel groups and loop bodies both carry their children here, the
	// way workflow.Step does — one field for "the steps inside me".
	Steps       []workflowStepDef `json:"steps,omitempty"`
	MaxParallel *int              `json:"max_parallel,omitempty"`

	// fan_out steps
	Lanes []workflowLaneDef `json:"lanes,omitempty"`
	Merge *workflowMergeDef `json:"merge,omitempty"`

	// loop steps. Exactly one of Count and ForEach is set: the driver. A
	// ForEach list is templates, not values — what it iterates is discovered
	// at run time, so a definition reader cannot know the iteration count.
	Count         *int     `json:"count,omitempty"`
	ForEach       []string `json:"for_each,omitempty"`
	MaxIterations *int     `json:"max_iterations,omitempty"`

	// include steps (§7.9). Workflow is the registry name an authored
	// include carries; ResolvedFrom is the chain of workflows a step was
	// spliced through, outermost first, and appears only in a task's
	// snapshot. Both are carried here so one DTO describes a registry entry
	// and a snapshot alike, which is what a lane's ResolvedFrom already does.
	//
	// They never appear together: expansion replaces the include with the
	// steps it resolved to, so the step carrying the name is gone by the time
	// anything carries a chain.
	Workflow     string   `json:"workflow,omitempty"`
	ResolvedFrom []string `json:"resolved_from,omitempty"`
}

// workflowLaneDef is one fan_out lane. Exactly one of Workflow and Steps is
// set in an authored file; ResolvedFrom is written only into a task's
// snapshot, never into a registry entry, and is carried here so the same DTO
// can describe both.
type workflowLaneDef struct {
	ID           string            `json:"id"`
	If           string            `json:"if,omitempty"`
	Workflow     string            `json:"workflow,omitempty"`
	ResolvedFrom string            `json:"resolved_from,omitempty"`
	Steps        []workflowStepDef `json:"steps,omitempty"`
	Fields       map[string]string `json:"fields,omitempty"`
	Agent        string            `json:"agent,omitempty"`
	Model        string            `json:"model,omitempty"`
	Effort       string            `json:"effort,omitempty"`
	Priority     *int              `json:"priority,omitempty"`
}

// workflowMergeDef is a fan_out step's join. OnConflict is as authored and so
// may be empty, which means the `block` default; Agent is a whole step,
// required by and only by `on_conflict: agent`.
type workflowMergeDef struct {
	OnConflict string           `json:"on_conflict,omitempty"`
	Agent      *workflowStepDef `json:"agent,omitempty"`
}

// handleWorkflowDefinition serves GET /v1/workflows/definition?name=&project_id=.
func (s *Server) handleWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDParam(s, w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "name is required")
		return
	}
	entry, found := s.deps.Workflows.Lookup(projectID, name)
	if !found {
		writeError(w, http.StatusNotFound, CodeNotFound, "no workflow named "+name)
		return
	}
	writeJSON(w, http.StatusOK, toWorkflowDefinitionResponse(entry))
}

func toWorkflowDefinitionResponse(e workflow.Entry) workflowDefinitionResponse {
	out := workflowDefinitionResponse{
		Name:              e.Name,
		Scope:             string(e.Scope),
		File:              e.File,
		PlatformSupported: e.RunsHere(),
		RequiresInput:     e.NeedsInputAgent(),
	}
	if e.File != "" {
		if v, err := workflow.Version(e.File); err == nil {
			out.Version = v
		}
	}
	if e.ProjectID != 0 {
		id := e.ProjectID
		out.ProjectID = &id
	}
	if len(e.Errors) > 0 {
		out.Errors = e.Errors
		msg := e.Errors.Error()
		out.Error = &msg
	}
	if len(e.Warnings) > 0 {
		out.Warnings = e.Warnings
	}
	if e.Workflow != nil {
		out.Platforms = e.Workflow.Platforms
		out.Definition = toWorkflowDefinition(e.Workflow)
	}
	return out
}

func toWorkflowDefinition(wf *workflow.Workflow) *workflowDefinition {
	return &workflowDefinition{
		Name:        wf.Name,
		Description: wf.Description,
		Platforms:   wf.Platforms,
		Fields:      toWorkflowFieldResponses(wf.Fields),
		Defaults: workflowDefaults{
			Agent:          wf.Defaults.Agent,
			Model:          wf.Defaults.Model,
			Effort:         wf.Defaults.Effort,
			PermissionMode: wf.Defaults.PermissionMode,
			OnInput:        wf.Defaults.OnInput,
			InputTimeout:   durationString(wf.Defaults.InputTimeout),
			MaxRetries:     wf.Defaults.MaxRetries,
			RetryBackoff:   durationString(wf.Defaults.RetryBackoff),
			Timeout:        durationString(wf.Defaults.Timeout),
			Container:      toWorkflowContainerDef(wf.Defaults.Container),
		},
		Steps: toWorkflowStepDefs(wf.Steps),
	}
}

func toWorkflowStepDefs(steps []workflow.Step) []workflowStepDef {
	out := make([]workflowStepDef, 0, len(steps))
	for _, st := range steps {
		out = append(out, toWorkflowStepDef(st))
	}
	return out
}

func toWorkflowStepDef(st workflow.Step) workflowStepDef {
	out := workflowStepDef{
		ID:             st.ID,
		Name:           st.Name,
		Type:           st.Type,
		MaxRetries:     st.MaxRetries,
		RetryBackoff:   durationString(st.RetryBackoff),
		Timeout:        durationString(st.Timeout),
		If:             st.If,
		AllowFailure:   st.AllowFailure,
		Prompt:         st.Prompt,
		Agent:          st.Agent,
		Model:          st.Model,
		Effort:         st.Effort,
		PermissionMode: st.PermissionMode,
		OnInput:        st.OnInput,
		InputTimeout:   durationString(st.InputTimeout),
		Check:          st.Check,
		CheckTimeout:   durationString(st.CheckTimeout),
		Run:            st.Run,
		Shell:          st.Shell,
		Env:            st.Env,
		Instructions:   st.Instructions,
		MaxParallel:    st.MaxParallel,
		Count:          st.Count,
		MaxIterations:  st.MaxIterations,
		Workflow:       st.Workflow,
		ResolvedFrom:   st.ResolvedFrom,
	}
	if len(st.Steps) > 0 {
		out.Steps = toWorkflowStepDefs(st.Steps)
	}
	if len(st.ForEach) > 0 {
		out.ForEach = []string(st.ForEach)
	}
	for _, lane := range st.Lanes {
		out.Lanes = append(out.Lanes, toWorkflowLaneDef(lane))
	}
	if st.Merge != nil {
		merge := &workflowMergeDef{OnConflict: st.Merge.OnConflict}
		if st.Merge.Agent != nil {
			agent := toWorkflowStepDef(*st.Merge.Agent)
			merge.Agent = &agent
		}
		out.Merge = merge
	}
	return out
}

func toWorkflowLaneDef(lane workflow.Lane) workflowLaneDef {
	out := workflowLaneDef{
		ID:           lane.ID,
		If:           lane.If,
		Workflow:     lane.Workflow,
		ResolvedFrom: lane.ResolvedFrom,
		Fields:       lane.Fields,
		Agent:        lane.Agent,
		Model:        lane.Model,
		Effort:       lane.Effort,
		Priority:     lane.Priority,
	}
	if len(lane.Steps) > 0 {
		out.Steps = toWorkflowStepDefs(lane.Steps)
	}
	return out
}

// durationString renders an optional duration the way §13.2 spells every
// other one — "60m", not a nanosecond count — and keeps "unset" distinct from
// "zero".
func durationString(d *config.Duration) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}
