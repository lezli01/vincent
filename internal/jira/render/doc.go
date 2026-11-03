// Package render converts between the bodies Jira Cloud serves and the
// Markdown vincent stores (#906, part of epic #896).
//
// Two directions, both pure functions with no I/O:
//
//   - HTMLToMarkdown turns the HTML Jira renders for an issue's description
//     (`expand=renderedFields`) or a comment (`expand=renderedBody`) into
//     Markdown. #896 decision 7 chose rendered HTML over walking ADF: one
//     converter covers ADF and wiki-markup bodies alike, so a later Data
//     Center leg reuses it. The HTML is parsed with golang.org/x/net/html
//     (BSD-3-Clause, maintained by the Go team) for its tolerant HTML5 tree
//     builder; the conversion itself is written here rather than delegated
//     to a general HTML→Markdown library, for the reason task 073 decision 1
//     hand-rolled the TUI's Markdown — what comes out has to be exactly the
//     subset the TUI renders.
//   - MarkdownToADF wraps an outgoing comment as a minimal Atlassian Document
//     Format doc: paragraphs, hard breaks and fenced code. Rich ADF output
//     (tables, mentions, marks) is out of scope.
//
// # The Markdown subset
//
// Issue bodies are rendered as Markdown on the issue detail (task 130
// decision 12, spec §5.6), by internal/tui/markdown.go's hand-rolled parser
// (task 073, task 075). Anything outside its subset renders literally, so
// HTMLToMarkdown emits only: ATX headings, paragraphs, emphasis, strong,
// ordered and unordered (nested) lists, blockquotes, inline code, fenced
// code, horizontal rules, pipe tables, inline links and inline images. It
// never emits autolinks, reference or titled links, setext headings, raw
// HTML or backslash escapes, and text goes out as-is — the TUI would show
// an escaping backslash literally.
//
// Where the parser would misread text, the text is changed instead of
// escaped: a `|` in a table cell becomes `¦`, a square bracket in a link
// label or image alt becomes `⁅`/`⁆`, a bold-italic italic is spelled `_`,
// and an image inside a link is written after the link rather than in its
// label.
//
// # Stability
//
// The importer (#908) compares a re-rendered body with the stored one to
// decide whether an issue changed (spec §12.3's sync), so an unchanged input
// must convert to byte-identical output. Nothing on the output path iterates
// a map, whitespace is normalised by fixed rules, and the output is trimmed
// with no trailing newline.
//
// # Privacy
//
// A Jira mention carries the user's accountId in its link; the converter
// emits the display name only — the text of the mention's whole subtree,
// whichever element carries the marking, with no link or image inside it —
// so no account id reaches a stored body (#906, "mentions").
//
// The package is a leaf: it imports the standard library and x/net/html
// only. internal/jira (the client, #908) sits beside it.
package render
