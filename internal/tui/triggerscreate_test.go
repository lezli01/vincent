package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// #1123: the create prompt picks the source, hides the poll rows for any
// source but command, and sends only what that source takes.
func TestTriggerCreatePicksASource(t *testing.T) {
	v := &triggersView{}
	v.project = projectSel{id: 3, name: "api"}
	v.openCreate()
	f := v.create
	f.command.SetValue("./poll.sh")
	f.focusRow(trigCreateRowSource)
	v.updateCreateKey(namedKey("right")) // issues
	v.updateCreateKey(namedKey("right")) // schedule
	if got := trigCreateSources[f.source].value; got != "schedule" {
		t.Fatalf("two presses of → picked %q, want schedule", got)
	}
	out := ansi.Strip(v.renderCreate(140, 30))
	if strings.Contains(out, "poll every") || !strings.Contains(out, "‹ schedule ›") {
		t.Fatalf("a schedule source still shows the poll rows, or not its name:\n%s", out)
	}
	f.focusRow(trigCreateRowCommand)
	if f.row == trigCreateRowCommand || f.row == trigCreateRowInterval {
		t.Fatal("the cursor stopped on a poll row a schedule does not have")
	}
	req := f.request("nightly")
	if req.SourceType != "schedule" || len(req.Command) != 0 || req.PollInterval != "" {
		t.Fatalf("the request sent %+v, want a schedule with no poll fields", req)
	}
}
