package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `vincent config`'s key table is a hand-written list, deliberately (see
// config.go). What a hand-written list needs is a drift check: a key the
// daemon serves and this command cannot name is a key that is editable from
// the TUI and not from a script, which is the split task 060 exists to close.
func TestConfigCommandCoversEveryServedKey(t *testing.T) {
	named := configFields()
	var missing []string
	for _, path := range servedPaths(reflect.TypeOf(apiclient.Config{}), "") {
		if _, ok := named[path]; !ok {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		t.Errorf("GET /v1/config serves keys `vincent config` cannot name: %v\n"+
			"add them to configFields()", missing)
	}
}

// servedPaths walks the wire type into the dotted paths config.yaml carries.
// A struct field becomes a path segment; anything else is a leaf.
func servedPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		// ConfigInherit is a union with its own marshaller: it is a leaf on
		// the wire even though it is a struct in Go.
		if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(apiclient.ConfigInherit{}) {
			out = append(out, servedPaths(f.Type, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

// Every value `get` prints has to be one `set` accepts, or the obvious way to
// use the command — read it, change it, write it back — silently does
// something else.
func TestConfigValuesRoundTripThroughSet(t *testing.T) {
	cfg := apiclient.Config{
		Listen: "127.0.0.1:0", MaxParallelTasks: 3, BranchTemplate: "vincent/{{.ID}}",
		Defaults:                apiclient.ConfigDefaults{AgentTimeout: "1h0m0s", CommandTimeout: "15m0s", InputTimeout: "24h0m0s"},
		TranscriptRetentionDays: 90, TranscriptMaxBytes: 512 << 20, MaxTaskCostUSD: 2.5,
		UsageLimitRecheck: "15m0s", LogLevel: "info",
		Environment: apiclient.ConfigEnvironment{
			Inherit: apiclient.ConfigInherit{Mode: "list", Names: []string{"PATH", "HOME"}},
			Unset:   []string{"MSYSTEM"},
			Set:     map[string]string{"LANG": "C.UTF-8"},
		},
		Agents:   apiclient.ConfigAgents{Claude: apiclient.AgentPath{Path: "/usr/bin/claude"}},
		Parallel: apiclient.ConfigParallel{MaxParallel: 4},
		FanOut:   apiclient.ConfigFanOut{MaxDepth: 3, MaxTasks: 64},
		Loop:     apiclient.ConfigLoop{MaxIterations: 10},
		Include:  apiclient.ConfigInclude{MaxDepth: 5},
		MCP:      apiclient.ConfigMCP{WireSteps: true, MaxDepth: 3, MaxTasks: 32},
		GitHub:   apiclient.ConfigGitHub{Enabled: true, PollInterval: "5m0s"},
		Update:   apiclient.ConfigUpdate{Check: true, PollInterval: "24h0m0s"},
		Notify:   apiclient.ConfigNotify{On: []string{"blocked"}, Command: []string{"/bin/echo", "hi"}},
		TUI:      apiclient.ConfigTUI{Board: apiclient.ConfigBoard{GroupBy: []string{"project", "workflow"}}},
	}
	for _, path := range configPaths() {
		value, ok := configValue(cfg, path)
		if !ok {
			t.Fatalf("configPaths lists %q and configValue does not know it", path)
		}
		patch, err := configPatchFor(path, value)
		if err != nil {
			t.Errorf("%s: `get` printed %q and `set` refuses it: %v", path, value, err)
			continue
		}
		// The patch has to carry exactly the key that was asked for: a table
		// entry wired to the wrong field would otherwise pass silently.
		b, err := json.Marshal(patch)
		if err != nil {
			t.Fatalf("%s: marshal patch: %v", path, err)
		}
		top := strings.Split(path, ".")[0]
		if !strings.Contains(string(b), `"`+top+`"`) {
			t.Errorf("%s: the patch does not mention %q: %s", path, top, b)
		}
	}
}
