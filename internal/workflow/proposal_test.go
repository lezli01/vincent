package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func wfSource(name, comment string) string {
	return "# " + comment + "\nname: " + name + "\nsteps:\n  - id: one\n    type: command\n    run: 'git status'\n"
}

// proposalFixture is a global scope holding alpha.yaml and beta.yml, and an
// empty staging directory for task 7.
type proposalFixture struct {
	global, staging string
}

func newProposalFixture(t *testing.T) proposalFixture {
	t.Helper()
	root := t.TempDir()
	f := proposalFixture{
		global:  filepath.Join(root, "config", GlobalDirName),
		staging: ProposalDir(filepath.Join(root, "data"), 7),
	}
	for _, dir := range []string{f.global, f.staging} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(f.global, "alpha.yaml"), wfSource("alpha", "live alpha"))
	writeTestFile(t, filepath.Join(f.global, "beta.yml"), wfSource("beta", "live beta"))
	return f
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f proposalFixture) version(t *testing.T, base string) string {
	t.Helper()
	v, err := Version(filepath.Join(f.global, base))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// stage writes files into staging and the manifest beside them.
func (f proposalFixture) stage(t *testing.T, files, manifest map[string]string) {
	t.Helper()
	for base, body := range files {
		writeTestFile(t, filepath.Join(f.staging, base), body)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(f.staging, ProposalManifest), string(raw))
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, de := range des {
		b, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[de.Name()] = string(b)
	}
	return out
}

// Every refusal writes nothing: each live file stays byte for byte and the
// staging directory stays as it was staged (task 123 decision 5). One case per
// refusal the decision lists.
func TestApplyProposalRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f proposalFixture)
		want  string // a substring of one refusal
	}{
		{"invalid file", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"alpha.yaml": "name: alpha\nsteps: nope\n"},
				map[string]string{"alpha.yaml": f.version(t, "alpha.yaml")})
		}, "alpha.yaml: string was used where sequence"},
		{"manifest entry with no file", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"alpha.yaml": wfSource("alpha", "new")},
				map[string]string{"alpha.yaml": f.version(t, "alpha.yaml"), "beta.yml": f.version(t, "beta.yml")})
		}, "beta.yml: is named in manifest.json but not staged"},
		{"file with no entry", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"alpha.yaml": wfSource("alpha", "new")}, map[string]string{})
		}, "alpha.yaml: has no entry in manifest.json"},
		{"stray file", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"alpha.yaml": wfSource("alpha", "new"), "notes.txt": "hi"},
				map[string]string{"alpha.yaml": f.version(t, "alpha.yaml")})
		}, "notes.txt: is neither a staged workflow file"},
		{"stale version", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"alpha.yaml": wfSource("alpha", "new")},
				map[string]string{"alpha.yaml": "1-deadbeef"})
		}, "changed since the proposal recorded it"},
		{"absent now exists", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"alpha.yaml": wfSource("alpha", "new")},
				map[string]string{"alpha.yaml": ProposalAbsent})
		}, "was recorded as absent"},
		{"recorded file gone", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"gamma.yaml": wfSource("gamma", "new")},
				map[string]string{"gamma.yaml": "1-deadbeef"})
		}, "no longer exists"},
		{"path escape", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"alpha..yaml": wfSource("alpha.", "new")},
				map[string]string{"alpha..yaml": ProposalAbsent})
		}, "is not a bare file name"},
		{"new file under the wrong name", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"shared.yml": wfSource("gamma", "new")},
				map[string]string{"shared.yml": ProposalAbsent})
		}, "must be staged as gamma.yaml"},
		{"rename", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"alpha.yaml": wfSource("alpha2", "renamed")},
				map[string]string{"alpha.yaml": f.version(t, "alpha.yaml")})
		}, `renames the workflow "alpha" to "alpha2"`},
		{"duplicate of a live name", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{"beta.yaml": wfSource("beta", "a second beta")},
				map[string]string{"beta.yaml": ProposalAbsent})
		}, "is already declared by"},
		{"two staged files with one name", func(t *testing.T, f proposalFixture) {
			f.stage(t, map[string]string{
				"alpha.yaml": wfSource("gamma", "one"),
				"gamma.yaml": wfSource("gamma", "two"),
			}, map[string]string{"alpha.yaml": f.version(t, "alpha.yaml"), "gamma.yaml": ProposalAbsent})
		}, "is also declared by the staged"},
		{"manifest not an object", func(t *testing.T, f proposalFixture) {
			writeTestFile(t, filepath.Join(f.staging, ProposalManifest), "[]")
		}, "manifest.json: must be a JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newProposalFixture(t)
			tc.setup(t, f)
			liveBefore, stagedBefore := snapshot(t, f.global), snapshot(t, f.staging)
			for _, check := range []bool{true, false} {
				paths, refusals, err := ApplyProposal(f.staging, f.global, Options{}, check)
				if err != nil {
					t.Fatalf("check=%v: err = %v, want refusals", check, err)
				}
				if paths != nil {
					t.Errorf("check=%v: paths = %v, want none on a refusal", check, paths)
				}
				var all []string
				for _, r := range refusals {
					all = append(all, r.String())
				}
				if !strings.Contains(strings.Join(all, "\n"), tc.want) {
					t.Errorf("check=%v: refusals = %q, want one containing %q", check, all, tc.want)
				}
			}
			if got := snapshot(t, f.global); !reflect.DeepEqual(got, liveBefore) {
				t.Errorf("a refused proposal changed the global scope:\n%v\nwant\n%v", got, liveBefore)
			}
			if got := snapshot(t, f.staging); !reflect.DeepEqual(got, stagedBefore) {
				t.Errorf("a refused proposal changed its staging directory")
			}
		})
	}
}

// Success installs every file, a replacement and a new one, and removes the
// staging directory; --check before it writes and removes nothing and lists
// the staged paths, sorted.
func TestApplyProposalInstalls(t *testing.T) {
	f := newProposalFixture(t)
	alpha, gamma := wfSource("alpha", "rewritten alpha"), wfSource("gamma", "new gamma")
	f.stage(t, map[string]string{"alpha.yaml": alpha, "gamma.yaml": gamma},
		map[string]string{"alpha.yaml": f.version(t, "alpha.yaml"), "gamma.yaml": ProposalAbsent})
	liveBefore := snapshot(t, f.global)

	paths, refusals, err := ApplyProposal(f.staging, f.global, Options{}, true)
	if err != nil || refusals != nil {
		t.Fatalf("check: refusals = %v, err = %v", refusals, err)
	}
	wantStaged := []string{filepath.Join(f.staging, "alpha.yaml"), filepath.Join(f.staging, "gamma.yaml")}
	if !reflect.DeepEqual(paths, wantStaged) {
		t.Errorf("check paths = %v, want %v", paths, wantStaged)
	}
	if got := snapshot(t, f.global); !reflect.DeepEqual(got, liveBefore) {
		t.Error("--check wrote into the global scope")
	}
	if _, err := os.Stat(f.staging); err != nil {
		t.Errorf("--check removed the staging directory: %v", err)
	}

	paths, refusals, err = ApplyProposal(f.staging, f.global, Options{}, false)
	if err != nil || refusals != nil {
		t.Fatalf("apply: refusals = %v, err = %v", refusals, err)
	}
	wantWritten := []string{filepath.Join(f.global, "alpha.yaml"), filepath.Join(f.global, "gamma.yaml")}
	if !reflect.DeepEqual(paths, wantWritten) {
		t.Errorf("written = %v, want %v", paths, wantWritten)
	}
	got := snapshot(t, f.global)
	want := map[string]string{"alpha.yaml": alpha, "beta.yml": liveBefore["beta.yml"], "gamma.yaml": gamma}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("global scope after apply = %v, want %v", got, want)
	}
	if _, err := os.Stat(f.staging); !os.IsNotExist(err) {
		t.Errorf("staging survived a successful apply: %v", err)
	}
	if _, _, err := ApplyProposal(f.staging, f.global, Options{}, false); err == nil ||
		!strings.Contains(err.Error(), "no proposal staged") {
		t.Errorf("second apply err = %v, want nothing staged", err)
	}
}

// An empty manifest is the pass finding every workflow already right: it
// checks clean, installs nothing and removes the staging directory — into a
// global scope that does not even exist.
func TestApplyProposalEmpty(t *testing.T) {
	f := newProposalFixture(t)
	f.stage(t, nil, map[string]string{})
	missing := filepath.Join(t.TempDir(), "nowhere")
	paths, refusals, err := ApplyProposal(f.staging, missing, Options{}, true)
	if err != nil || refusals != nil || paths != nil {
		t.Fatalf("check: paths = %v, refusals = %v, err = %v", paths, refusals, err)
	}
	paths, refusals, err = ApplyProposal(f.staging, missing, Options{}, false)
	if err != nil || refusals != nil || paths != nil {
		t.Fatalf("apply: paths = %v, refusals = %v, err = %v", paths, refusals, err)
	}
	if _, err := os.Stat(f.staging); !os.IsNotExist(err) {
		t.Errorf("staging survived an empty apply: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("an empty apply created the global directory: %v", err)
	}
}

// A live file that does not parse is still one the pass may repair, and it
// keeps its name when its name: is readable.
func TestApplyProposalRepairsAnInvalidLiveFile(t *testing.T) {
	f := newProposalFixture(t)
	writeTestFile(t, filepath.Join(f.global, "alpha.yaml"), "name: alpha\nsteps: nope\n")
	f.stage(t, map[string]string{"alpha.yaml": wfSource("alpha", "repaired")},
		map[string]string{"alpha.yaml": f.version(t, "alpha.yaml")})
	if _, refusals, err := ApplyProposal(f.staging, f.global, Options{}, false); err != nil || refusals != nil {
		t.Fatalf("refusals = %v, err = %v", refusals, err)
	}
}

// ls --global's reader (task 123 decision 3): every file, sorted, with the
// version PATCH checks, and an invalid one still listed so it can be repaired.
func TestListGlobal(t *testing.T) {
	f := newProposalFixture(t)
	writeTestFile(t, filepath.Join(f.global, "broken.yaml"), "name: broken\nsteps: nope\n")
	writeTestFile(t, filepath.Join(f.global, "notes.txt"), "not a workflow")
	got, problems := ListGlobal(f.global, Options{})
	if problems != nil {
		t.Fatalf("problems = %v", problems)
	}
	var names []string
	for _, l := range got {
		names = append(names, l.Name)
		if want := f.version(t, filepath.Base(l.File)); l.Version != want {
			t.Errorf("%s version = %q, want workflow.Version's %q", l.Name, l.Version, want)
		}
		if l.Valid != (l.Name != "broken") || (len(l.Errors) == 0) != l.Valid {
			t.Errorf("%s valid = %v, errors = %v", l.Name, l.Valid, l.Errors)
		}
	}
	if want := []string{"alpha", "beta", "broken"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}

	if got, problems := ListGlobal(filepath.Join(f.global, "missing"), Options{}); got != nil || problems != nil {
		t.Errorf("missing dir = %v, %v; want nothing", got, problems)
	}
}
