package workflow

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/lezli01/vincent/internal/config"
)

// Step types (spec §8.2).
const (
	StepAgent   = "agent"
	StepCommand = "command"
	StepManual  = "manual"
)

// Permission modes (spec §9.4).
const (
	PermissionFullAuto   = "full-auto"
	PermissionRestricted = "restricted"
)

// Input policies (spec §7.4).
const (
	InputWait = "wait"
	InputDeny = "deny"
)

// Shells a command step may pin (spec §8.3).
const (
	ShellSh   = "sh"
	ShellPwsh = "pwsh"
	ShellCmd  = "cmd"
)

// Workflow is a parsed workflow definition (spec §8.1).
type Workflow struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Defaults    Defaults `yaml:"defaults"`
	Steps       []Step   `yaml:"steps"`
}

// Defaults are the workflow-level fallbacks for step fields. Pointer fields
// distinguish "not set" (inherit the daemon default) from an explicit value.
type Defaults struct {
	Agent          string           `yaml:"agent"`
	Model          string           `yaml:"model"`
	Effort         string           `yaml:"effort"`
	PermissionMode string           `yaml:"permission_mode"`
	OnInput        string           `yaml:"on_input"`
	InputTimeout   *config.Duration `yaml:"input_timeout"`
	MaxRetries     *int             `yaml:"max_retries"`
	Timeout        *config.Duration `yaml:"timeout"`
}

// Step is one workflow step. The struct is flat across all three step types;
// fields that do not belong to a step's type are rejected by validation
// (spec §8.2), which keeps the YAML shape and its errors simple.
type Step struct {
	ID         string           `yaml:"id"`
	Name       string           `yaml:"name"`
	Type       string           `yaml:"type"`
	MaxRetries *int             `yaml:"max_retries"`
	Timeout    *config.Duration `yaml:"timeout"`

	// agent steps
	Prompt         string           `yaml:"prompt"`
	Agent          string           `yaml:"agent"`
	Model          string           `yaml:"model"`
	Effort         string           `yaml:"effort"`
	PermissionMode string           `yaml:"permission_mode"`
	OnInput        string           `yaml:"on_input"`
	InputTimeout   *config.Duration `yaml:"input_timeout"`

	// agent and command steps
	Check        string           `yaml:"check"`
	CheckTimeout *config.Duration `yaml:"check_timeout"`

	// command steps
	Run   string            `yaml:"run"`
	Shell string            `yaml:"shell"`
	Env   map[string]string `yaml:"env"`

	// manual steps
	Instructions string `yaml:"instructions"`
}

// DisplayName is the step's display name, falling back to its id (§8.2).
func (s Step) DisplayName() string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

// Options tune validation. KnownAgents is the set of registered adapter
// names; when empty the `agent` field is not checked against it (option
// catalog validation arrives with T2.11).
type Options struct {
	KnownAgents []string
}

// Error is a single validation failure, located in the source file when the
// offending node can be found (spec §8.2, T2.1).
type Error struct {
	// Path is the YAML path of the offending node, e.g. "steps[1].timeout".
	Path string `json:"path,omitempty"`
	// Line is the 1-based source line, or 0 when it could not be resolved.
	Line int `json:"line,omitempty"`
	// Message describes the problem.
	Message string `json:"message"`
}

func (e Error) String() string {
	switch {
	case e.Line > 0 && e.Path != "":
		return fmt.Sprintf("line %d: %s: %s", e.Line, e.Path, e.Message)
	case e.Line > 0:
		return fmt.Sprintf("line %d: %s", e.Line, e.Message)
	case e.Path != "":
		return fmt.Sprintf("%s: %s", e.Path, e.Message)
	default:
		return e.Message
	}
}

// Errors is the list of validation failures for one workflow file.
type Errors []Error

// Error implements the error interface, joining every failure.
func (es Errors) Error() string {
	parts := make([]string, 0, len(es))
	for _, e := range es {
		parts = append(parts, e.String())
	}
	return strings.Join(parts, "; ")
}

// Parse decodes and validates a workflow definition. Decoding is strict:
// unknown keys are errors, to catch typos (§8.2). A non-nil error is always
// of type Errors.
func Parse(src []byte, opts Options) (*Workflow, error) {
	var wf Workflow
	if err := yaml.UnmarshalWithOptions(src, &wf, yaml.DisallowUnknownField()); err != nil {
		return nil, Errors{{Line: lineOfDecodeError(err), Message: cleanDecodeError(err)}}
	}
	loc := newLocator(src)
	if errs := validate(&wf, opts, loc); len(errs) > 0 {
		return nil, errs
	}
	return &wf, nil
}

// Marshal re-encodes a workflow as YAML. It exists for `edit + retry`, which
// rewrites one step inside a task's own snapshot (spec §5.3, §6).
//
// The output is canonical rather than faithful: comments and field order
// from the original file are lost, and fields left unset are written out
// empty. That is acceptable because a snapshot is machine-owned — only Parse
// ever reads it — and validation judges a step by the values it carries, not
// by which keys are present.
func Marshal(wf *Workflow) ([]byte, error) {
	out, err := yaml.Marshal(wf)
	if err != nil {
		return nil, fmt.Errorf("encode workflow: %w", err)
	}
	return out, nil
}

// asErrors extracts the validation failures from an error returned by Parse.
func asErrors(err error, target *Errors) bool { return errors.As(err, target) }

// yamlUnmarshalLenient decodes without strict field checking, for probing a
// file that already failed validation.
func yamlUnmarshalLenient(src []byte, v any) error {
	if err := yaml.Unmarshal(src, v); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	return nil
}

// validate applies the §8.2 constraints. Every check reports its own error
// so one file surfaces all its problems in a single pass.
func validate(wf *Workflow, opts Options, loc *locator) Errors {
	var errs Errors
	add := func(path, format string, args ...any) {
		errs = append(errs, Error{Path: path, Line: loc.line(path), Message: fmt.Sprintf(format, args...)})
	}

	if wf.Name == "" {
		add("name", "name is required")
	} else if strings.ContainsAny(wf.Name, " \t/\\") {
		add("name", "name %q must not contain whitespace or path separators", wf.Name)
	}
	validateDefaults(wf, opts, add)

	if len(wf.Steps) == 0 {
		add("steps", "steps must not be empty")
	}
	seen := make(map[string]int, len(wf.Steps))
	for i, step := range wf.Steps {
		base := fmt.Sprintf("steps[%d]", i)
		switch {
		case step.ID == "":
			add(base+".id", "id is required")
		case !isSlug(step.ID):
			add(base+".id", "id %q must be a slug (lowercase letters, digits, '-', '_', '.')", step.ID)
		default:
			if prev, dup := seen[step.ID]; dup {
				add(base+".id", "duplicate step id %q (first used by steps[%d])", step.ID, prev)
			}
			seen[step.ID] = i
		}
		validateStep(step, base, opts, add)
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Line < errs[j].Line })
	return errs
}

func validateDefaults(wf *Workflow, opts Options, add func(string, string, ...any)) {
	d := wf.Defaults
	if d.Agent != "" && !knownAgent(d.Agent, opts.KnownAgents) {
		add("defaults.agent", "unknown agent %q (known: %s)", d.Agent, strings.Join(opts.KnownAgents, ", "))
	}
	if d.PermissionMode != "" && !isPermissionMode(d.PermissionMode) {
		add("defaults.permission_mode", "permission_mode must be %q or %q, got %q",
			PermissionFullAuto, PermissionRestricted, d.PermissionMode)
	}
	if d.OnInput != "" && !isInputPolicy(d.OnInput) {
		add("defaults.on_input", "on_input must be %q or %q, got %q", InputWait, InputDeny, d.OnInput)
	}
	if d.MaxRetries != nil && *d.MaxRetries < 0 {
		add("defaults.max_retries", "max_retries must not be negative, got %d", *d.MaxRetries)
	}
	if d.Timeout != nil && *d.Timeout <= 0 {
		add("defaults.timeout", "timeout must be positive, got %s", d.Timeout)
	}
	if d.InputTimeout != nil && *d.InputTimeout <= 0 {
		add("defaults.input_timeout", "input_timeout must be positive, got %s", d.InputTimeout)
	}
}

// validateStep checks one step: its type, the fields that type requires, the
// fields that do not belong to it, and every template it carries.
func validateStep(step Step, base string, opts Options, add func(string, string, ...any)) {
	switch step.Type {
	case "":
		add(base+".type", "type is required (one of %s, %s, %s)", StepAgent, StepCommand, StepManual)
	case StepAgent:
		if step.Prompt == "" {
			add(base+".prompt", "agent steps require a prompt")
		}
		if step.Agent != "" && !knownAgent(step.Agent, opts.KnownAgents) {
			add(base+".agent", "unknown agent %q (known: %s)", step.Agent, strings.Join(opts.KnownAgents, ", "))
		}
		if step.PermissionMode != "" && !isPermissionMode(step.PermissionMode) {
			add(base+".permission_mode", "permission_mode must be %q or %q, got %q",
				PermissionFullAuto, PermissionRestricted, step.PermissionMode)
		}
		if step.OnInput != "" && !isInputPolicy(step.OnInput) {
			add(base+".on_input", "on_input must be %q or %q, got %q", InputWait, InputDeny, step.OnInput)
		}
		rejectFields(step, base, add, "run", "shell", "env", "instructions")
	case StepCommand:
		if step.Run == "" {
			add(base+".run", "command steps require a run command")
		}
		if step.Shell != "" && !isShell(step.Shell) {
			add(base+".shell", "shell must be one of %s, %s, %s; got %q",
				ShellSh, ShellPwsh, ShellCmd, step.Shell)
		}
		rejectFields(step, base, add, "prompt", "agent", "model", "effort",
			"permission_mode", "on_input", "input_timeout", "instructions")
	case StepManual:
		if step.Instructions == "" {
			add(base+".instructions", "manual steps require instructions")
		}
		rejectFields(step, base, add, "prompt", "agent", "model", "effort",
			"permission_mode", "on_input", "input_timeout", "check", "check_timeout",
			"run", "shell", "env")
	default:
		add(base+".type", "unknown step type %q (one of %s, %s, %s)",
			step.Type, StepAgent, StepCommand, StepManual)
	}

	if step.MaxRetries != nil && *step.MaxRetries < 0 {
		add(base+".max_retries", "max_retries must not be negative, got %d", *step.MaxRetries)
	}
	if step.Timeout != nil && *step.Timeout <= 0 {
		add(base+".timeout", "timeout must be positive, got %s", step.Timeout)
	}
	if step.CheckTimeout != nil && *step.CheckTimeout <= 0 {
		add(base+".check_timeout", "check_timeout must be positive, got %s", step.CheckTimeout)
	}
	if step.InputTimeout != nil && *step.InputTimeout <= 0 {
		add(base+".input_timeout", "input_timeout must be positive, got %s", step.InputTimeout)
	}
	for field, text := range map[string]string{
		"prompt": step.Prompt, "run": step.Run, "check": step.Check, "instructions": step.Instructions,
	} {
		if text == "" {
			continue
		}
		if _, err := template.New(field).Parse(text); err != nil {
			add(base+"."+field, "template does not parse: %v", err)
		}
	}
}

// rejectFields reports the named fields as not allowed for this step's type.
// Strict decoding catches unknown keys; this catches keys that are known but
// belong to a different step type (§8.2).
func rejectFields(step Step, base string, add func(string, string, ...any), fields ...string) {
	set := map[string]bool{
		"prompt": step.Prompt != "", "agent": step.Agent != "", "model": step.Model != "",
		"effort": step.Effort != "", "permission_mode": step.PermissionMode != "",
		"on_input": step.OnInput != "", "input_timeout": step.InputTimeout != nil,
		"check": step.Check != "", "check_timeout": step.CheckTimeout != nil,
		"run": step.Run != "", "shell": step.Shell != "", "env": len(step.Env) > 0,
		"instructions": step.Instructions != "",
	}
	for _, f := range fields {
		if set[f] {
			add(base+"."+f, "%s is not valid on a %s step", f, step.Type)
		}
	}
}

func knownAgent(name string, known []string) bool {
	if len(known) == 0 {
		return true
	}
	for _, k := range known {
		if k == name {
			return true
		}
	}
	return false
}

func isPermissionMode(s string) bool { return s == PermissionFullAuto || s == PermissionRestricted }

func isInputPolicy(s string) bool { return s == InputWait || s == InputDeny }

func isShell(s string) bool { return s == ShellSh || s == ShellPwsh || s == ShellCmd }

// isSlug reports whether s is a step id: lowercase alphanumerics plus
// '-', '_' and '.', starting with an alphanumeric.
func isSlug(s string) bool {
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_' || r == '.') && i > 0:
		default:
			return false
		}
	}
	return s != ""
}

// locator maps YAML paths to source line numbers so semantic errors can be
// reported at the offending line, the way decode errors already are.
type locator struct {
	file *ast.File
}

func newLocator(src []byte) *locator {
	f, err := parser.ParseBytes(src, 0)
	if err != nil {
		return &locator{}
	}
	return &locator{file: f}
}

// line resolves a dotted/indexed path such as "steps[1].timeout" to its
// source line, walking up to the nearest ancestor that exists (a missing
// `prompt` key still points at its step). Returns 0 when nothing matches.
func (l *locator) line(path string) int {
	if l == nil || l.file == nil {
		return 0
	}
	for p := path; p != ""; p = parentPath(p) {
		yp, err := yaml.PathString("$." + p)
		if err != nil {
			continue
		}
		node, err := yp.FilterFile(l.file)
		if err != nil || node == nil {
			continue
		}
		if tok := node.GetToken(); tok != nil {
			return tok.Position.Line
		}
	}
	return 0
}

// decodeErrorPos matches the "[line:column]" prefix goccy puts on decode
// errors, which is how a strict-decoding failure reports its location.
var decodeErrorPos = regexp.MustCompile(`^\[(\d+):(\d+)\]\s*`)

func lineOfDecodeError(err error) int {
	m := decodeErrorPos.FindStringSubmatch(firstLine(err.Error()))
	if m == nil {
		return 0
	}
	line, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return 0
	}
	return line
}

// cleanDecodeError reduces a goccy error to its first line without the
// position prefix; the position travels in Error.Line instead.
func cleanDecodeError(err error) string {
	return decodeErrorPos.ReplaceAllString(firstLine(err.Error()), "")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// parentPath drops the last path element: "steps[1].timeout" → "steps[1]",
// "steps[1]" → "steps", "steps" → "".
func parentPath(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i]
	}
	if i := strings.LastIndex(path, "["); i > 0 {
		return path[:i]
	}
	return ""
}
