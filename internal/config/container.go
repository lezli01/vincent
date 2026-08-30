package config

import (
	"fmt"
	"strings"
)

// Container configures running a task's steps inside a container instead of
// on the daemon's host (spec §16, §20 — task 061).
//
// Image is the whole switch. `image: ""` is the default and means today's
// behaviour exactly: the step runs on the host, no runtime is consulted, and
// an existing installation is byte-for-byte unchanged. Everything else in this
// block is inert until an image is named.
//
// The image is the user's. It must already carry the agent CLI a workflow's
// agent steps resolve to, and `git`; vincent builds nothing, publishes nothing
// and bundles nothing — the posture it already takes toward `gh` and `cosign`.
type Container struct {
	// Image is the container image to run in. Empty runs on the host.
	Image string `yaml:"image"`
	// Runtime is a docker-CLI-compatible binary. Only `docker` is verified in
	// CI; podman and nerdctl are accepted because they take the same argv,
	// and the documentation says which of the three is tested.
	Runtime string `yaml:"runtime"`
	// MountAgentConfig bind-mounts the host's agent configuration directories
	// into the container read-write. It defaults to true because
	// subscription-based auth takes no key from the environment, and cursor
	// persists `--model` to its own config (§9.7). Turning it off is
	// supported and its consequence — an agent CLI that cannot authenticate —
	// is documented rather than hidden.
	MountAgentConfig bool `yaml:"mount_agent_config"`
	// Network keeps outbound traffic on, which is the default. False drops
	// the container off the network entirely; combined with
	// `mcp.wire_steps: true` that is a contradiction, and it is refused at
	// task creation rather than producing an agent wired to a dead endpoint
	// (task 061 decision 1).
	Network bool `yaml:"network"`
	// ExtraMounts are additional bind mounts, each `host:container` or
	// `host:container:ro`. The project repository and the task's worktree are
	// mounted automatically at their own absolute paths (decision 2) and need
	// no entry here.
	ExtraMounts []string `yaml:"extra_mounts"`
}

// Enabled reports whether steps run in a container at all.
func (c Container) Enabled() bool { return strings.TrimSpace(c.Image) != "" }

// RuntimeBinary is the configured runtime with the built-in default applied,
// so callers never have to repeat the fallback.
func (c Container) RuntimeBinary() string {
	if r := strings.TrimSpace(c.Runtime); r != "" {
		return r
	}
	return "docker"
}

// ContainerOverride is the workflow-level form of Container (§8.6 precedence:
// workflow `defaults:` beats `config.yaml`). Every field is a pointer so a
// workflow can set one key without restating the block — and, in particular,
// so `image: ""` in a workflow can mean "run this one on the host" rather than
// being indistinguishable from an absent key.
//
// There is deliberately no third level in this delivery: no task column, no
// `POST /v1/tasks` field, no CLI flag, no New-task TUI control (task 061
// decision 6). A per-task override is a follow-up whose trigger is the first
// person who needs one task run against a different image.
type ContainerOverride struct {
	Image            *string  `yaml:"image"`
	Runtime          *string  `yaml:"runtime"`
	MountAgentConfig *bool    `yaml:"mount_agent_config"`
	Network          *bool    `yaml:"network"`
	ExtraMounts      []string `yaml:"extra_mounts"`
}

// Merge layers an override onto the configured block. A nil override, or one
// with every field unset, returns the block unchanged.
func (c Container) Merge(o *ContainerOverride) Container {
	if o == nil {
		return c
	}
	if o.Image != nil {
		c.Image = *o.Image
	}
	if o.Runtime != nil {
		c.Runtime = *o.Runtime
	}
	if o.MountAgentConfig != nil {
		c.MountAgentConfig = *o.MountAgentConfig
	}
	if o.Network != nil {
		c.Network = *o.Network
	}
	if o.ExtraMounts != nil {
		c.ExtraMounts = o.ExtraMounts
	}
	return c
}

// Validate rejects a block that cannot produce a runnable container. It is
// called from Config.validate and again from workflow validation, so a
// malformed mount is a load error on whichever side spelled it.
func (c Container) Validate() error {
	if strings.ContainsAny(c.Runtime, " \t") {
		return fmt.Errorf("container.runtime %q must be a single binary name or path", c.Runtime)
	}
	for _, m := range c.ExtraMounts {
		if err := validateMount(m); err != nil {
			return err
		}
	}
	return nil
}

// validateMount checks one `host:container[:ro]` entry. Both paths must be
// absolute: a relative source resolves against the daemon's working directory,
// which is not a thing a workflow author can reason about.
//
// "Absolute" is judged POSIX-style on both sides, deliberately, and not with
// filepath.IsAbs. filepath.IsAbs answers "absolute on the platform this binary
// was built for", and a Windows daemon answers `/srv/cache` with no. But a
// Windows daemon refuses containerized tasks outright (decision 2 — paths are
// identical inside and out, and `C:\...` cannot exist in a Linux container),
// so the only host that ever acts on this key is a POSIX one. Judging it by
// the host's own rules would make a config.yaml that is correct for the Linux
// daemon it was written for fail to *load* on a Windows machine sharing it,
// over a key that machine will never reach.
func validateMount(spec string) error {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return fmt.Errorf("container.extra_mounts %q must be host:container or host:container:ro", spec)
	}
	if len(parts) == 3 && parts[2] != "ro" && parts[2] != "rw" {
		return fmt.Errorf("container.extra_mounts %q: mode must be ro or rw, got %q", spec, parts[2])
	}
	if !strings.HasPrefix(parts[0], "/") {
		return fmt.Errorf("container.extra_mounts %q: host path must be absolute", spec)
	}
	if !strings.HasPrefix(parts[1], "/") {
		return fmt.Errorf("container.extra_mounts %q: container path must be absolute", spec)
	}
	return nil
}

// ContainerEnvironment reinterprets the §12.3 environment policy for a
// containerized step (task 061 decision 7). `inherit: all` — the default —
// becomes `none`, because a macOS or Linux host's PATH, HOME, TMPDIR and SHELL
// inside a Linux image is a broken container, not an inherited one. An
// explicit name list is honoured verbatim, and `unset`, `set` and §8.5's
// VINCENT_* block apply exactly as specified on top of the image's own
// environment.
//
// The alternative it beat: a separate `container.env` block, which is more
// honest about one key meaning two things but duplicates the whole vocabulary,
// so every future environment feature would land twice.
func ContainerEnvironment(e Environment) Environment {
	if e.Inherit.Mode == InheritAllMode {
		e.Inherit = Inherit{Mode: InheritNoneMode}
	}
	return e
}
