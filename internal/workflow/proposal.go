package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Proposals are how a global update-workflows run changes {config_dir}/workflows
// (task 123). No repository versions those files, so there is no branch diff
// to review: the agent stages whole rewritten files in
// {data_dir}/workflow-proposals/{task_id}/ beside a manifest, a person approves
// at a manual gate, and `vincent workflow apply` — ApplyProposal — installs
// them. It is task 098's trigger proposal with a workflow's checks in place of
// the never-arm rule: a staged file must validate, match what the manifest
// recorded on disk, keep its name, and not collide with another global
// workflow's name (decision 5).
//
// Nothing here resolves a directory. The CLI owns config.ResolveDirs and hands
// both paths in, which keeps this package a leaf of config (CLAUDE.md).

// ProposalsDir is the directory under {data_dir} holding one staging
// directory per task.
const ProposalsDir = "workflow-proposals"

// ProposalManifest is a staging directory's manifest: a JSON object mapping
// each staged file's base name to the version token its listing reported, or
// ProposalAbsent for a file that does not exist yet. internal/trigger's
// proposals use the same two names.
const ProposalManifest = "manifest.json"

// ProposalAbsent is the manifest's version for a new file.
const ProposalAbsent = "absent"

// ProposalDir is one task's staging directory.
func ProposalDir(dataDir string, taskID int64) string {
	return filepath.Join(dataDir, ProposalsDir, strconv.FormatInt(taskID, 10))
}

// GlobalListing is one global workflow file as `vincent workflow ls --global
// --json` prints it: what a proposal's manifest is written from.
type GlobalListing struct {
	File    string `json:"file"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Valid   bool   `json:"valid"`
	Errors  Errors `json:"errors"`
}

// ListGlobal reads every workflow file in dir without a daemon (task 123
// decision 3), sorted by path. A file that does not parse is still listed —
// named by fallbackName — so the update pass can repair it; one that fails
// §5.2's sourcing bounds (not a regular file, or over MaxSourceBytes) or
// cannot be read is returned as a problem instead, since there is nothing a
// proposal could record for it. A missing directory is no workflows.
func ListGlobal(dir string, opts Options) ([]GlobalListing, []error) {
	files, err := yamlFiles(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{fmt.Errorf("read global workflow directory: %w", err)}
	}
	var out []GlobalListing
	var problems []error
	for _, f := range files {
		src, reject, err := readSource(f)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("%s: %w", f.path, err))
			continue
		case reject != "":
			problems = append(problems, errors.New(reject))
			continue
		}
		fi, err := os.Stat(f.path)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", f.path, err))
			continue
		}
		l := GlobalListing{
			File:    f.path,
			Version: VersionOf(fi.ModTime().UTC().UnixNano(), src),
			Errors:  Errors{},
		}
		wf, _, perr := Parse(src, opts)
		if perr != nil {
			var errs Errors
			if !asErrors(perr, &errs) {
				errs = Errors{{Message: perr.Error()}}
			}
			l.Name, l.Errors = fallbackName(src, f.path), errs
		} else {
			l.Name, l.Valid = wf.Name, true
		}
		out = append(out, l)
	}
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

// stagedFile is one proposed file, parsed.
type stagedFile struct {
	base string // the base name it is staged and installed under
	src  []byte
	wf   *Workflow
}

// ApplyProposal installs the workflow files staged in staging into globalDir,
// validating each with opts. Every check runs before any write: a non-empty
// refusal list means nothing was written and staging is untouched (task 123
// decision 5). With check set it stops there, writes and removes nothing, and
// returns the staged files' absolute paths, sorted; that is the global run's
// relist. Otherwise it writes every file with WriteFile — atomic per file, an
// existing file's mode kept, a new one 0644 — returns the written paths and
// removes staging. An I/O failure part-way is returned with what was written
// so far, and is not rolled back.
//
// There is no override. A refusal is one of: a file that does not validate; a
// file and a manifest entry that do not pair up, or anything else in the
// directory; a file whose live copy changed since it was recorded, appeared
// while recorded absent, or vanished; a name that is not a bare base name, or
// a new file not named FileName of its name:; a rename; and a name another
// global file, or another staged file, already declares.
func ApplyProposal(staging, globalDir string, opts Options, check bool) (paths []string, refusals []Refusal, err error) {
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
	if raw, err := os.ReadFile(filepath.Join(staging, ProposalManifest)); err != nil {
		refuse(ProposalManifest, "", "cannot be read: %v", err)
	} else if err := json.Unmarshal(raw, &manifest); err != nil {
		refuse(ProposalManifest, "", "must be a JSON object mapping each staged file's base name to its version or %q: %v", ProposalAbsent, err)
	}

	var staged []stagedFile
	for _, e := range entries {
		name := e.Name()
		switch strings.ToLower(filepath.Ext(name)) {
		case ".yaml", ".yml":
		default:
			if name != ProposalManifest {
				refuse(name, "", "is neither a staged workflow file (*.yaml or *.yml) nor %s", ProposalManifest)
			}
			continue
		}
		src, reject, err := readSource(yamlFile{path: filepath.Join(staging, name)})
		switch {
		case err != nil:
			refuse(name, "", "cannot be read: %v", err)
			continue
		case reject != "":
			refuse(name, "", "%s", reject)
			continue
		}
		staged = append(staged, stagedFile{base: name, src: src})
	}
	stagedNames := make(map[string]bool, len(staged))
	for _, s := range staged {
		stagedNames[s.base] = true
	}
	for base := range manifest {
		if !stagedNames[base] {
			refuse(base, "", "is named in %s but not staged", ProposalManifest)
		}
	}

	declaredBy := map[string]string{} // a staged name: → the staged file declaring it first
	for i := range staged {
		s := &staged[i]
		wf, _, perr := Parse(s.src, opts)
		if perr != nil {
			var errs Errors
			if !asErrors(perr, &errs) {
				errs = Errors{{Message: perr.Error()}}
			}
			for _, e := range errs {
				refuse(s.base, e.Path, "%s", e.Message)
			}
			continue
		}
		s.wf = wf
		if first, dup := declaredBy[wf.Name]; dup {
			refuse(s.base, "name", "%q is also declared by the staged %s; a scope holds one workflow per name", wf.Name, first)
		} else {
			declaredBy[wf.Name] = s.base
		}
		recorded, ok := manifest[s.base]
		if !ok {
			refuse(s.base, "", "has no entry in %s", ProposalManifest)
			continue
		}
		if !bareBaseName(s.base) {
			refuse(s.base, "", "is not a bare file name, so it could land outside %s", globalDir)
			continue
		}
		checkLive(filepath.Join(globalDir, s.base), recorded, wf.Name, func(key, format string, args ...any) {
			refuse(s.base, key, format, args...)
		})
	}
	refuseTakenNames(globalDir, stagedNames, declaredBy, refuse)

	if len(refusals) > 0 {
		sort.SliceStable(refusals, func(i, j int) bool { return refusals[i].File < refusals[j].File })
		return nil, refusals, nil
	}
	sort.Slice(staged, func(i, j int) bool { return staged[i].base < staged[j].base })
	if check {
		for _, s := range staged {
			paths = append(paths, filepath.Join(staging, s.base))
		}
		return paths, nil, nil
	}

	if len(staged) > 0 {
		// Every file may be new: a registry with no global scope yet. 0750 for
		// the reason Registry.Destination gives.
		if err := os.MkdirAll(globalDir, 0o750); err != nil {
			return nil, nil, fmt.Errorf("create global workflow directory: %w", err)
		}
	}
	for _, s := range staged {
		path := filepath.Join(globalDir, s.base)
		if err := WriteFile(path, s.src); err != nil {
			return paths, nil, fmt.Errorf("%s: %w", s.base, err)
		}
		paths = append(paths, path)
	}
	if err := os.RemoveAll(staging); err != nil {
		return paths, nil, fmt.Errorf("remove proposal: %w", err)
	}
	return paths, nil, nil
}

// bareBaseName reports whether a staged name can only address a file directly
// inside the global directory: no separator of either platform, no "..", and
// nothing filepath would reduce to something else.
func bareBaseName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\:`) && !strings.Contains(name, "..") &&
		filepath.Base(name) == name
}

// checkLive compares the live file at path with what the manifest recorded.
// A new file must be named where POST /v1/workflows would put it; a replaced
// one must be the version that was read and keep the name it declares.
func checkLive(path, recorded, name string, refuse func(key, format string, args ...any)) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if recorded != ProposalAbsent {
			refuse("", "was recorded at version %s, but %s no longer exists", recorded, path)
			return
		}
		if want, err := FileName(name); err != nil {
			refuse("name", "%v", err)
		} else if want != filepath.Base(path) {
			refuse("", "is a new workflow, so it must be staged as %s, the file its name %q is written to", want, name)
		}
		return
	}
	if err != nil {
		refuse("", "cannot stat %s: %v", path, err)
		return
	}
	if recorded == ProposalAbsent {
		refuse("", "was recorded as absent, but %s exists now; a proposal never overwrites a file it did not read", path)
		return
	}
	src, reject, err := readSource(yamlFile{path: path})
	switch {
	case err != nil:
		refuse("", "cannot read %s: %v", path, err)
		return
	case reject != "":
		refuse("", "%s", reject)
		return
	}
	if current := VersionOf(fi.ModTime().UTC().UnixNano(), src); current != recorded {
		refuse("", "%s changed since the proposal recorded it (version %s, now %s)", path, recorded, current)
		return
	}
	// Read leniently, so a live file being repaired still counts; one with
	// no readable name has nothing to preserve.
	if live := DeclaredName(src); live != "" && live != name {
		refuse("name", "renames the workflow %q to %q; every task, include and trigger that names it would break", live, name)
	}
}

// refuseTakenNames refuses a staged name another live global file already
// declares — a file the proposal is not replacing (§5.2's duplicate, which
// the write endpoints refuse too). A live file that cannot be read declares
// nothing here; the listing already reported it.
func refuseTakenNames(globalDir string, replaced map[string]bool, declaredBy map[string]string, refuse func(file, key, format string, args ...any)) {
	files, err := yamlFiles(globalDir)
	if err != nil {
		return
	}
	for _, f := range files {
		if replaced[filepath.Base(f.path)] {
			continue
		}
		src, reject, err := readSource(f)
		if err != nil || reject != "" {
			continue
		}
		if base, taken := declaredBy[DeclaredName(src)]; taken {
			refuse(base, "name", "%q is already declared by %s, which this proposal does not replace", DeclaredName(src), f.path)
		}
	}
}
