package main_test

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// FAKEAGENT_ASK_QUESTIONS exists for scripts/screenshots.sh, which CI does
// not run, so the replacement is pinned here: the control request carries
// exactly the questions it was given, and a value that does not parse leaves
// the built-in question in place rather than asking nothing.
func TestAskQuestionsReplaceTheBuiltIns(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	custom := `[{"question":"Restore from where?","header":"Source","options":[` +
		`{"label":"Snapshot","description":"fast"},{"label":"Base backup","description":"slow"}],"multiSelect":false}]`

	got := askedQuestions(t, bin, "FAKEAGENT_ASK_QUESTIONS="+custom)
	if len(got) != 1 || got[0]["question"] != "Restore from where?" || got[0]["header"] != "Source" {
		t.Fatalf("asked %v, want the one custom question", got)
	}

	got = askedQuestions(t, bin, "FAKEAGENT_ASK_QUESTIONS=not json")
	if len(got) != 1 || got[0]["question"] != "Which color do you prefer?" {
		t.Fatalf("asked %v, want the built-in question for an unparseable value", got)
	}
}

// askedQuestions runs the ask-question scenario in the daemon's stream-json
// input mode with stdin closed after the prompt — it exits nonzero once no
// answer can arrive; without that mode it would wait forever — and returns
// the questions its control request carried.
func askedQuestions(t *testing.T, bin string, env ...string) []map[string]any {
	t.Helper()
	cmd := exec.Command(bin, append(claudeArgs, "--input-format", "stream-json")...)
	cmd.Stdin = strings.NewReader(`{"type":"user","message":{"content":[{"type":"text","text":"do a thing"}]}}` + "\n")
	cmd.Env = append(cmd.Environ(), append([]string{"FAKEAGENT_SCENARIO=ask-question"}, env...)...)
	out, _ := cmd.Output() // the exit is the closed stdin, not a failure here
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		var line struct {
			Type    string `json:"type"`
			Request struct {
				Input struct {
					Questions []map[string]any `json:"questions"`
				} `json:"input"`
			} `json:"request"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Type == "control_request" {
			return line.Request.Input.Questions
		}
	}
	t.Fatalf("no control_request in:\n%s", out)
	return nil
}
