package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestResumeSettingsChangeCapture pins agent.ResumeSettingsChanger to the
// capture that justifies it (task 162 decision 8): a session begun on haiku,
// resumed with `--model sonnet --effort low`, ran on sonnet under the same
// session id. Were a future build to keep the session's own model, a new
// capture would show it and this capability would go false with it.
func TestResumeSettingsChangeCapture(t *testing.T) {
	const fixture = "resume_settings_2.1.329.jsonl"
	const session = "71aa896d-fd17-4562-869b-94a84a62bfb7"
	f, err := os.Open(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var initModel, replyModel, initSession string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		var rec struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			Model     string `json:"model"`
			SessionID string `json:"session_id"`
			Message   struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		switch {
		case rec.Type == "system" && rec.Subtype == "init":
			initModel, initSession = rec.Model, rec.SessionID
		case rec.Type == "assistant":
			replyModel = rec.Message.Model
		}
	}
	if initSession != session {
		t.Fatalf("init session = %q, want the resumed %q", initSession, session)
	}
	if initModel != "claude-sonnet-5-5" || replyModel != "claude-sonnet-5-5" {
		t.Errorf("resumed run's model = init %q / reply %q, want claude-sonnet-5-5 (the changed one)",
			initModel, replyModel)
	}
	events, _ := fixtureEvents(t, fixture)
	if !slices.ContainsFunc(events, func(e agent.Event) bool { return e.Type == agent.EventResult }) {
		t.Error("capture does not parse to a result")
	}
	if !agent.CanChangeOnResume(New(nil)) {
		t.Error("claude: CanChangeOnResume = false, want true from the capture")
	}
}

// TestResumeArgvCarriesChangedSettings: a resumed turn with a changed model
// and effort puts both beside --resume, the argv the capture ran.
func TestResumeArgvCarriesChangedSettings(t *testing.T) {
	args, err := buildArgs(agent.RunSpec{
		ResumeSessionID: "s1", Model: "sonnet", Effort: "low", PermissionMode: agent.FullAuto,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"--resume", "s1"}, {"--model", "sonnet"}, {"--effort", "low"}} {
		i := slices.Index(args, pair[0])
		if i < 0 || i+1 >= len(args) || args[i+1] != pair[1] {
			t.Errorf("argv %v: want %s %s", args, pair[0], pair[1])
		}
	}
}
