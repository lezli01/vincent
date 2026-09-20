package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// The init line's `skills` array (task 124.16, #512): the one machine signal
// that separates claude's bundled skills from its built-in commands, both of
// which its `initialize` reply marks `builtin`.

// TestParseInitReadsSkills proves parseInit carries the array through, in the
// CLI's order, from every capture that has one — and leaves it empty for the
// 2.1.226 captures, which predate the field.
func TestParseInitReadsSkills(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		// count is the array's length in the capture, so a re-capture that
		// changes it fails here rather than silently weakening the test.
		count int
		// first is the array's opening name, which pins the order.
		first string
	}{
		{fixture: "stream_subagent_sync_2.1.263.jsonl", count: 60, first: "find-skills"},
		{fixture: "stream_subagent_async_2.1.268.jsonl", count: 84, first: "find-skills"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			h := runHeaderOf(t, tc.fixture)
			if len(h.Skills) != tc.count || h.Skills[0] != tc.first {
				t.Fatalf("skills = %d names starting %q, want %d starting %q",
					len(h.Skills), first(h.Skills), tc.count, tc.first)
			}
			if !slices.Equal(h.Skills, fixtureInitArray(t, tc.fixture, "skills")) {
				t.Error("skills is not the capture's array in the CLI's order")
			}
			// What the array is for: a bundled skill is in it and a built-in
			// command is not, while `slash_commands` holds both.
			for _, bundled := range []string{"simplify", "loop", "run"} {
				if !slices.Contains(h.Skills, bundled) {
					t.Errorf("the capture no longer names the bundled skill %q", bundled)
				}
			}
			for _, command := range []string{"clear", "compact"} {
				if slices.Contains(h.Skills, command) {
					t.Errorf("%q is a built-in command and must never be in `skills`", command)
				}
				if !slices.Contains(fixtureInitArray(t, tc.fixture, "slash_commands"), command) {
					t.Errorf("the capture no longer holds %q in `slash_commands`", command)
				}
			}
		})
	}
	for _, fixture := range []string{
		"stream_permission_allow_2.1.226.jsonl",
		"stream_question_2.1.226.jsonl",
	} {
		t.Run(fixture, func(t *testing.T) {
			if h := runHeaderOf(t, fixture); len(h.Skills) != 0 {
				t.Errorf("skills = %v, want none: this build sends no such array", h.Skills)
			}
		})
	}
}

// TestInitLineDecodesNothingElse: the init line's four other arrays stay
// undecoded. `slash_commands` above all — it mixes the built-in commands back
// in, which is the one thing `skills` exists to keep out — and nothing reads
// `terminal_slash_commands`, `agents` or `plugins` either.
func TestInitLineDecodesNothingElse(t *testing.T) {
	line := `{"type":"system","subtype":"init","cwd":"/w","tools":["Bash"],` +
		`"skills":["simplify"],"slash_commands":["clear","simplify"],` +
		`"terminal_slash_commands":["vim"],"agents":["general"],"plugins":["demo"]}`
	ev := parseLine([]byte(line))
	if ev.Type != agent.EventRunHeader || ev.Header == nil {
		t.Fatalf("got %q, want the run header", ev.Type)
	}
	if !slices.Equal(ev.Header.Skills, []string{"simplify"}) {
		t.Fatalf("skills = %v, want just the `skills` array", ev.Header.Skills)
	}
	// The struct is what decides: a `slash_commands` field would put `clear`
	// somewhere, and there is nowhere for it to go.
	var probe streamLine
	if err := json.Unmarshal([]byte(line), &probe); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(probe.Skills, "clear") || slices.Contains(probe.Tools, "clear") {
		t.Error("a command name reached the header: `slash_commands` is being decoded")
	}
}

// runHeaderOf is the run header a capture opens with.
func runHeaderOf(t *testing.T, fixture string) *agent.RunHeader {
	t.Helper()
	events, _ := fixtureEvents(t, fixture)
	if len(events) == 0 || events[0].Type != agent.EventRunHeader || events[0].Header == nil {
		t.Fatalf("%s does not open with a run header", fixture)
	}
	return events[0].Header
}

// fixtureInitArray reads one array off a capture's first line, decoded the
// way a reader who trusts nothing in stream.go would: generically.
func fixtureInitArray(t *testing.T, fixture, key string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(b), "\n")
	var line map[string]any
	if err := json.Unmarshal([]byte(first), &line); err != nil {
		t.Fatalf("%s: %v", fixture, err)
	}
	raw, _ := line[key].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

func first(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}
