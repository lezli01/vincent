package container

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// dockerRuntime drives a docker-CLI-compatible binary. Every method shells
// out; none of them holds state, so `container.runtime` can change under a hot
// reload and the next task picks it up.
type dockerRuntime struct{ bin string }

func (d *dockerRuntime) Name() string { return d.bin }

func (d *dockerRuntime) Available(ctx context.Context) error {
	// `version --format` rather than `info`: it fails the same way when the
	// daemon is unreachable and costs a fraction of the output.
	out, err := d.run(ctx, "version", "--format", "{{.Server.APIVersion}}")
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnavailable, d.bin, err)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("%w: %s reported no server version", ErrUnavailable, d.bin)
	}
	return nil
}

func (d *dockerRuntime) EnsureImage(ctx context.Context, image string) error {
	if _, err := d.run(ctx, "image", "inspect", image); err == nil {
		return nil
	}
	// Inspect-then-pull rather than pull-always: a pull on every admission
	// turns an offline machine into a broken one for images it already has.
	if _, err := d.run(ctx, "pull", image); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrImageUnavailable, image, err)
	}
	return nil
}

func (d *dockerRuntime) Create(ctx context.Context, spec CreateSpec) (string, error) {
	argv := []string{"run", "--detach", "--name", spec.Name}
	for _, k := range sortedKeys(spec.Labels) {
		argv = append(argv, "--label", k+"="+spec.Labels[k])
	}
	if !spec.Network {
		argv = append(argv, "--network", "none")
	}
	if spec.AddHostGateway {
		argv = append(argv, "--add-host=host.docker.internal:host-gateway")
	}
	if spec.User != "" {
		argv = append(argv, "--user", spec.User)
	}
	// mode=1777 so a step running as the invoking user can write its pid file
	// into a scratch mount an image's own user may also touch.
	argv = append(argv, "--tmpfs", ScratchDir+":rw,mode=1777")
	for _, m := range spec.Mounts {
		v := m.Source + ":" + m.Target
		if m.ReadOnly {
			v += ":ro"
		}
		argv = append(argv, "--volume", v)
	}
	// The entrypoint is overridden so the image's own CMD — an agent CLI, a
	// shell, an init — does not decide whether the container stays up. What
	// keeps it up is this sleep loop, and nothing else runs until a step
	// execs in.
	argv = append(argv, "--entrypoint", "/bin/sh", spec.Image, "-c", "while :; do sleep 3600; done")
	out, err := d.run(ctx, argv...)
	if err != nil {
		return "", fmt.Errorf("%w: create %s: %w", ErrUnavailable, spec.Image, err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", fmt.Errorf("%w: create %s returned no id", ErrUnavailable, spec.Image)
	}
	return id, nil
}

func (d *dockerRuntime) Exec(id string, spec ExecSpec) []string {
	// -i, never -t (decision 10): a TTY merges stdout and stderr and
	// translates newlines, which corrupts the JSONL an adapter's LineParser
	// reads and therefore §17's token and cost records.
	argv := []string{d.bin, "exec", "--interactive"}
	if spec.WorkDir != "" {
		argv = append(argv, "--workdir", spec.WorkDir)
	}
	if spec.User != "" {
		argv = append(argv, "--user", spec.User)
	}
	for _, e := range spec.Env {
		argv = append(argv, "--env", e)
	}
	argv = append(argv, id, "/bin/sh", "-c", pidFileWrapper(spec.Key), "vincent")
	return append(argv, spec.Argv...)
}

// pidFileWrapper writes the in-container pid and then execs the real argv, so
// the wrapper is not a process of its own by the time the step runs. `"$@"`
// carries the argv through with no quoting: `sh -c script name args...` passes
// them as argv, never as text to re-parse.
func pidFileWrapper(key string) string {
	return "echo $$ > " + ScratchDir + "/" + key + ".pid; exec \"$@\""
}

func (d *dockerRuntime) Signal(ctx context.Context, id, key, signal string) error {
	// The process group first, the process second. A `docker exec` process is
	// usually its own group leader, in which case the first form reaches a
	// `run:` body's children too; where it is not, the second still reaches
	// the body itself, and whole-task Remove is what collects the rest.
	script := fmt.Sprintf(
		"p=$(cat %s/%s.pid 2>/dev/null); [ -n \"$p\" ] || exit 0; "+
			"kill -%s -$p 2>/dev/null || kill -%s $p 2>/dev/null || true",
		ScratchDir, key, signal, signal)
	if _, err := d.run(ctx, "exec", id, "/bin/sh", "-c", script); err != nil {
		return fmt.Errorf("signal %s in %s: %w", signal, id, err)
	}
	return nil
}

func (d *dockerRuntime) Remove(ctx context.Context, id string) error {
	if _, err := d.run(ctx, "rm", "--force", "--volumes", id); err != nil {
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	return nil
}

func (d *dockerRuntime) Lookup(ctx context.Context, name string) (string, error) {
	out, err := d.run(ctx, "inspect", "--format", "{{.Id}} {{.State.Running}}", name)
	if err != nil {
		// No container by that name. Not an error: "none yet" is the ordinary
		// answer on a task's first admission.
		return "", nil
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 || fields[1] != "true" {
		// A stopped container cannot be exec'd into, and reviving one whose
		// state nothing here explains would hide the failure that stopped it.
		return "", nil
	}
	return fields[0], nil
}

func (d *dockerRuntime) TaskLabel(ctx context.Context, id string) (string, error) {
	out, err := d.run(ctx, "inspect", "--format", "{{index .Config.Labels \""+LabelTask+"\"}}", id)
	if err != nil {
		// A container that is gone is not an error to recovery: "already
		// removed" and "removed by me" are the same outcome.
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

func (d *dockerRuntime) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.bin, args...) //nolint:gosec // the runtime binary the user configured
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%s %s: %w: %s", d.bin, args[0], err, msg)
	}
	return string(out), nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Sorted so an argv assertion is a table rather than a set comparison.
	sort.Strings(keys)
	return keys
}
