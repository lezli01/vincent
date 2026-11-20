package chatrun

// Mentions of vincent objects and file line ranges in a chat message (task
// 166, issue #1021, spec §5.5 "Mentions of vincent objects and line ranges").
//
// `#issue-N`, `#task-N`, `#pr-N` and `@path#L20-45` are vincent's own
// mentions, not an agent CLI's: the daemon expands each one into a bounded
// block when the turn starts, on every adapter alike, and the block rides
// ahead of the message the way a linked chat's opening context does (task 166
// decisions 1 and 2). A bare `@path` is not one of them — it stays the CLI's,
// passed through and never rewritten (task 124 decision 9, task 126
// decision 1).
//
// Nothing here can refuse a turn. A token that does not resolve — an unknown
// id, another project's object, a pull request with no GitHub behind it, a
// path outside the workspace, a line past the end — is left as prose and the
// message is sent as typed: refusing a turn over a mention would be the
// daemon judging the text.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/pathx"
	"github.com/lezli01/vincent/internal/store"
)

// Mention kinds.
const (
	MentionIssue = "issue"
	MentionTask  = "task"
	MentionPull  = "pr"
	MentionLines = "lines"
)

// Bounds on what a mention expands into (task 166 decision 1 and 4). The body
// cap is task 140's issue-chat body cap, so an issue reads the same in a
// mention as in an issue chat's opening context; the line cap is the same
// number, so a range is never a way to inline a whole large file. MaxMentions
// bounds one message: past it, later tokens stay prose.
const (
	MentionBodyLimit  = 16 << 10
	MentionLinesLimit = 16 << 10
	MaxMentions       = 20
)

// mentionTruncated is the marker a clipped body or range ends with.
const mentionTruncated = "…(truncated)"

// mentionIntro opens the expanded block. It tells the model where the block
// came from, so an issue's text is never read as the human's own words.
const mentionIntro = "The human's message below mentions vincent objects or file line ranges. " +
	"vincent expanded each mention here, as it was when this turn started.\n\n"

// Mention is one token found in a message.
type Mention struct {
	// Kind is MentionIssue, MentionTask, MentionPull or MentionLines.
	Kind string `json:"kind"`
	// Token is the text as typed, `#issue-3` or `@a/b.go#L2-3`.
	Token string `json:"token"`
	// ID is the issue or task id, or the pull request number. Zero for a
	// line range.
	ID int64 `json:"id,omitempty"`
	// Path, From and To are a line range's file and lines, From == To for a
	// single line. To is what the human typed, before any clamping.
	Path string `json:"path,omitempty"`
	From int    `json:"from,omitempty"`
	To   int    `json:"to,omitempty"`
}

// Expansion is what a message's mentions expanded into.
type Expansion struct {
	// Context is the assembled block, "" when nothing resolved.
	Context string
	// Resolved and Unresolved partition the message's distinct mentions;
	// an unresolved one stayed prose.
	Resolved   []Mention
	Unresolved []Mention
}

// MentionPullRequest is what a `#pr-N` block carries (task 166 decision 4).
type MentionPullRequest struct {
	Number     int
	Title      string
	Body       string
	URL        string
	State      string // open or closed
	Merged     bool
	Draft      bool
	HeadBranch string
	BaseBranch string
}

// ErrNoPull is a PullRequest lookup that has nothing to look in: the project
// has no GitHub binding, or the integration is unusable. The mention stays
// prose either way.
var ErrNoPull = errors.New("no GitHub pull request lookup for this project")

// ParseMentions returns the distinct vincent mentions in text, in order of
// first appearance, at most MaxMentions of them. A token typed twice is one
// mention, expanded once.
//
// The grammar needs a boundary before the sigil — the start of the text,
// whitespace, or opening punctuation — so `foo#issue-1` and a Markdown
// heading are not mentions, and a boundary after: `#issue-1x` is not one,
// `#issue-1.` is.
func ParseMentions(text string) []Mention {
	var out []Mention
	seen := map[string]bool{}
	for i := 0; i < len(text) && len(out) < MaxMentions; {
		c := text[i]
		if (c != '#' && c != '@') || !boundaryBefore(text, i) {
			i++
			continue
		}
		var m Mention
		var n int
		if c == '#' {
			m, n = parseObject(text[i:])
		} else {
			m, n = parseLines(text[i:])
		}
		if n == 0 {
			i++
			continue
		}
		key := m.key()
		if !seen[key] {
			seen[key] = true
			out = append(out, m)
		}
		i += n
	}
	return out
}

// key identifies a mention for de-duplication: `#issue-03` and `#issue-3`
// are the same issue.
func (m Mention) key() string {
	if m.Kind == MentionLines {
		return fmt.Sprintf("lines:%s:%d-%d", m.Path, m.From, m.To)
	}
	return fmt.Sprintf("%s:%d", m.Kind, m.ID)
}

// boundaryBefore reports whether the sigil at i may open a mention.
func boundaryBefore(text string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:i])
	return unicode.IsSpace(r) || strings.ContainsRune("([{<\"'`,;:", r)
}

// boundaryAfter reports whether a token may end at i.
func boundaryAfter(text string, i int) bool {
	if i >= len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[i:])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '#'
}

// parseObject reads `#issue-N`, `#task-N` or `#pr-N` at the start of s,
// answering the mention and its length, or a zero length.
func parseObject(s string) (Mention, int) {
	for _, kind := range []string{MentionIssue, MentionTask, MentionPull} {
		prefix := "#" + kind + "-"
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		digits := leadingDigits(s[len(prefix):])
		end := len(prefix) + len(digits)
		if digits == "" || !boundaryAfter(s, end) {
			return Mention{}, 0
		}
		id, err := strconv.ParseInt(digits, 10, 64)
		if err != nil || id < 1 {
			return Mention{}, 0
		}
		return Mention{Kind: kind, Token: s[:end], ID: id}, end
	}
	return Mention{}, 0
}

// parseLines reads `@path#L20-45` or `@path#L20` at the start of s. A bare
// `@path` answers zero: it is the CLI's mention, not vincent's.
func parseLines(s string) (Mention, int) {
	pathEnd := 1
	for pathEnd < len(s) {
		r, size := utf8.DecodeRuneInString(s[pathEnd:])
		if unicode.IsSpace(r) || r == '#' {
			break
		}
		pathEnd += size
	}
	if pathEnd == 1 || !strings.HasPrefix(s[pathEnd:], "#L") {
		return Mention{}, 0
	}
	path := s[1:pathEnd]
	rest := s[pathEnd+2:]
	from := leadingDigits(rest)
	if from == "" {
		return Mention{}, 0
	}
	end := pathEnd + 2 + len(from)
	to := from
	if strings.HasPrefix(s[end:], "-") {
		// `-L45` is accepted as well as `-45`, the form GitHub's own links
		// spell.
		off := 1
		if strings.HasPrefix(s[end+1:], "L") {
			off = 2
		}
		if d := leadingDigits(s[end+off:]); d != "" {
			to = d
			end += off + len(d)
		}
	}
	if !boundaryAfter(s, end) {
		return Mention{}, 0
	}
	a, errA := strconv.Atoi(from)
	b, errB := strconv.Atoi(to)
	if errA != nil || errB != nil || a < 1 || b < a {
		return Mention{}, 0
	}
	return Mention{Kind: MentionLines, Token: s[:end], Path: path, From: a, To: b}, end
}

func leadingDigits(s string) string {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return s[:n]
}

// ExpandMentions expands text's mentions for chat's next turn: issues and
// tasks through the store, pull requests through Deps.PullRequest, and line
// ranges from the chat's workspace (Workspace's directory). It is the one
// function both a turn and the preview route run (task 166 decision 6), so
// the two cannot disagree. It never fails a message: anything it cannot
// resolve is reported in Unresolved and left as prose.
func (r *Runner) ExpandMentions(ctx context.Context, chat *store.Chat, text string) Expansion {
	mentions := ParseMentions(text)
	if len(mentions) == 0 {
		return Expansion{}
	}
	var workDir string
	var workErr error
	workResolved := false
	var exp Expansion
	var sb strings.Builder
	for _, m := range mentions {
		var block string
		var ok bool
		switch m.Kind {
		case MentionIssue:
			block, ok = r.issueBlock(ctx, chat, m)
		case MentionTask:
			block, ok = r.taskBlock(ctx, chat, m)
		case MentionPull:
			block, ok = r.pullBlock(ctx, chat, m)
		case MentionLines:
			if !workResolved {
				workDir, workErr = r.Workspace(ctx, chat)
				workResolved = true
			}
			if workErr == nil {
				block, ok = linesBlock(workDir, m)
			}
		}
		if !ok {
			exp.Unresolved = append(exp.Unresolved, m)
			continue
		}
		exp.Resolved = append(exp.Resolved, m)
		sb.WriteString(block)
	}
	if sb.Len() > 0 {
		exp.Context = mentionIntro + sb.String()
	}
	return exp
}

func (r *Runner) issueBlock(ctx context.Context, chat *store.Chat, m Mention) (string, bool) {
	iss, err := r.deps.Store.GetIssue(ctx, m.ID)
	if err != nil || iss.ProjectID != chat.ProjectID {
		return "", false
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<issue id=%q state=%q", strconv.FormatInt(iss.ID, 10), string(iss.State))
	if iss.CloseReason != "" {
		fmt.Fprintf(&sb, " close_reason=%q", string(iss.CloseReason))
	}
	if iss.Kind != "" {
		fmt.Fprintf(&sb, " kind=%q", iss.Kind)
	}
	if iss.Remote != nil && iss.Remote.URL != "" {
		fmt.Fprintf(&sb, " url=%q", iss.Remote.URL)
	}
	if iss.MainWorktree.Branch != "" {
		fmt.Fprintf(&sb, " branch=%q", iss.MainWorktree.Branch)
	}
	sb.WriteString(">\n")
	writeTitleBody(&sb, iss.Title, iss.Body)
	sb.WriteString("</issue>\n\n")
	return sb.String(), true
}

func (r *Runner) taskBlock(ctx context.Context, chat *store.Chat, m Mention) (string, bool) {
	task, err := r.deps.Store.GetTask(ctx, m.ID)
	if err != nil || task.ProjectID != chat.ProjectID {
		return "", false
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<task id=%q state=%q", strconv.FormatInt(task.ID, 10), string(task.State))
	if task.WorkflowName != "" {
		fmt.Fprintf(&sb, " workflow=%q", task.WorkflowName)
	}
	if task.BranchName != "" {
		fmt.Fprintf(&sb, " branch=%q", task.BranchName)
	}
	if task.BaseBranch != "" {
		fmt.Fprintf(&sb, " base=%q", task.BaseBranch)
	}
	sb.WriteString(">\n")
	writeTitleBody(&sb, task.Title, task.Description)
	sb.WriteString("</task>\n\n")
	return sb.String(), true
}

func (r *Runner) pullBlock(ctx context.Context, chat *store.Chat, m Mention) (string, bool) {
	if r.deps.PullRequest == nil {
		return "", false
	}
	pull, err := r.deps.PullRequest(ctx, chat.ProjectID, int(m.ID))
	if err != nil {
		return "", false
	}
	state := pull.State
	switch {
	case pull.Merged:
		state = "merged"
	case pull.Draft && state == "open":
		state = "draft"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<pull_request number=%q state=%q", strconv.Itoa(pull.Number), state)
	if pull.HeadBranch != "" {
		fmt.Fprintf(&sb, " head=%q", pull.HeadBranch)
	}
	if pull.BaseBranch != "" {
		fmt.Fprintf(&sb, " base=%q", pull.BaseBranch)
	}
	if pull.URL != "" {
		fmt.Fprintf(&sb, " url=%q", pull.URL)
	}
	sb.WriteString(">\n")
	writeTitleBody(&sb, pull.Title, pull.Body)
	sb.WriteString("</pull_request>\n\n")
	return sb.String(), true
}

func writeTitleBody(sb *strings.Builder, title, body string) {
	fmt.Fprintf(sb, "title: %s\n", title)
	if body = strings.TrimRight(body, "\r\n"); body != "" {
		sb.WriteString("\n" + clipBytes(body, MentionBodyLimit) + "\n")
	}
}

// linesBlock reads m's range from the workspace at dir and renders it as a
// fenced block. A path that is absolute, climbs out with `..`, or resolves
// through a symlink to somewhere outside the workspace is not read: vincent
// does not read outside the workspace for an agent (task 166 decision 1). A
// missing file, a directory, a binary file and a range that starts past the
// end are left as prose too; a range that ends past the end is clamped.
func linesBlock(dir string, m Mention) (string, bool) {
	rel := filepath.FromSlash(m.Path)
	if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" ||
		strings.HasPrefix(m.Path, "/") || strings.HasPrefix(m.Path, `\`) {
		return "", false
	}
	for _, part := range strings.FieldsFunc(m.Path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", false
		}
	}
	full := filepath.Join(dir, rel)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil || !pathx.Contains(realDir, realFull) {
		return "", false
	}
	info, err := os.Stat(realFull)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	f, err := os.Open(realFull)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	lines, last, truncated, ok := readRange(f, m.From, m.To, MentionLinesLimit)
	if !ok {
		return "", false
	}
	body := strings.Join(lines, "\n")
	fence := fenceFor(body)
	span := strconv.Itoa(m.From)
	if last != m.From {
		span += "-" + strconv.Itoa(last)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<file path=%q lines=%q>\n%s\n%s\n", filepath.ToSlash(m.Path), span, fence, body)
	if truncated {
		sb.WriteString(mentionTruncated + "\n")
	}
	fmt.Fprintf(&sb, "%s\n</file>\n\n", fence)
	return sb.String(), true
}

// readRange reads lines from..to (1-based, inclusive) from rd, CRLF or LF,
// at most limit bytes of them. It answers the lines, the last line number it
// returned, whether the limit cut the range short, and false when from is
// past the end or the range holds a NUL byte (a binary file is not inlined).
//
// Memory is bounded by limit, not by the file (review F5): a line is read in
// bufio-sized slices and only the part that can still be inlined is kept, so
// a minified bundle's one 200 MB line or a log's nine-millionth line costs a
// buffer, not the line or the prefix before it.
func readRange(rd io.Reader, from, to, limit int) (lines []string, last int, truncated, ok bool) {
	br := bufio.NewReader(rd)
	size := 0
	for n := 1; n <= to; n++ {
		keep := 0
		if n >= from {
			keep = limit - size
		}
		ln, err := readLine(br, keep)
		if !ln.read {
			break
		}
		if n < from {
			if err != nil {
				break
			}
			continue
		}
		if ln.nul {
			return nil, 0, false, false
		}
		if size+ln.length+1 > limit {
			if room := limit - size; room > 0 {
				lines = append(lines, clipRunes(ln.kept, room))
				last = n
			}
			return lines, last, true, last >= from
		}
		size += ln.length + 1
		lines = append(lines, ln.kept)
		last = n
		if err != nil {
			break
		}
	}
	return lines, last, false, last >= from
}

// rangeLine is one line as readLine consumed it.
type rangeLine struct {
	kept   string // at most the first keep bytes of the line, CR/LF excluded
	length int    // the whole line's length, CR/LF excluded
	nul    bool   // the line holds a NUL byte anywhere
	read   bool   // anything was read at all; false at the end of input
}

// readLine consumes one line from br, keeping at most keep bytes of it and
// discarding the rest without holding it, while still scanning all of it for
// a NUL. The error is the reader's: nil when the line ended in a newline.
func readLine(br *bufio.Reader, keep int) (rangeLine, error) {
	var (
		ln     rangeLine
		kept   []byte
		lastCR bool
	)
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			ln.read = true
		}
		body := chunk
		if err == nil {
			body = chunk[:len(chunk)-1]
		}
		if bytes.IndexByte(body, 0) >= 0 {
			ln.nul = true
		}
		// One byte past keep, so a trailing CR inside it can be dropped
		// without losing a byte of the line.
		if room := keep + 1 - len(kept); room > 0 {
			kept = append(kept, body[:min(room, len(body))]...)
		}
		if len(body) > 0 {
			lastCR = body[len(body)-1] == '\r'
		}
		ln.length += len(body)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if lastCR {
			ln.length--
		}
		ln.kept = string(kept[:min(len(kept), ln.length, keep)])
		return ln, err
	}
}

// fenceFor answers a backtick fence longer than any run inside body, so a
// Markdown file's own fences cannot close the block early.
func fenceFor(body string) string {
	longest, run := 0, 0
	for i := range len(body) {
		if body[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

// clipBytes cuts s to at most n bytes on a rune boundary, saying so.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return clipRunes(s, n) + "\n" + mentionTruncated
}

// clipRunes cuts s to at most n bytes on a rune boundary.
func clipRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// joinPreamble is what rides ahead of a turn's message: a linked or issue
// chat's opening context on its first turn, then the turn's own mention
// context (task 166 decision 2).
func joinPreamble(opening, mentions string) string {
	switch {
	case opening == "":
		return mentions
	case mentions == "":
		return opening
	}
	return strings.TrimRight(opening, "\n") + "\n\n" + mentions
}
