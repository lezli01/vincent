package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1134: the repair, follow-up and new-chat forms send one request at a time,
// the way #1052 made the new-task and project forms do. A second ctrl+s while
// the first is in flight sends nothing; a failure frees the form to resend.

func TestRepairFormSubmitsOnceWhileInFlight(t *testing.T) {
	f := newRepairForm(7, "check_failed", "build")
	f.prompt = "fix the test"
	client := apiclient.New("http://127.0.0.1:1", "token") // never dialled: the command is not run
	if cmd := f.submit(client); cmd == nil || !f.submitting {
		t.Fatalf("the first submit sent nothing (err %q)", f.err)
	}
	if cmd := f.submit(client); cmd != nil {
		t.Fatal("a second submit while the first is in flight sent another repair")
	}
	if out := f.render(80, 40); !strings.Contains(out, "starting the repair agent…") {
		t.Errorf("the form does not say the repair is in flight:\n%s", out)
	}
	f.submitting = false // what the detail view does with a refusal
	if cmd := f.submit(client); cmd == nil {
		t.Fatal("the form cannot resubmit after a failure")
	}
}

func TestFollowUpFormSubmitsOnceWhileInFlight(t *testing.T) {
	f := newFollowUpForm(7, 1, "done")
	f.setBody("rerun it")
	client := apiclient.New("http://127.0.0.1:1", "token")
	if cmd := f.submit(client); cmd == nil || !f.submitting {
		t.Fatalf("the first submit sent nothing (err %q)", f.err)
	}
	if cmd := f.submit(client); cmd != nil {
		t.Fatal("a second submit while the first is in flight sent another follow-up")
	}
	if out := f.render(80, 40); !strings.Contains(out, "queueing the follow-up…") {
		t.Errorf("the form does not say the follow-up is in flight:\n%s", out)
	}
	f.submitting = false
	if cmd := f.submit(client); cmd == nil {
		t.Fatal("the form cannot resubmit after a failure")
	}
}

func TestNewChatFormSubmitsOnceWhileInFlight(t *testing.T) {
	f := chatFormWithCatalogs()
	f.client = apiclient.New("http://127.0.0.1:1", "token")
	f.title.SetValue("a chat")
	if cmd := f.submit(); cmd == nil || !f.submitting {
		t.Fatalf("the first submit sent nothing (err %q)", f.err)
	}
	if cmd := f.submit(); cmd != nil {
		t.Fatal("a second submit while the first is in flight created another chat")
	}
	if out := f.render(100, 40); !strings.Contains(out, "creating…") {
		t.Errorf("the form does not say the create is in flight:\n%s", out)
	}
	f.applyFailure(errors.New("down"))
	if cmd := f.submit(); cmd == nil {
		t.Fatal("the form cannot resubmit after a failure")
	}
}
