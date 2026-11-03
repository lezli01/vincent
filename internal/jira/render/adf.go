package render

import (
	"bytes"
	"encoding/json"
	"strings"
)

// adfDoc is the root of an Atlassian Document Format document. Its content
// is always present, as ADF requires.
type adfDoc struct {
	Type    string    `json:"type"`
	Version int       `json:"version"`
	Content []adfNode `json:"content"`
}

// adfNode is the one node shape MarkdownToADF emits: paragraph, text,
// hardBreak and codeBlock. Field order is the struct's, so the encoding is
// deterministic.
type adfNode struct {
	Type    string    `json:"type"`
	Attrs   *adfAttrs `json:"attrs,omitempty"`
	Content []adfNode `json:"content,omitempty"`
	Text    string    `json:"text,omitempty"`
}

type adfAttrs struct {
	Language string `json:"language,omitempty"`
}

// MarkdownToADF wraps a Markdown comment as a minimal ADF doc for Jira's
// comment endpoint.
//
// Blank-line-separated blocks become paragraphs, a single newline inside a
// block becomes a hardBreak, and a ``` fence becomes a codeBlock — with
// attrs.language when the info string names one, and running to the end of
// the input when the fence is never closed. Everything else, inline Markdown
// and headings included, goes out as literal text: rich ADF output is out of
// scope (#906). CRLF and lone CR line endings are read as LF.
//
// An empty comment is a doc holding one empty paragraph, and no text node is
// ever the empty string — ADF rejects both an empty text node and, on some
// endpoints, a doc with no content.
func MarkdownToADF(md string) json.RawMessage {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")
	lines := strings.Split(md, "\n")

	content := []adfNode{}
	var para []string
	flush := func() {
		if len(para) == 0 {
			return
		}
		p := adfNode{Type: "paragraph"}
		for i, l := range para {
			if i > 0 {
				p.Content = append(p.Content, adfNode{Type: "hardBreak"})
			}
			if l != "" {
				p.Content = append(p.Content, adfNode{Type: "text", Text: l})
			}
		}
		content = append(content, p)
		para = nil
	}

	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if fence, lang, ok := openFence(l); ok {
			flush()
			var body []string
			j := i + 1
			for ; j < len(lines) && !closesFence(lines[j], fence); j++ {
				body = append(body, lines[j])
			}
			if j == len(lines) && len(body) > 0 && body[len(body)-1] == "" {
				// A fence running to the end of the input: the input's own
				// final newline is not part of the code, so "```\ncode\n"
				// and "```\ncode\n```" carry the same text (review F15).
				body = body[:len(body)-1]
			}
			code := adfNode{Type: "codeBlock"}
			if lang != "" {
				code.Attrs = &adfAttrs{Language: lang}
			}
			if text := strings.Join(body, "\n"); text != "" {
				code.Content = []adfNode{{Type: "text", Text: text}}
			}
			content = append(content, code)
			i = j
			continue
		}
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		para = append(para, l)
	}
	flush()
	if len(content) == 0 {
		content = []adfNode{{Type: "paragraph"}}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(adfDoc{Type: "doc", Version: 1, Content: content}); err != nil {
		// Only strings and ints are encoded; this cannot fail.
		return json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph"}]}`)
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n"))
}

// openFence reports whether l opens a backtick fence: up to three spaces of
// indent, three or more backticks, and an info string with no backtick in
// it. lang is the info string's first word.
func openFence(l string) (fence, lang string, ok bool) {
	t := strings.TrimLeft(l, " ")
	if len(l)-len(t) > 3 {
		return "", "", false
	}
	n := 0
	for n < len(t) && t[n] == '`' {
		n++
	}
	if n < 3 {
		return "", "", false
	}
	info := strings.TrimSpace(t[n:])
	if strings.Contains(info, "`") {
		return "", "", false
	}
	if f := strings.Fields(info); len(f) > 0 {
		lang = f[0]
	}
	return t[:n], lang, true
}

// closesFence reports whether l is a closing fence for fence: only
// backticks, at least as many, and surrounding spaces.
func closesFence(l, fence string) bool {
	t := strings.TrimSpace(l)
	return len(t) >= len(fence) && strings.Trim(t, "`") == ""
}
