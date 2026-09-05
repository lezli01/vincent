package workflow

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// This file is the write half of §8 (task 065): the daemon edits a workflow
// file on behalf of a client. It extends internal/config's line-oriented
// editor (task 060 decision 9) to the structural operations a workflow needs
// and config never did — inserting and removing sequence items, creating a
// nested block, writing a `|` block scalar, reordering steps.
//
// It is line-oriented for the reason SetName's doc states outright: a workflow
// file is hand-written and the create-workflow built-in writes it with
// comments, so a marshal round trip through Workflow would silently delete an
// author's notes on the first save. goccy's AST does not help — it does not
// re-emit untouched bytes identically, and blank lines are not tokens. The
// guarantee this file owes its callers is therefore stated on the bytes: an
// untouched region comes back byte-identical (task 065 decision 1).
//
// The client never composes YAML. It sends operations — a path plus set /
// insert / remove / move — and the daemon renders the lines, which is what
// keeps the original bytes under the daemon's control end to end
// (decision 2).

// Op is one edit. Kind is "set", "insert", "remove" or "move".
type Op struct {
	Kind string `json:"op"`
	// Path is dotted with list indices: "description", "steps[2].prompt",
	// "steps[1].steps[0].id", "steps[3].lanes[0].merge.on_conflict",
	// "fields[1].values".
	Path string `json:"path"`
	// Value is the rendered YAML text of a "set". A single line unless Block
	// is set, in which case it is the literal body of a `|` block scalar and
	// may hold newlines.
	Value string `json:"value,omitempty"`
	Block bool   `json:"block,omitempty"`
	// Item is the new sequence entry an "insert" writes, as ordered keys.
	// The daemon renders them; a client that had to spell YAML would have had
	// to discard the comments to do it.
	Item []OpField `json:"item,omitempty"`
	// To is the destination index of a "move". The source is Path's own
	// trailing index, and both are indices into the same sequence.
	To int `json:"to,omitempty"`
}

// OpField is one key of an inserted sequence entry.
type OpField struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
	Block bool   `json:"block,omitempty"`
}

// Op kinds.
const (
	OpSet    = "set"
	OpInsert = "insert"
	OpRemove = "remove"
	OpMove   = "move"
)

// MaxOps bounds one PATCH. A form submits one operation per changed row, and
// a workflow has far fewer rows than this; the bound exists so a request
// cannot make the daemon rewrite a file an unbounded number of times.
const MaxOps = 512

// FilePerm is the mode a workflow file this package creates is written with.
// It is 0644 rather than config's 0600 deliberately: a project workflow lives
// in the repository and is meant to be committed and read by a team (§5.2),
// and it carries no secret — the agent options it names are not credentials.
// An existing file keeps whatever mode it already has.
const FilePerm os.FileMode = 0o644

// Edit applies ops to src and returns the result. Operations are applied in
// order against the evolving document, so a caller may insert a step and then
// set a field on it. Every byte outside the lines an operation rewrote is
// returned unchanged, CRLF line endings included.
func Edit(src []byte, ops []Op) ([]byte, error) {
	if len(ops) > MaxOps {
		return nil, fmt.Errorf("at most %d operations may be applied at once, got %d", MaxOps, len(ops))
	}
	d := newDoc(src)
	for i, op := range ops {
		if err := d.apply(op); err != nil {
			return nil, fmt.Errorf("ops[%d]: %w", i, err)
		}
	}
	return d.bytes(), nil
}

// document is a workflow source held as lines plus the line ending it was
// written with. Everything below edits the slice; nothing re-emits a line it
// did not change.
type document struct {
	lines []string
	// eol is "\r\n" for a file that used CRLF anywhere, "\n" otherwise. A
	// file is written back with the endings it arrived with rather than
	// normalised: rewriting every line of a Windows author's file is exactly
	// the byte-identity failure this package exists to avoid.
	eol string
	// trailingNewline records whether the source ended with one, so a file
	// that did not is not silently given one.
	trailingNewline bool
}

func newDoc(src []byte) *document {
	s := string(src)
	d := &document{eol: "\n"}
	if strings.Contains(s, "\r\n") {
		d.eol = "\r\n"
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	if strings.HasSuffix(s, "\n") {
		d.trailingNewline = true
		s = s[:len(s)-1]
	}
	if s == "" {
		d.lines = nil
		return d
	}
	d.lines = strings.Split(s, "\n")
	return d
}

func (d *document) bytes() []byte {
	out := strings.Join(d.lines, d.eol)
	if d.trailingNewline && (out != "" || len(d.lines) > 0) {
		out += d.eol
	}
	return []byte(out)
}

// node is one mapping entry or sequence item in the parsed document. line is
// the index of the line that starts it and end is one past its last content
// line — trailing blank lines and the comment block introducing the next
// entry are deliberately outside it, so removing a step does not take the
// next step's header comment with it.
type node struct {
	key      string // mapping key; "" for a sequence item
	index    int    // sequence position; -1 for a mapping entry
	indent   int    // column the entry starts at
	content  int    // column this node's children start at; -1 when unknown
	line     int
	end      int
	children []*node
}

func (n *node) isItem() bool { return n.index >= 0 }

var (
	// wfKeyLine matches a mapping key. Workflow keys are snake_case like
	// config's, which keeps a prose comment from reading as a key.
	wfKeyLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.-]*):(\s|$)`)
	// blockScalar matches the value side of a key introducing a block
	// scalar: `|`, `|-`, `>+`, `|2` and the rest of the family.
	blockScalar = regexp.MustCompile(`^[|>][0-9]*[+-]?\s*(#.*)?$`)
	// pathSegment splits a dotted path into keys and `[n]` indices.
	pathSegment = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.-]*)((\[[0-9]+\])*)$`)
	// safeName restricts the file name a workflow is written to. It is the
	// registry's own slug shape, so a name that reaches the disk cannot
	// escape the scope directory it was addressed to.
	safeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
	// wfPlainScalar matches a string YAML reads back verbatim unquoted.
	wfPlainScalar = regexp.MustCompile(`^[A-Za-z0-9_./\\:+@-]+$`)
)

// parse builds the tree for the current lines. It is rebuilt before every
// operation rather than maintained: an edit shifts line numbers, and a stale
// tree is the bug that would silently corrupt a file.
func (d *document) parse() *node {
	root := &node{index: -1, indent: -1, content: 0, line: 0, end: len(d.lines)}
	stack := []*node{root}
	pop := func(to int, upto int) {
		for len(stack) > upto {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			n.end = d.trimBack(n.line+1, to)
		}
	}
	for i := 0; i < len(d.lines); i++ {
		raw := d.lines[i]
		body := strings.TrimLeft(raw, " \t")
		if body == "" || strings.HasPrefix(body, "#") {
			continue
		}
		col := len(raw) - len(body)
		if strings.HasPrefix(body, "- ") || body == "-" {
			// A sequence item belongs to the nearest mapping key at or left
			// of its dash — YAML allows the dash in the key's own column,
			// so an item is not popped by "indent <= parent".
			for len(stack) > 1 {
				top := stack[len(stack)-1]
				if top.indent < col || (!top.isItem() && top.indent == col) {
					break
				}
				stack = stack[:len(stack)-1]
				top.end = d.trimBack(top.line+1, i)
			}
			parent := stack[len(stack)-1]
			item := &node{index: len(parent.children), indent: col, content: -1, line: i, end: i + 1}
			parent.children = append(parent.children, item)
			stack = append(stack, item)
			if body == "-" {
				continue
			}
			// The rest of the line is the item's first entry, at the column
			// it actually occupies.
			inner := strings.TrimLeft(body[1:], " ")
			col += len(body) - len(inner)
			body = inner
			item.content = col
		}
		m := wfKeyLine.FindStringSubmatch(body)
		if m == nil {
			// A bare scalar sequence item ("- name") or a continuation line.
			// Neither is addressable by a path, so it only bounds extents.
			continue
		}
		for len(stack) > 1 && stack[len(stack)-1].indent >= col {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			top.end = d.trimBack(top.line+1, i)
		}
		parent := stack[len(stack)-1]
		if parent.content < 0 {
			parent.content = col
		}
		n := &node{key: m[1], index: -1, indent: col, content: -1, line: i, end: i + 1}
		parent.children = append(parent.children, n)
		stack = append(stack, n)
		if rest := strings.TrimSpace(body[len(m[1])+1:]); blockScalar.MatchString(rest) && rest != "" {
			// Consume the block scalar's body: those lines are text, not
			// YAML, and a "name:" inside a prompt is not a key.
			j := i + 1
			for ; j < len(d.lines); j++ {
				l := d.lines[j]
				if strings.TrimSpace(l) == "" {
					continue
				}
				if len(l)-len(strings.TrimLeft(l, " \t")) <= col {
					break
				}
			}
			n.end = d.trimBack(i+1, j)
			i = n.end - 1
			stack = stack[:len(stack)-1]
		}
	}
	pop(len(d.lines), 1)
	root.end = len(d.lines)
	return root
}

// trimBack returns end with trailing blank lines and the comment block that
// introduces whatever comes next excluded, but never below from.
func (d *document) trimBack(from, end int) int {
	for end > from {
		body := strings.TrimSpace(d.lines[end-1])
		if body == "" || strings.HasPrefix(body, "#") {
			end--
			continue
		}
		break
	}
	if end < from {
		return from
	}
	return end
}

// segment is one step of a resolved path: a key, then any indices applied to
// the sequence it names.
type segment struct {
	key     string
	indices []int
}

func parsePath(path string) ([]segment, error) {
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	var out []segment
	for _, part := range strings.Split(path, ".") {
		m := pathSegment.FindStringSubmatch(part)
		if m == nil {
			return nil, fmt.Errorf("malformed path segment %q in %q", part, path)
		}
		seg := segment{key: m[1]}
		for _, idx := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(m[2], "["), "]"), "][") {
			if idx == "" {
				continue
			}
			n, err := strconv.Atoi(idx)
			if err != nil {
				return nil, fmt.Errorf("malformed index in %q", part)
			}
			seg.indices = append(seg.indices, n)
		}
		out = append(out, seg)
	}
	return out, nil
}

// find walks segs from n, returning the addressed node. Missing is not an
// error here: set creates what it needs, and the callers that require an
// existing node say so.
func findNode(n *node, segs []segment) *node {
	cur := n
	for _, seg := range segs {
		next := child(cur, seg.key)
		if next == nil {
			return nil
		}
		cur = next
		for _, idx := range seg.indices {
			if idx < 0 || idx >= len(cur.children) {
				return nil
			}
			cur = cur.children[idx]
		}
	}
	return cur
}

func child(n *node, key string) *node {
	for _, c := range n.children {
		if c.key == key {
			return c
		}
	}
	return nil
}

func (d *document) apply(op Op) error {
	segs, err := parsePath(op.Path)
	if err != nil {
		return err
	}
	switch op.Kind {
	case OpSet:
		return d.set(segs, op.Value, op.Block)
	case OpInsert:
		return d.insert(segs, op.Item)
	case OpRemove:
		return d.remove(segs)
	case OpMove:
		return d.move(segs, op.To)
	default:
		return fmt.Errorf("unknown operation %q (one of %s, %s, %s, %s)",
			op.Kind, OpSet, OpInsert, OpRemove, OpMove)
	}
}

// set assigns a scalar, flow value or block scalar to a path, creating the
// mapping blocks above it when they do not exist yet. A path whose missing
// parent is a sequence index is refused rather than invented: there is no
// defensible place to put an item nobody asked for.
func (d *document) set(segs []segment, value string, block bool) error {
	root := d.parse()
	if n := findNode(root, segs); n != nil {
		d.replaceValue(n, value, block)
		return nil
	}
	// Walk as deep as the document goes, then create the rest.
	depth := 0
	parent := root
	for depth < len(segs) {
		n := findNode(parent, segs[depth:depth+1])
		if n == nil {
			break
		}
		parent = n
		depth++
	}
	if depth == len(segs) {
		return fmt.Errorf("cannot resolve %q", joinPath(segs))
	}
	for _, seg := range segs[depth:] {
		if len(seg.indices) > 0 {
			return fmt.Errorf("no %s exists at %q", seg.key, joinPath(segs[:depth+1]))
		}
	}
	col := d.childColumn(parent)
	at := parent.end
	var block1 []string
	for i, seg := range segs[depth : len(segs)-1] {
		block1 = append(block1, strings.Repeat(" ", col+i*2)+seg.key+":")
	}
	last := segs[len(segs)-1].key
	lead := col + (len(segs)-depth-1)*2
	block1 = append(block1, d.renderEntry(lead, last, value, block)...)
	d.splice(at, at, block1)
	return nil
}

// childColumn is the column a new child of n belongs at: the column its
// existing children already use, or two spaces in from n.
func (d *document) childColumn(n *node) int {
	if n.content >= 0 {
		return n.content
	}
	if n.indent < 0 {
		return 0
	}
	return n.indent + 2
}

// renderEntry renders "key: value", or a `|` block scalar when block is set.
func (d *document) renderEntry(col int, key, value string, block bool) []string {
	pad := strings.Repeat(" ", col)
	if !block {
		if value == "" {
			return []string{pad + key + ":"}
		}
		return []string{pad + key + ": " + value}
	}
	out := []string{pad + key + ": |"}
	body := strings.Repeat(" ", col+2)
	for _, l := range strings.Split(strings.TrimRight(strings.ReplaceAll(value, "\r\n", "\n"), "\n"), "\n") {
		if l == "" {
			out = append(out, "")
			continue
		}
		out = append(out, body+l)
	}
	return out
}

// replaceValue rewrites the value of an existing key, keeping the key's own
// column and its trailing comment where one fits. A key that held a block
// scalar has its whole body replaced; a key that gains one grows into the
// lines it needs.
func (d *document) replaceValue(n *node, value string, block bool) {
	if n.isItem() {
		// Addressing a sequence item as a scalar is meaningless; the caller
		// addresses one of its keys instead. Leave the document alone.
		return
	}
	line := d.lines[n.line]
	body := strings.TrimLeft(line, " ")
	rest := ""
	if m := wfKeyLine.FindStringSubmatch(body); m != nil {
		rest = body[len(m[1])+1:]
	}
	out := d.renderEntry(n.indent, n.key, value, block)
	if !block && n.end == n.line+1 {
		// Keep the comment, and keep the column it sat in where the new
		// value still leaves room for it: a file whose comments line up is
		// one an author lined up on purpose.
		if c := wfCommentStart(rest); c >= 0 {
			comment := strings.TrimRight(rest[c:], " ")
			col := n.indent + len(n.key) + 1 + c
			if pad := col - len(out[0]); pad > 0 {
				out[0] += strings.Repeat(" ", pad) + comment
			} else {
				out[0] += " " + comment
			}
		}
	}
	d.splice(n.line, n.end, out)
}

// wfCommentStart returns the index of a trailing "#" comment in a value, or
// -1, skipping a "#" inside quotes.
func wfCommentStart(s string) int {
	var quote byte
	for i := range len(s) {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return i
		}
	}
	return -1
}

// insert adds a sequence entry. The path's last segment names the sequence
// and carries the position: "steps[2]" inserts before the current third step,
// and an index equal to the current length appends.
func (d *document) insert(segs []segment, item []OpField) error {
	if len(item) == 0 {
		return fmt.Errorf("insert requires at least one field")
	}
	last := segs[len(segs)-1]
	if len(last.indices) == 0 {
		return fmt.Errorf("insert needs a list index, as in %s[0]", last.key)
	}
	at := last.indices[len(last.indices)-1]
	holder := segs[len(segs)-1]
	holder.indices = holder.indices[:len(holder.indices)-1]
	lookup := append(append([]segment{}, segs[:len(segs)-1]...), holder)

	root := d.parse()
	seq := findNode(root, lookup)
	if seq == nil {
		// The sequence key does not exist yet: create it empty, then place
		// the item inside it.
		if err := d.set(lookup, "", false); err != nil {
			return err
		}
		root = d.parse()
		seq = findNode(root, lookup)
		if seq == nil {
			return fmt.Errorf("cannot resolve %q", joinPath(lookup))
		}
	}
	if at < 0 || at > len(seq.children) {
		return fmt.Errorf("index %d is out of range for %q (%d entries)", at, joinPath(lookup), len(seq.children))
	}
	dash, col := d.itemColumns(seq)
	lines := d.renderItem(dash, col, item)
	pos := seq.end
	if at < len(seq.children) {
		pos = d.leadStart(seq.children[at].line)
	}
	d.splice(pos, pos, lines)
	return nil
}

// leadStart backs line up over the comment block that introduces it. An
// entry's extent deliberately excludes that block — it belongs to the entry
// below it — so an insertion has to reclaim it, or a new step would land
// between a comment and the step it describes.
func (d *document) leadStart(line int) int {
	for line > 0 && strings.HasPrefix(strings.TrimSpace(d.lines[line-1]), "#") {
		line--
	}
	return line
}

// itemColumns reports the dash column and the content column new entries of
// seq must use — copied from the entries already there, so a file that
// indents its sequences keeps indenting them.
func (d *document) itemColumns(seq *node) (dash, content int) {
	if len(seq.children) > 0 {
		first := seq.children[0]
		c := first.content
		if c < 0 {
			c = first.indent + 2
		}
		return first.indent, c
	}
	if seq.indent < 0 {
		return 0, 2
	}
	return seq.indent, seq.indent + 2
}

func (d *document) renderItem(dash, col int, item []OpField) []string {
	var out []string
	for i, f := range item {
		lines := d.renderEntry(col, f.Key, f.Value, f.Block)
		if i == 0 {
			head := lines[0]
			lines[0] = strings.Repeat(" ", dash) + "- " + strings.TrimLeft(head, " ")
			if pad := col - dash - 2; pad > 0 {
				lines[0] = strings.Repeat(" ", dash) + "-" + strings.Repeat(" ", pad+1) + strings.TrimLeft(head, " ")
			}
		}
		out = append(out, lines...)
	}
	return out
}

// remove deletes the addressed entry — a sequence item with its whole body,
// or a mapping key with its whole block.
func (d *document) remove(segs []segment) error {
	root := d.parse()
	n := findNode(root, segs)
	if n == nil {
		return fmt.Errorf("nothing at %q to remove", joinPath(segs))
	}
	d.splice(n.line, n.end, nil)
	return nil
}

// move reorders a sequence entry. Both indices are into the same sequence,
// which is why no re-indentation is needed: the lines keep the columns they
// already had.
func (d *document) move(segs []segment, to int) error {
	last := segs[len(segs)-1]
	if len(last.indices) == 0 {
		return fmt.Errorf("move needs a list index, as in %s[0]", last.key)
	}
	from := last.indices[len(last.indices)-1]
	holder := last
	holder.indices = holder.indices[:len(holder.indices)-1]
	lookup := append(append([]segment{}, segs[:len(segs)-1]...), holder)

	root := d.parse()
	seq := findNode(root, lookup)
	if seq == nil {
		return fmt.Errorf("nothing at %q to move", joinPath(lookup))
	}
	n := len(seq.children)
	if from < 0 || from >= n {
		return fmt.Errorf("index %d is out of range for %q (%d entries)", from, joinPath(lookup), n)
	}
	if to < 0 || to >= n {
		return fmt.Errorf("destination %d is out of range for %q (%d entries)", to, joinPath(lookup), n)
	}
	if from == to {
		return nil
	}
	src := seq.children[from]
	moved := append([]string(nil), d.lines[src.line:src.end]...)
	d.splice(src.line, src.end, nil)

	root = d.parse()
	seq = findNode(root, lookup)
	if seq == nil {
		return fmt.Errorf("nothing at %q to move", joinPath(lookup))
	}
	pos := seq.end
	if to < len(seq.children) {
		pos = d.leadStart(seq.children[to].line)
	}
	d.splice(pos, pos, moved)
	return nil
}

// splice replaces lines [from,to) with repl.
func (d *document) splice(from, to int, repl []string) {
	if from < 0 {
		from = 0
	}
	if to > len(d.lines) {
		to = len(d.lines)
	}
	if to < from {
		to = from
	}
	out := make([]string, 0, len(d.lines)-(to-from)+len(repl))
	out = append(out, d.lines[:from]...)
	out = append(out, repl...)
	out = append(out, d.lines[to:]...)
	d.lines = out
}

func joinPath(segs []segment) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		p := s.key
		for _, i := range s.indices {
			p += "[" + strconv.Itoa(i) + "]"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ".")
}

// RenderScalar renders a Go string as a one-line YAML scalar: bare where YAML
// reads it back unchanged, double-quoted otherwise. It is config.RenderString's
// rule, repeated here rather than imported because internal/config must not
// become a dependency of internal/workflow's write path.
func RenderScalar(s string) string {
	if s == "" || !wfPlainScalar.MatchString(s) || isYAMLKeyword(s) {
		return strconv.Quote(s)
	}
	return s
}

func isYAMLKeyword(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	return false
}

// RenderList renders a []string as a flow sequence.
func RenderList(items []string) string {
	parts := make([]string, 0, len(items))
	for _, s := range items {
		parts = append(parts, RenderScalar(s))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// RenderMap renders a map[string]string as a flow mapping, keys sorted so the
// same map always writes the same bytes.
func RenderMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, RenderScalar(k)+": "+RenderScalar(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// FileName is the file a workflow named name is written to in a scope
// directory. The name is the registry key (§5.2) and is restricted to a slug
// so it cannot address anything outside the directory it was scoped to.
func FileName(name string) (string, error) {
	if !safeName.MatchString(name) {
		return "", fmt.Errorf("workflow name %q must be lowercase letters, digits, '-', '_' or '.', starting with a letter or digit", name)
	}
	if name == "." || strings.Contains(name, "..") {
		return "", fmt.Errorf("workflow name %q is not a valid file name", name)
	}
	return name + ".yaml", nil
}

// WriteFile writes b to path atomically. A new file is created 0644 (§5.2: a
// project workflow is committed and shared); an existing file keeps the mode
// it already had, because the daemon is not the authority on a file a
// repository owns.
func WriteFile(path string, b []byte) (err error) {
	perm := FilePerm
	if fi, statErr := os.Stat(path); statErr == nil {
		perm = fi.Mode().Perm()
	}
	tmp := path + ".vincent-tmp"
	//nolint:gosec // G304: path is a scope directory the daemon resolved itself
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("write workflow: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("write workflow: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("write workflow: %w", err)
	}
	// A rename keeps the temporary file's own mode, so it is set on the
	// replacement rather than inherited from the target — which is why the
	// chmod and the rename are one step (replace_unix.go, replace_windows.go).
	if err = replaceFile(tmp, path, perm); err != nil {
		return fmt.Errorf("write workflow: %w", err)
	}
	return nil
}
