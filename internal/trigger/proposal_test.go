package trigger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// proposalDoc is a valid command trigger for project with the three arming
// switches as given; "" leaves a key out, which is how "absent" is written.
func proposalDoc(id string, project int64, enabled bool, onFire, permission string) string {
	var b strings.Builder
	b.WriteString("id: " + id + "\n")
	b.WriteString("enabled: " + strconv.FormatBool(enabled) + "\n")
	b.WriteString("source:\n  type: command\n  project: " + strconv.FormatInt(project, 10) + "\n")
	b.WriteString("  poll_interval: 5m\n  command: [\"never-run\"]\n")
	b.WriteString("action:\n  type: create_task\n  workflow: wf\n  title: \"{{ .Event.id }}\"\n")
	if onFire != "" {
		b.WriteString("on_fire: " + onFire + "\n")
	}
	if permission != "" {
		b.WriteString("permission: " + permission + "\n")
	}
	return b.String()
}

func TestArmingChanges(t *testing.T) {
	armed := Switches{Enabled: true, OnFire: OnFireCreate, Permission: PermissionWorkflow}
	for _, tc := range []struct {
		name          string
		before, after Switches
		want          string
	}{
		{"absent to absent", Switches{}, Switches{}, ""},
		{"absent to every armed value", Switches{}, armed, "enabled,on_fire,permission"},
		{"explicit disarmed values to armed", Switches{OnFire: OnFirePropose, Permission: PermissionRestricted}, armed, "enabled,on_fire,permission"},
		{"explicit disarmed values are not arming", Switches{}, Switches{OnFire: OnFirePropose, Permission: PermissionRestricted}, ""},
		{"armed kept", armed, armed, ""},
		{"disarming", armed, Switches{}, ""},
		{"one switch", Switches{Enabled: true}, Switches{Enabled: true, OnFire: OnFireCreate}, "on_fire"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(ArmingChanges(tc.before, tc.after), ","); got != tc.want {
				t.Errorf("ArmingChanges = %q, want %q", got, tc.want)
			}
		})
	}
}

type proposalFixture struct {
	staging string
	w       *Writer
}

func newProposalFixture(t *testing.T) *proposalFixture {
	t.Helper()
	return &proposalFixture{
		staging: ProposalDir(t.TempDir(), 42),
		w:       NewWriter(filepath.Join(t.TempDir(), "triggers")),
	}
}

// onDisk writes trigger "t" the way a human would and returns its version.
// Every test proposes against that one id, so it is not a parameter.
func (f *proposalFixture) onDisk(t *testing.T, doc string) string {
	t.Helper()
	v, err := f.w.Create("t", []byte(doc))
	if err != nil {
		t.Fatalf("create t: %v", err)
	}
	return v
}

func (f *proposalFixture) stage(t *testing.T, files map[string]string, manifest map[string]string) {
	t.Helper()
	if err := os.MkdirAll(f.staging, 0o700); err != nil {
		t.Fatal(err)
	}
	for id, doc := range files {
		if err := os.WriteFile(filepath.Join(f.staging, id+".yaml"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.staging, ManifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *proposalFixture) read(t *testing.T, id string) string {
	t.Helper()
	path, _ := f.w.Path(id)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The whole point of apply: a proposal may add or rewrite a disarmed trigger,
// may keep a switch a human already armed, and may never be the thing that
// arms one — on a new file or an existing one.
func TestApplyProposalArming(t *testing.T) {
	const project = 7
	for _, tc := range []struct {
		name       string
		existing   string // "" is a new file
		staged     string
		wantRefuse string // the refused keys, "" when it applies
	}{
		{"new disarmed", "", proposalDoc("t", project, false, "", ""), ""},
		{"new with explicit disarmed values", "", proposalDoc("t", project, false, OnFirePropose, PermissionRestricted), ""},
		{"new enabled", "", proposalDoc("t", project, true, "", ""), "enabled"},
		{"new on_fire create", "", proposalDoc("t", project, false, OnFireCreate, ""), "on_fire"},
		{"new permission workflow", "", proposalDoc("t", project, false, "", PermissionWorkflow), "permission"},
		{"disabled to enabled", proposalDoc("t", project, false, "", ""), proposalDoc("t", project, true, "", ""), "enabled"},
		{"propose to create", proposalDoc("t", project, false, OnFirePropose, ""), proposalDoc("t", project, false, OnFireCreate, ""), "on_fire"},
		{"restricted to workflow", proposalDoc("t", project, false, "", PermissionRestricted), proposalDoc("t", project, false, "", PermissionWorkflow), "permission"},
		{
			"armed values kept",
			proposalDoc("t", project, true, OnFireCreate, PermissionWorkflow),
			proposalDoc("t", project, true, OnFireCreate, PermissionWorkflow) + "# a comment the pass added\n",
			"",
		},
		{"disarming", proposalDoc("t", project, true, OnFireCreate, PermissionWorkflow), proposalDoc("t", project, false, "", ""), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProposalFixture(t)
			version := Absent
			if tc.existing != "" {
				version = f.onDisk(t, tc.existing)
			}
			f.stage(t, map[string]string{"t": tc.staged}, map[string]string{"t": version})

			written, refusals, err := ApplyProposal(f.staging, f.w, project)
			if err != nil {
				t.Fatalf("ApplyProposal error = %v", err)
			}
			var keys []string
			for _, r := range refusals {
				keys = append(keys, r.Key)
			}
			if got := strings.Join(keys, ","); got != tc.wantRefuse {
				t.Fatalf("refused keys = %q, want %q (%v)", got, tc.wantRefuse, refusals)
			}
			_, stagingErr := os.Stat(f.staging)
			if tc.wantRefuse != "" {
				if len(written) != 0 || f.read(t, "t") != tc.existing {
					t.Errorf("a refused proposal wrote %v; the file must be untouched", written)
				}
				if stagingErr != nil {
					t.Errorf("a refused proposal removed its staging directory: %v", stagingErr)
				}
				return
			}
			if len(written) != 1 || f.read(t, "t") != tc.staged {
				t.Errorf("written = %v, file = %q; want the staged document installed", written, f.read(t, "t"))
			}
			if !errors.Is(stagingErr, os.ErrNotExist) {
				t.Errorf("staging directory survived a successful apply: %v", stagingErr)
			}
		})
	}
}

// Every other refusal writes nothing either — including a valid sibling of
// the file at fault, because a proposal lands whole or not at all.
func TestApplyProposalRefusesWholesale(t *testing.T) {
	const project = 7
	clean := proposalDoc("clean", project, false, "", "")
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *proposalFixture) (files, manifest map[string]string)
		want  string
	}{
		{
			name: "stale version",
			setup: func(t *testing.T, f *proposalFixture) (map[string]string, map[string]string) {
				f.onDisk(t, proposalDoc("t", project, false, "", ""))
				return map[string]string{"t": proposalDoc("t", project, false, OnFirePropose, "")},
					map[string]string{"t": "0-deadbeef"}
			},
			want: "changed since the proposal recorded it",
		},
		{
			name: "appeared after being recorded absent",
			setup: func(t *testing.T, f *proposalFixture) (map[string]string, map[string]string) {
				f.onDisk(t, proposalDoc("t", project, false, "", ""))
				return map[string]string{"t": proposalDoc("t", project, false, OnFirePropose, "")},
					map[string]string{"t": Absent}
			},
			want: "was recorded as absent",
		},
		{
			name: "recorded file is gone",
			setup: func(*testing.T, *proposalFixture) (map[string]string, map[string]string) {
				return map[string]string{"t": proposalDoc("t", project, false, "", "")},
					map[string]string{"t": "0-deadbeef"}
			},
			want: "no longer exists",
		},
		{
			name: "another project's trigger",
			setup: func(*testing.T, *proposalFixture) (map[string]string, map[string]string) {
				return map[string]string{"t": proposalDoc("t", project+1, false, "", "")},
					map[string]string{"t": Absent}
			},
			want: "not this project's id",
		},
		{
			name: "replacing a file another project owns",
			setup: func(t *testing.T, f *proposalFixture) (map[string]string, map[string]string) {
				v := f.onDisk(t, proposalDoc("t", project+1, false, "", ""))
				return map[string]string{"t": proposalDoc("t", project, false, "", "")},
					map[string]string{"t": v}
			},
			want: "does not belong to project",
		},
		{
			name: "invalid file",
			setup: func(*testing.T, *proposalFixture) (map[string]string, map[string]string) {
				return map[string]string{"t": proposalDoc("t", project, false, "", "") + "container: {}\n"},
					map[string]string{"t": Absent}
			},
			want: "container",
		},
		{
			name: "id not the file name",
			setup: func(*testing.T, *proposalFixture) (map[string]string, map[string]string) {
				return map[string]string{"t": proposalDoc("other", project, false, "", "")},
					map[string]string{"t": Absent}
			},
			want: "must match the file name",
		},
		{
			name: "staged file missing from the manifest",
			setup: func(*testing.T, *proposalFixture) (map[string]string, map[string]string) {
				return map[string]string{"t": proposalDoc("t", project, false, "", "")}, map[string]string{}
			},
			want: "has no entry in manifest.json",
		},
		{
			name: "manifest entry with no staged file",
			setup: func(*testing.T, *proposalFixture) (map[string]string, map[string]string) {
				return map[string]string{}, map[string]string{"ghost": Absent}
			},
			want: "not staged",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProposalFixture(t)
			files, manifest := tc.setup(t, f)
			files["clean"], manifest["clean"] = clean, Absent
			before := f.read(t, "t")
			f.stage(t, files, manifest)

			written, refusals, err := ApplyProposal(f.staging, f.w, project)
			if err != nil {
				t.Fatalf("ApplyProposal error = %v", err)
			}
			var all []string
			for _, r := range refusals {
				all = append(all, r.String())
			}
			if !strings.Contains(strings.Join(all, "\n"), tc.want) {
				t.Fatalf("refusals = %q, want one mentioning %q", all, tc.want)
			}
			if len(written) != 0 || f.read(t, "clean") != "" || f.read(t, "t") != before {
				t.Errorf("a refused proposal wrote something: %v", written)
			}
		})
	}
}

func TestApplyProposalWithNothingStaged(t *testing.T) {
	f := newProposalFixture(t)
	if _, _, err := ApplyProposal(f.staging, f.w, 7); err == nil || !strings.Contains(err.Error(), "no proposal") {
		t.Errorf("missing staging dir: err = %v, want a no-proposal error", err)
	}
}

// ls lists an invalid file that still names its project, so update-triggers
// can repair it, and reports the one it cannot attribute rather than guessing.
func TestListReadsInvalidFilesLeniently(t *testing.T) {
	dir := t.TempDir()
	write := func(name, doc string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("good.yaml", proposalDoc("good", 3, true, OnFireCreate, ""))
	write("broken.yaml", proposalDoc("broken", 3, false, "", "")+"unknown_key: 1\n")
	write("orphan.yaml", "id: orphan\nsource: {type: command}\n")
	write("notes.txt", "not a trigger")

	got, problems := List(dir)
	if len(got) != 2 || got[0].ID != "broken" || got[1].ID != "good" {
		t.Fatalf("List = %+v, want broken and good", got)
	}
	if b := got[0]; b.Valid || b.Project != 3 || len(b.Errors) == 0 || b.Version == "" {
		t.Errorf("broken = %+v, want an invalid project-3 listing with errors and a version", b)
	}
	if g := got[1]; !g.Valid || !g.Enabled || g.OnFire != OnFireCreate || g.Permission != "" || g.Errors == nil {
		t.Errorf("good = %+v, want its switches as written and an empty error list", g)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "orphan.yaml") {
		t.Errorf("problems = %v, want the orphan reported", problems)
	}
	if _, problems := List(filepath.Join(dir, "missing")); problems != nil {
		t.Errorf("a missing directory is no triggers, got %v", problems)
	}
}
