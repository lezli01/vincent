package fakejira

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

// adfNode is one node of an Atlassian Document Format document.
type adfNode struct {
	Type    string          `json:"type"`
	Version int             `json:"version,omitempty"`
	Text    string          `json:"text,omitempty"`
	Content []adfNode       `json:"content,omitempty"`
	Marks   json.RawMessage `json:"marks,omitempty"`
	Attrs   json.RawMessage `json:"attrs,omitempty"`
}

// RenderADF renders a POSTed ADF body to the HTML Jira's renderedBody would
// carry, for exactly the subset #906 emits: a `doc` (version 1) of
// `paragraph`s holding `text` and `hardBreak`, and `codeBlock`s holding
// `text` only, with an optional `attrs.language`. Any other node, any mark
// and any other attribute is an error the route answers 400, so a client
// drifting from the subset fails loudly (decision 3). It is deliberately not
// a converter the code under test could share.
func RenderADF(raw json.RawMessage) (string, error) {
	var doc adfNode
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("body is not an ADF document: %w", err)
	}
	if doc.Type != "doc" || doc.Version != 1 {
		return "", fmt.Errorf("body must be a version 1 doc")
	}
	if err := plain(doc); err != nil {
		return "", err
	}
	var paras []string
	for _, p := range doc.Content {
		if p.Type == "codeBlock" {
			block, err := codeBlock(p)
			if err != nil {
				return "", err
			}
			paras = append(paras, block)
			continue
		}
		if p.Type != "paragraph" {
			return "", fmt.Errorf("unsupported ADF node %q", p.Type)
		}
		if err := plain(p); err != nil {
			return "", err
		}
		var b strings.Builder
		b.WriteString("<p>")
		for _, in := range p.Content {
			if err := plain(in); err != nil {
				return "", err
			}
			switch in.Type {
			case "text":
				if len(in.Content) > 0 {
					return "", fmt.Errorf("text nodes have no content")
				}
				b.WriteString(html.EscapeString(in.Text))
			case "hardBreak":
				b.WriteString("<br/>")
			default:
				return "", fmt.Errorf("unsupported ADF node %q", in.Type)
			}
		}
		b.WriteString("</p>")
		paras = append(paras, b.String())
	}
	return strings.Join(paras, "\n"), nil
}

// codeBlock renders a `codeBlock` as Jira does, `<pre><code>`, with the
// language as a class when attrs names one. attrs may carry `language` and
// nothing else; the children are unmarked `text` nodes.
func codeBlock(n adfNode) (string, error) {
	if len(n.Marks) > 0 && string(n.Marks) != "null" && string(n.Marks) != "[]" {
		return "", fmt.Errorf("unsupported ADF marks on %q", n.Type)
	}
	var attrs map[string]json.RawMessage
	if len(n.Attrs) > 0 && string(n.Attrs) != "null" {
		if err := json.Unmarshal(n.Attrs, &attrs); err != nil {
			return "", fmt.Errorf("codeBlock attrs: %w", err)
		}
	}
	var lang string
	for k, v := range attrs {
		if k != "language" {
			return "", fmt.Errorf("unsupported ADF attr %q on codeBlock", k)
		}
		if err := json.Unmarshal(v, &lang); err != nil {
			return "", fmt.Errorf("codeBlock language: %w", err)
		}
	}
	var b strings.Builder
	b.WriteString("<pre><code")
	if lang != "" {
		b.WriteString(` class="language-` + html.EscapeString(lang) + `"`)
	}
	b.WriteString(">")
	for _, in := range n.Content {
		if err := plain(in); err != nil {
			return "", err
		}
		if in.Type != "text" || len(in.Content) > 0 {
			return "", fmt.Errorf("unsupported ADF node %q in codeBlock", in.Type)
		}
		b.WriteString(html.EscapeString(in.Text))
	}
	b.WriteString("</code></pre>")
	return b.String(), nil
}

func plain(n adfNode) error {
	if len(n.Marks) > 0 && string(n.Marks) != "null" && string(n.Marks) != "[]" {
		return fmt.Errorf("unsupported ADF marks on %q", n.Type)
	}
	if len(n.Attrs) > 0 && string(n.Attrs) != "null" && string(n.Attrs) != "{}" {
		return fmt.Errorf("unsupported ADF attrs on %q", n.Type)
	}
	return nil
}
