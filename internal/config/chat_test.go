package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestChatViewDefaults: tui.chat ships empty, so an absent block changes no
// chat header and no composer (task 170).
func TestChatViewDefaults(t *testing.T) {
	if got := Default().TUI.Chat; got != (ChatView{}) {
		t.Errorf("default tui.chat = %+v, want no status line and vim off", got)
	}
	cfg, err := Load(writeConfig(t, "tui:\n  hyperlinks: true\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TUI.Chat != (ChatView{}) {
		t.Errorf("absent tui.chat = %+v, want the zero value", cfg.TUI.Chat)
	}
}

func TestChatViewDecodes(t *testing.T) {
	cfg, err := Load(writeConfig(t,
		"tui:\n  chat:\n    status_line: \"{{.Model}} · {{.ContextPercent}}% · {{.Cost}}\"\n    vim: true\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := ChatView{StatusLine: "{{.Model}} · {{.ContextPercent}}% · {{.Cost}}", Vim: true}
	if cfg.TUI.Chat != want {
		t.Errorf("tui.chat = %+v, want %+v", cfg.TUI.Chat, want)
	}
	if !reflect.DeepEqual(cfg.TUI.Tabs, Default().TUI.Tabs) {
		t.Errorf("setting tui.chat dropped the default tabs: %v", cfg.TUI.Tabs)
	}
	// Strict decoding reaches the new block too.
	if _, err := Load(writeConfig(t, "tui:\n  chat:\n    emacs: true\n")); err == nil {
		t.Error("Load accepted an unknown key under tui.chat")
	}
}

func TestValidateChatStatusLine(t *testing.T) {
	accepted := []string{
		"",
		"plain text",
		"{{.Model}} {{.Effort}} {{.Agent}} {{.PermissionMode}} {{.Branch}} {{.State}} {{.Cost}} {{.ContextPercent}} {{.Turns}}",
		`{{if .Cost}}{{.Cost}}{{else}}free{{end}} · {{printf "%d%%" .ContextPercent}}`,
		"{{if gt .ContextPercent 80}}!{{end}}{{.Turns}} turns",
		"{{with .Cost}}{{.}}{{else}}{{.Model}}{{end}} {{$.Turns}}",
		"{{$m := .Model}}{{$m}} {{(.Model)}}",
	}
	for _, tmpl := range accepted {
		if err := ValidateChatStatusLine(tmpl); err != nil {
			t.Errorf("ValidateChatStatusLine(%q) = %v, want accepted", tmpl, err)
		}
	}
	refused := map[string]struct{ tmpl, want string }{
		"unknown field":   {"{{.Tokens}}", "Tokens"},
		// Review F5: fields a zero value never reaches still fail the load.
		"field in an if":       {"{{if .Cost}}{{.Bogus}}{{end}}", "Bogus"},
		"field in an else":     {"{{if .Cost}}x{{else if .Turns}}{{.Bogus}}{{end}}", "Bogus"},
		"field in a with else": {"{{with .Cost}}{{.}}{{else}}{{.Bogus}}{{end}}", "Bogus"},
		"field in a with body": {"{{with .Cost}}{{.Len}}{{end}}", "Len"},
		"field in a range":     {"{{range .Branch}}{{$.Bogus}}{{end}}", "Bogus"},
		"field of a field":     {"{{if .Cost}}{{.Cost.Amount}}{{end}}", "Amount"},
		"parse error":     {"{{.Model", "tui.chat.status_line"},
		"unknown func":    {"{{upper .Model}}", "upper"},
		"newline":         {"{{.Model}}\n{{.Cost}}", "single line"},
		"carriage return": {"{{.Model}}\r", "single line"},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			err := ValidateChatStatusLine(tc.tmpl)
			if err == nil {
				t.Fatalf("ValidateChatStatusLine(%q) accepted", tc.tmpl)
			}
			for _, want := range []string{"tui.chat.status_line", tc.want} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "template: ") {
				t.Errorf("error %q leaks text/template's own prefix", err)
			}
		})
	}
}

// TestChatStatusLineIsValidatedOnLoad: the same refusal reaches Load, so a
// file edited by hand cannot hand a client a template it would draw as
// nothing.
func TestChatStatusLineIsValidatedOnLoad(t *testing.T) {
	_, err := Load(writeConfig(t, "tui:\n  chat:\n    status_line: \"{{.Nope}}\"\n"))
	if err == nil {
		t.Fatal("Load accepted a status line naming an unknown field")
	}
	for _, want := range []string{"tui.chat.status_line", "Nope", ".ContextPercent"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestChatStatusFieldNamesMatchTheStruct holds the list the error prints to
// ChatStatusFields, so a field added to one cannot be missing from the other.
func TestChatStatusFieldNamesMatchTheStruct(t *testing.T) {
	typ := reflect.TypeFor[ChatStatusFields]()
	var want []string
	for i := range typ.NumField() {
		want = append(want, "."+typ.Field(i).Name)
	}
	if !reflect.DeepEqual(chatStatusFieldNames, want) {
		t.Errorf("chatStatusFieldNames = %v, want the struct's %v", chatStatusFieldNames, want)
	}
}

// TestChatKeysEditInPlace: both keys reach a shipped file through the line
// editor — the commented block uncomments where it stands — and the result
// loads. A bare file, with no tui block at all, takes them too.
func TestChatKeysEditInPlace(t *testing.T) {
	tmpl := `{{.Model}} "{{.State}}" · {{.ContextPercent}}%`
	for name, src := range map[string]string{
		"shipped template": defaultConfigYAML,
		"bare file":        "log_level: info\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, err := Apply([]byte(src), []Set{
				{Path: "tui.chat.status_line", Value: RenderString(tmpl)},
				{Path: "tui.chat.vim", Value: "true"},
			})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			cfg, err := Load(writeConfig(t, string(out)))
			if err != nil {
				t.Fatalf("Load the edited file: %v\n%s", err, out)
			}
			if cfg.TUI.Chat.StatusLine != tmpl || !cfg.TUI.Chat.Vim {
				t.Errorf("tui.chat = %+v after the edit\n%s", cfg.TUI.Chat, out)
			}
		})
	}
}
