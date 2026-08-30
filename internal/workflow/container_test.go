package workflow

import (
	"strings"
	"testing"
)

// TestContainerizedWorkflowRefusesPwshAtLoad is decision 8's first half: a
// workflow that pins its own image is the one case load-time validation can
// judge, because the image is right there in the file.
func TestContainerizedWorkflowRefusesPwshAtLoad(t *testing.T) {
	src := `
name: build
defaults:
  container:
    image: ghcr.io/example/dev:1
steps:
  - id: compile
    type: command
    run: go build ./...
    shell: pwsh
`
	_, _, err := Parse([]byte(strings.TrimSpace(src)), Options{})
	if err == nil {
		t.Fatal("a pwsh step in an image-pinning workflow loaded")
	}
	if !strings.Contains(err.Error(), "compile") {
		t.Errorf("the error must name the step, got %v", err)
	}
	if !strings.Contains(err.Error(), "pwsh") {
		t.Errorf("the error must name the shell, got %v", err)
	}
}

// TestUncontainerizedWorkflowKeepsPwsh is the other half. Containerization
// also resolves from config.yaml, which is hot-reloadable and which a workflow
// being parsed knows nothing about — so a workflow with no image of its own is
// judged at task creation, not here.
func TestUncontainerizedWorkflowKeepsPwsh(t *testing.T) {
	src := `
name: build
steps:
  - id: compile
    type: command
    run: go build ./...
    shell: pwsh
`
	if _, _, err := Parse([]byte(strings.TrimSpace(src)), Options{}); err != nil {
		t.Fatalf("a workflow with no image must still load: %v", err)
	}
}

// TestContainerShellConflictsFindsNestedSteps keeps the refusal honest for the
// structure steps: a group member and a loop body are steps too.
func TestContainerShellConflictsFindsNestedSteps(t *testing.T) {
	src := `
name: build
steps:
  - id: group
    type: parallel
    steps:
      - id: inner
        type: command
        run: go vet ./...
        shell: cmd
`
	wf, _, err := Parse([]byte(strings.TrimSpace(src)), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	conflicts := ContainerShellConflicts(wf)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want exactly one", conflicts)
	}
	if !strings.Contains(conflicts[0].Message, "inner") {
		t.Errorf("the conflict must name the step, got %q", conflicts[0].Message)
	}
}

// TestWorkflowContainerOverrideRoundTrips pins that `defaults.container:`
// parses into the §8.6 override rather than being silently dropped by strict
// decoding.
func TestWorkflowContainerOverrideRoundTrips(t *testing.T) {
	src := `
name: build
defaults:
  container:
    image: ghcr.io/example/dev:1
    network: false
steps:
  - id: compile
    type: command
    run: go build ./...
`
	wf, _, err := Parse([]byte(strings.TrimSpace(src)), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c := wf.Defaults.Container
	if c == nil || c.Image == nil || *c.Image != "ghcr.io/example/dev:1" {
		t.Fatalf("image did not round-trip: %+v", c)
	}
	if c.Network == nil || *c.Network {
		t.Errorf("network: false did not round-trip: %+v", c)
	}
	if c.MountAgentConfig != nil {
		t.Errorf("an unset key must stay unset, got %v", *c.MountAgentConfig)
	}
}
