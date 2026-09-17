package container

import (
	"reflect"
	"testing"
)

// TestExecArgv pins the host argv a step is spawned with. It is table-driven
// against literal expectations for the reason adapter parsing is: the argv is
// the contract with a CLI vincent does not own, and a silent change to it is a
// change to what every containerized step runs.
func TestExecArgv(t *testing.T) {
	rt := New("docker")
	wrapper := "echo $$ > /vincent-run/step-7.pid; exec \"$@\""
	cases := []struct {
		name string
		spec ExecSpec
		want []string
	}{
		{
			name: "shell command",
			spec: ExecSpec{Key: "step-7", Argv: []string{"/bin/sh", "-c", "go test ./..."}, WorkDir: "/repo/wt"},
			want: []string{
				"docker", "exec", "--interactive", "--workdir", "/repo/wt",
				"cid", "/bin/sh", "-c", wrapper, "vincent", "/bin/sh", "-c", "go test ./...",
			},
		},
		{
			name: "user and env",
			spec: ExecSpec{
				Key: "step-7", Argv: []string{"true"},
				Env: []string{"A=1", "B=2"}, User: "501:20",
			},
			want: []string{
				"docker", "exec", "--interactive", "--user", "501:20",
				"--env", "A=1", "--env", "B=2",
				"cid", "/bin/sh", "-c", wrapper, "vincent", "true",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rt.Exec("cid", tc.spec)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Exec argv =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestExecNeverAllocatesATTY is decision 10 as an assertion. A TTY merges
// stdout and stderr and translates newlines, which corrupts the JSONL an
// adapter's LineParser reads and therefore §17's token and cost records.
func TestExecNeverAllocatesATTY(t *testing.T) {
	argv := New("docker").Exec("cid", ExecSpec{Key: "k", Argv: []string{"true"}})
	for _, a := range argv {
		if a == "-t" || a == "--tty" {
			t.Fatalf("Exec allocated a TTY: %q", argv)
		}
	}
}

// TestNameIsDerivedFromTheTaskID is what makes creation idempotent across a
// daemon restart: a task that comes back finds the container it left.
func TestNameIsDerivedFromTheTaskID(t *testing.T) {
	if got := Name(42); got != "vincent-task-42" {
		t.Errorf("Name(42) = %q, want vincent-task-42", got)
	}
}

// TestRuntimeDefaultsToDocker pins the empty-string fallback so a config that
// names no runtime does not shell out to "".
func TestRuntimeDefaultsToDocker(t *testing.T) {
	if got := New("").Name(); got != "docker" {
		t.Errorf("New(\"\").Name() = %q, want docker", got)
	}
	if got := New("podman").Name(); got != "podman" {
		t.Errorf("New(\"podman\").Name() = %q, want podman", got)
	}
}

// TestExecDirectArgv pins the unwrapped exec an agent CLI is resolved and
// probed through (task 062.2 decision 2): the same flags as Exec, and the argv
// as the exec's own process — no pid file, since nothing signals a probe.
func TestExecDirectArgv(t *testing.T) {
	got := New("docker").ExecDirect("cid", ExecSpec{
		Key: "ignored", Argv: []string{"/bin/sh", "-c", `command -v "$1"`, "vincent", "claude"},
		Env: []string{"HOME=/vincent-home"}, User: "501:20",
	})
	want := []string{
		"docker", "exec", "--interactive", "--user", "501:20", "--env", "HOME=/vincent-home",
		"cid", "/bin/sh", "-c", `command -v "$1"`, "vincent", "claude",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExecDirect argv =\n  %q\nwant\n  %q", got, want)
	}
}

// TestCreateArgs pins the container's creation argv, and in particular task
// 062.2 decision 3's tmpfs home: present only when asked for, and ahead of the
// bind mounts that land beneath it.
func TestCreateArgs(t *testing.T) {
	base := CreateSpec{
		Image: "img", Name: "vincent-task-1", Labels: map[string]string{LabelTask: "1"},
		Mounts:  []Mount{{Source: "/h/.claude", Target: HomeDir + "/.claude"}},
		Network: true, AddHostGateway: true,
	}
	tail := []string{
		"--volume", "/h/.claude:/vincent-home/.claude",
		"--entrypoint", "/bin/sh", "img", "-c", "while :; do sleep 3600; done",
	}
	head := []string{
		"run", "--detach", "--name", "vincent-task-1", "--label", LabelTask + "=1",
		"--add-host=host.docker.internal:host-gateway", "--tmpfs", "/vincent-run:rw,mode=1777",
	}
	cases := []struct {
		name string
		home bool
		want []string
	}{
		{"no home", false, append(append([]string(nil), head...), tail...)},
		{"home", true, append(append(append([]string(nil), head...), "--tmpfs", "/vincent-home:rw,mode=1777"), tail...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := base
			spec.Home = tc.home
			if got := createArgs(spec); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("createArgs =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestSplitEnv is the rule that keeps a step's secrets off the host argv
// (task 062.2): every variable is a bare name on the argv with its value in the
// client's environment, except the client's own resolution variables, which
// keep their host values client-side and go in literally.
func TestSplitEnv(t *testing.T) {
	flags, client := SplitEnv([]string{
		"PATH=/usr/bin", "VINCENT_MCP_TOKEN=s3cret", "HOME=/vincent-home",
		"DOCKER_HOST=unix:///x", "A=1", "A=2", "malformed",
	})
	wantFlags := []string{"PATH=/usr/bin", "VINCENT_MCP_TOKEN", "HOME=/vincent-home", "DOCKER_HOST=unix:///x", "A"}
	wantClient := []string{"VINCENT_MCP_TOKEN=s3cret", "A=2"}
	if !reflect.DeepEqual(flags, wantFlags) {
		t.Errorf("flags = %q, want %q", flags, wantFlags)
	}
	if !reflect.DeepEqual(client, wantClient) {
		t.Errorf("client = %q, want %q", client, wantClient)
	}
}
