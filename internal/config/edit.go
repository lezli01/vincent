package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// This file is the write half of §12.3 (task 060): the daemon edits its own
// config.yaml on behalf of a client, and the file it edits is documentation as
// much as it is settings. EnsureDefaultFile ships a heavily commented template
// in which several blocks are commented out on purpose, so regenerating the
// file from the Config struct — the obvious implementation — would flatten the
// only explanation most installations have of what the keys mean.
//
// So the editor is line-oriented rather than a marshal/unmarshal round trip.
// It applies dotted-path assignments to the bytes: an existing key is edited
// in place, a key that exists only as a commented-out block is uncommented
// where it stands, and a key with no block at all is appended. Comments, key
// order and blank lines survive all three.
//
// It lives in internal/config rather than in internal/api so it sits beside
// the template it edits, and so validate() can stay unexported: the API
// validates through Validate/Decode, never by reaching into the struct.

// Set is one assignment: a dotted path into config.yaml and the YAML text of
// its new value. Value is already rendered — the caller chose the scalar or
// flow-style form — because the editor deals in lines, not in types.
type Set struct {
	// Path is dotted and lowercase: "log_level", "defaults.agent_timeout",
	// "agents.claude.path", "tui.board.group_by".
	Path string
	// Value is a single line of YAML: a scalar ("60m", "true", "3"), a flow
	// sequence ("[project, workflow]") or a flow mapping ("{LANG: C.UTF-8}").
	// Multi-line block style is deliberately not produced: a value that
	// occupies one line can be swapped without disturbing what surrounds it.
	Value string
}

// keyLine matches a mapping key at the start of a (virtual) line. Names are
// restricted to the snake_case shape every config key has, which is what
// keeps a prose comment like "# WARNING: ..." from being read as a key.
var keyLine = regexp.MustCompile(`^([a-z_][a-z0-9_]*):(\s|$)`)

// plainScalar matches a string that YAML will read back verbatim without
// quotes. Anything else is emitted double-quoted.
var plainScalar = regexp.MustCompile(`^[A-Za-z0-9_./\\:+@-]+$`)

// Apply returns src with every set applied, preserving comments, key order and
// blank lines. Sets are applied in order, so two sets on one path leave the
// last one in the file.
func Apply(src []byte, sets []Set) ([]byte, error) {
	lines := splitLines(string(src))
	for _, s := range sets {
		segs := strings.Split(s.Path, ".")
		if len(segs) == 0 || s.Path == "" {
			return nil, fmt.Errorf("empty config path")
		}
		lines = applySet(lines, segs, s.Value)
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// applySet places one assignment, in the three-step order the file's shape
// asks for: edit an active key, else uncomment a documented block, else
// append. It cannot fail: the third case is a fallback rather than a refusal,
// so every path a caller can name lands somewhere.
func applySet(lines []string, segs []string, value string) []string {
	if i, ok := findKey(lines, segs, false); ok {
		lines[i] = setValue(lines[i], value)
		return lines
	}
	if i, ok := findKey(lines, segs, true); ok {
		return uncomment(lines, segs, i, value)
	}
	return appendKey(lines, segs, value)
}

// findKey returns the index of the line holding segs. With commented true it
// searches the *virtual* file — commented lines read as the lines they would
// be if uncommented — which is how a documented-but-disabled block is found.
//
// Both walks track a key stack by indentation, so "max_depth" under "fan_out"
// is not confused with "max_depth" under "include".
func findKey(lines []string, segs []string, commented bool) (int, bool) {
	var stack []string
	var indents []int
	for i, raw := range lines {
		ind, body, isComment := virtual(raw)
		if isComment && !commented {
			continue
		}
		m := keyLine.FindStringSubmatch(body)
		if m == nil {
			continue
		}
		for len(indents) > 0 && indents[len(indents)-1] >= ind {
			stack = stack[:len(stack)-1]
			indents = indents[:len(indents)-1]
		}
		stack = append(stack, m[1])
		indents = append(indents, ind)
		if len(stack) == len(segs) && samePath(stack, segs) {
			// A commented search still accepts an active line: an active
			// parent with a commented child is the environment block's shape.
			return i, true
		}
	}
	return 0, false
}

func samePath(stack, segs []string) bool {
	for i := range segs {
		if stack[i] != segs[i] {
			return false
		}
	}
	return true
}

// virtual reads a line as the mapping entry it represents. A commented line
// reports the indentation it would have once uncommented: "#   command:"
// inside a top-level block is a key at indent 2, exactly as "  command:" is.
func virtual(line string) (indent int, body string, commented bool) {
	rest := strings.TrimLeft(line, " ")
	indent = len(line) - len(rest)
	if !strings.HasPrefix(rest, "#") {
		return indent, rest, false
	}
	rest = rest[1:]
	// Exactly one space belongs to the comment marker; the rest is the
	// indentation of the line that was commented out.
	rest = strings.TrimPrefix(rest, " ")
	inner := strings.TrimLeft(rest, " ")
	return indent + len(rest) - len(inner), inner, true
}

// setValue replaces the value on an existing key line, keeping its trailing
// comment and, where it still fits, the column that comment sat in.
func setValue(line, value string) string {
	ind, body, _ := virtual(line)
	m := keyLine.FindStringSubmatch(body)
	if m == nil {
		return line
	}
	rest := body[len(m[1])+1:]
	prefix := strings.Repeat(" ", ind) + m[1] + ": " + value
	c := commentStart(rest)
	if c < 0 {
		return prefix
	}
	col := ind + len(m[1]) + 1 + c
	if pad := col - len(prefix); pad > 0 {
		return prefix + strings.Repeat(" ", pad) + strings.TrimRight(rest[c:], " ")
	}
	return prefix + " " + strings.TrimRight(rest[c:], " ")
}

// commentStart returns the index of a trailing "#" comment in a value, or -1.
// It skips a "#" inside quotes, which a webhook URL fragment or a colour
// literal can carry.
func commentStart(s string) int {
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

// uncomment activates the commented block at idx and every commented ancestor
// above it, so setting notify.on turns "# notify:" into "notify:" in the same
// edit. Ancestors that are already active are left alone.
func uncomment(lines []string, segs []string, idx int, value string) []string {
	for depth := 1; depth < len(segs); depth++ {
		if i, ok := findKey(lines, segs[:depth], true); ok {
			if _, _, isComment := virtual(lines[i]); isComment {
				lines[i] = activate(lines[i])
			}
		}
	}
	lines[idx] = setValue(activate(lines[idx]), value)
	return lines
}

// activate rewrites a commented line as the line it stands for.
func activate(line string) string {
	ind, body, commented := virtual(line)
	if !commented {
		return line
	}
	return strings.Repeat(" ", ind) + body
}

// appendKey adds a key the file has no block for at all — the fallback path,
// reached only by a config.yaml written before the key's documented block was
// added to the template. Existing ancestors are reused so a second key under
// an appended parent lands inside it rather than beside it.
func appendKey(lines []string, segs []string, value string) []string {
	depth := 0
	insert := len(lines)
	for d := len(segs) - 1; d >= 1; d-- {
		if i, ok := findKey(lines, segs[:d], false); ok {
			depth = d
			insert = blockEnd(lines, i)
			break
		}
	}
	var block []string
	if depth == 0 && insert == len(lines) {
		block = append(block, "")
	}
	for d := depth; d < len(segs)-1; d++ {
		block = append(block, strings.Repeat("  ", d)+segs[d]+":")
	}
	block = append(block, strings.Repeat("  ", len(segs)-1)+segs[len(segs)-1]+": "+value)
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:insert]...)
	out = append(out, block...)
	out = append(out, lines[insert:]...)
	return out
}

// blockEnd returns the index just past the last line belonging to the mapping
// that starts at idx: its nested entries, including the comments among them.
func blockEnd(lines []string, idx int) int {
	base, _, _ := virtual(lines[idx])
	end := idx + 1
	for i := idx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		ind, _, _ := virtual(lines[i])
		if ind <= base {
			return end
		}
		end = i + 1
	}
	return end
}

// splitLines splits on "\n" and tolerates CRLF, so a config.yaml edited on
// Windows in an editor that writes CRLF is not rewritten with mixed endings.
func splitLines(s string) []string {
	out := strings.Split(s, "\n")
	for i, l := range out {
		out[i] = strings.TrimSuffix(l, "\r")
	}
	return out
}

// WriteFile writes b to path atomically at FilePerm (0600). The temporary
// file is created beside the target — a rename across filesystems is not
// atomic — and is named so config.Watch ignores it: the watcher filters on
// the exact base name, and "config.yaml.tmp" is not it.
func WriteFile(path string, b []byte) (err error) {
	tmp := path + ".tmp"
	//nolint:gosec // G304: path is ConfigPath() under a dir the daemon owns
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, FilePerm)
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	// A file that predates the tightened modes keeps its own mode through a
	// rename, so the mode is set on the replacement rather than inherited.
	if err = os.Chmod(tmp, FilePerm); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// RenderString renders a Go string as a YAML scalar: bare when YAML reads it
// back unchanged, double-quoted otherwise. An empty string is always quoted —
// a bare empty value parses as null, which is a different thing from "".
func RenderString(s string) string {
	if s == "" || !plainScalar.MatchString(s) || isYAMLWord(s) {
		return strconv.Quote(s)
	}
	return s
}

// isYAMLWord reports whether a bare token would parse as something other than
// a string: the booleans and the nulls YAML 1.1 recognises.
func isYAMLWord(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	return false
}

// RenderList renders a []string as a flow sequence. An empty list renders as
// "[]" rather than being omitted: an empty tui.board.group_by is a configured
// choice (a flat table), not an absent key.
func RenderList(items []string) string {
	parts := make([]string, 0, len(items))
	for _, s := range items {
		parts = append(parts, RenderString(s))
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
		parts = append(parts, RenderString(k)+": "+RenderString(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
