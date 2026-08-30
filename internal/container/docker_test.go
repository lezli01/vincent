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
