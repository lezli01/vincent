package trigger

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/lezli01/vincent/internal/workflow"
)

// Source types. The served schema carries each to every client as a variant,
// so a type added here reaches the form with no client change.
const (
	// SourceCommand runs an argv on an interval and reads NDJSON (096.2,
	// appendix A).
	SourceCommand = "command"
	// SourceGitHubIssues and SourceGitHubPRs diff a project's issues or pull
	// requests on the github.poll_interval tick (096.3, decisions 10 and 31D).
	SourceGitHubIssues = "github_issues"
	SourceGitHubPRs    = "github_prs"
	// SourceHTTP accepts a pushed, signed event on
	// POST /v1/triggers/{id}/events (096.5, decision 31G).
	SourceHTTP = "http"
	// SourceIssues reads vincent's own durable issue.* events (task 130.15):
	// every project has issues, GitHub remote or not, and an imported change
	// arrives as the same event a local one does, with `by: sync`. It is a
	// mapper over the events table, not a state diff, so its only state is an
	// event-id cursor.
	SourceIssues = "issues"
	// SourceSchedule is the clock: a cron expression or a fixed interval,
	// evaluated against the wall clock on the manager's one-second tick
	// (task 121). Nothing downstream of the source knows the difference.
	SourceSchedule = "schedule"
)

// Action types.
const (
	// ActionCreateTask replays POST /v1/tasks.
	ActionCreateTask = "create_task"
	// ActionFollowUp, ActionRetry and ActionCancel replay the matching §6
	// action against the task whose branch the event names (096.4, decision
	// 31C).
	ActionFollowUp = "follow_up"
	ActionRetry    = "retry"
	ActionCancel   = "cancel"
)

// TargetBranch is the one reaction target: the unarchived task in
// source.project whose branch_name equals the rendered `branch:`.
const TargetBranch = "branch"

// SignatureGitHubHMACSHA256 is `X-Hub-Signature-256`: an HMAC-SHA256 of the
// raw body, hex, prefixed `sha256=`. The set is closed and has one member
// (decision 31G); the schema is shaped so a later scheme is one more value.
const SignatureGitHubHMACSHA256 = "github_hmac_sha256"

// on_fire values (decision 7). Absent means propose.
const (
	OnFirePropose = "propose"
	OnFireCreate  = "create"
)

// overrun values (task 122): what to do with an event whose concurrency
// group already has work in flight.
const (
	// OverrunParallel is the default and the behaviour every trigger file
	// written before task 122 has: no check at all, fire regardless.
	OverrunParallel = "parallel"
	// OverrunSkip records the event `superseded` and drops it. The in-flight
	// work stands.
	OverrunSkip = "skip"
	// OverrunCancelPrevious replays §6's cancel against every in-flight task
	// in the group, then fires. The cancelled task keeps its branch and
	// worktree; the new one gets its own, and the delivery records which task
	// it superseded.
	OverrunCancelPrevious = "cancel_previous"
	// OverrunQueueCoalesce holds the event; when the group empties the
	// *newest* held event fires and the rest are recorded `superseded`.
	OverrunQueueCoalesce = "queue_coalesce"
	// OverrunQueueSerial holds the event; when the group empties the *oldest*
	// fires, and so on until the backlog drains. Nothing is dropped except at
	// the cap.
	OverrunQueueSerial = "queue_serial"
)

// Overruns are the `overrun:` values this build accepts.
func Overruns() []string {
	return []string{OverrunParallel, OverrunSkip, OverrunCancelPrevious, OverrunQueueCoalesce, OverrunQueueSerial}
}

// Queues reports whether an overrun mode holds events in the backlog.
func Queues(overrun string) bool {
	return overrun == OverrunQueueCoalesce || overrun == OverrunQueueSerial
}

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
	ID string `yaml:"id" json:"id"`
	// Enabled is the per-trigger switch; absent means false (task 096,
	// Security: "off by default, both per trigger and globally").
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Source  Source `yaml:"source" json:"source"`
	// Match is the cheap structural prefilter in front of If (decision 3):
	// dotted paths into the event, each with the value it must have.
	Match map[string]any `yaml:"match" json:"match,omitempty"`
	// If is an `if:` guard over `.Event`, with §7.7's strict true/false.
	If string `yaml:"if" json:"if,omitempty"`
	// AllowedActors is, on a GitHub source, the issue or pull request
	// *authors* whose events may pass (decision 31F). A state diff has no
	// actor (decision 10), so the author is the only identity there is — on a
	// public repository, the field an outsider controls, which is exactly why
	// a trigger that can match an event an outsider causes must name it.
	AllowedActors []string `yaml:"allowed_actors" json:"allowed_actors,omitempty"`
	Action        Action   `yaml:"action" json:"action"`
	// OnFire is `propose` (the default, decision 7) or `create`.
	OnFire string `yaml:"on_fire" json:"on_fire,omitempty"`
	// DedupeKey is a template over `.Event`; absent means the event's `id`
	// (appendix A).
	DedupeKey string `yaml:"dedupe_key" json:"dedupe_key,omitempty"`
	// Overrun is what happens to an event whose concurrency group already has
	// work in flight (task 122); absent means `parallel`, which is no check.
	Overrun string `yaml:"overrun" json:"overrun,omitempty"`
	// ConcurrencyKey is a template over `.Event` naming the group, absent
	// meaning the trigger id for a create_task and the resolved target task
	// for a reaction. It is deliberately not DedupeKey: for the motivating
	// case — a ticket saved four times in a minute — the dedupe key is per
	// modification while the group is the ticket, so collapsing the two would
	// disarm the feature on exactly the case it exists for. It carries
	// dedupe_key's standing warning: changing it on a live trigger re-groups
	// events already in flight.
	ConcurrencyKey string `yaml:"concurrency_key" json:"concurrency_key,omitempty"`
	Limits         Limits `yaml:"limits" json:"limits"`
	// Permission is `restricted` (the default, decision 12) or `workflow`,
	// which runs the workflow as written (decision 17).
	Permission string `yaml:"permission" json:"permission,omitempty"`
}

// Source is where events come from.
type Source struct {
	Type string `yaml:"type" json:"type"`
	// Project is the id of the project the action's task is created in, or
	// whose branches a reaction resolves against — POST /v1/tasks'
	// `project_id`, sent verbatim. An id rather than a name because the
	// replay then needs no resolution step of its own, and the form's project
	// picker (GET /v1/projects) writes it.
	Project      int64    `yaml:"project" json:"project"`
	PollInterval string   `yaml:"poll_interval" json:"poll_interval,omitempty"`
	Command      []string `yaml:"command" json:"command,omitempty"`
	// Cron and Every are a `type: schedule` source's position, exactly one
	// of them set (task 121). Cron is the five-field grammar CronGrammar
	// states; Every is a duration counted from the anchor arming stored.
	Cron  string `yaml:"cron" json:"cron,omitempty"`
	Every string `yaml:"every" json:"every,omitempty"`
	// Timezone is the IANA zone a cron expression is read in, and the zone
	// `.Event`'s broken-out parts are in. Absent means the daemon host's.
	Timezone string `yaml:"timezone" json:"timezone,omitempty"`
	// Signature is a `type: http` source's verification (decision 31G).
	Signature *Signature `yaml:"signature" json:"signature,omitempty"`
}

// Signature is how a pushed event proves it came from the configured sender.
type Signature struct {
	Scheme string `yaml:"scheme" json:"scheme"`
	// SecretEnv names the variable in the daemon's environment that holds the
	// shared secret. The secret itself is never in the file (§2).
	SecretEnv string `yaml:"secret_env" json:"secret_env"`
}

// Action is what an event that passed the filter does.
type Action struct {
	Type        string            `yaml:"type" json:"type"`
	Workflow    string            `yaml:"workflow" json:"workflow,omitempty"`
	Title       string            `yaml:"title" json:"title,omitempty"`
	Description string            `yaml:"description" json:"description,omitempty"`
	Fields      map[string]string `yaml:"fields" json:"fields,omitempty"`
	// Issue is a template that must render to a vincent issue id, or to
	// nothing; it is replayed as POST /v1/tasks' `issue_id` (task 130.15).
	Issue string `yaml:"issue" json:"issue,omitempty"`
	// GitHubPull is a template that must render to a pull-request number, or
	// to nothing. There is no github_issue beside it: task 130 decision 7
	// removed it, so a file that still carries one fails strict decoding,
	// stays an invalid entry and keeps its cursor until it is edited to
	// `issue:` (task 130.11).
	GitHubPull string `yaml:"github_pull" json:"github_pull,omitempty"`
	// Target and Branch pick a reaction's task (decision 31C).
	Target string `yaml:"target" json:"target,omitempty"`
	Branch string `yaml:"branch" json:"branch,omitempty"`
	// Prompt is a follow_up's prompt, or a retry's prompt override.
	Prompt string `yaml:"prompt" json:"prompt,omitempty"`
	// MergeBack makes a create_task with an issue a side task (task
	// 134.15): its own worktree cut from the issue's main branch and merged
	// back afterwards, rather than a main task queued behind the issue's
	// main line. It is replayed as POST /v1/tasks' `merge_back`, which keeps
	// its 400 for an issue with no main branch yet, so such a delivery fails.
	MergeBack *ActionMergeBack `yaml:"merge_back" json:"merge_back,omitempty"`
}

// ActionMergeBack is a create_task action's `merge_back:` mapping.
type ActionMergeBack struct {
	// OnConflict is who resolves a conflicting merge-back: MergeBackBlock
	// (a human, the default when empty) or MergeBackAgent.
	OnConflict string `yaml:"on_conflict" json:"on_conflict,omitempty"`
}

// The `merge_back.on_conflict` values, spelled as POST /v1/tasks takes them.
const (
	MergeBackBlock = "block"
	MergeBackAgent = "agent"
)

// EffectiveOnConflict is OnConflict with the empty default resolved.
func (m *ActionMergeBack) EffectiveOnConflict() string {
	if m.OnConflict == "" {
		return MergeBackBlock
	}
	return m.OnConflict
}

// Limits are the per-trigger bounds.
type Limits struct {
	// MaxPerHour caps `fired` deliveries in a trailing hour; 0 is no cap. An
	// event over it is recorded `rate_limited` and dropped (decision 28).
	MaxPerHour int `yaml:"max_per_hour" json:"max_per_hour,omitempty"`
	// MaxTaskCostUSD is sent as the created task's own cost cap (decision
	// 18); 0 sends nothing.
	MaxTaskCostUSD float64 `yaml:"max_task_cost_usd" json:"max_task_cost_usd,omitempty"`
}

// Interval is the parsed poll interval of a valid command definition.
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

// EffectiveOverrun resolves the absent default.
func (d *Definition) EffectiveOverrun() string {
	if d.Overrun == "" {
		return OverrunParallel
	}
	return d.Overrun
}

// EffectiveConcurrencyKey resolves the absent default for a create_task: the
// trigger id, so an author who sets only `overrun:` gets one at a time per
// trigger. A reaction's default is the task its branch resolved to, which is
// not known here — fire.go supplies it once the target exists (decision 2).
func (d *Definition) EffectiveConcurrencyKey() string {
	if d.ConcurrencyKey == "" {
		return d.ID
	}
	return d.ConcurrencyKey
}

// EffectivePermission resolves the absent default.
func (d *Definition) EffectivePermission() string {
	if d.Permission == "" {
		return PermissionRestricted
	}
	return d.Permission
}

// Polls reports whether the source is one the poller runs on its own
// interval. GitHub sources ride the reconciler's tick, `http` has no poll,
// and a schedule rides the manager's schedule tick.
func (d *Definition) Polls() bool { return d.Source.Type == SourceCommand }

// IsSchedule reports whether the source is the clock.
func (d *Definition) IsSchedule() bool { return d.Source.Type == SourceSchedule }

// IsGitHub reports whether the source is one of the two GitHub state diffs.
func (d *Definition) IsGitHub() bool {
	return d.Source.Type == SourceGitHubIssues || d.Source.Type == SourceGitHubPRs
}

// IsIssues reports whether the source is vincent's own issue events.
func (d *Definition) IsIssues() bool { return d.Source.Type == SourceIssues }

// hasAuthor reports whether the source's events carry an issue or pull
// request author that allowed_actors can be matched against.
func (d *Definition) hasAuthor() bool { return d.IsGitHub() || d.IsIssues() }

var safeID = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	// refuse reports a key the chosen variant does not take. A key that is
	// set and ignored is a control that does not control, which is worse than
	// an error in front of the author.
	refuse := func(path string, set bool, why string) {
		if set {
			add(path, "is not allowed here: %s", why)
		}
	}

	switch {
	case d.ID == "":
		add("id", "is required")
	case ValidID(d.ID) != nil:
		add("id", "%v", ValidID(d.ID))
	case stem != "" && d.ID != stem:
		add("id", "must match the file name %q", stem+".yaml")
	}

	validateSource(d, add, refuse)

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
	validateGitHubTrust(d, add, refuse)

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
	checkTemplate("concurrency_key", d.ConcurrencyKey)

	validateAction(d, add, refuse, checkTemplate)

	switch d.OnFire {
	case "", OnFirePropose, OnFireCreate:
	default:
		add("on_fire", "must be %q or %q", OnFirePropose, OnFireCreate)
	}
	switch d.Overrun {
	case "", OverrunParallel, OverrunSkip, OverrunCancelPrevious, OverrunQueueCoalesce, OverrunQueueSerial:
	default:
		add("overrun", "must be one of %s", strings.Join(Overruns(), ", "))
	}
	// A concurrency_key with no overrun names a group nothing consults: a
	// control that does not control, which refuse() elsewhere already treats
	// as worse than an error in front of the author.
	refuse("concurrency_key", d.ConcurrencyKey != "" && d.EffectiveOverrun() == OverrunParallel,
		"it names a group only overrun: consults; set overrun to something other than "+OverrunParallel)
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

type addFunc func(path, format string, args ...any)

type refuseFunc func(path string, set bool, why string)

func validateSource(d *Definition, add addFunc, refuse refuseFunc) {
	s := d.Source
	switch s.Type {
	case "":
		add("source.type", "is required")
		return
	case SourceCommand, SourceGitHubIssues, SourceGitHubPRs, SourceIssues, SourceHTTP, SourceSchedule:
	default:
		add("source.type", "must be one of %s", strings.Join(SourceTypes(), ", "))
		return
	}
	if s.Project <= 0 {
		add("source.project", "is required: the id of the project tasks are created in")
	}
	switch s.Type {
	case SourceCommand:
		switch iv, err := time.ParseDuration(s.PollInterval); {
		case s.PollInterval == "":
			add("source.poll_interval", "is required")
		case err != nil:
			add("source.poll_interval", "is not a duration: %v", err)
		case iv < MinPollInterval:
			add("source.poll_interval", "must be at least %s", MinPollInterval)
		}
		if len(s.Command) == 0 || strings.TrimSpace(s.Command[0]) == "" {
			add("source.command", "is required: the argv to run, executed directly and never through a shell")
		}
		refuse("source.signature", s.Signature != nil, "only a type: http source is signed")
	case SourceGitHubIssues, SourceGitHubPRs:
		// One listing per project per github.poll_interval tick, shared by
		// every trigger on it (decision 31D): a per-trigger interval would be
		// a per-trigger listing, and API use would grow with trigger count.
		refuse("source.poll_interval", s.PollInterval != "",
			"GitHub sources are judged on the github.poll_interval tick in config.yaml")
		refuse("source.command", len(s.Command) > 0, "a GitHub source runs no command")
		refuse("source.signature", s.Signature != nil, "only a type: http source is signed")
	case SourceIssues:
		// Woken by the commit of each issue.* event, so there is no interval
		// to set (task 130.15 decision 7).
		refuse("source.poll_interval", s.PollInterval != "",
			"a type: issues source is woken by each issue event, never on a poll interval")
		refuse("source.command", len(s.Command) > 0, "a type: issues source runs no command")
		refuse("source.signature", s.Signature != nil, "only a type: http source is signed")
	case SourceSchedule:
		refuse("source.poll_interval", s.PollInterval != "",
			"a type: schedule source is due on its own cron or every, never on a poll interval")
		refuse("source.command", len(s.Command) > 0, "a type: schedule source runs no command")
		refuse("source.signature", s.Signature != nil, "only a type: http source is signed")
		validateSchedule(s, add)
	case SourceHTTP:
		refuse("source.poll_interval", s.PollInterval != "", "a type: http source is pushed, never polled")
		refuse("source.command", len(s.Command) > 0, "a type: http source runs no command")
		if s.Signature == nil {
			add("source.signature", "is required: a pushed event must be signed")
		} else {
			switch s.Signature.Scheme {
			case "":
				add("source.signature.scheme", "is required")
			case SignatureGitHubHMACSHA256:
			default:
				add("source.signature.scheme", "must be one of %s", strings.Join(SignatureSchemes(), ", "))
			}
			switch {
			case s.Signature.SecretEnv == "":
				add("source.signature.secret_env", "is required: the environment variable holding the secret")
			case !envName.MatchString(s.Signature.SecretEnv):
				add("source.signature.secret_env", "must be an environment variable name")
			}
		}
	}
	if !d.IsSchedule() {
		const why = "only a type: schedule source has a clock"
		refuse("source.cron", s.Cron != "", why)
		refuse("source.every", s.Every != "", why)
		refuse("source.timezone", s.Timezone != "", why)
	}
	if !d.hasAuthor() {
		refuse("allowed_actors", len(d.AllowedActors) > 0,
			"only a GitHub or issues source has an author to match; a command or http event carries no identity vincent can verify")
	}
}

// validateSchedule refuses a `type: schedule` source's clock field by field,
// each at the path a form renders it against. ParseSchedule refuses the same
// documents with one error; this is the same rules with the paths.
func validateSchedule(s Source, add addFunc) {
	cronOK := false
	switch {
	case s.Cron != "" && s.Every != "":
		add("source.every", "cannot be combined with cron: a schedule is one or the other")
	case s.Cron == "" && s.Every == "":
		add("source.cron", "is required unless every is set: %s", CronGrammar)
	case s.Every != "":
		switch iv, err := time.ParseDuration(s.Every); {
		case err != nil:
			add("source.every", "is not a duration: %v", err)
		case iv < MinPollInterval:
			add("source.every", "must be at least %s", MinPollInterval)
		}
	default:
		cronOK = parseCronOK(s.Cron, add)
	}
	// An unknown zone refuses at load rather than falling back to UTC
	// silently: a trigger that fires at the wrong hour every day is worse
	// than one that refuses to load.
	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			add("source.timezone", "is not a known IANA time zone: %v", err)
			return
		}
	}
	// The fields parse and the whole clock still has to strike: an expression
	// no calendar satisfies — 31 April — is caught by building it.
	if cronOK {
		if _, err := ParseSchedule(s); err != nil {
			add("source.cron", "%v", err)
		}
	}
}

func parseCronOK(expr string, add addFunc) bool {
	if _, err := parseCron(expr); err != nil {
		add("source.cron", "%v", err)
		return false
	}
	return true
}

// githubEvents are the `action` values each GitHub source synthesizes, and
// the `type: issues` source's, which are github_issues' minus `assigned`: a
// vincent issue has no assignee (task 130.15 decision 4).
var githubEvents = map[string][]string{
	SourceGitHubIssues: {"opened", "closed", "reopened", "labeled", "unlabeled", "assigned"},
	SourceGitHubPRs:    {"opened", "ready_for_review", "review_requested", "closed", "merged"},
	SourceIssues:       {"opened", "closed", "reopened", "labeled", "unlabeled", ActionLaneChanged},
}

// ActionLaneChanged is the `type: issues` action for an issue's board lane
// moving (task 134.7), and ByTask the `by` a move a task caused carries.
const (
	ActionLaneChanged = "lane_changed"
	ByTask            = "task"
)

// optInEvents are the events a source synthesizes only for a trigger whose
// match.action names them (task 134.7 decision 3). An action-less trigger
// matches defaultEvents, which leaves them out — so arming no trigger that
// loaded before lane_changed existed changes what it fires on.
var optInEvents = map[string]map[string]bool{
	SourceIssues: {ActionLaneChanged: true},
}

// defaultEvents are the events a trigger with no match.action can match.
func defaultEvents(sourceType string) []string {
	var out []string
	for _, e := range githubEvents[sourceType] {
		if !optInEvents[sourceType][e] {
			out = append(out, e)
		}
	}
	return out
}

// wantsAction reports whether d can see an event of this action at all: an
// opt-in action only when match.action names it.
func (d *Definition) wantsAction(action string) bool {
	if !optInEvents[d.Source.Type][action] {
		return true
	}
	want, ok := d.Match["action"]
	return ok && matchIncludes(want, action)
}

// trustedEvents are the events whose triggering change an outsider cannot
// make on a public repository (decision 31F), each confirmed against
// GitHub's repository role table: applying or removing a label and assigning
// need triage, and merging needs write. Everything else is untrusted —
// opening and reopening are the author's; so is closing, because an author
// may close their own issue or pull request; marking a draft ready is the
// author's; and a review request, although setting one by hand needs triage,
// is also made automatically by CODEOWNERS on a pull request an outsider
// opened, so its reviewer field is one an outsider can cause.
//
// On `type: issues` the same table is read for `by: sync` events only, since
// those are GitHub's changes arriving through the importer; a `human` or
// `agent` change was made on this machine and is trusted (task 130.15
// decision 1), as is a `task` lane move. A sync-delivered lane_changed is
// untrusted: it comes only from a GitHub close or reopen, which an outsider
// can make exactly as they make closed and reopened (task 134.7 decision 4).
var trustedEvents = map[string]map[string]bool{
	SourceGitHubIssues: {"labeled": true, "unlabeled": true, "assigned": true},
	SourceGitHubPRs:    {"merged": true},
	SourceIssues:       {"labeled": true, "unlabeled": true},
}

// GitHubEvents returns the events a GitHub source type synthesizes.
func GitHubEvents(sourceType string) []string { return slices.Clone(githubEvents[sourceType]) }

// TrustedGitHubEvent reports whether an event of a GitHub source is trusted.
func TrustedGitHubEvent(sourceType, action string) bool { return trustedEvents[sourceType][action] }

// validateGitHubTrust refuses, at load, a GitHub trigger that can match an
// untrusted event and names no allowed_actors (decision 31F). "Can match" is
// read off `match.action`: absent, it matches every event the source has
// except the opt-in ones (defaultEvents).
//
// A `type: issues` trigger is held to it too, but only for the events sync
// delivers (task 130.15 decision 1): one whose `match.by` leaves out `sync`
// can never see an outsider's change, and needs no allowlist.
func validateGitHubTrust(d *Definition, add addFunc, _ refuseFunc) {
	if !d.hasAuthor() {
		return
	}
	known := githubEvents[d.Source.Type]
	if d.IsIssues() {
		if want, ok := d.Match["action"]; ok && matchIncludes(want, "assigned") {
			add("match.action", "%q is not an event %s synthesizes: a vincent issue has no assignee (%s)",
				"assigned", SourceIssues, strings.Join(known, ", "))
			return
		}
	}
	actions := defaultEvents(d.Source.Type)
	if want, ok := d.Match["action"]; ok {
		actions = nil
		vals := []any{want}
		if l, isList := want.([]any); isList {
			vals = l
		}
		for _, v := range vals {
			a := scalarString(v)
			if !slices.Contains(known, a) {
				add("match.action", "%q is not an event %s synthesizes (%s)",
					a, d.Source.Type, strings.Join(known, ", "))
				continue
			}
			actions = append(actions, a)
		}
	}
	if len(d.AllowedActors) > 0 {
		return
	}
	if d.IsIssues() {
		if want, ok := d.Match["by"]; ok && !matchIncludes(want, "sync") {
			return
		}
	}
	for _, a := range actions {
		if !trustedEvents[d.Source.Type][a] {
			if d.IsIssues() {
				add("allowed_actors", "is required: this trigger can match %q arriving by sync from GitHub, "+
					"whose author an outsider controls on a public repository; list the authors to accept, "+
					"match only %s, or leave sync out of match.by",
					a, strings.Join(trustedList(d.Source.Type), ", "))
				return
			}
			add("allowed_actors", "is required: this trigger can match %q, whose author an outsider "+
				"controls on a public repository; list the authors to accept, or match only %s",
				a, strings.Join(trustedList(d.Source.Type), ", "))
			return
		}
	}
}

// matchIncludes reports whether a `match:` value — a scalar or a list of
// them — names v.
func matchIncludes(want any, v string) bool {
	vals := []any{want}
	if l, isList := want.([]any); isList {
		vals = l
	}
	for _, w := range vals {
		if scalarString(w) == v {
			return true
		}
	}
	return false
}

func trustedList(sourceType string) []string {
	var out []string
	for _, e := range githubEvents[sourceType] {
		if trustedEvents[sourceType][e] {
			out = append(out, e)
		}
	}
	return out
}

func validateAction(d *Definition, add addFunc, refuse refuseFunc, checkTemplate func(path, text string)) {
	a := d.Action
	switch a.Type {
	case "":
		add("action.type", "is required")
		return
	case ActionCreateTask:
		if strings.TrimSpace(a.Title) == "" {
			add("action.title", "is required")
		}
		checkTemplate("action.workflow", a.Workflow)
		checkTemplate("action.title", a.Title)
		checkTemplate("action.description", a.Description)
		checkTemplate("action.issue", a.Issue)
		checkTemplate("action.github_pull", a.GitHubPull)
		for k, v := range a.Fields {
			checkTemplate("action.fields."+k, v)
		}
		// POST /v1/tasks refuses both, and a trigger that could only ever be
		// refused is better caught at load than in the ledger.
		if a.Issue != "" && a.GitHubPull != "" {
			add("action.issue", "cannot be combined with github_pull")
		}
		if a.MergeBack != nil {
			// POST /v1/tasks 400s merge_back without issue_id: a side task
			// is merged back into an issue's main branch, so it needs one.
			if a.Issue == "" {
				add("action.merge_back", "needs action.issue: a side task is merged back into its issue's main branch")
			}
			switch a.MergeBack.OnConflict {
			case "", MergeBackBlock, MergeBackAgent:
			default:
				add("action.merge_back.on_conflict", "must be %q or %q", MergeBackBlock, MergeBackAgent)
			}
		}
		const why = "only a follow_up, retry or cancel names a target"
		refuse("action.target", a.Target != "", why)
		refuse("action.branch", a.Branch != "", why)
		refuse("action.prompt", a.Prompt != "", "a created task's work is its workflow and description")
	case ActionFollowUp, ActionRetry, ActionCancel:
		switch a.Target {
		case "":
			add("action.target", "is required: %q", TargetBranch)
		case TargetBranch:
		default:
			add("action.target", "must be %q", TargetBranch)
		}
		if strings.TrimSpace(a.Branch) == "" {
			add("action.branch", "is required: a template rendering the branch whose task this acts on")
		}
		checkTemplate("action.branch", a.Branch)
		switch a.Type {
		case ActionFollowUp:
			if strings.TrimSpace(a.Prompt) == "" {
				add("action.prompt", "is required: what the follow-up run is told to do")
			}
			checkTemplate("action.prompt", a.Prompt)
		case ActionRetry:
			checkTemplate("action.prompt", a.Prompt)
		case ActionCancel:
			refuse("action.prompt", a.Prompt != "", "a cancel runs nothing")
			// Decision 31C: cancel has no paused form, so the propose default
			// cannot be honoured; the author must write the unattended
			// opt-in explicitly, which decision 7 makes the only way to get it.
			if d.OnFire != OnFireCreate {
				add("on_fire", "must be %q for a cancel action: there is no held cancel to propose", OnFireCreate)
			}
		}
		const why = "only a create_task creates a task"
		refuse("action.workflow", a.Workflow != "", why)
		refuse("action.title", a.Title != "", why)
		refuse("action.description", a.Description != "", why)
		refuse("action.fields", len(a.Fields) > 0, why)
		refuse("action.issue", a.Issue != "", why)
		refuse("action.github_pull", a.GitHubPull != "", why)
		refuse("action.merge_back", a.MergeBack != nil, why)
		refuse("permission", d.Permission != "", "the restricted clamp is set when a task is created")
		refuse("limits.max_task_cost_usd", d.Limits.MaxTaskCostUSD != 0, "a task's cost cap is set when it is created")
	default:
		add("action.type", "must be one of %s", strings.Join(ActionTypes(), ", "))
	}
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
func SourceTypes() []string {
	return []string{SourceCommand, SourceGitHubIssues, SourceGitHubPRs, SourceIssues, SourceHTTP, SourceSchedule}
}

// ActionTypes are the `action.type` values this build accepts.
func ActionTypes() []string {
	return []string{ActionCreateTask, ActionFollowUp, ActionRetry, ActionCancel}
}

// SignatureSchemes are the `source.signature.scheme` values this build
// accepts.
func SignatureSchemes() []string { return []string{SignatureGitHubHMACSHA256} }

// ErrNotFound reports an id the registry does not hold, or a file that is
// not there.
var ErrNotFound = errors.New("no such trigger")

// Deprecations are the warnings a valid definition earns without being
// refused: a file that uses one still loads, arms and fires, so no user file
// is disarmed by an upgrade (task 130.15 decision 6). `vincent trigger
// apply` and `vincent trigger validate` print them.
func Deprecations(d *Definition) []string {
	if d == nil || d.Source.Type != SourceGitHubIssues {
		return nil
	}
	return []string{"source.type: " + SourceGitHubIssues + " is deprecated: use " + SourceIssues +
		", which reads the project's imported issues (by: sync) beside its local ones, and link the task with " +
		"action.issue: '{{ .Event.issue_id }}'; " + SourceGitHubIssues + " keeps firing until a later release removes it"}
}
