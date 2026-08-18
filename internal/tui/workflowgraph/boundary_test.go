package workflowgraph

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The three stages are separate so that topology and geometry stay testable
// without terminal events and reusable by a future builder's command model
// (task 017 decision 2). That is a property of the imports, and imports drift
// silently — a single convenience call to lipgloss.Width inside layout.go
// would cost the separation without failing anything else.
func TestPureStagesImportNoTerminalPackages(t *testing.T) {
	forbidden := []string{"bubbletea", "lipgloss", "bubbles", "charmbracelet"}
	for _, file := range []string{"diagram.go", "layout.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if strings.Contains(path, bad) {
					t.Errorf("%s imports %s: the diagram and layout stages must stay "+
						"free of the terminal stack", file, path)
				}
			}
		}
	}
}
