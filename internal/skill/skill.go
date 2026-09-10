package skill

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
	"golang.org/x/mod/semver"

	"github.com/lezli01/vincent/skills"
)

// The install layout of the `skills` CLI, which is what vincent reads (§9.8).
const (
	// StoreDir is the single global store, relative to the user's home. One
	// copy lives here per skill; every agent directory is a link into it,
	// which is why there is no per-agent version to report (decision 3).
	StoreDir = ".agents"
	// skillsSubdir is the directory each root holds its skills under —
	// `~/.agents/skills/<name>` in the store, `~/.claude/skills/<name>` in an
	// agent's own root.
	skillsSubdir = "skills"
	// manifest is the one file a skill must have. Detection reads it and is
	// agnostic to whether the entry holding it is a symlink into the store
	// (the CLI's default) or a real directory (`--copy`, which is what
	// Windows needs, where creating a symlink is privileged).
	manifest = "SKILL.md"
)

// Store states a skill's copy can be in. They are the row's vocabulary and
// they are reported, never acted on: nothing here moves `vincent doctor`'s
// exit code (decision 5).
const (
	// StateAbsent: no copy on this machine.
	StateAbsent = "absent"
	// StateCurrent: the installed version is the shipped one.
	StateCurrent = "current"
	// StateOlder: the installed copy predates the one this binary ships.
	StateOlder = "older"
	// StateNewer: the installed copy is ahead of it — a downgraded binary,
	// not an up-to-date skill, and the row must not call it one.
	StateNewer = "newer"
	// StateDiffers: the two versions are not equal and at least one of them
	// is not semver, so no direction can be claimed. Both are printed
	// (decision 7).
	StateDiffers = "differs"
	// StateUnreadable: a copy exists and its SKILL.md could not be read or
	// parsed. Different from absent, and the difference is the whole point.
	StateUnreadable = "unreadable"
)

// Published is one skill this binary ships, read from its embedded front
// matter. Version is `metadata.version` — under `metadata:` because that is
// the format's extension point, and a bare top-level `version:` is not a key
// the skills format defines (decision 6).
type Published struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	// Path is the embedded path the entry came from, which is also the
	// directory name under `skills/` and the argument `skills add --skill`
	// takes.
	Path string `json:"-"`
	// Error is a front matter that did not parse. The entry is still
	// reported: a skill whose marker cannot be read is a fact worth showing,
	// not one worth hiding behind an empty list.
	Error string `json:"error,omitempty"`
}

// Link is one agent root holding this skill. Agent is the directory's name
// without its leading dot — `claude`, `cursor`, `zed` — because that is what
// is on disk; Adapter is the vincent adapter it corresponds to, empty for the
// agents vincent does not drive. Reporting those is honest rather than noisy:
// one store serves every agent on the machine.
type Link struct {
	Agent   string `json:"agent"`
	Adapter string `json:"adapter,omitempty"`
	Path    string `json:"path"`
	// Symlink distinguishes the CLI's default from `--copy`. It changes
	// nothing about detection and is reported because it is the difference
	// between a copy that follows the store and one that does not.
	Symlink bool `json:"symlink"`
}

// Status is one published skill's installation state — one row per skill, not
// one per adapter per skill (decision 3): the store holds a single copy, so a
// per-adapter version column would be identical on every row by construction.
type Status struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Shipped is `metadata.version` in the copy this binary embeds.
	Shipped string `json:"shipped_version"`
	// Installed is `metadata.version` in the copy on disk, empty when there
	// is none or it could not be read.
	Installed string `json:"installed_version,omitempty"`
	State     string `json:"state"`
	// StorePath is `~/.agents/skills/<name>`, reported whether or not it
	// exists so a user knows where to look.
	StorePath string `json:"store_path"`
	Links     []Link `json:"links"`
	// Message explains a state the fields alone cannot — an unreadable
	// manifest, a front matter with no version.
	Message string `json:"message,omitempty"`
}

// InstalledLabel is Installed for display, naming the copy that carries no
// `metadata.version` rather than rendering it as an empty string. An installed
// copy is never nameless in a row.
func (s Status) InstalledLabel() string {
	switch {
	case s.Installed != "":
		return s.Installed
	case s.State == StateAbsent:
		return ""
	default:
		return "unversioned"
	}
}

// Linked reports whether any agent on this machine holds the skill.
func (s Status) Linked() bool { return len(s.Links) > 0 }

// Agents lists the agent names this skill is linked into, in the order they
// were found (sorted).
func (s Status) Agents() []string {
	out := make([]string, 0, len(s.Links))
	for _, l := range s.Links {
		out = append(out, l.Agent)
	}
	return out
}

// Publish reads the published set out of fsys — `skills.FS` in every caller
// but a test. The enumeration is a glob and not a list in Go, which is what
// makes a second directory under `skills/` reach doctor, the command and the
// TUI with no code change (decision 10).
func Publish(fsys fs.FS) []Published {
	if fsys == nil {
		fsys = skills.FS
	}
	paths, err := fs.Glob(fsys, "*/"+manifest)
	if err != nil {
		return nil
	}
	sort.Strings(paths)
	out := make([]Published, 0, len(paths))
	for _, p := range paths {
		dir := path0(p)
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			out = append(out, Published{Name: dir, Path: dir, Error: err.Error()})
			continue
		}
		pub := parseFrontMatter(string(b))
		pub.Path = dir
		if pub.Name == "" {
			// The directory name is what `skills add --skill` takes, so it
			// is the authoritative name when the front matter has none.
			pub.Name = dir
		}
		out = append(out, pub)
	}
	return out
}

// path0 is the first element of a slash-separated embedded path. embed always
// uses forward slashes, whatever the host separator is.
func path0(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// frontMatter is the subset of the skills format vincent reads. Unknown keys
// are ignored rather than rejected: this parses a published format vincent
// does not own.
type frontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		Version string `yaml:"version"`
	} `yaml:"metadata"`
}

// parseFrontMatter reads the leading `---` block. A file with no front matter
// at all, or one whose YAML does not parse, yields an Error rather than a
// panic or a silent zero: the caller renders "unreadable", which is a state.
func parseFrontMatter(text string) Published {
	body, ok := frontMatterBlock(text)
	if !ok {
		return Published{Error: "no yaml front matter"}
	}
	var fm frontMatter
	if err := yaml.Unmarshal([]byte(body), &fm); err != nil {
		return Published{Error: "front matter does not parse: " + err.Error()}
	}
	return Published{
		Name:        strings.TrimSpace(fm.Name),
		Description: strings.TrimSpace(fm.Description),
		Version:     strings.TrimSpace(fm.Metadata.Version),
	}
}

// frontMatterBlock returns the YAML between the opening and closing `---`
// fences. CRLF is tolerated: a SKILL.md checked out on Windows has it, and a
// version marker that only parses on POSIX would be a cross-platform fault.
func frontMatterBlock(text string) (string, bool) {
	t := strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(t, "---\n") {
		return "", false
	}
	rest := t[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// Options are Detect's inputs. The zero value is what every real caller
// wants; a test injects both.
type Options struct {
	// Home is the root the store and the agent directories hang off. Empty
	// means os.UserHomeDir(), which is what makes this work on Windows,
	// where `~` is not a path.
	Home string
	// FS is where the published set is read from. Nil means skills.FS.
	FS fs.FS
}

// Detect reports every published skill's installation state. It never runs
// `npx` and never opens a network connection — it is a readdir and a handful
// of file reads, which is why it answers on a machine that could not install
// anything (decision 2).
func Detect(opts Options) []Status {
	published := Publish(opts.FS)
	home := opts.Home
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			// No home is no store and no agent roots. Every row says absent
			// with the reason, which beats an empty report.
			out := make([]Status, 0, len(published))
			for _, p := range published {
				out = append(out, Status{
					Name: p.Name, Description: p.Description, Shipped: p.Version,
					State: StateAbsent, Message: "no home directory: " + err.Error(),
				})
			}
			return out
		}
		home = h
	}
	roots := agentRoots(home)
	out := make([]Status, 0, len(published))
	for _, p := range published {
		out = append(out, detectOne(home, roots, p))
	}
	return out
}

func detectOne(home string, roots []string, p Published) Status {
	dir := p.Path
	if dir == "" {
		dir = p.Name
	}
	st := Status{
		Name:        p.Name,
		Description: p.Description,
		Shipped:     p.Version,
		StorePath:   filepath.Join(home, StoreDir, skillsSubdir, dir),
		Links:       []Link{},
	}
	if p.Error != "" {
		// The shipped copy is the one this binary embeds; if that cannot be
		// read, no comparison is possible and saying so is the whole answer.
		st.State = StateUnreadable
		st.Message = "the shipped copy could not be read: " + p.Error
		return st
	}
	for _, root := range roots {
		entry := filepath.Join(home, root, skillsSubdir, dir)
		info, err := os.Lstat(entry)
		if err != nil {
			continue
		}
		st.Links = append(st.Links, Link{
			Agent:   strings.TrimPrefix(root, "."),
			Adapter: adapterForRoot(root),
			Path:    entry,
			Symlink: info.Mode()&os.ModeSymlink != 0,
		})
	}
	installed, found, readErr := installedVersion(st.StorePath, st.Links)
	switch {
	case !found:
		st.State = StateAbsent
		return st
	case readErr != nil:
		st.State = StateUnreadable
		st.Message = readErr.Error()
		return st
	case installed == "":
		// A copy that reads fine and carries no `metadata.version` is not
		// unreadable — it is a copy from before the marker existed, which is
		// every copy installed before task 095 shipped. That is a direction
		// this can claim rather than a comparison it cannot make.
		st.State = StateOlder
		st.Message = "the installed copy carries no metadata.version, so it predates the marker"
		return st
	}
	st.Installed = installed
	st.State, st.Message = compare(p.Version, installed)
	return st
}

// installedVersion reads the copy on disk. The store is the source — there is
// one copy and every link points at it — but a link is consulted when the
// store is missing, because an agent directory holding a real `--copy` is
// still an installed skill and reporting it as absent would be false.
//
// Three answers, and the caller renders a different state for each. found is
// false when nothing is on disk. A non-nil readErr is a copy whose SKILL.md
// could not be read or parsed. An empty version with a nil error is the third:
// a manifest that reads perfectly and carries no `metadata.version`, which is
// every copy installed before that marker existed — a fact, not a failure.
func installedVersion(storePath string, links []Link) (version string, found bool, readErr error) {
	paths := make([]string, 0, 1+len(links))
	if _, err := os.Stat(storePath); err == nil {
		paths = append(paths, storePath)
	}
	for _, l := range links {
		paths = append(paths, l.Path)
	}
	if len(paths) == 0 {
		return "", false, nil
	}
	var first error
	unversioned := false
	for _, p := range paths {
		// G304: the path is a documented install location under the user's own
		// home, and the file read out of it is one this repository published.
		b, err := os.ReadFile(filepath.Join(p, manifest)) //nolint:gosec
		if err != nil {
			if first == nil {
				first = errors.New("cannot read " + filepath.Join(p, manifest))
			}
			continue
		}
		fm := parseFrontMatter(string(b))
		if fm.Error != "" {
			if first == nil {
				first = errors.New(filepath.Join(p, manifest) + ": " + fm.Error)
			}
			continue
		}
		if fm.Version == "" {
			// Keep looking: another copy may carry one, and a version is a
			// better answer than the absence of one.
			unversioned = true
			continue
		}
		return fm.Version, true, nil
	}
	if unversioned {
		return "", true, nil
	}
	return "", true, first
}

// compare is decision 7: semver where both sides parse, and an explicit
// "differs" where either does not. It never guesses a direction — a row that
// says "older" about two strings it could not order would send somebody
// updating a skill that is already ahead.
func compare(shipped, installed string) (state, message string) {
	switch shipped {
	case installed:
		return StateCurrent, ""
	case "":
		return StateDiffers, "this binary ships no metadata.version to compare against"
	}
	s, i := canonical(shipped), canonical(installed)
	if s == "" || i == "" {
		return StateDiffers, "installed " + installed + ", shipped " + shipped +
			" — not comparable as semver, so neither is called newer"
	}
	switch semver.Compare(i, s) {
	case 0:
		return StateCurrent, ""
	case -1:
		return StateOlder, ""
	default:
		return StateNewer, "this binary ships " + shipped +
			", which is older than the copy on disk"
	}
}

// canonical normalizes a version for semver, which requires a leading `v`.
// An empty answer means the string is not semver at all.
func canonical(v string) string {
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return semver.Canonical(v)
}

// agentRoots lists the dot-directories under home that could hold a skills
// directory, sorted. It is a scan rather than a table because the `skills`
// CLI serves far more agents than vincent drives, and a hard-coded list would
// either lie by omission or assert paths this repository cannot verify. The
// store itself is excluded: `~/.agents` is where the copy lives, not an agent
// that holds a link to it.
func agentRoots(home string) []string {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil
	}
	out := make([]string, 0, 8)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, ".") || name == StoreDir {
			continue
		}
		// A symlinked agent root is still an agent root, so this stats
		// through the link rather than trusting the dirent's type.
		if info, err := os.Stat(filepath.Join(home, name)); err != nil || !info.IsDir() {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
