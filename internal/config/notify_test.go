package config

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/taskstate"
)

// TestNotifyAbsentIsInert is the property that makes this feature safe to
// ship on by default: a config that never mentions `notify` gets the zero
// value, which fires nothing, and changes no other default.
func TestNotifyAbsentIsInert(t *testing.T) {
	cfg, err := Load(writeConfig(t, "log_level: warn\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Notify.Enabled() {
		t.Errorf("absent notify block is enabled: %+v", cfg.Notify)
	}
	if len(cfg.Notify.On) != 0 || len(cfg.Notify.Command) != 0 {
		t.Errorf("absent notify block is not zero: %+v", cfg.Notify)
	}
	want := Default()
	want.LogLevel = "warn"
	if cfg.MaxParallelTasks != want.MaxParallelTasks || cfg.TranscriptMaxBytes != want.TranscriptMaxBytes {
		t.Errorf("unrelated defaults moved: %+v", cfg)
	}
	if Default().Notify.Enabled() {
		t.Error("Default() ships notify enabled; it must be off until configured")
	}
}

// TestNotifyLoads checks the block round-trips as written, argv included.
func TestNotifyLoads(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
notify:
  on: [blocked, awaiting_gate, awaiting_input, done]
  command: ["/usr/local/bin/notify-me", "--tag", "vincent"]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Notify.Enabled() {
		t.Fatal("configured notify block reports disabled")
	}
	wantArgv := []string{"/usr/local/bin/notify-me", "--tag", "vincent"}
	if len(cfg.Notify.Command) != len(wantArgv) {
		t.Fatalf("command = %q, want %q", cfg.Notify.Command, wantArgv)
	}
	for i, arg := range wantArgv {
		if cfg.Notify.Command[i] != arg {
			t.Errorf("command[%d] = %q, want %q", i, cfg.Notify.Command[i], arg)
		}
	}
	for _, s := range []taskstate.State{
		taskstate.Blocked, taskstate.AwaitingGate, taskstate.AwaitingInput, taskstate.Done,
	} {
		if !cfg.Notify.Fires(s) {
			t.Errorf("Fires(%s) = false, want true", s)
		}
	}
	if cfg.Notify.Fires(taskstate.Running) {
		t.Error("Fires(running) = true for a state not listed")
	}
}

// TestNotifyAcceptsEveryState holds the validator to §6's vocabulary rather
// than to the four states the feature was designed around: a state that is
// legal for a task is legal to notify on.
func TestNotifyAcceptsEveryState(t *testing.T) {
	for _, s := range taskstate.All {
		cfg, err := Load(writeConfig(t, "notify:\n  on: ["+string(s)+"]\n  command: [\"true\"]\n"))
		if err != nil {
			t.Errorf("state %s rejected: %v", s, err)
			continue
		}
		if !cfg.Notify.Fires(s) {
			t.Errorf("state %s loaded but does not fire", s)
		}
	}
}

// TestNotifyUnknownStateRejected is the acceptance the issue asked for
// literally: a typo fails Load with an error naming the offending value, so
// the watcher keeps the last good configuration (§12.3).
func TestNotifyUnknownStateRejected(t *testing.T) {
	_, err := Load(writeConfig(t, "notify:\n  on: [blocked, awating_input]\n  command: [\"true\"]\n"))
	if err == nil {
		t.Fatal("unknown state accepted")
	}
	if !strings.Contains(err.Error(), "awating_input") {
		t.Errorf("error does not name the offending value: %v", err)
	}
}

func TestNotifyDuplicateStateRejected(t *testing.T) {
	_, err := Load(writeConfig(t, "notify:\n  on: [blocked, blocked]\n  command: [\"true\"]\n"))
	if err == nil {
		t.Fatal("duplicate state accepted")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("error does not explain the duplicate: %v", err)
	}
}

// TestNotifyEmptyArgvElementRejected: an empty argv element is a mistake no
// exec can act on, and it would reach exec.Command as a real, empty argument.
func TestNotifyEmptyArgvElementRejected(t *testing.T) {
	_, err := Load(writeConfig(t, "notify:\n  on: [blocked]\n  command: [\"notify-me\", \"\"]\n"))
	if err == nil {
		t.Fatal("empty argv element accepted")
	}
	if !strings.Contains(err.Error(), "notify.command") {
		t.Errorf("error does not name the key: %v", err)
	}
}

// TestNotifyWarnings pins both half-written spellings to warnings rather than
// load failures (task 046 decision 10), and checks nothing warns when the
// block is whole or absent.
func TestNotifyWarnings(t *testing.T) {
	notifyWarnings := func(c Config) []string {
		var out []string
		for _, w := range c.Warnings() {
			if strings.Contains(w, "notify") {
				out = append(out, w)
			}
		}
		return out
	}

	cases := []struct {
		name string
		body string
		want string
	}{
		{"command without on", "notify:\n  command: [\"true\"]\n", "notify.command is set"},
		{"on without command", "notify:\n  on: [blocked]\n", "notify.on lists states"},
		{"whole block", "notify:\n  on: [blocked]\n  command: [\"true\"]\n", ""},
		{"absent", "log_level: info\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.body))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := notifyWarnings(cfg)
			if tc.want == "" {
				if len(got) > 0 {
					t.Errorf("unexpected warnings: %q", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("warnings = %q, want exactly one", got)
			}
			if !strings.Contains(got[0], tc.want) {
				t.Errorf("warning = %q, want it to contain %q", got[0], tc.want)
			}
		})
	}
}

// TestNotifyFiresNeedsBothHalves: a state list with no command must not fire,
// which is what keeps the warned-about configuration inert rather than
// spawning an empty argv.
func TestNotifyFiresNeedsBothHalves(t *testing.T) {
	n := Notify{On: []taskstate.State{taskstate.Blocked}}
	if n.Fires(taskstate.Blocked) {
		t.Error("Fires = true with no command configured")
	}
	if n.Enabled() {
		t.Error("Enabled = true with no command configured")
	}
	n = Notify{Command: []string{"true"}}
	if n.Fires(taskstate.Blocked) {
		t.Error("Fires = true with an empty on: list")
	}
	if n.Enabled() {
		t.Error("Enabled = true with an empty on: list")
	}
}

// TestDefaultConfigFileDocumentsNotify keeps the generated file honest: the
// block is present, commented out, and the shipped defaults stay inert.
func TestDefaultConfigFileDocumentsNotify(t *testing.T) {
	if !strings.Contains(defaultConfigYAML, "# notify:") {
		t.Error("the default config.yaml does not document the notify block")
	}
	path := writeConfig(t, defaultConfigYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("default config.yaml does not load: %v", err)
	}
	if cfg.Notify.Enabled() {
		t.Error("the default config.yaml enables notify; it must ship off")
	}
	if len(cfg.Warnings()) != 0 {
		t.Errorf("the default config.yaml warns: %q", cfg.Warnings())
	}
}
