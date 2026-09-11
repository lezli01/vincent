package trigger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/workflow"
)

// The writer is the daemon's half of the triggers view's create, edit and
// delete (task 096.6), built on task 065's machinery rather than beside it:
// edits are workflow.Edit operations, so a comment or an untouched line comes
// back byte-identical, and every write carries workflow.VersionOf's token so
// a second writer — $EDITOR, or #363's built-ins writing the directory from an
// agent run — is caught with a 409 rather than overwritten.
//
// It departs from workflows in one respect, on purpose: every write is
// config.FilePerm (0600), new file or existing (decision 20). 065 decision 8
// kept an existing workflow's mode because a repository owns a project
// workflow; no repository owns a trigger (decision 8), and its argv may carry
// a secret (decision 2), so the file is owner-only the way config.yaml is.

// ErrExists reports a create whose id already has a file.
var ErrExists = errors.New("trigger already exists")

// StaleError is a write whose version token no longer matches the file:
// someone else wrote it since the client read it. Current is the token the
// file has now, which the API returns in the 409's details.
type StaleError struct{ Current string }

func (e *StaleError) Error() string {
	return "trigger file changed since it was read (current version " + e.Current + ")"
}

// InvalidError is a write refused because its result does not validate. The
// file is untouched.
type InvalidError struct{ Errors workflow.Errors }

func (e *InvalidError) Error() string { return e.Errors.Error() }

// Writer creates, edits and deletes trigger files in one directory. Writes
// serialize on its mutex, as workflow writes do per daemon, so two PATCHes
// cannot both pass the version check against the same bytes.
type Writer struct {
	mu  sync.Mutex
	dir string
}

// NewWriter returns a writer for dir, normally {config_dir}/triggers.
func NewWriter(dir string) *Writer { return &Writer{dir: dir} }

// Dir is the triggers directory.
func (w *Writer) Dir() string { return w.dir }

// Path is the file a trigger id lives in.
func (w *Writer) Path(id string) (string, error) {
	name, err := FileName(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(w.dir, name), nil
}

// Create writes a new trigger file from src, which must validate as the
// document for id. It returns the new file's version.
func (w *Writer) Create(id string, src []byte) (string, error) {
	path, err := w.Path(id)
	if err != nil {
		return "", err
	}
	if _, errs := Parse(src, id); len(errs) > 0 {
		return "", &InvalidError{Errors: errs}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%w: %s", ErrExists, id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat trigger: %w", err)
	}
	if err := os.MkdirAll(w.dir, config.DirPerm); err != nil {
		return "", fmt.Errorf("create triggers directory: %w", err)
	}
	if err := config.WriteFile(path, src); err != nil {
		return "", err
	}
	return workflow.Version(path)
}

// Patch applies ops to an existing trigger file when version still matches.
// The result must validate: a refused patch returns *InvalidError and leaves
// the file byte-identical, which is also how an edit that tried to change
// `id` away from the file name is refused.
func (w *Writer) Patch(id, version string, ops []workflow.Op) (string, error) {
	path, err := w.Path(id)
	if err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	src, current, err := w.read(path)
	if err != nil {
		return "", err
	}
	if version != current {
		return "", &StaleError{Current: current}
	}
	out, err := workflow.Edit(src, ops)
	if err != nil {
		return "", &InvalidError{Errors: workflow.Errors{{Message: err.Error()}}}
	}
	if _, errs := Parse(out, id); len(errs) > 0 {
		return "", &InvalidError{Errors: errs}
	}
	if err := config.WriteFile(path, out); err != nil {
		return "", err
	}
	return workflow.Version(path)
}

// Delete removes a trigger file when version still matches. The ledger is
// kept and the cursor goes by decision 16, both of which are the registry's
// and poller's business, not the file's.
func (w *Writer) Delete(id, version string) error {
	path, err := w.Path(id)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, current, err := w.read(path)
	if err != nil {
		return err
	}
	if version != current {
		return &StaleError{Current: current}
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete trigger: %w", err)
	}
	return nil
}

func (w *Writer) read(path string) ([]byte, string, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("%w: %s", ErrNotFound, Stem(path))
	}
	if err != nil {
		return nil, "", fmt.Errorf("stat trigger: %w", err)
	}
	//nolint:gosec // G304: path is FileName(id) under the daemon's triggers dir
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read trigger: %w", err)
	}
	return b, workflow.VersionOf(fi.ModTime().UTC().UnixNano(), b), nil
}

// StarterSpec is what the view's create prompt collects.
type StarterSpec struct {
	ID           string
	Project      int64
	PollInterval string
	Command      []string
	Workflow     string
	Title        string
}

// Starter renders the document a create writes: disabled, and with **no
// `on_fire` line**, so it means propose (decision 7) until someone writes
// otherwise and confirms it. Every absent value gets a placeholder that
// validates, so the starter is a working file the form then edits by ops.
func Starter(s StarterSpec) []byte {
	iv := s.PollInterval
	if iv == "" {
		iv = "5m"
	}
	argv := s.Command
	if len(argv) == 0 {
		argv = []string{"/path/to/poll-command"}
	}
	title := s.Title
	if title == "" {
		title = "{{ .Event.id }}"
	}
	var b strings.Builder
	b.WriteString("# vincent trigger: runs source.command on an interval and creates a task per new event.\n")
	b.WriteString("# It is inert until enabled here and triggers.enabled is on in config.yaml.\n")
	b.WriteString("id: " + workflow.RenderScalar(s.ID) + "\n")
	b.WriteString("enabled: false\n")
	b.WriteString("source:\n")
	b.WriteString("  type: " + SourceCommand + "\n")
	b.WriteString("  project: " + strconv.FormatInt(s.Project, 10) + "\n")
	b.WriteString("  poll_interval: " + workflow.RenderScalar(iv) + "\n")
	b.WriteString("  command: " + workflow.RenderList(argv) + "\n")
	b.WriteString("action:\n")
	b.WriteString("  type: " + ActionCreateTask + "\n")
	if s.Workflow != "" {
		b.WriteString("  workflow: " + workflow.RenderScalar(s.Workflow) + "\n")
	}
	b.WriteString("  title: " + workflow.RenderScalar(title) + "\n")
	return []byte(b.String())
}
