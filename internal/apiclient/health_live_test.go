package apiclient_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestAgentHealthFacetsRoundTrip wires the client to the real handlers and
// asserts the five §9.5 health facets task 040 named arrive on both endpoints
// that carry them. It is the drift guard: the server DTOs are unexported, so
// nothing but a live round-trip catches a field renamed on one side.
//
// The harness runs claude against the fake agent, whose --version reports a
// fixture-verified build, and points codex at a path that does not exist.
func TestAgentHealthFacetsRoundTrip(t *testing.T) {
	h := newCreateHarness(t)

	agents, err := h.client.ListAgents(t.Context(), false)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	cl, ok := agents.Find("claude")
	if !ok {
		t.Fatalf("claude missing from %+v", agents)
	}
	// installed, authenticated, protocol-compatible (both halves),
	// permission-compatible. Model-catalog health is ProbeError, which §9.6
	// already defines — it is not duplicated as a verdict.
	if !cl.Available {
		t.Fatalf("claude unavailable: %s", cl.Error)
	}
	if cl.VersionVerdict != apiclient.VersionVerdictTested {
		t.Errorf("VersionVerdict = %q, want %q (version %q)",
			cl.VersionVerdict, apiclient.VersionVerdictTested, cl.Version)
	}
	if cl.TestedVersions == "" {
		t.Error("TestedVersions is empty; an untested row would have nothing to name")
	}
	if cl.RestrictedVerdict != apiclient.RestrictedVerdictSupported {
		t.Errorf("RestrictedVerdict = %q, want %q", cl.RestrictedVerdict, apiclient.RestrictedVerdictSupported)
	}
	if cl.CannotRestrict() {
		t.Error("CannotRestrict = true for claude, which restricts on every platform")
	}
	if cl.InputVerdict != apiclient.InputVerdictSupported {
		t.Errorf("InputVerdict = %q, want %q", cl.InputVerdict, apiclient.InputVerdictSupported)
	}

	// An adapter with nothing installed has no build to judge — and still a
	// restricted verdict, because that answer does not depend on a binary.
	cx, ok := agents.Find("codex")
	if !ok {
		t.Fatalf("codex missing from %+v", agents)
	}
	if cx.VersionVerdict != "" {
		t.Errorf("VersionVerdict = %q for a missing binary, want no judgement", cx.VersionVerdict)
	}
	if cx.RestrictedVerdict != apiclient.RestrictedVerdictSupported {
		t.Errorf("RestrictedVerdict = %q for a missing binary, want %q",
			cx.RestrictedVerdict, apiclient.RestrictedVerdictSupported)
	}

	info, err := h.client.Info(t.Context())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	var found bool
	for _, a := range info.Agents {
		if a.Name != "claude" {
			continue
		}
		found = true
		if a.VersionVerdict != apiclient.VersionVerdictTested {
			t.Errorf("/v1/info VersionVerdict = %q, want %q", a.VersionVerdict, apiclient.VersionVerdictTested)
		}
		if a.RestrictedVerdict != apiclient.RestrictedVerdictSupported {
			t.Errorf("/v1/info RestrictedVerdict = %q, want %q",
				a.RestrictedVerdict, apiclient.RestrictedVerdictSupported)
		}
		if a.TestedVersions == "" {
			t.Error("/v1/info TestedVersions is empty")
		}
	}
	if !found {
		t.Fatal("claude missing from /v1/info")
	}
}
