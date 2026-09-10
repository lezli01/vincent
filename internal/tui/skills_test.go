package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The daemon view's §9.8 offer (task 095). It follows task 082's shape, and
// these assert the three things that shape promises: the offer appears when
// there is something to install, a decline silences it, and the decline
// outlives the process.

func skillsReport(states ...string) *apiclient.DoctorReport {
	rep := &apiclient.DoctorReport{}
	for i, s := range states {
		rep.Skills = append(rep.Skills, apiclient.DoctorSkill{
			Name:      []string{"vincent-workflows", "vincent-reviews"}[i],
			State:     s,
			Shipped:   "1.2.0",
			Installed: map[bool]string{true: "1.0.0", false: ""}[s == apiclient.SkillOlder],
		})
	}
	return rep
}

func TestSkillsOfferAppearsWhenOneIsMissing(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	d.doctor = skillsReport(apiclient.SkillAbsent)
	d.doctorOK = true
	line, ok := d.skillsLine()
	if !ok {
		t.Fatal("no offer with a skill missing")
	}
	if !strings.Contains(line, "not installed") || !strings.Contains(line, "S") {
		t.Errorf("offer = %q", line)
	}
}

// An installed and current set still says so. That line is the only place `S`
// is discoverable on the view, and an install with no visible review is a key
// nobody finds.
func TestSkillsLineStaysWhenEverythingIsCurrent(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	d.doctor = skillsReport(apiclient.SkillCurrent, apiclient.SkillCurrent)
	d.doctorOK = true
	line, ok := d.skillsLine()
	if !ok {
		t.Fatal("no line with everything current")
	}
	if !strings.Contains(line, "current") {
		t.Errorf("line = %q", line)
	}
}

// Before a report lands there is nothing to say, and a view that guessed
// would be holding state the daemon does not have.
func TestSkillsLineSilentWithoutAReport(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	if _, ok := d.skillsLine(); ok {
		t.Error("the view offered something before any report landed")
	}
}

// TestSkillsDeclineSurvivesARestart is the whole point of putting the flag in
// tui.json: `n` writes it, the offer goes away, and a second TUI over the
// same data dir never asks again.
func TestSkillsDeclineSurvivesARestart(t *testing.T) {
	dataDir := t.TempDir()
	d := newTestDaemonView(nil, nil)
	d.dataDir = dataDir
	d.doctor = skillsReport(apiclient.SkillAbsent)
	d.doctorOK = true

	if _, ok := d.skillsLine(); !ok {
		t.Fatal("no offer to decline")
	}
	if _, cmd := d.update(key("S")); cmd != nil {
		_ = cmd
	}
	if d.skills == nil {
		t.Fatal("S did not open the flow")
	}
	if !d.capturesInput() {
		t.Error("the flow does not own the keyboard while it is open")
	}
	if _, cmd := d.update(key("n")); cmd != nil {
		// The close re-reads the flag; run the command the way the runtime
		// would and feed the message back in.
		if msg := cmd(); msg != nil {
			d.update(msg)
		}
	}
	if d.skills != nil {
		t.Error("n left the flow open")
	}
	if !d.skillsDeclined {
		t.Fatal("the decline did not reach the view")
	}
	if _, ok := d.skillsLine(); ok {
		t.Error("the offer came back after being declined")
	}

	// A second process over the same data dir.
	again := newTestDaemonView(nil, nil)
	again.dataDir = dataDir
	again.doctor = skillsReport(apiclient.SkillAbsent)
	again.doctorOK = true
	if msg := again.skillsDeclineCmd()(); msg != nil {
		again.update(msg)
	}
	if !again.skillsDeclined {
		t.Fatal("the decline did not survive the restart")
	}
	if _, ok := again.skillsLine(); ok {
		t.Error("a restarted TUI asked again")
	}
}

// The flow shows the exact command before it runs anything, the way the
// status-line flow shows the exact JSON. It is the one screen where vincent
// says what it is about to do outside its own directories.
func TestSkillsFlowShowsTheCommand(t *testing.T) {
	f := newSkillsFlow(skillsReport(apiclient.SkillAbsent).Skills, []string{"claude"})
	body := strings.Join(f.render(80), "\n")
	if !strings.Contains(body, "npx skills add lezli01/vincent --skill vincent-workflows --agent claude-code --yes --global") {
		t.Errorf("the offer does not show the command it runs:\n%s", body)
	}
	if !strings.Contains(body, "enter") || !strings.Contains(body, "esc") {
		t.Errorf("the offer does not print its keys:\n%s", body)
	}
}

// Nothing to install is still a screen: `S` is documented and a documented
// key that silently does nothing is worse than one that explains itself.
func TestSkillsFlowWithNothingToInstall(t *testing.T) {
	f := newSkillsFlow(skillsReport(apiclient.SkillCurrent).Skills, nil)
	body := strings.Join(f.render(80), "\n")
	if !strings.Contains(body, "Nothing to install") {
		t.Errorf("body = %s", body)
	}
	if _, done := f.update(key("enter"), t.TempDir()); !done {
		t.Error("enter did not close a flow with nothing to do")
	}
}

// A failed install reports what the CLI said, and closes on any key.
func TestSkillsFlowReportsAFailure(t *testing.T) {
	f := newSkillsFlow(skillsReport(apiclient.SkillAbsent).Skills, nil)
	f.running = true
	f.applyInstalled(skillsInstalledMsg{output: "npm ERR! nope\n", err: errTestInstall})
	if !f.reporting() || f.running {
		t.Fatal("the flow did not land the attempt")
	}
	body := strings.Join(f.render(80), "\n")
	if !strings.Contains(body, "npm ERR! nope") {
		t.Errorf("the failure hid what the CLI said:\n%s", body)
	}
	if _, done := f.update(key("x"), t.TempDir()); !done {
		t.Error("a reported flow did not close")
	}
}

var errTestInstall = errors.New("npx skills add failed: exit status 1")

// openSkillsFlow is the §9.8 takeover, open over a temp data dir so a probe
// that declines writes into a directory the test owns.
func openSkillsFlow(t *testing.T) *daemonView {
	t.Helper()
	d := newTestDaemonView([]string{"a log line"}, nil)
	d.dataDir = t.TempDir()
	d.doctor = skillsReport(apiclient.SkillCurrent)
	d.doctorOK = true
	d.skills = newSkillsFlow(d.reportedSkills(), nil)
	return d
}
