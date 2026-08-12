package config

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestEnvironmentResolve(t *testing.T) {
	base := []string{"PATH=/bin", "HOME=/home/me", "MSYSTEM=MINGW64", "LANG=en_US"}

	cases := []struct {
		name string
		env  Environment
		want []string
	}{
		{
			// The default has to be today's behavior exactly, or T4.23 is a
			// breaking change wearing a config key.
			name: "default inherits everything",
			env:  Environment{Inherit: InheritAll()},
			want: []string{"HOME=/home/me", "LANG=en_US", "MSYSTEM=MINGW64", "PATH=/bin"},
		},
		{
			// The one live case the task rests on: a daemon parented by Git
			// Bash cannot run a cursor step that edits, and MSYSTEM is why.
			name: "inherit all except",
			env:  Environment{Inherit: InheritAll(), Unset: []string{"MSYSTEM"}},
			want: []string{"HOME=/home/me", "LANG=en_US", "PATH=/bin"},
		},
		{
			name: "allowlist",
			env:  Environment{Inherit: InheritNames("PATH", "HOME")},
			want: []string{"HOME=/home/me", "PATH=/bin"},
		},
		{
			name: "allowlist naming something absent is not an error",
			env:  Environment{Inherit: InheritNames("PATH", "NOPE")},
			want: []string{"PATH=/bin"},
		},
		{
			name: "none",
			env:  Environment{Inherit: InheritNone()},
			want: nil,
		},
		{
			name: "none plus set is the hermetic case",
			env: Environment{
				Inherit: InheritNone(),
				Set:     map[string]string{"PATH": "/opt/toolchain/bin"},
			},
			want: []string{"PATH=/opt/toolchain/bin"},
		},
		{
			name: "set wins over inherited",
			env:  Environment{Inherit: InheritAll(), Set: map[string]string{"LANG": "C.UTF-8"}},
			want: []string{"HOME=/home/me", "LANG=C.UTF-8", "MSYSTEM=MINGW64", "PATH=/bin"},
		},
		{
			// Order is inherit → unset → set, so set has the last word even
			// over a name unset in the same policy. Stating it here keeps the
			// order from being reversed by a later refactor.
			name: "set beats unset",
			env: Environment{
				Inherit: InheritAll(),
				Unset:   []string{"LANG"},
				Set:     map[string]string{"LANG": "C.UTF-8"},
			},
			want: []string{"HOME=/home/me", "LANG=C.UTF-8", "MSYSTEM=MINGW64", "PATH=/bin"},
		},
		{
			// Values are literal: adding expansion later would silently
			// reinterpret exactly this config.
			name: "values are literal",
			env: Environment{
				Inherit: InheritNone(),
				Set:     map[string]string{"COST": "${PATH} is not expanded"},
			},
			want: []string{"COST=${PATH} is not expanded"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.env.Resolve(base)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Resolve() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Windows carries entries like "=C:=C:\\" whose name legitimately begins with
// an '='. Cutting on the first byte would invent a variable called "" and
// then hand it to a child.
func TestEnvironmentKeepsOddWindowsEntries(t *testing.T) {
	got := Environment{Inherit: InheritAll()}.Resolve([]string{`=C:=C:\work`, "PATH=/bin"})
	for _, kv := range got {
		if strings.HasPrefix(kv, "=") {
			continue // the odd entry itself is fine to keep
		}
		if !strings.Contains(kv, "=") {
			t.Errorf("resolved a malformed entry: %q", kv)
		}
	}
	if len(got) != 2 {
		t.Errorf("Resolve() = %v, want both entries kept", got)
	}
}

// A resolved environment is sorted so the startup log is stable and a reload
// can tell "changed" from "same set, different order".
func TestEnvironmentResolveIsSorted(t *testing.T) {
	got := Environment{Inherit: InheritAll()}.Resolve([]string{"Z=1", "A=2", "M=3"})
	want := []string{"A=2", "M=3", "Z=1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve() = %v, want %v", got, want)
	}
}

func TestNamesDropsValues(t *testing.T) {
	got := Names([]string{"B=secret", "A=alsosecret"})
	want := []string{"A", "B"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for _, n := range got {
		if strings.Contains(n, "secret") {
			t.Errorf("Names() leaked a value: %q", n)
		}
	}
}

// The failure this warns about is silent and late: adapters are started by
// absolute path, so an agent with no PATH starts and then fails when the CLI
// shells out.
func TestLoadBearing(t *testing.T) {
	if missing := LoadBearing([]string{"PATH=/bin", "SystemRoot=C:\\Windows"}); len(missing) != 0 {
		t.Errorf("LoadBearing() = %v, want none", missing)
	}
	missing := LoadBearing([]string{"HOME=/home/me"})
	if len(missing) == 0 || missing[0] != "PATH" {
		t.Fatalf("LoadBearing() = %v, want PATH named first", missing)
	}
	if runtime.GOOS == "windows" && len(missing) != 2 {
		t.Errorf("LoadBearing() = %v, want SystemRoot named on Windows too", missing)
	}
}

func TestInheritUnmarshal(t *testing.T) {
	cases := map[string]Inherit{
		"inherit: all":          InheritAll(),
		"inherit: none":         InheritNone(),
		"inherit: ALL":          InheritAll(),
		"inherit: [PATH, HOME]": InheritNames("PATH", "HOME"),
		"inherit:\n  - PATH\n":  InheritNames("PATH"),
		// An omitted key is the default, not an empty list.
		"unset: [MSYSTEM]":        InheritAll(),
		"set:\n  LANG: C.UTF-8\n": InheritAll(),
	}
	for src, want := range cases {
		t.Run(src, func(t *testing.T) {
			var env Environment
			if err := yaml.Unmarshal([]byte(src), &env); err != nil {
				t.Fatalf("Unmarshal(%q): %v", src, err)
			}
			if env.Inherit.Mode != want.Mode || !reflect.DeepEqual(env.Inherit.Names, want.Names) {
				t.Errorf("Inherit = %+v, want %+v", env.Inherit, want)
			}
		})
	}
}

// TestEmptyInheritListMeansNothing is why Inherit carries an explicit mode
// rather than inferring one from len(Names): inferring would read `inherit:
// []` — the narrowest request expressible — as "inherit everything", which is
// the widest. A silent inversion of exactly the setting someone reached for
// deliberately.
func TestEmptyInheritListMeansNothing(t *testing.T) {
	var env Environment
	if err := yaml.Unmarshal([]byte("inherit: []"), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if env.Inherit.Mode != InheritListMode {
		t.Fatalf("Inherit.Mode = %v, want the list form", env.Inherit.Mode)
	}
	if got := env.Resolve([]string{"PATH=/bin", "HOME=/home/me"}); len(got) != 0 {
		t.Errorf("Resolve() = %v, want nothing inherited", got)
	}
}

func TestInheritRejectsNonsense(t *testing.T) {
	var env Environment
	if err := yaml.Unmarshal([]byte("inherit: sometimes"), &env); err == nil {
		t.Fatal("accepted an unknown inherit keyword")
	} else if !strings.Contains(err.Error(), "all") {
		t.Errorf("error does not name the valid forms: %v", err)
	}
}

// The config file is where a reader learns the shape, so it has to parse and
// mean the default — the same contract the rest of the bootstrap file has.
func TestDefaultConfigCarriesTheEnvironmentBlock(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(defaultConfigYAML), &cfg); err != nil {
		t.Fatalf("default config does not parse: %v", err)
	}
	if got, want := cfg.Environment.Inherit.String(), "all"; got != want {
		t.Errorf("default inherit = %q, want %q", got, want)
	}
	if len(cfg.Environment.Unset) != 0 || len(cfg.Environment.Set) != 0 {
		t.Errorf("default environment is not empty: %+v", cfg.Environment)
	}
	if !strings.Contains(defaultConfigYAML, "MSYSTEM") {
		t.Error("the commented example does not name the case this exists for")
	}
}

func TestEnvironmentValidation(t *testing.T) {
	bad := map[string]Environment{
		"empty unset name":    {Unset: []string{" "}},
		"assignment in unset": {Unset: []string{"LANG=C"}},
		"assignment in set":   {Set: map[string]string{"LANG=C": "x"}},
		"empty inherit name":  {Inherit: InheritNames("")},
	}
	for name, env := range bad {
		t.Run(name, func(t *testing.T) {
			if err := env.validate(); err == nil {
				t.Error("accepted an invalid environment")
			}
		})
	}
}
