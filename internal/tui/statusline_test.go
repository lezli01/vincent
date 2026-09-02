package tui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// testExe is a vincent path with a space in it, which is the case the quoting
// in the argv contract exists for: macOS puts things under "Application
// Support" and nothing stops a binary living there.
const testExe = "/Users/x/Application Support/bin/vincent"

// claudeSettings writes a settings file under a throwaway home and answers its
// path. An empty body means the common case: no file at all.
func claudeSettings(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	if body == "" {
		return path
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return path
}

// readSettings decodes the file back the way the plan reads it: raw values, so
// an assertion can look at the exact bytes of a key vincent claims not to have
// touched.
func readSettings(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("parse settings %s: %v", b, err)
	}
	return out
}

// sameJSON compares two fragments as values rather than as bytes. That is the
// bar the write is held to: encoding/json sorts keys and re-indents, and the
// brief allows exactly that much reformatting and no more.
func sameJSON(t *testing.T, got, want json.RawMessage) bool {
	t.Helper()
	var a, b bytes.Buffer
	if err := json.Compact(&a, got); err != nil {
		t.Fatalf("compact %s: %v", got, err)
	}
	if err := json.Compact(&b, want); err != nil {
		t.Fatalf("compact %s: %v", want, err)
	}
	return a.String() == b.String()
}

// pressDaemon sends a key and runs whatever command it produced, which is what the
// bubbletea runtime does with it. The status-line flow re-reads the settings
// file on the way out, and a test that dropped the command would be asserting
// against the view's memory of a file it has just changed.
func pressDaemon(t *testing.T, d *daemonView, key string) {
	t.Helper()
	_, cmd := d.updateKey(registryKey(t, key))
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		d.update(msg)
	}
}

func mustPlan(t *testing.T, path string) statusLinePlan {
	t.Helper()
	p, err := readStatusLinePlan(path, testExe)
	if err != nil {
		t.Fatalf("readStatusLinePlan: %v", err)
	}
	return p
}

// The wrap half of the argv contract: an existing status line is carried in
// the payload rather than replaced, the payload is RawURLEncoding of that
// object's own bytes, and the binary path is quoted.
func TestStatusLineInstallWrapsAnExistingCommand(t *testing.T) {
	const original = `{"type":"command","command":"~/bin/mine.sh --flag"}`
	path := claudeSettings(t, `{
  "model": "opus",
  "permissions": {"allow": ["Bash(git:*)"]},
  "statusLine": `+original+`
}`)

	if err := mustPlan(t, path).install(); err != nil {
		t.Fatalf("install: %v", err)
	}

	settings := readSettings(t, path)
	var got struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(settings[statusLineKey], &got); err != nil {
		t.Fatalf("parse the written statusLine: %v", err)
	}
	if got.Type != "command" {
		t.Errorf("statusLine.type = %q, want %q", got.Type, "command")
	}
	prefix := `"` + testExe + `" statusline ` + statusLineWrapFlag + " "
	payload, ok := strings.CutPrefix(got.Command, prefix)
	if !ok {
		t.Fatalf("command = %q, want the quoted binary and %s", got.Command, statusLineWrapFlag)
	}
	// Unquoted-safe in both /bin/sh and pwsh is the whole reason for
	// RawURLEncoding, so the token is asserted, not assumed.
	if strings.ContainsAny(payload, "+/= '\"") {
		t.Errorf("payload %q carries a character that needs quoting in a shell", payload)
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("the payload is not RawURLEncoding: %v", err)
	}
	if string(raw) != original {
		t.Errorf("payload decodes to %s, want the original object %s", raw, original)
	}

	// Everything else in the file is somebody else's.
	if !sameJSON(t, settings["model"], json.RawMessage(`"opus"`)) {
		t.Errorf("model = %s, want it untouched", settings["model"])
	}
	if !sameJSON(t, settings["permissions"],
		json.RawMessage(`{"allow":["Bash(git:*)"]}`)) {
		t.Errorf("permissions = %s, want it untouched", settings["permissions"])
	}
}

// Reversibility, which is the term §16's write was accepted on.
func TestStatusLineUninstallRestoresWhatWasThere(t *testing.T) {
	const original = `{"type":"command","command":"~/bin/mine.sh --flag","padding":0}`
	path := claudeSettings(t, `{"model":"opus","statusLine":`+original+`}`)

	if err := mustPlan(t, path).install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	installed := mustPlan(t, path)
	if !installed.installed {
		t.Fatal("the plan does not recognise vincent's own status line")
	}
	if err := installed.uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	settings := readSettings(t, path)
	if !sameJSON(t, settings[statusLineKey], json.RawMessage(original)) {
		t.Errorf("statusLine = %s, want the original back verbatim: %s",
			settings[statusLineKey], original)
	}
	if !sameJSON(t, settings["model"], json.RawMessage(`"opus"`)) {
		t.Errorf("model = %s, want it untouched", settings["model"])
	}
}

// "There was none" is a state to restore too: the key goes away rather than
// being left behind holding vincent, or an empty object.
func TestStatusLineUninstallRestoresAnAbsence(t *testing.T) {
	path := claudeSettings(t, `{"model":"opus"}`)

	plan := mustPlan(t, path)
	if len(plan.current) != 0 {
		t.Fatalf("current = %s, want no statusLine to start with", plan.current)
	}
	if err := plan.install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	installed := mustPlan(t, path)
	if !installed.installed {
		t.Fatal("the plan does not recognise vincent's own status line")
	}
	if len(installed.restore) != 0 {
		t.Fatalf("restore = %s, want nothing to put back", installed.restore)
	}
	// With nothing wrapped the command carries no flag at all, which is what
	// tells the CLI side there was no prior status line.
	if want := `"` + testExe + `" statusline`; installed.command() != want {
		t.Errorf("command = %q, want %q", installed.command(), want)
	}
	if err := installed.uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	settings := readSettings(t, path)
	if _, ok := settings[statusLineKey]; ok {
		t.Errorf("statusLine = %s, want the key gone", settings[statusLineKey])
	}
	if !sameJSON(t, settings["model"], json.RawMessage(`"opus"`)) {
		t.Errorf("model = %s, want it untouched", settings["model"])
	}
}

// A missing settings file is the common case, not an error, and installing
// into one creates it with nothing else in it.
func TestStatusLineInstallCreatesAMissingSettingsFile(t *testing.T) {
	path := claudeSettings(t, "")
	plan := mustPlan(t, path)
	if plan.exists {
		t.Fatal("a missing settings file was read as existing")
	}
	if err := plan.install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	settings := readSettings(t, path)
	if len(settings) != 1 {
		t.Errorf("settings = %v, want only the statusLine key", settings)
	}
}

// A file vincent cannot parse is a file vincent will not rewrite: every key it
// failed to read would be dropped by the write.
func TestStatusLinePlanRefusesAnUnparseableFile(t *testing.T) {
	path := claudeSettings(t, `{"model": "opus"`)
	if _, err := readStatusLinePlan(path, testExe); err == nil {
		t.Fatal("readStatusLinePlan on a broken settings file = nil error")
	}
}

// A wrapped payload that will not decode must not be resolved by throwing the
// wrapped status line away.
func TestStatusLineUninstallRefusesAnUndecodableWrap(t *testing.T) {
	path := claudeSettings(t, `{"statusLine":{"type":"command",`+
		`"command":"\"/opt/vincent\" statusline --wrap-b64 not-base64!"}}`)
	plan := mustPlan(t, path)
	if !plan.installed {
		t.Fatal("the plan does not recognise a vincent status line at another path")
	}
	if plan.restoreErr == nil {
		t.Fatal("a payload that does not decode was accepted")
	}
	if err := plan.uninstall(); err == nil {
		t.Fatal("uninstall deleted a status line it could not restore")
	}
	if got := readSettings(t, path)[statusLineKey]; len(got) == 0 {
		t.Error("the refused uninstall still changed the file")
	}
}

func TestCommandIsVincent(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want bool
	}{
		{`"/Users/x/Application Support/bin/vincent" statusline`, true},
		{`"/opt/bin/vincent" statusline --wrap-b64 eyJhIjoxfQ`, true},
		{`C:\Program\vincent.exe statusline`, true},
		{`vincent statusline`, true},
		{`"/opt/bin/vincent" doctor`, false},
		{`"/opt/bin/vincentine" statusline`, false},
		{`~/bin/mine.sh --flag`, false},
		{`"/opt/bin/vincent statusline`, false},
		{``, false},
	} {
		if got := commandIsVincent(tc.cmd); got != tc.want {
			t.Errorf("commandIsVincent(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

// statusLineFixture is a daemon view with claude available and one reading of
// a settings file that is this test's, never the developer's.
func statusLineFixture(t *testing.T, dataDir, path string) *daemonView {
	t.Helper()
	d := newTestDaemonView(nil, nil)
	d.dataDir = dataDir
	d.settingsPath = func() (string, error) { return path, nil }
	d.exePath = func() (string, error) { return testExe, nil }
	d.update(daemonInfoMsg{info: testInfo()})
	cmd := d.statusLineCmd()
	if cmd == nil {
		t.Fatal("statusLineCmd = nil")
	}
	d.update(cmd())
	return d
}

// The §16 term that carries the rest: nothing is written until the exact JSON
// has been on screen.
func TestStatusLineFlowPreviewsTheExactJSONItWrites(t *testing.T) {
	const original = `{"type":"command","command":"~/bin/mine.sh"}`
	path := claudeSettings(t, `{"statusLine":`+original+`}`)
	d := statusLineFixture(t, ackedDir(t), path)

	if line, ok := d.statusLineLine(); !ok || !strings.Contains(line, "status line") {
		t.Fatalf("the daemon view does not offer the status line: %q", line)
	}

	d.updateKey(registryKey(t, "i"))
	if d.statusLine == nil {
		t.Fatal("i did not open the status-line flow")
	}
	screen := strings.Join(d.statusLine.render(100), "\n")
	// The preview is the bytes, not a description of them: the command a
	// human reads on screen is the command that lands in the file.
	want := d.statusLinePlan.command()
	quoted, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal the command: %v", err)
	}
	for _, frag := range []string{`"statusLine": {`, `"command": ` + string(quoted), path} {
		if !strings.Contains(screen, frag) {
			t.Errorf("the preview omits %q:\n%s", frag, screen)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file is gone before anything was confirmed: %v", err)
	}
	if before := readSettings(t, path); !sameJSON(t, before[statusLineKey], json.RawMessage(original)) {
		t.Fatal("the preview alone changed the file")
	}

	d.updateKey(registryKey(t, "enter"))
	got := readSettings(t, path)[statusLineKey]
	if !sameJSON(t, got, d.statusLinePlan.object()) {
		t.Errorf("wrote %s, want the previewed object %s", got, d.statusLinePlan.object())
	}
	var obj struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("parse the written statusLine: %v", err)
	}
	if obj.Command != want {
		t.Errorf("wrote command %q, want the previewed %q", obj.Command, want)
	}
	if d.statusLine == nil {
		t.Fatal("the flow closed without reporting what it did")
	}
	if !strings.Contains(strings.Join(d.statusLine.render(100), "\n"), "vincent is now") {
		t.Error("the flow does not say the write happened")
	}
}

// The offer is remembered, both ways: a decline survives a restart, and the
// same key still opens the flow for somebody who changes their mind.
func TestStatusLineDeclineSurvivesARestart(t *testing.T) {
	dataDir := ackedDir(t)
	path := claudeSettings(t, `{"model":"opus"}`)

	first := statusLineFixture(t, dataDir, path)
	if _, ok := first.statusLineLine(); !ok {
		t.Fatal("the first launch did not offer the status line")
	}
	first.updateKey(registryKey(t, "i"))
	pressDaemon(t, first, "n")
	if first.statusLine != nil {
		t.Fatal("n did not close the flow")
	}
	if _, ok := first.statusLineLine(); ok {
		t.Error("the offer is still up in the session that declined it")
	}
	if before := readSettings(t, path); len(before) != 1 {
		t.Errorf("declining wrote to the settings file: %v", before)
	}

	second := statusLineFixture(t, dataDir, path)
	if !second.statusLineDeclined {
		t.Fatal("the decline did not survive the restart")
	}
	if line, ok := second.statusLineLine(); ok {
		t.Errorf("the offer came back on the next launch: %q", line)
	}
	// Declining hides the offer, not the key.
	second.updateKey(registryKey(t, "i"))
	if second.statusLine == nil {
		t.Fatal("i no longer opens the flow after a decline")
	}
}

// Once vincent is the status line the view says so, and the same key takes it
// back out: an install with no reachable uninstall is not reversible.
func TestStatusLineOfferBecomesTheRemoval(t *testing.T) {
	const original = `{"type":"command","command":"~/bin/mine.sh"}`
	path := claudeSettings(t, `{"statusLine":`+original+`}`)
	d := statusLineFixture(t, ackedDir(t), path)

	d.updateKey(registryKey(t, "i"))
	d.updateKey(registryKey(t, "enter"))
	// Closing re-reads the file, which is what turns the offer into the
	// removal without anybody pressing R.
	pressDaemon(t, d, "esc")
	if d.statusLine != nil {
		t.Fatal("the flow did not close")
	}
	line, ok := d.statusLineLine()
	if !ok || !strings.Contains(line, "remove") {
		t.Fatalf("the view does not offer the removal: %q, ok=%v", line, ok)
	}

	d.updateKey(registryKey(t, "i"))
	screen := strings.Join(d.statusLine.render(100), "\n")
	// Verbatim, which is the promise: the restore preview shows the bytes
	// that go back, not a re-encoding of them.
	if !strings.Contains(screen, original) {
		t.Errorf("the removal does not show what comes back:\n%s", screen)
	}
	d.updateKey(registryKey(t, "enter"))
	if !sameJSON(t, readSettings(t, path)[statusLineKey], json.RawMessage(original)) {
		t.Error("the removal did not restore the original status line")
	}
}

// The offer reads the machine, not a guess about it: no claude, no offer.
func TestStatusLineOfferNeedsClaude(t *testing.T) {
	path := claudeSettings(t, `{"model":"opus"}`)
	d := statusLineFixture(t, ackedDir(t), path)
	d.update(daemonInfoMsg{info: apiclient.Info{Agents: []apiclient.AgentStatus{
		{Name: "claude", Available: false},
		{Name: "codex", Available: true},
	}}})
	if line, ok := d.statusLineLine(); ok {
		t.Errorf("offered claude's status line on a machine without claude: %q", line)
	}
}

// A settings file that could not be read still opens on `i` — the key is
// documented — and says what is wrong instead of offering a write.
func TestStatusLineFlowReportsAnUnreadableFile(t *testing.T) {
	path := claudeSettings(t, `{"model": "opus"`)
	d := statusLineFixture(t, ackedDir(t), path)
	if _, ok := d.statusLineLine(); ok {
		t.Error("a settings file that will not parse was still offered a write")
	}
	d.updateKey(registryKey(t, "i"))
	screen := strings.Join(d.statusLine.render(100), "\n")
	if !strings.Contains(screen, "parse") {
		t.Errorf("the flow does not say why it cannot act:\n%s", screen)
	}
	d.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.statusLine != nil {
		t.Fatal("enter on an unreadable file left the flow open")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the unparseable file was touched: %v", err)
	}
}
