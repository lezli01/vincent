package apiclient_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/workflow"
)

// The clients split on the tag the daemon writes. Going through
// workflow.AppendFailureBlock rather than a hand-written block is the point:
// a renamed tag fails here instead of silently unmarking the join on both the
// TUI and the CLI.
func TestSplitFailureTrailerRoundTrip(t *testing.T) {
	const prompt = "Implement the change described in OPS-42."
	appended := workflow.AppendFailureBlock(prompt, 2,
		workflow.Failure{Reason: "check command failed (exit 1)", Output: "line1\nline2\n"})

	body, trailer := apiclient.SplitFailureTrailer(appended)
	if body != prompt {
		t.Errorf("body = %q, want the workflow's own render %q", body, prompt)
	}
	if trailer == "" || body+"\n\n"+trailer != appended {
		t.Errorf("trailer = %q, want the whole appended block of %q", trailer, appended)
	}

	// A first attempt has nothing appended, so nothing is split off.
	first := workflow.AppendFailureBlock(prompt, 1, workflow.Failure{Reason: "unused"})
	if body, trailer := apiclient.SplitFailureTrailer(first); body != prompt || trailer != "" {
		t.Errorf("first attempt split = (%q, %q), want the prompt and no trailer", body, trailer)
	}
}
