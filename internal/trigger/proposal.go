package trigger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/lezli01/vincent/internal/workflow"
)

// Proposals are how the create-trigger and update-triggers built-ins write a
// trigger file without being able to arm one (task 098 decisions 2, 3 and 5).
// An agent never writes {config_dir}/triggers/ itself: it stages full files
// in {data_dir}/trigger-proposals/{task_id}/ beside a manifest, and a command
// step runs `vincent trigger apply`, which is ApplyProposal. The rule the
// prompts state — a built-in authors a disarmed trigger and never arms one —
// is therefore enforced here, in code a prompt cannot talk its way past.
//
// The staging directory lives under the data dir rather than a worktree
// because a trigger's poll argv may carry a token (096 decision 2): the file
// must never land in a repository, and never exist only as message text.

// ProposalsDir is the directory under {data_dir} holding one staging
// directory per task.
const ProposalsDir = "trigger-proposals"

// ManifestName is the staging directory's manifest: a JSON object mapping each
// staged trigger id to the version token `vincent trigger ls --json` reported
// for its file, or Absent for a trigger that has no file yet.
const ManifestName = "manifest.json"

// Absent is the manifest's version for a new trigger.
const Absent = "absent"

// ProposalDir is one task's staging directory.
func ProposalDir(dataDir string, taskID int64) string {
	return filepath.Join(dataDir, ProposalsDir, strconv.FormatInt(taskID, 10))
}

// Switches are the three per-file values that arm a trigger (task 098
// decision 2). The fourth arming switch, `triggers.enabled`, lives in
// config.yaml, which nothing in this package writes.
type Switches struct {
	Enabled    bool
	OnFire     string
	Permission string
}

// SwitchesOf reads a valid definition's switches as written, absent as "".
func SwitchesOf(d *Definition) Switches {
	return Switches{Enabled: d.Enabled, OnFire: d.OnFire, Permission: d.Permission}
}

// ArmingChanges names the keys that go from disarmed to armed between before
// and after, in file order. A value that was already armed may be kept, and
// disarming is never named: the check exists to stop an agent adding a switch
// a human did not, not to stop it leaving one alone. A new file compares
// against the zero Switches, which is "absent".
func ArmingChanges(before, after Switches) []string {
	var keys []string
	if !before.Enabled && after.Enabled {
		keys = append(keys, "enabled")
	}
	if before.OnFire != OnFireCreate && after.OnFire == OnFireCreate {
		keys = append(keys, "on_fire")
	}
	if before.Permission != PermissionWorkflow && after.Permission == PermissionWorkflow {
		keys = append(keys, "permission")
	}
	return keys
}

// loose is what can be read from a trigger file that does not validate. An
// invalid file that still names its project must stay listable, or the
// update-triggers pass could never repair it (decision 4).
type loose struct {
	project    int64
	hasProject bool
	switches   Switches
}

// peek reads source.project and the switches without the strict decode. A
// value of the wrong type reads as absent, which for a switch is the disarmed
// side — the conservative direction for ArmingChanges' "before".
func peek(src []byte) loose {
	var doc map[string]any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return loose{}
	}
	var l loose
	if b, ok := doc["enabled"].(bool); ok {
		l.switches.Enabled = b
	}
	if s, ok := doc["on_fire"].(string); ok {
		l.switches.OnFire = s
	}
	if s, ok := doc["permission"].(string); ok {
		l.switches.Permission = s
	}
	var project any
	switch s := doc["source"].(type) {
	case map[string]any:
		project = s["project"]
	case map[any]any:
		project = s["project"]
	}
	l.project, l.hasProject = asInt64(project)
	return l
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint64:
		if n > 1<<62 {
			return 0, false
		}
		return int64(n), true
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

// Listing is one trigger file as `vincent trigger ls --json` prints it. It is
// the shape a proposal's manifest is written from: the id and the version.
type Listing struct {
	File       string          `json:"file"`
	ID         string          `json:"id"`
	Project    int64           `json:"project"`
	Version    string          `json:"version"`
	Valid      bool            `json:"valid"`
	Enabled    bool            `json:"enabled"`
	OnFire     string          `json:"on_fire"`
	Permission string          `json:"permission"`
	Errors     workflow.Errors `json:"errors"`
}

// List reads every trigger file in dir without a daemon, sorted by id. A file
// that does not validate is still listed when its source.project can be read;
// one whose project cannot be read at all, or that cannot be read, is returned
// as a problem instead. A missing directory is no triggers.
func List(dir string) ([]Listing, []error) {
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{fmt.Errorf("read triggers directory: %w", err)}
	}
	var out []Listing
	var problems []error
	for _, f := range files {
		if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ".yaml") {
			continue
		}
		path := filepath.Join(dir, f.Name())
		e, err := readEntry(path, Stem(path))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		l := Listing{File: path, ID: e.ID, Version: e.Version, Valid: e.Valid(), Errors: e.Errors}
		var sw Switches
		if e.Def != nil {
			l.Project, sw = e.Def.Source.Project, SwitchesOf(e.Def)
		} else {
			lo := peek(e.Source)
			if !lo.hasProject {
				problems = append(problems, fmt.Errorf("%s: source.project cannot be read, so the file is not listed", path))
				continue
			}
			l.Project, sw = lo.project, lo.switches
		}
		l.Enabled, l.OnFire, l.Permission = sw.Enabled, sw.OnFire, sw.Permission
		if l.Errors == nil {
			l.Errors = workflow.Errors{}
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, problems
}

// Refusal is one reason ApplyProposal wrote nothing: a staged file, the key
// at fault ("" for the file as a whole), and why.
type Refusal struct {
	File    string
	Key     string
	Message string
}

func (r Refusal) String() string {
	if r.Key == "" {
		return r.File + ": " + r.Message
	}
	return r.File + ": " + r.Key + ": " + r.Message
}

// armingText says what each arming key's change would have done.
var armingText = map[string]string{
	"enabled":    "would turn the trigger on (enabled: true)",
	"on_fire":    "would make it create running tasks (on_fire: create)",
	"permission": "would run its workflow unrestricted (permission: workflow)",
}

// ApplyProposal installs the staged files in staging through w, for the
// project whose id is project. It checks every file before writing any: a
// non-empty refusal list means nothing was written and staging is untouched.
// On success every file is written (0600, through the writer) and staging is
// removed.
//
// The checks are decision 3's, in order of what a reader wants to hear first:
// the file must validate, belong to this project, match the manifest's record
// of what was on disk, and arm nothing that was not already armed. There is no
// override. A human arms a trigger in the TUI, which asks, or in an editor.
func ApplyProposal(staging string, w *Writer, project int64) (written []string, refusals []Refusal, err error) {
	entries, err := os.ReadDir(staging)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("no proposal staged at %s", staging)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read proposal: %w", err)
	}
	refuse := func(file, key, format string, args ...any) {
		refusals = append(refusals, Refusal{File: file, Key: key, Message: fmt.Sprintf(format, args...)})
	}

	manifest := map[string]string{}
	//nolint:gosec // G304: the manifest of a staging directory the CLI derived from the data dir and a task id
	if raw, err := os.ReadFile(filepath.Join(staging, ManifestName)); err != nil {
		refuse(ManifestName, "", "cannot be read: %v", err)
	} else if err := json.Unmarshal(raw, &manifest); err != nil {
		refuse(ManifestName, "", "must be a JSON object mapping each trigger id to its version or %q: %v", Absent, err)
	}

	staged := map[string][]byte{}
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == ManifestName:
		case e.IsDir() || !strings.HasSuffix(name, ".yaml"):
			refuse(name, "", "is neither a staged trigger file (<id>.yaml) nor %s", ManifestName)
		default:
			//nolint:gosec // G304: a file listed in the staging directory
			src, err := os.ReadFile(filepath.Join(staging, name))
			if err != nil {
				refuse(name, "", "cannot be read: %v", err)
				continue
			}
			staged[strings.TrimSuffix(name, ".yaml")] = src
		}
	}
	for id := range manifest {
		if _, ok := staged[id]; !ok {
			refuse(id+".yaml", "", "is named in %s but not staged", ManifestName)
		}
	}
	if len(staged) == 0 && len(refusals) == 0 {
		// An empty manifest is update-triggers finding every trigger already
		// right: a proposal to change nothing applies as nothing.
		if err := os.RemoveAll(staging); err != nil {
			return nil, nil, fmt.Errorf("remove proposal: %w", err)
		}
		return nil, nil, nil
	}

	ids := make([]string, 0, len(staged))
	for id := range staged {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		file := id + ".yaml"
		def, errs := Parse(staged[id], id)
		if len(errs) > 0 {
			for _, e := range errs {
				refuse(file, e.Path, "%s", e.Message)
			}
			continue
		}
		if def.Source.Project != project {
			refuse(file, "source.project", "is %d, not this project's id %d", def.Source.Project, project)
		}
		recorded, ok := manifest[id]
		if !ok {
			refuse(file, "", "has no entry in %s", ManifestName)
			continue
		}
		path, err := w.Path(id)
		if err != nil {
			refuse(file, "id", "%v", err)
			continue
		}
		before, ok := checkOnDisk(path, recorded, project, func(format string, args ...any) {
			refuse(file, "", format, args...)
		})
		if !ok {
			continue
		}
		for _, key := range ArmingChanges(before, SwitchesOf(def)) {
			refuse(file, key, "%s; arming is a human's change, made in the TUI's triggers view or an editor", armingText[key])
		}
	}
	if len(refusals) > 0 {
		sort.SliceStable(refusals, func(i, j int) bool { return refusals[i].File < refusals[j].File })
		return nil, refusals, nil
	}

	for _, id := range ids {
		var path string
		if manifest[id] == Absent {
			_, err = w.Create(id, staged[id])
		} else {
			_, err = w.Replace(id, manifest[id], staged[id])
		}
		if err != nil {
			return written, nil, fmt.Errorf("%s.yaml: %w", id, err)
		}
		path, _ = w.Path(id)
		written = append(written, path)
	}
	if err := os.RemoveAll(staging); err != nil {
		return written, nil, fmt.Errorf("remove proposal: %w", err)
	}
	return written, nil, nil
}

// checkOnDisk compares the file at path with what the manifest recorded and
// returns the switches it has now — zero for a file that is still absent.
// false means a refusal was reported and the arming check cannot be judged.
func checkOnDisk(path, recorded string, project int64, refuse func(format string, args ...any)) (Switches, bool) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		if recorded != Absent {
			refuse("was recorded at version %s, but %s no longer exists", recorded, path)
			return Switches{}, false
		}
		return Switches{}, true
	}
	if err != nil {
		refuse("cannot stat %s: %v", path, err)
		return Switches{}, false
	}
	if recorded == Absent {
		refuse("was recorded as absent, but %s exists now; a proposal never overwrites a file it did not read", path)
		return Switches{}, false
	}
	//nolint:gosec // G304: FileName(id) under the triggers directory
	src, err := os.ReadFile(path)
	if err != nil {
		refuse("cannot read %s: %v", path, err)
		return Switches{}, false
	}
	if current := workflow.VersionOf(fi.ModTime().UTC().UnixNano(), src); current != recorded {
		refuse("%s changed since the proposal recorded it (version %s, now %s)", path, recorded, current)
		return Switches{}, false
	}
	// Replacing a file that belongs to another project would be writing that
	// project's trigger under this project's name.
	lo := peek(src)
	if !lo.hasProject || lo.project != project {
		refuse("%s on disk does not belong to project %d", path, project)
		return Switches{}, false
	}
	return lo.switches, true
}
