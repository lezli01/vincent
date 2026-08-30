package config

import (
	"reflect"
	"testing"
)

// TestContainerDefaultIsTheHost is the regression that matters most: an
// installation that names no image behaves exactly as it did before task 061.
func TestContainerDefaultIsTheHost(t *testing.T) {
	c := Default().Container
	if c.Enabled() {
		t.Fatalf("container is enabled by default: %+v", c)
	}
	if c.RuntimeBinary() != "docker" {
		t.Errorf("default runtime = %q, want docker", c.RuntimeBinary())
	}
	if !c.MountAgentConfig || !c.Network {
		t.Errorf("mounts and network should default on, got %+v", c)
	}
}

// TestContainerMergeIsPerField pins the §8.6 precedence shape: a workflow that
// sets one key does not restate the block, and `image: ""` from a workflow is
// a real value meaning "run this one on the host".
func TestContainerMergeIsPerField(t *testing.T) {
	base := Container{Image: "ghcr.io/x:1", Runtime: "docker", MountAgentConfig: true, Network: true}
	empty := ""
	off := false
	if got := base.Merge(nil); !reflect.DeepEqual(got, base) {
		t.Errorf("Merge(nil) changed the block: %+v", got)
	}
	if got := base.Merge(&ContainerOverride{Network: &off}); got.Image != base.Image || got.Network {
		t.Errorf("Merge network-only = %+v", got)
	}
	got := base.Merge(&ContainerOverride{Image: &empty})
	if got.Enabled() {
		t.Errorf("a workflow that sets image: \"\" must run on the host, got %+v", got)
	}
	if !got.MountAgentConfig {
		t.Errorf("Merge overwrote an unset field: %+v", got)
	}
}

func TestContainerValidate(t *testing.T) {
	cases := []struct {
		name    string
		c       Container
		wantErr bool
	}{
		{"empty", Container{}, false},
		{"good mount", Container{ExtraMounts: []string{"/a:/b"}}, false},
		{"good ro mount", Container{ExtraMounts: []string{"/a:/b:ro"}}, false},
		{"relative host", Container{ExtraMounts: []string{"a:/b"}}, true},
		{"relative target", Container{ExtraMounts: []string{"/a:b"}}, true},
		{"bad mode", Container{ExtraMounts: []string{"/a:/b:rx"}}, true},
		{"one part", Container{ExtraMounts: []string{"/a"}}, true},
		{"runtime with a space", Container{Runtime: "docker --context x"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.c.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestContainerEnvironmentRereadsInheritAll is decision 7. A macOS or Linux
// host's PATH, HOME, TMPDIR and SHELL inside a Linux image is a broken
// container, not an inherited one — so the default reads as `none`, while an
// explicit list is honoured verbatim.
func TestContainerEnvironmentRereadsInheritAll(t *testing.T) {
	got := ContainerEnvironment(Environment{Inherit: InheritAll()})
	if got.Inherit.Mode != InheritNoneMode {
		t.Errorf("inherit: all should read as none in a container, got %v", got.Inherit.Mode)
	}
	list := Environment{
		Inherit: Inherit{Mode: InheritListMode, Names: []string{"TERM"}},
		Set:     map[string]string{"CI": "1"},
		Unset:   []string{"MSYSTEM"},
	}
	kept := ContainerEnvironment(list)
	if kept.Inherit.Mode != InheritListMode || len(kept.Inherit.Names) != 1 {
		t.Errorf("an explicit list must be honoured verbatim, got %+v", kept.Inherit)
	}
	if kept.Set["CI"] != "1" || len(kept.Unset) != 1 {
		t.Errorf("set/unset must survive the reinterpretation, got %+v", kept)
	}
}

// TestContainerEnvironmentTakesNothingFromTheHost is the same decision
// observed at the only place it can bite: what a step actually gets.
func TestContainerEnvironmentTakesNothingFromTheHost(t *testing.T) {
	e := ContainerEnvironment(Environment{Inherit: InheritAll(), Set: map[string]string{"K": "v"}})
	got := e.Resolve([]string{"PATH=/host/bin", "HOME=/Users/x"})
	for _, kv := range got {
		if kv == "PATH=/host/bin" || kv == "HOME=/Users/x" {
			t.Fatalf("a host variable reached a containerized step: %q", got)
		}
	}
	if len(got) != 1 || got[0] != "K=v" {
		t.Errorf("resolved = %q, want exactly [K=v]", got)
	}
}
