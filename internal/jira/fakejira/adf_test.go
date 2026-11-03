package fakejira

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/jira/render"
)

// TestRenderADFAcceptsWhatRenderEmits is review F2: the fake's renderer and
// render.MarkdownToADF are two sides of one contract, so real converter
// output — fences included, terminated or not — must render rather than 400.
func TestRenderADFAcceptsWhatRenderEmits(t *testing.T) {
	for _, md := range []string{
		"",
		"hi",
		"one\ntwo\n\nthree",
		"hi\n```go\nx := 1\n```",
		"before\n```\n<b>&\n\nmore\n```\nafter",
		"```\nunterminated\n",
		"```\n```",
	} {
		got, err := RenderADF(render.MarkdownToADF(md))
		if err != nil {
			t.Errorf("%q: %v", md, err)
			continue
		}
		if strings.Contains(md, "```") && !strings.Contains(got, "<pre><code") {
			t.Errorf("%q: no code block in %q", md, got)
		}
	}
	got, err := RenderADF(render.MarkdownToADF("```go\na < b\n```"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `<pre><code class="language-go">a &lt; b</code></pre>`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestRenderADFRejectsOutsideTheSubset keeps decision 3: a codeBlock is
// accepted, but not with marks, other attrs or non-text children.
func TestRenderADFRejectsOutsideTheSubset(t *testing.T) {
	for _, body := range []string{
		`{"type":"doc","version":1,"content":[{"type":"heading","content":[{"type":"text","text":"x"}]}]}`,
		`{"type":"doc","version":1,"content":[{"type":"codeBlock","attrs":{"language":"go","x":1}}]}`,
		`{"type":"doc","version":1,"content":[{"type":"codeBlock","content":[{"type":"hardBreak"}]}]}`,
		`{"type":"doc","version":1,"content":[{"type":"codeBlock","content":[{"type":"text","text":"x","marks":[{"type":"strong"}]}]}]}`,
		`{"type":"doc","version":1,"content":[{"type":"codeBlock","marks":[{"type":"strong"}]}]}`,
	} {
		if got, err := RenderADF(json.RawMessage(body)); err == nil {
			t.Errorf("%s: accepted as %q", body, got)
		}
	}
}
