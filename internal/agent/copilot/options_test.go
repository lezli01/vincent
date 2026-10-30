package copilot

import (
	"os"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

func values(opts []agent.Option, source string) []string {
	var out []string
	for _, o := range opts {
		if o.Source == source {
			out = append(out, o.Value)
		}
	}
	return out
}

// TestParseHelpFixture reads the captured clap-style help: exactly the seven
// `--reasoning-effort` values, every one of them source cli.
func TestParseHelpFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/help_1.0.105.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	for name, help := range map[string]string{
		"lf":   string(b),
		"crlf": string(crlf(b)),
	} {
		t.Run(name, func(t *testing.T) {
			efforts, err := parseHelp(help)
			if err != nil {
				t.Fatalf("parseHelp: %v", err)
			}
			if !slices.Equal(efforts, want) {
				t.Errorf("efforts = %v, want %v", efforts, want)
			}
			opts := mergeOptions(efforts)
			if got := values(opts.Efforts, agent.SourceCLI); !slices.Equal(got, want) {
				t.Errorf("cli efforts = %v, want %v", got, want)
			}
			if got := values(opts.Efforts, agent.SourceCurated); len(got) != 0 {
				t.Errorf("curated efforts = %v, want none left over", got)
			}
		})
	}
}

func crlf(b []byte) []byte {
	var out []byte
	for _, c := range b {
		if c == '\n' {
			out = append(out, '\r')
		}
		out = append(out, c)
	}
	return out
}

// TestParseHelpWithoutClapLine is a build that does not list effort values in
// clap's shape — a commander-style build below 1.0.84 — or none at all.
func TestParseHelpWithoutClapLine(t *testing.T) {
	for name, help := range map[string]string{
		"commander": "Usage: copilot [options]\n\nOptions:\n" +
			"  --model <model>             Set the AI model to use\n" +
			"  --reasoning-effort <level>  Set the reasoning effort level (choices: \"low\", \"medium\", \"high\")\n",
		"absent": "Usage: copilot [OPTIONS]\n\nOptions:\n  -h, --help\n          Print help\n",
		"empty":  "      --reasoning-effort <level>\n          Set the reasoning effort level [possible values: ]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if efforts, err := parseHelp(help); err == nil {
				t.Errorf("parseHelp = %v, nil; want an error naming the missing line", efforts)
			}
		})
	}
}

// TestOptionsProbe runs the probe against the fake's clap-style help: the
// probed values come first as cli, the rest of the floor follows as curated,
// and the probe carries --no-auto-update like every spawn.
func TestOptionsProbe(t *testing.T) {
	t.Setenv("FAKEAGENT_DIALECT", "copilot")
	opts, err := fakeAdapter(t).Options(t.Context())
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	if got, want := values(opts.Efforts, agent.SourceCLI), []string{"max", "high", "low", "fake-ultra"}; !slices.Equal(got, want) {
		t.Errorf("cli efforts = %v, want %v", got, want)
	}
	if got, want := values(opts.Efforts, agent.SourceCurated), []string{"none", "minimal", "medium", "xhigh"}; !slices.Equal(got, want) {
		t.Errorf("curated efforts = %v, want %v", got, want)
	}
	assertCatalogInvariants(t, "probed", opts)
}

// TestOptionsDegrade is every way the probe fails: the curated catalog comes
// back alongside an error, never a silent floor.
func TestOptionsDegrade(t *testing.T) {
	cases := map[string]func(t *testing.T) *Adapter{
		"missing binary": func(*testing.T) *Adapter {
			return New(func() string { return "/nonexistent/copilot-not-here" })
		},
		// The fake's default help is commander-style, as a copilot build
		// below 1.0.84 prints.
		"commander help": func(t *testing.T) *Adapter {
			t.Setenv("FAKEAGENT_DIALECT", "")
			return fakeAdapter(t)
		},
	}
	for name, adapter := range cases {
		t.Run(name, func(t *testing.T) {
			opts, err := adapter(t).Options(t.Context())
			if err == nil {
				t.Fatal("Options error = nil, want the probe failure")
			}
			if !slices.EqualFunc(opts.Efforts, New(nil).Curated().Efforts, func(a, b agent.Option) bool { return a == b }) {
				t.Errorf("efforts = %v, want the curated floor", opts.Efforts)
			}
			assertCatalogInvariants(t, name, opts)
		})
	}
}

// TestCuratedFloor pins the catalog: no models, no default, the seven-value
// effort floor, no input, restricted on every OS (task 138.10).
func TestCuratedFloor(t *testing.T) {
	opts := New(nil).Curated()
	if got := values(opts.Efforts, agent.SourceCurated); !slices.Equal(got, curatedEfforts) || len(got) != len(opts.Efforts) {
		t.Errorf("efforts = %v, want the curated floor %v", opts.Efforts, curatedEfforts)
	}
	assertCatalogInvariants(t, "Curated", opts)
}

func assertCatalogInvariants(t *testing.T, name string, opts agent.Options) {
	t.Helper()
	if opts.InputSupport != agent.InputNever {
		t.Errorf("%s InputSupport = %q, want never", name, opts.InputSupport)
	}
	if opts.RestrictedSupport != agent.RestrictedAlways {
		t.Errorf("%s RestrictedSupport = %q, want always", name, opts.RestrictedSupport)
	}
	if len(opts.Models) != 0 || opts.DefaultModel != "" {
		t.Errorf("%s models %v default %q, want none (decision 9)", name, opts.Models, opts.DefaultModel)
	}
}
