package workflow

import (
	"runtime"
	"strings"
	"testing"
)

const platformSource = `name: posix-tools
platforms: [posix]
steps:
  - id: report
    type: command
    run: cat README.md | wc -l
`

func TestParsePlatforms(t *testing.T) {
	wf, _, err := Parse([]byte(platformSource), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(wf.Platforms) != 1 || wf.Platforms[0] != PlatformPosix {
		t.Fatalf("platforms = %v, want [posix]", wf.Platforms)
	}
	if got := wf.PlatformSummary(); got != "posix" {
		t.Errorf("PlatformSummary = %q, want posix", got)
	}
}

func TestSupportsPlatform(t *testing.T) {
	tests := []struct {
		name      string
		platforms []string
		goos      string
		want      bool
	}{
		{"unrestricted runs anywhere", nil, "windows", true},
		{"empty list runs anywhere", []string{}, "windows", true},
		{"posix admits linux", []string{PlatformPosix}, "linux", true},
		{"posix admits darwin", []string{PlatformPosix}, "darwin", true},
		{"posix admits an unlisted unix", []string{PlatformPosix}, "freebsd", true},
		{"posix refuses windows", []string{PlatformPosix}, "windows", false},
		{"explicit pair admits a member", []string{"linux", "darwin"}, "darwin", true},
		{"explicit pair refuses a stranger", []string{"linux", "darwin"}, "windows", false},
		{"windows-only refuses linux", []string{PlatformWindows}, "linux", false},
		{"mixed list admits either side", []string{PlatformPosix, PlatformWindows}, "windows", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wf := &Workflow{Platforms: tc.platforms}
			if got := wf.SupportsPlatform(tc.goos); got != tc.want {
				t.Errorf("SupportsPlatform(%q) with %v = %v, want %v",
					tc.goos, tc.platforms, got, tc.want)
			}
		})
	}
}

func TestSupportsHostMatchesRuntime(t *testing.T) {
	wf := &Workflow{Platforms: []string{runtime.GOOS}}
	if !wf.SupportsHost() {
		t.Errorf("a workflow naming %q does not support its own host", runtime.GOOS)
	}
	if HostPlatform() != runtime.GOOS {
		t.Errorf("HostPlatform = %q, want %q", HostPlatform(), runtime.GOOS)
	}
}

func TestPlatformMismatch(t *testing.T) {
	wf := &Workflow{Platforms: []string{PlatformLinux, PlatformDarwin}}
	if got := wf.PlatformMismatch("linux"); got != "" {
		t.Errorf("PlatformMismatch on a supported host = %q, want empty", got)
	}
	got := wf.PlatformMismatch("windows")
	for _, want := range []string{"linux, darwin", "windows"} {
		if !strings.Contains(got, want) {
			t.Errorf("PlatformMismatch = %q, want it to mention %q", got, want)
		}
	}
}

func TestValidatePlatformsRejectsUnknownTokens(t *testing.T) {
	src := `name: broken
platforms: [linux, macos]
steps:
  - id: s
    type: command
    run: echo hi
`
	_, _, err := Parse([]byte(src), Options{})
	var errs Errors
	if !asErrors(err, &errs) {
		t.Fatalf("Parse = %v, want validation errors", err)
	}
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly one", errs)
	}
	if errs[0].Path != "platforms[1]" || !strings.Contains(errs[0].Message, `unknown platform "macos"`) {
		t.Errorf("error = %+v, want the unknown token located at platforms[1]", errs[0])
	}
	if errs[0].Line != 2 {
		t.Errorf("error line = %d, want 2 — the platforms list", errs[0].Line)
	}
}

func TestValidatePlatformsRejectsDuplicates(t *testing.T) {
	src := `name: dup
platforms: [linux, linux]
steps:
  - id: s
    type: command
    run: echo hi
`
	_, _, err := Parse([]byte(src), Options{})
	var errs Errors
	if !asErrors(err, &errs) {
		t.Fatalf("Parse = %v, want validation errors", err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Message, `duplicate platform "linux"`) {
		t.Fatalf("errors = %v, want one duplicate-platform error", errs)
	}
}

// A restriction survives the snapshot round-trip, which is what `edit + retry`
// rewrites and what the engine re-reads at admission (§5.3).
func TestMarshalKeepsPlatforms(t *testing.T) {
	wf, _, err := Parse([]byte(platformSource), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Marshal(wf)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, _, err := Parse(out, Options{})
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if len(back.Platforms) != 1 || back.Platforms[0] != PlatformPosix {
		t.Errorf("platforms after round-trip = %v, want [posix]", back.Platforms)
	}
}

func TestEntryRunsHere(t *testing.T) {
	other := PlatformWindows
	if runtime.GOOS == PlatformWindows {
		other = PlatformLinux
	}
	if e := (Entry{Workflow: &Workflow{Platforms: []string{other}}}); e.RunsHere() {
		t.Errorf("entry restricted to %q claims to run on %q", other, runtime.GOOS)
	}
	if e := (Entry{Workflow: &Workflow{}}); !e.RunsHere() {
		t.Error("unrestricted entry does not run here")
	}
	// An unparsed entry is rejected for its errors, not for a platform it
	// never got to declare.
	if e := (Entry{Errors: Errors{{Message: "boom"}}}); !e.RunsHere() {
		t.Error("invalid entry should not also claim a platform mismatch")
	}
}
