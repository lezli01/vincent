package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Syntax highlighting for fenced code blocks (task 075, §15).
//
// The highlighter is vincent's own, over a written-down language list, and not
// chroma (decision 1). That follows the posture recorded twice already —
// task 017 decision 2 refusing a graph widget and task 055 decision 1 refusing
// sigstore-go — and the runtime `require` block stays where it is. The cost is
// stated rather than hidden: this is a coarse token scanner, not a parser, and
// a pathological string or a dialect with nested block comments will be
// mis-tinted. Nothing depends on it being right; it is a tint over text that
// is already correct.
//
// Two properties make that trade safe, and both are asserted by tests:
//
//   - **Styles only, never characters.** Concatenating the returned segments
//     reproduces the argument byte for byte. A highlighted block therefore has
//     exactly the fence's content once the escape sequences are stripped, so
//     highlighting cannot change what a reader copies out of the pane, and the
//     transcript on disk was never in this path at all.
//   - **Colour is the only thing added.** Every structural distinction the
//     block already had — the rail, the language header, the indentation, the
//     hard wrap — is a character, so a monochrome or ASCII profile loses the
//     tint and nothing else.
//
// Scanning is per line and carries no state between lines: a block comment or
// a string spanning two lines is tinted as two independent lines. That is the
// coarseness above, made explicit rather than half-fixed.

var (
	styleHLComment = lipgloss.NewStyle().Faint(true)
	styleHLString  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleHLNumber  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styleHLKeyword = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	styleHLPunct   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

// mdLang is one language's coarse model: how a comment starts, what quotes a
// string, and which identifiers are keywords. A language absent from the table
// renders plain, which is the fallback and not a failure.
type mdLang struct {
	lineComment []string
	quotes      string
	keywords    map[string]bool
}

// mdKeywords builds a keyword set from a space-separated list, which is how
// the table below stays readable.
func mdKeywords(list string) map[string]bool {
	out := make(map[string]bool)
	for _, w := range strings.Fields(list) {
		out[w] = true
	}
	return out
}

// mdLangs is the written-down list. Aliases are separate entries rather than a
// normalization table, so what an agent may write is visible here.
var mdLangs = func() map[string]*mdLang {
	cLike := []string{"//", "/*"}
	goLang := &mdLang{
		lineComment: cLike,
		quotes:      "\"'`",
		keywords: mdKeywords(`break case chan const continue default defer else fallthrough for func go goto
			if import interface map package range return select struct switch type var
			nil true false iota make new len cap append copy delete panic recover
			string int int8 int16 int32 int64 uint uint8 uint16 uint32 uint64 byte rune
			float32 float64 bool error any`),
	}
	python := &mdLang{
		lineComment: []string{"#"},
		quotes:      "\"'",
		keywords: mdKeywords(`and as assert async await break class continue def del elif else except
			finally for from global if import in is lambda none nonlocal not or pass raise
			return try while with yield True False None self print len range`),
	}
	js := &mdLang{
		lineComment: cLike,
		quotes:      "\"'`",
		keywords: mdKeywords(`async await break case catch class const continue debugger default delete do
			else export extends finally for function if import in instanceof let new of return
			static super switch this throw try typeof var void while with yield
			true false null undefined interface type enum implements readonly namespace declare`),
	}
	rust := &mdLang{
		lineComment: cLike,
		quotes:      "\"'",
		keywords: mdKeywords(`as async await break const continue crate dyn else enum extern fn for if impl
			in let loop match mod move mut pub ref return self Self static struct super trait
			type unsafe use where while true false Some None Ok Err String Vec Option Result`),
	}
	cLang := &mdLang{
		lineComment: cLike,
		quotes:      "\"'",
		keywords: mdKeywords(`auto break case char class const constexpr continue default delete do double
			else enum extern float for friend goto if inline int long namespace new nullptr
			operator private protected public register return short signed sizeof static
			struct switch template this throw try typedef typename union unsigned using
			virtual void volatile while true false NULL include define ifdef ifndef endif`),
	}
	java := &mdLang{
		lineComment: cLike,
		quotes:      "\"'",
		keywords: mdKeywords(`abstract assert boolean break byte case catch char class const continue
			default do double else enum extends final finally float for goto if implements
			import instanceof int interface long native new package private protected public
			return short static strictfp super switch synchronized this throw throws
			transient try void volatile while true false null var record sealed`),
	}
	shell := &mdLang{
		lineComment: []string{"#"},
		quotes:      "\"'",
		keywords: mdKeywords(`if then else elif fi for while until do done case esac function return
			in select time coproc local export readonly declare unset shift eval exec exit
			set trap source echo cd test`),
	}
	ruby := &mdLang{
		lineComment: []string{"#"},
		quotes:      "\"'",
		keywords: mdKeywords(`alias and begin break case class def defined do else elsif end ensure false
			for if in module next nil not or redo rescue retry return self super then true
			undef unless until when while yield require attr_accessor puts`),
	}
	sql := &mdLang{
		lineComment: []string{"--"},
		quotes:      "\"'",
		keywords: mdKeywords(`select from where insert into values update set delete create table drop
			alter add column primary key foreign references index unique not null default
			join left right inner outer on group by order having limit offset union all
			distinct as and or in exists between like case when then else end begin commit
			rollback transaction with returning`),
	}
	json := &mdLang{quotes: "\"", keywords: mdKeywords(`true false null`)}
	yaml := &mdLang{
		lineComment: []string{"#"},
		quotes:      "\"'",
		keywords:    mdKeywords(`true false null yes no on off`),
	}
	return map[string]*mdLang{
		"go": goLang, "golang": goLang,
		"python": python, "py": python,
		"javascript": js, "js": js, "typescript": js, "ts": js, "tsx": js, "jsx": js,
		"rust": rust, "rs": rust,
		"c": cLang, "cpp": cLang, "c++": cLang, "h": cLang, "hpp": cLang,
		"java": java,
		"sh":   shell, "bash": shell, "zsh": shell, "shell": shell, "console": shell,
		"ruby": ruby, "rb": ruby,
		"sql":  sql,
		"json": json,
		"yaml": yaml, "yml": yaml,
	}
}()

// highlightSegments splits one line of a fenced block into styled runs.
//
// The returned segments concatenate to the argument exactly — the property the
// whole scheme rests on. A language that is not on the list, or a fence with no
// info string, gets one plain segment, which is what every fenced block looked
// like before this.
func highlightSegments(lang, line string) []segment {
	l := mdLangs[strings.ToLower(lang)]
	if l == nil || line == "" {
		return []segment{{text: line, style: styleMDCode}}
	}
	var out []segment
	emit := func(text string, style lipgloss.Style) {
		if text == "" {
			return
		}
		if n := len(out); n > 0 && sameStyle(out[n-1].style, style) {
			out[n-1].text += text
			return
		}
		out = append(out, segment{text: text, style: style})
	}
	for i := 0; i < len(line); {
		if l.commentAt(line, i) > 0 {
			// A comment runs to the end of the line. A `/*` that closes on a
			// later line is the stateless coarseness above.
			emit(line[i:], styleHLComment)
			break
		}
		if strings.IndexByte(l.quotes, line[i]) >= 0 {
			end := stringEnd(line, i)
			emit(line[i:end], styleHLString)
			i = end
			continue
		}
		if isDigitByte(line[i]) {
			end := i
			for end < len(line) && (isWordByte(line[end]) || line[end] == '.') {
				end++
			}
			emit(line[i:end], styleHLNumber)
			i = end
			continue
		}
		if isWordByte(line[i]) {
			end := i
			for end < len(line) && isWordByte(line[end]) {
				end++
			}
			word := line[i:end]
			style := styleMDCode
			if l.keywords[word] {
				style = styleHLKeyword
			}
			emit(word, style)
			i = end
			continue
		}
		if isPunctByte(line[i]) {
			emit(line[i:i+1], styleHLPunct)
			i++
			continue
		}
		// Whitespace and everything multi-byte: plain, one byte at a time, so
		// a UTF-8 sequence is never split between two segments of different
		// styles — emit coalesces the runs back together anyway.
		end := i + 1
		for end < len(line) && !isWordByte(line[end]) && !isPunctByte(line[end]) &&
			strings.IndexByte(l.quotes, line[end]) < 0 && l.commentAt(line, end) == 0 {
			end++
		}
		emit(line[i:end], styleMDCode)
		i = end
	}
	if len(out) == 0 {
		return []segment{{text: line, style: styleMDCode}}
	}
	return out
}

// commentAt reports the length of the comment opener at i, or 0.
func (l *mdLang) commentAt(line string, i int) int {
	for _, c := range l.lineComment {
		if strings.HasPrefix(line[i:], c) {
			return len(c)
		}
	}
	return 0
}

// stringEnd finds the byte after a quoted run, honoring backslash escapes and
// stopping at the end of the line for a string that does not close on it.
func stringEnd(line string, i int) int {
	q := line[i]
	for j := i + 1; j < len(line); j++ {
		switch line[j] {
		case '\\':
			j++
		case q:
			return j + 1
		}
	}
	return len(line)
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

func isWordByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', isDigitByte(c), c == '_':
		return true
	}
	return false
}

func isPunctByte(c byte) bool {
	return strings.IndexByte("{}()[]<>,;:.!?=+-*/%&|^~@#$", c) >= 0
}
