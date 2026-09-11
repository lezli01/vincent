package trigger

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/lezli01/vincent/internal/workflow"
)

// SourceCommand is the only source type in 096.2; `github_issues`,
// `github_prs` (096.3) and `http` (096.5) join SourceTypes, and the served
// schema carries them to every client with no client change.
const SourceCommand = "command"

// ActionCreateTask is the only action type in 096.2; `follow_up`, `retry`
// and `cancel` arrive with 096.4.
const ActionCreateTask = "create_task"

// on_fire values (decision 7). Absent means propose.
const (
	OnFirePropose = "propose"
	OnFireCreate  = "create"
)

// permission values (decision 17). Absent means restricted.
const (
	PermissionRestricted = "restricted"
	PermissionWorkflow   = "workflow"
)

// MinPollInterval is the floor on `source.poll_interval`.
//
// One second, fixed rather than configured, in decision 13's style. The
// bound exists to stop a typo — `1ms`, a bare `5` read as nanoseconds by a
// careless author — from turning a poll command, which is a process spawn and
// usually a network call against somebody's rate limit, into a fork loop. A
// spawn a second per trigger is well within what a laptop absorbs, and it is
// what lets the m16 gate watch a seed and then a fire in seconds rather than
// minutes. A real integration polls every minute or five; a number that
// protects those authors would be a number that slows every test of this
// package down by the same factor.
const MinPollInterval = time.Second

// Definition is one trigger file, `{config_dir}/triggers/{id}.yaml`.
type Definition struct {
	// ID is the registry key, the cursor's key and the ledger's key. It must
	// equal the file's stem, which is what makes the filesystem enforce its
	// uniqueness: two files could otherwise declare one id and share a cursor
	// and a dedupe history neither of them owns.
	ID string `yaml:"id"`
	// Enabled is the per-trigger switch; absent means false (task 096,
	// Security: "off by default, both per trigger and globally").
	Enabled bool   `yaml:"enabled"`
	Source  Source `yaml:"source"`
	// Match is the cheap structural prefilter in front of If (decision 3):
	// dotted paths into the event, each with the value it must have.
	Match map[string]any `yaml:"match"`
	// If is an `if:` guard over `.Event`, with §7.7's strict true/false.
	If     string `yaml:"if"`
	Action Action `yaml:"action"`
	// OnFire is `propose` (the default, decision 7) or `create`.
	OnFire string `yaml:"on_fire"`
	// DedupeKey is a template over `.Event`; absent means the event's `id`
	// (appendix A).
	DedupeKey string `yaml:"dedupe_key"`
	Limits    Limits `yaml:"limits"`
	// Permission is `restricted` (the default, decision 12) or `workflow`,
	// which runs the workflow as written (decision 17).
	Permission string `yaml:"permission"`
}

// Source is where events come from.
type Source struct {
	Type string `yaml:"type"`
	// Project is the id of the project the action's task is created in —
	// POST /v1/tasks' `project_id`, sent verbatim. An id rather than a name
	// because the replay then needs no resolution step of its own, and the
	// form's project picker (GET /v1/projects) writes it.
	Project      int64    `yaml:"project"`
	PollInterval string   `yaml:"poll_interval"`
	Command      []string `yaml:"command"`
}

// Action is what an event that passed the filter does.
type Action struct {
	Type        string            `yaml:"type"`
	Workflow    string            `yaml:"workflow"`
	Title       string            `yaml:"title"`
	Description string            `yaml:"description"`
	Fields      map[string]string `yaml:"fields"`
	// GitHubIssue and GitHubPull are templates that must render to an issue
	// or pull-request number, or to nothing.
	GitHubIssue string `yaml:"github_issue"`
	GitHubPull  string `yaml:"github_pull"`
}

// Limits are the per-trigger bounds.
type Limits struct {
	// MaxPerHour caps `fired` deliveries in a trailing hour; 0 is no cap. An
	// event over it is recorded `rate_limited` and dropped (decision 28).
	MaxPerHour int `yaml:"max_per_hour"`
	// MaxTaskCostUSD is sent as the created task's own cost cap (decision
	// 18); 0 sends nothing.
	MaxTaskCostUSD float64 `yaml:"max_task_cost_usd"`
}

// Interval is the parsed poll interval of a valid definition.
func (d *Definition) Interval() time.Duration {
	iv, err := time.ParseDuration(d.Source.PollInterval)
	if err != nil {
		return MinPollInterval
	}
	return iv
}

// EffectiveOnFire resolves the absent default.
func (d *Definition) EffectiveOnFire() string {
	if d.OnFire == "" {
		return OnFirePropose
	}
	return d.OnFire
}

// EffectivePermission resolves the absent default.
func (d *Definition) EffectivePermission() string {
	if d.Permission == "" {
		return PermissionRestricted
	}
	return d.Permission
}

var safeID = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// ValidID reports whether id may name a trigger file: a slug, so it cannot
// address anything outside the triggers directory.
func ValidID(id string) error {
	if !safeID.MatchString(id) || strings.Contains(id, "..") {
		return fmt.Errorf("trigger id %q must be lowercase letters, digits, '-', '_' or '.', starting with a letter or digit", id)
	}
	return nil
}

// FileName is the file a trigger with this id lives in.
func FileName(id string) (string, error) {
	if err := ValidID(id); err != nil {
		return "", err
	}
	return id + ".yaml", nil
}

// Stem is the id a trigger file's name implies.
func Stem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// Parse decodes and validates a trigger document. stem is the file's stem,
// which the id must equal; "" skips that check (POST /v1/triggers/validate on
// a document with no file yet). Decoding is strict — an unknown key is an
// error, to catch typos, as §8.2 is for workflows. A non-nil error list comes
// with a nil definition.
func Parse(src []byte, stem string) (*Definition, workflow.Errors) {
	var d Definition
	if err := yaml.UnmarshalWithOptions(src, &d, yaml.DisallowUnknownField()); err != nil {
		return nil, workflow.Errors{workflow.DecodeError(err)}
	}
	errs := validate(&d, stem)
	if len(errs) > 0 {
		return nil, workflow.Locate(src, errs)
	}
	return &d, nil
}

func validate(d *Definition, stem string) workflow.Errors {
	var errs workflow.Errors
	add := func(path, format string, args ...any) {
		errs = append(errs, workflow.Error{Path: path, Message: fmt.Sprintf(format, args...)})
	}

	switch {
	case d.ID == "":
		add("id", "is required")
	case ValidID(d.ID) != nil:
		add("id", "%v", ValidID(d.ID))
	case stem != "" && d.ID != stem:
		add("id", "must match the file name %q", stem+".yaml")
	}

	switch d.Source.Type {
	case "":
		add("source.type", "is required")
	case SourceCommand:
		if d.Source.Project <= 0 {
			add("source.project", "is required: the id of the project tasks are created in")
		}
		switch iv, err := time.ParseDuration(d.Source.PollInterval); {
		case d.Source.PollInterval == "":
			add("source.poll_interval", "is required")
		case err != nil:
			add("source.poll_interval", "is not a duration: %v", err)
		case iv < MinPollInterval:
			add("source.poll_interval", "must be at least %s", MinPollInterval)
		}
		if len(d.Source.Command) == 0 || strings.TrimSpace(d.Source.Command[0]) == "" {
			add("source.command", "is required: the argv to run, executed directly and never through a shell")
		}
	default:
		add("source.type", "must be one of %s", strings.Join(SourceTypes(), ", "))
	}

	for key, want := range d.Match {
		path := "match." + key
		if strings.TrimSpace(key) == "" || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") ||
			strings.Contains(key, "..") {
			add(path, "must be a dotted path into the event, such as fields.status")
			continue
		}
		if !matchValue(want) {
			add(path, "must be a scalar or a list of scalars")
		}
	}

	checkTemplate := func(path, text string) {
		if text == "" {
			return
		}
		if _, err := template.New(path).Option("missingkey=error").Parse(text); err != nil {
			add(path, "is not a valid template: %v", err)
		}
	}
	checkTemplate("if", d.If)
	checkTemplate("dedupe_key", d.DedupeKey)

	switch d.Action.Type {
	case "":
		add("action.type", "is required")
	case ActionCreateTask:
		if strings.TrimSpace(d.Action.Title) == "" {
			add("action.title", "is required")
		}
		checkTemplate("action.workflow", d.Action.Workflow)
		checkTemplate("action.title", d.Action.Title)
		checkTemplate("action.description", d.Action.Description)
		checkTemplate("action.github_issue", d.Action.GitHubIssue)
		checkTemplate("action.github_pull", d.Action.GitHubPull)
		for k, v := range d.Action.Fields {
			checkTemplate("action.fields."+k, v)
		}
		// POST /v1/tasks refuses both, and a trigger that could only ever be
		// refused is better caught at load than in the ledger.
		if d.Action.GitHubIssue != "" && d.Action.GitHubPull != "" {
			add("action.github_pull", "cannot be combined with github_issue")
		}
	default:
		add("action.type", "must be one of %s", strings.Join(ActionTypes(), ", "))
	}

	switch d.OnFire {
	case "", OnFirePropose, OnFireCreate:
	default:
		add("on_fire", "must be %q or %q", OnFirePropose, OnFireCreate)
	}
	switch d.Permission {
	case "", PermissionRestricted, PermissionWorkflow:
	default:
		add("permission", "must be %q or %q", PermissionRestricted, PermissionWorkflow)
	}
	if d.Limits.MaxPerHour < 0 {
		add("limits.max_per_hour", "must not be negative")
	}
	if d.Limits.MaxTaskCostUSD < 0 || math.IsNaN(d.Limits.MaxTaskCostUSD) || math.IsInf(d.Limits.MaxTaskCostUSD, 0) {
		add("limits.max_task_cost_usd", "must be a non-negative number")
	}
	sortErrors(errs)
	return errs
}

// sortErrors orders errors by path so a map-driven check reports the same
// list on every run.
func sortErrors(errs workflow.Errors) {
	for i := 1; i < len(errs); i++ {
		for j := i; j > 0 && errs[j].Path < errs[j-1].Path; j-- {
			errs[j], errs[j-1] = errs[j-1], errs[j]
		}
	}
}

// matchValue reports whether v is a value `match:` can compare: a scalar, or
// a list of scalars meaning "any of these".
func matchValue(v any) bool {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if !scalar(e) {
				return false
			}
		}
		return len(t) > 0
	default:
		return scalar(v)
	}
}

func scalar(v any) bool {
	switch v.(type) {
	case string, bool, int, int64, uint64, float64, uint, int32, float32:
		return true
	}
	return false
}

// SourceTypes are the `source.type` values this build accepts.
func SourceTypes() []string { return []string{SourceCommand} }

// ActionTypes are the `action.type` values this build accepts.
func ActionTypes() []string { return []string{ActionCreateTask} }

// ErrNotFound reports an id the registry does not hold, or a file that is
// not there.
var ErrNotFound = errors.New("no such trigger")
