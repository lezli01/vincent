package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/trigger"
	"github.com/lezli01/vincent/internal/workflow"
)

// The daemon-free trigger verbs (task 098): validate, ls and apply, run through
// the real cobra tree against VINCENT_CONFIG_DIR / VINCENT_DATA_DIR.

type triggerDirs struct{ config, data string }

func newTriggerDirs(t *testing.T) triggerDirs {
	t.Helper()
	d := triggerDirs{config: t.TempDir(), data: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(d.config, "triggers"), 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d triggerDirs) run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	t.Setenv(config.EnvConfigDir, d.config)
	t.Setenv(config.EnvDataDir, d.data)
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SilenceErrors = true
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"trigger"}, args...))
	code = asExitCode(root.ExecuteContext(context.Background()))
	return out.String(), errOut.String(), code
}

func (d triggerDirs) write(t *testing.T, path, doc string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func authorDoc(id string, project int64, extra string) string {
	return "id: " + id + "\n" +
		"enabled: false\n" +
		"source:\n" +
		"  type: command\n" +
		"  project: " + strconv.FormatInt(project, 10) + "\n" +
		"  poll_interval: 5m\n" +
		"  command: [\"never-run\"]\n" +
		"action:\n" +
		"  type: create_task\n" +
		"  title: \"{{ .Event.id }}\"\n" + extra
}

// validate's verdict is POST /v1/triggers/validate's, which is exactly
// trigger.Parse (096 decision 29): one table through both, compared finding
// by finding, so the offline validator cannot drift into a second opinion.
func TestTriggerValidateAgreesWithTheAPI(t *testing.T) {
	s := api.New(api.Deps{
		Token:       "t",
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	handler := s.Handler()

	d := newTriggerDirs(t)
	for _, tc := range []struct {
		name, doc string
	}{
		{"valid", authorDoc("shared", 3, "")},
		{"unknown key", authorDoc("shared", 3, "container: {}\n")},
		{"id is not the file name", authorDoc("other", 3, "")},
		{"untrusted github event without allowed_actors", "id: shared\nsource:\n  type: github_issues\n  project: 3\n" +
			"action:\n  type: create_task\n  title: x\n"},
		{"cancel reaction that is not on_fire create", "id: shared\nsource:\n  type: http\n  project: 3\n" +
			"  signature: {scheme: github_hmac_sha256, secret_env: S}\n" +
			"action:\n  type: cancel\n  target: branch\n  branch: \"{{ .Event.branch }}\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := d.write(t, filepath.Join(t.TempDir(), "shared.yaml"), tc.doc)
			stdout, _, code := d.run(t, "validate", "--json", file)
			var cli triggerValidateResult
			if err := json.Unmarshal([]byte(stdout), &cli); err != nil {
				t.Fatalf("validate --json: %v\n%s", err, stdout)
			}

			body, _ := json.Marshal(map[string]string{"source": tc.doc, "id": "shared"})
			req := httptest.NewRequest(http.MethodPost, "/v1/triggers/validate", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer t")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST /v1/triggers/validate = %d: %s", rec.Code, rec.Body)
			}
			var remote struct {
				Valid  bool             `json:"valid"`
				Errors []workflow.Error `json:"errors"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &remote); err != nil {
				t.Fatal(err)
			}

			if cli.Valid != remote.Valid || !sameFindings(cli.Errors, remote.Errors) {
				t.Errorf("CLI said valid=%v %v; API said valid=%v %v", cli.Valid, cli.Errors, remote.Valid, remote.Errors)
			}
			if want := map[bool]int{true: 0, false: 1}[remote.Valid]; code != want {
				t.Errorf("exit = %d, want %d", code, want)
			}
		})
	}
}

func sameFindings(a, b []workflow.Error) bool {
	flat := func(errs []workflow.Error) string {
		var s []string
		for _, e := range errs {
			s = append(s, strconv.Itoa(e.Line)+" "+e.Path+" "+e.Message)
		}
		sort.Strings(s)
		return strings.Join(s, "\n")
	}
	return flat(a) == flat(b)
}

// The one check validate adds to the API's: the registry loads *.yaml only.
func TestTriggerValidateRefusesAFileTheRegistryWouldNotLoad(t *testing.T) {
	d := newTriggerDirs(t)
	file := d.write(t, filepath.Join(t.TempDir(), "shared.yml"), authorDoc("shared", 3, ""))
	stdout, stderr, code := d.run(t, "validate", file)
	if code != 1 || !strings.Contains(stdout, "invalid") || !strings.Contains(stderr, ".yaml") {
		t.Errorf("validate a .yml = %d, stdout %q, stderr %q; want exit 1 naming .yaml", code, stdout, stderr)
	}
	if _, stderr, code := d.run(t, "validate", filepath.Join(t.TempDir(), "missing.yaml")); code != 1 || stderr == "" {
		t.Errorf("validate a missing file = %d (%q), want exit 1 with the error", code, stderr)
	}
}

func TestTriggerLsFiltersByProject(t *testing.T) {
	d := newTriggerDirs(t)
	dir := filepath.Join(d.config, "triggers")
	mine := d.write(t, filepath.Join(dir, "mine.yaml"), authorDoc("mine", 3, "on_fire: create\n"))
	broken := d.write(t, filepath.Join(dir, "broken.yaml"), authorDoc("broken", 3, "unknown: 1\n"))
	d.write(t, filepath.Join(dir, "theirs.yaml"), authorDoc("theirs", 4, ""))
	d.write(t, filepath.Join(dir, "orphan.yaml"), "id: orphan\n")

	stdout, stderr, code := d.run(t, "ls", "--project", "3")
	if code != 0 {
		t.Fatalf("ls = %d: %s", code, stderr)
	}
	if want := broken + "\n" + mine + "\n"; stdout != want {
		t.Errorf("ls stdout = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "orphan.yaml") {
		t.Errorf("ls stderr = %q, want the unattributable file reported", stderr)
	}

	stdout, _, _ = d.run(t, "ls", "--project", "3", "--json")
	var got []trigger.Listing
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("ls --json: %v\n%s", err, stdout)
	}
	if len(got) != 2 || got[1].ID != "mine" || got[1].OnFire != "create" || got[1].Version == "" || got[0].Valid {
		t.Errorf("ls --json = %+v", got)
	}

	if stdout, _, code := d.run(t, "ls", "--project", "99"); code != 1 || stdout != "" {
		t.Errorf("ls for a project with none = %d %q, want exit 1 and no output", code, stdout)
	}
	if stdout, _, code := d.run(t, "ls", "--project", "99", "--json"); code != 1 || strings.TrimSpace(stdout) != "[]" {
		t.Errorf("ls --json for a project with none = %d %q, want exit 1 and []", code, stdout)
	}
}

// apply end to end through the CLI: a manifest built from `ls --json`'s
// version replaces the file, an arming change is refused by name with nothing
// written, and the staging directory goes only on success.
func TestTriggerApplyInstallsOnlyDisarmedChanges(t *testing.T) {
	d := newTriggerDirs(t)
	dir := filepath.Join(d.config, "triggers")
	existing := d.write(t, filepath.Join(dir, "mine.yaml"), authorDoc("mine", 3, ""))

	stdout, _, _ := d.run(t, "ls", "--project", "3", "--json")
	var listed []trigger.Listing
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("ls --json = %q (%v)", stdout, err)
	}
	staging := trigger.ProposalDir(d.data, 42)
	manifest := `{"mine": "` + listed[0].Version + `", "fresh": "absent"}`
	d.write(t, filepath.Join(staging, trigger.ManifestName), manifest)
	d.write(t, filepath.Join(staging, "fresh.yaml"), authorDoc("fresh", 3, ""))
	d.write(t, filepath.Join(staging, "mine.yaml"), strings.Replace(authorDoc("mine", 3, ""), "enabled: false", "enabled: true", 1))

	_, stderr, code := d.run(t, "apply", "--proposal", "42", "--project", "3")
	if code != 1 || !strings.Contains(stderr, "mine.yaml: enabled:") || !strings.Contains(stderr, "nothing was written") {
		t.Fatalf("arming apply = %d, stderr %q; want exit 1 naming mine.yaml's enabled", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.yaml")); !os.IsNotExist(err) {
		t.Errorf("a refused apply wrote fresh.yaml (%v)", err)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("a refused apply removed the staging directory: %v", err)
	}

	d.write(t, filepath.Join(staging, "mine.yaml"), authorDoc("mine", 3, "# reviewed\n"))
	stdout, stderr, code = d.run(t, "apply", "--proposal", "42", "--project", "3")
	if code != 0 {
		t.Fatalf("clean apply = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "wrote "+existing) || !strings.Contains(stdout, "removed "+staging) {
		t.Errorf("clean apply stdout = %q", stdout)
	}
	if b, _ := os.ReadFile(existing); !strings.Contains(string(b), "# reviewed") {
		t.Errorf("mine.yaml = %q, want the staged content", b)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging survived a clean apply (%v)", err)
	}

	// Nothing staged under that id any more.
	if _, stderr, code := d.run(t, "apply", "--proposal", "42", "--project", "3"); code != 1 || !strings.Contains(stderr, "no proposal") {
		t.Errorf("apply with nothing staged = %d %q", code, stderr)
	}
}
