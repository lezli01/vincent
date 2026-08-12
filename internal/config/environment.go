package config

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// Environment decides what the daemon's child processes inherit (spec §12.3,
// T4.23).
//
// Before this existed, nothing in the chain from CLI to agent process decided
// it: the detached spawn inherits the launching shell's environment,
// RunSpec.Env was never populated, and each adapter overrides only a non-nil
// one — so the same task, workflow and agent ran differently depending on
// what started the daemon, and nothing recorded which. The value being *set*
// was never the defect; an inherited USERPROFILE is where an agent CLI's own
// credentials live. The defect is that it was accidental and unrecorded.
//
// Resolution is one sentence: inherit → unset → set. Command and check steps
// then layer the §8.5 VINCENT_* variables and their own `env:` on top, so
// neither Unset nor Set can reach those — they are facts about the run, not
// inherited state.
type Environment struct {
	// Inherit selects what comes from the daemon's own environment: "all"
	// (the default), "none", or an explicit list of names.
	Inherit Inherit `yaml:"inherit"`
	// Unset drops names after Inherit. This is the "inherit all except" case,
	// which is the one a real failure has already needed: MSYSTEM, injected by
	// a Git Bash parent, blocks every cursor tool call on Windows (T5.7).
	Unset []string `yaml:"unset"`
	// Set assigns literal values last. Values are literal — `$` is not
	// special, and no expansion is performed. Adding expansion later would
	// silently reinterpret an existing literal containing "${", so it would
	// arrive as its own key rather than as a change of meaning here.
	Set map[string]string `yaml:"set"`
}

// InheritMode is which of Inherit's three forms was given. It is an explicit
// mode rather than something inferred from len(Names), because an empty list
// has to mean *nothing* and inference would silently make `inherit: []` mean
// everything — the widest possible reading of the narrowest possible request.
type InheritMode int

const (
	// InheritAllMode is the zero value, so an omitted key means today's
	// behavior without Default() having to reach in and say so.
	InheritAllMode InheritMode = iota
	// InheritNoneMode takes nothing from the daemon's environment.
	InheritNoneMode
	// InheritListMode takes only the named variables. An empty list is a
	// legitimate way to say "nothing".
	InheritListMode
)

// Inherit is Environment.Inherit: "all", "none", or a list of names. It is a
// union in YAML because the two common answers are words and the third is a
// list, and spelling the words as one-element lists would read worse than it
// parses.
type Inherit struct {
	Mode  InheritMode
	Names []string
}

// InheritAll is the default: everything the daemon has.
func InheritAll() Inherit { return Inherit{Mode: InheritAllMode} }

// InheritNames is the allowlist form.
func InheritNames(names ...string) Inherit {
	return Inherit{Mode: InheritListMode, Names: names}
}

// InheritNone is the hermetic form.
func InheritNone() Inherit { return Inherit{Mode: InheritNoneMode} }

// String renders the form the config file would carry, which is what the
// startup log reports.
func (i Inherit) String() string {
	switch i.Mode {
	case InheritNoneMode:
		return "none"
	case InheritListMode:
		return "[" + strings.Join(i.Names, " ") + "]"
	case InheritAllMode:
		return "all"
	default:
		return "all"
	}
}

// UnmarshalYAML implements yaml.BytesUnmarshaler.
func (i *Inherit) UnmarshalYAML(b []byte) error {
	var s string
	if err := yaml.Unmarshal(b, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "all":
			*i = InheritAll()
			return nil
		case "none":
			*i = InheritNone()
			return nil
		default:
			return fmt.Errorf("environment.inherit: want \"all\", \"none\", or a list of names; got %q", s)
		}
	}
	var names []string
	if err := yaml.Unmarshal(b, &names); err != nil {
		return fmt.Errorf("environment.inherit: want \"all\", \"none\", or a list of names: %w", err)
	}
	*i = Inherit{Mode: InheritListMode, Names: names}
	return nil
}

// MarshalYAML implements yaml.BytesMarshaler.
func (i Inherit) MarshalYAML() ([]byte, error) {
	if len(i.Names) > 0 {
		return yaml.Marshal(i.Names)
	}
	return []byte(i.String()), nil
}

// Resolve applies the policy to a base environment in "K=V" form (normally
// os.Environ()) and returns the environment a child should get, sorted by
// name so the result is stable to log and to compare across reloads.
//
// It is a pure function of its input: config depends on nothing internal, and
// a resolver that read the process environment itself could not be tested
// against the cases that matter.
func (e Environment) Resolve(base []string) []string {
	vars := make(map[string]string, len(base))

	switch e.Inherit.Mode {
	case InheritNoneMode:
		// nothing
	case InheritListMode:
		want := make(map[string]bool, len(e.Inherit.Names))
		for _, n := range e.Inherit.Names {
			want[n] = true
		}
		for _, kv := range base {
			if k, v, ok := splitEnv(kv); ok && want[k] {
				vars[k] = v
			}
		}
	case InheritAllMode:
		for _, kv := range base {
			if k, v, ok := splitEnv(kv); ok {
				vars[k] = v
			}
		}
	}

	for _, n := range e.Unset {
		delete(vars, n)
	}
	for k, v := range e.Set {
		vars[k] = v
	}

	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// splitEnv splits one "K=V" entry into its name and value.
//
// The separator is searched from index 1, not 0, because Windows carries
// entries whose name legitimately begins with '=' — `=C:=C:\work` records a
// per-drive working directory, and Go's own exec dedup special-cases them.
// Cutting on the first byte would name that variable "" and drop it, which
// would make the inherit-everything default quietly *not* everything.
func splitEnv(kv string) (name, value string, ok bool) {
	if len(kv) < 2 {
		return "", "", false
	}
	i := strings.IndexByte(kv[1:], '=')
	if i < 0 {
		return "", "", false
	}
	i++
	return kv[:i], kv[i+1:], true
}

// Names returns the sorted variable names of a resolved environment. The
// startup log reports these and never the values: an environment block is
// where every agent CLI's credentials live, and a log is something people
// paste.
func Names(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, ok := splitEnv(kv); ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// LoadBearing reports names a resolved environment is missing that a child
// will need. It exists because the failure is otherwise silent *and* late:
// adapters are resolved with exec.LookPath in the daemon and started by
// absolute path, so an agent with no PATH starts perfectly and then fails
// three steps in, when the CLI shells out to git.
//
// The policy is honored as written — a hermetic environment with an
// absolute-path toolchain is a legitimate thing to ask for — so this reports
// rather than corrects.
func LoadBearing(env []string) []string {
	have := make(map[string]bool, len(env))
	for _, n := range Names(env) {
		have[n] = true
	}
	want := []string{"PATH"}
	if runtime.GOOS == "windows" {
		// Without SystemRoot a Windows child fails inside socket startup,
		// with an error naming neither the variable nor vincent.
		want = append(want, "SystemRoot")
	}
	var missing []string
	for _, n := range want {
		if !have[n] && !have[strings.ToUpper(n)] {
			missing = append(missing, n)
		}
	}
	return missing
}

// ResolveProcess applies the policy to this process's own environment.
func (e Environment) ResolveProcess() []string { return e.Resolve(os.Environ()) }

func (e Environment) validate() error {
	for _, n := range e.Unset {
		if strings.TrimSpace(n) == "" {
			return fmt.Errorf("environment.unset: names must not be empty")
		}
		if strings.Contains(n, "=") {
			return fmt.Errorf("environment.unset: %q is a name, not an assignment", n)
		}
	}
	for _, n := range e.Inherit.Names {
		if strings.TrimSpace(n) == "" {
			return fmt.Errorf("environment.inherit: names must not be empty")
		}
		if strings.Contains(n, "=") {
			return fmt.Errorf("environment.inherit: %q is a name, not an assignment", n)
		}
	}
	for k := range e.Set {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("environment.set: names must not be empty")
		}
		if strings.Contains(k, "=") {
			return fmt.Errorf("environment.set: %q is a name, not an assignment", k)
		}
	}
	return nil
}
