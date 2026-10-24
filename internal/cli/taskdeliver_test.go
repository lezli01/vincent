package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestDeliverTaskID: --task wins; otherwise the step's VINCENT_TASK_ID; and
// outside a step with neither, the error names both.
func TestDeliverTaskID(t *testing.T) {
	t.Setenv(envTaskID, "42")
	if id, err := deliverTaskID(7); err != nil || id != 7 {
		t.Errorf("--task 7 = %d, %v; want 7", id, err)
	}
	if id, err := deliverTaskID(0); err != nil || id != 42 {
		t.Errorf("from the environment = %d, %v; want 42", id, err)
	}
	t.Setenv(envTaskID, "x")
	if _, err := deliverTaskID(0); err == nil || !strings.Contains(err.Error(), envTaskID) {
		t.Errorf("a malformed %s = %v, want an error naming it", envTaskID, err)
	}
	t.Setenv(envTaskID, "")
	if _, err := deliverTaskID(0); err == nil || !strings.Contains(err.Error(), "--task") {
		t.Errorf("outside a step = %v, want an error naming --task", err)
	}
}

// TestPrintDelivery: `task show`'s row and `deliver --task`'s block say the
// kind, whether the daemon wrote it, the short head and the title, with the
// long fields after.
func TestPrintDelivery(t *testing.T) {
	d := &apiclient.Delivery{
		Kind: "change", Implicit: true, HeadSHA: "0123456789abcdef0123", Title: "Fix the leak",
		Branch: "vincent/1-fix", Base: "main", Body: "## What\nFixed.",
	}
	if got := deliveryLine(d); got != "change (implicit) at 0123456789ab: Fix the leak" {
		t.Errorf("deliveryLine = %q", got)
	}
	var b bytes.Buffer
	printDelivery(&b, d)
	out := b.String()
	for _, want := range []string{"delivery  change (implicit)", "branch    vincent/1-fix", "base      main", "body:\n## What\nFixed."} {
		if !strings.Contains(out, want) {
			t.Errorf("printDelivery output lacks %q:\n%s", want, out)
		}
	}
	b.Reset()
	printDelivery(&b, nil)
	if b.Len() != 0 {
		t.Errorf("a nil delivery printed %q", b.String())
	}
}
