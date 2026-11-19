// Package chatexport renders a chat as one document — Markdown, a
// self-contained HTML page, or JSON — for `GET /v1/chats/{id}/export`
// (spec §5.5 "Exporting a chat", §13.2, task 156).
//
// The daemon renders and clients only write the bytes it serves (task 156
// decision on placement): the CLI, the TUI and curl are thin consumers of one
// output, which is the architecture's rule. The package therefore owns the
// document model, Export, and nothing that loads it: the API fills an Export
// from the chat row, its turns and each turn's *normalized* transcript (read
// through the same normalizer that serves
// `GET /v1/chats/{id}/turns/{seq}/transcript?format=normalized`), and this
// package turns it into bytes. It imports no internal package at all — not
// `store`, and never `api` — so the CLI's transcript renderer can share its
// one-line tool summary (ToolSummary) without pulling a database in.
//
// What is in a turn is fixed by task 156 decision 3: the prompt, one line per
// tool call (name, key argument, a failed marker — never the tool's output),
// the answer (the turn's result_text) and its usage. A turn whose transcript
// is gone still renders from its row, with a one-line note in place of its
// tool list (ToolsNote), and a missing transcript is never an error.
//
// HTML goes through goldmark with raw HTML left disabled — goldmark's
// default, kept on purpose (task 156 decision 2) — so agent output cannot
// inject markup, and an image in an answer becomes a link so the page loads
// nothing from anywhere: one file, inline CSS, no scripts.
package chatexport
