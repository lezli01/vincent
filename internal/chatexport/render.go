package chatexport

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Render writes e in format f.
func Render(w io.Writer, e *Export, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(e)
	case FormatHTML:
		return renderHTML(w, e)
	default:
		_, err := io.WriteString(w, Markdown(e))
		return err
	}
}

// metaRow is one line of the metadata block, shared by both text renderings
// so Markdown and HTML list the same fields in the same order.
type metaRow struct{ Key, Value string }

func metaRows(e *Export) []metaRow {
	c := e.Chat
	rows := []metaRow{{"id", strconv.FormatInt(c.ID, 10)}}
	add := func(k, v string) {
		if v != "" {
			rows = append(rows, metaRow{k, v})
		}
	}
	add("project", c.Project)
	add("agent", c.Agent)
	add("model", c.Model)
	add("effort", c.Effort)
	add("permission mode", c.PermissionMode)
	add("state", c.State)
	add("branch", c.Branch)
	add("base branch", c.BaseBranch)
	add("base sha", c.BaseSHA)
	addID := func(k string, id *int64) {
		if id != nil {
			add(k, "#"+strconv.FormatInt(*id, 10))
		}
	}
	addID("linked task", c.LinkedTaskID)
	addID("handoff task", c.HandoffTaskID)
	addID("issue", c.IssueID)
	add("created", stamp(c.CreatedAt))
	add("exported", stamp(e.ExportedAt))
	return rows
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// title is the document's heading.
func title(e *Export) string {
	t := strings.TrimSpace(e.Chat.Title)
	if t == "" {
		return fmt.Sprintf("Chat %d", e.Chat.ID)
	}
	return fmt.Sprintf("Chat %d: %s", e.Chat.ID, t)
}

// RanWith is a turn's model · effort · permission mode (task 162), "not
// recorded" for a turn older than the record, and the adapter's default named
// as such.
func (t *Turn) RanWith() string {
	if t.PermissionMode == nil {
		return "not recorded"
	}
	model, effort := "default model", "default effort"
	if t.Model != nil {
		model = *t.Model
	}
	if t.Effort != nil {
		effort = *t.Effort
	}
	return model + " · " + effort + " · " + *t.PermissionMode
}

// Usage is a turn's usage line: tokens, cost when the adapter reports one,
// duration. A cost the adapter never reports is absent, not `$0` (§9.9).
// The cache the turn read and wrote follows its input when the adapter
// counts it apart, so the line agrees with the chat heading's ↑ (review F5).
func (t *Turn) Usage() string {
	in := fmt.Sprintf("%d in", t.InputTokens)
	var cache int64
	for _, n := range []*int64{t.CacheReadTokens, t.CacheCreationTokens} {
		if n != nil {
			cache += *n
		}
	}
	if cache > 0 {
		in += fmt.Sprintf(" (+%d cache)", cache)
	}
	parts := []string{
		in,
		fmt.Sprintf("%d out", t.OutputTokens),
	}
	if t.CostUSD != nil {
		parts = append(parts, fmt.Sprintf("$%.4f", *t.CostUSD))
	}
	if t.DurationMS != nil {
		parts = append(parts, (time.Duration(*t.DurationMS) * time.Millisecond).Round(100*time.Millisecond).String())
	}
	return strings.Join(parts, " · ")
}

// Failure is a failed turn's reason line, empty for any other turn.
func (t *Turn) Failure() string {
	switch {
	case t.FailReason != "" && t.ErrorMessage != "":
		return t.FailReason + ": " + t.ErrorMessage
	case t.FailReason != "":
		return t.FailReason
	default:
		return t.ErrorMessage
	}
}

// line is a tool call's export line: its summary, then the failed marker.
func (c ToolCall) line() string {
	if c.Failed {
		return c.Summary() + " (failed)"
	}
	return c.Summary()
}

// fence is a code fence longer than any backtick run in body, so a diff or a
// tool argument that itself contains ``` cannot close it early.
func fence(body string) string {
	return strings.Repeat("`", max(3, longestRun(body)+1))
}

// inlineCode is s as a Markdown code span whose delimiter outruns any
// backtick run inside it, padded so a leading or trailing backtick survives.
func inlineCode(s string) string {
	if !strings.Contains(s, "`") {
		return "`" + s + "`"
	}
	d := strings.Repeat("`", longestRun(s)+1)
	return d + " " + s + " " + d
}

func longestRun(s string) int {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
			continue
		}
		run = 0
	}
	return longest
}

// entry is one section of the conversation: a turn or a shell run, exactly
// one of them set.
type entry struct {
	Turn  *Turn
	Shell *ShellRun
}

// conversation is e's turns and shell runs in the order they happened: each
// run after the turn its AfterTurn names, ahead of the next one. A run whose
// AfterTurn is no turn's goes before the first turn with a greater seq, so
// every run is drawn exactly once whatever the rows say.
func conversation(e *Export) []entry {
	out := make([]entry, 0, len(e.Turns)+len(e.ShellRuns))
	next := 0
	for i := range e.Turns {
		t := &e.Turns[i]
		for next < len(e.ShellRuns) && e.ShellRuns[next].AfterTurn < t.Seq {
			out = append(out, entry{Shell: &e.ShellRuns[next]})
			next++
		}
		out = append(out, entry{Turn: t})
	}
	for ; next < len(e.ShellRuns); next++ {
		out = append(out, entry{Shell: &e.ShellRuns[next]})
	}
	return out
}

// markdownShell writes one shell run's card.
func markdownShell(b *strings.Builder, r *ShellRun) {
	fmt.Fprintf(b, "\n## Shell · %s · %s\n\n", inlineCode(oneLine(r.Command)), r.Status())
	fmt.Fprintf(b, "_%s_\n\n", r.Sent())
	if strings.Contains(r.Command, "\n") {
		f := fence(r.Command)
		fmt.Fprintf(b, "%s\n%s\n%s\n\n", f, strings.TrimRight(r.Command, "\n"), f)
	}
	switch out := strings.TrimRight(r.Output, "\n"); {
	case r.OutputNote != "":
		fmt.Fprintf(b, "_%s_\n", r.OutputNote)
	case out == "":
		b.WriteString("_no output_\n")
	default:
		f := fence(out)
		fmt.Fprintf(b, "%s\n%s\n%s\n", f, out, f)
	}
	if r.Truncated {
		b.WriteString("\n_The output reached the daemon's cap and was cut._\n")
	}
}

// oneLine is a command's first line, marked when there is more.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// Markdown is e rendered as Markdown: a title and metadata, one section per
// turn — prompt, tool calls, answer, usage, in that order — and per shell
// run, in conversation order, and the diff last, when there is one.
func Markdown(e *Export) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title(e))
	for _, r := range metaRows(e) {
		fmt.Fprintf(&b, "- **%s:** %s\n", r.Key, r.Value)
	}
	for _, en := range conversation(e) {
		if en.Shell != nil {
			markdownShell(&b, en.Shell)
			continue
		}
		t := en.Turn
		fmt.Fprintf(&b, "\n## Turn %d · %s\n\n", t.Seq, t.State)
		fmt.Fprintf(&b, "_Ran on: %s_\n\n", t.RanWith())
		b.WriteString("### Prompt\n\n")
		b.WriteString(strings.TrimRight(t.Prompt, "\n"))
		if mc := strings.TrimRight(t.MentionContext, "\n"); mc != "" {
			// fence outruns the block's own fenced line ranges, so they
			// cannot close it early.
			f := fence(mc)
			fmt.Fprintf(&b, "\n\n### Mentions\n\n%s\n%s\n%s", f, mc, f)
		}
		b.WriteString("\n\n### Tool calls\n\n")
		switch {
		case t.ToolsNote != "":
			fmt.Fprintf(&b, "_%s_\n", t.ToolsNote)
		case len(t.Tools) == 0:
			b.WriteString("_none_\n")
		default:
			for _, c := range t.Tools {
				fmt.Fprintf(&b, "- %s\n", inlineCode(c.line()))
			}
		}
		b.WriteString("\n### Answer\n\n")
		if a := strings.TrimRight(t.Answer, "\n"); a != "" {
			b.WriteString(a)
			b.WriteString("\n")
		} else {
			b.WriteString("_no answer_\n")
		}
		if f := t.Failure(); f != "" {
			fmt.Fprintf(&b, "\n**Failed:** %s\n", f)
		}
		fmt.Fprintf(&b, "\n_Usage: %s_\n", t.Usage())
	}
	switch {
	case e.Diff != nil:
		f := fence(*e.Diff)
		fmt.Fprintf(&b, "\n## Diff\n\n%sdiff\n%s\n%s\n", f, strings.TrimRight(*e.Diff, "\n"), f)
	case e.DiffUnavailable != "":
		fmt.Fprintf(&b, "\n## Diff\n\n_diff unavailable: %s_\n", e.DiffUnavailable)
	}
	return b.String()
}

//go:embed page.html.tmpl
var pageSource string

var page = template.Must(template.New("page").Parse(pageSource))

// md is the one Markdown-to-HTML converter. Raw HTML stays disabled — no
// html.WithUnsafe — so a `<script>` in an answer is omitted, not run, and
// goldmark's own dangerous-URL filter keeps `javascript:` links out.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(imagesToLinks{}, 100))),
)

// imagesToLinks turns every image into a link to it, so the page loads
// nothing from anywhere when it is opened (task 159 decision 2: no external
// assets). The reader can still follow the link.
type imagesToLinks struct{}

func (imagesToLinks) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	var images []*ast.Image
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); ok && entering {
			images = append(images, img)
		}
		return ast.WalkContinue, nil
	})
	for _, img := range images {
		link := ast.NewLink()
		link.Destination = img.Destination
		link.Title = img.Title
		for c := img.FirstChild(); c != nil; {
			next := c.NextSibling()
			link.AppendChild(link, c)
			c = next
		}
		img.Parent().ReplaceChild(img.Parent(), img, link)
	}
}

func markdownHTML(src string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		// goldmark's renderer only fails on a writer error, and a buffer has
		// none; fall back to escaped text all the same.
		return template.HTML("<pre>" + template.HTMLEscapeString(src) + "</pre>") //nolint:gosec // escaped
	}
	return template.HTML(buf.String()) //nolint:gosec // goldmark with raw HTML disabled
}

type pageTurn struct {
	*Turn
	PromptHTML template.HTML
	AnswerHTML template.HTML
	ToolLines  []string
}

// pageShell is a shell run as the page draws it.
type pageShell struct {
	*ShellRun
	Title     string
	Multiline bool
}

// pageEntry is one section of the page: a turn or a shell run.
type pageEntry struct {
	Turn  *pageTurn
	Shell *pageShell
}

type pageData struct {
	Title           string
	Meta            []metaRow
	Entries         []pageEntry
	Diff            *string
	DiffUnavailable string
}

func renderHTML(w io.Writer, e *Export) error {
	d := pageData{
		Title:           title(e),
		Meta:            metaRows(e),
		Diff:            e.Diff,
		DiffUnavailable: e.DiffUnavailable,
	}
	for _, en := range conversation(e) {
		if r := en.Shell; r != nil {
			d.Entries = append(d.Entries, pageEntry{Shell: &pageShell{
				ShellRun: r, Title: oneLine(r.Command), Multiline: strings.Contains(r.Command, "\n"),
			}})
			continue
		}
		t := en.Turn
		pt := &pageTurn{Turn: t, PromptHTML: markdownHTML(t.Prompt)}
		if t.Answer != "" {
			pt.AnswerHTML = markdownHTML(t.Answer)
		}
		for _, c := range t.Tools {
			pt.ToolLines = append(pt.ToolLines, c.line())
		}
		d.Entries = append(d.Entries, pageEntry{Turn: pt})
	}
	return page.Execute(w, d)
}
