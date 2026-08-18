package workflow

import (
	"fmt"
	"strings"
)

// Guard evaluation (spec §7.7, task 015).
//
// A guard is an ordinary §8.4 template — same context, same
// `missingkey=error`, same parse-at-load validation as `prompt`, `run` and
// `check`. One language in the file was worth more than the readability an
// expression language would have bought, because a second language means a
// second context shape to keep in sync with §8.4 forever (decision 4).

// TrueLiteral and FalseLiteral are the only two things a guard may render to.
const (
	TrueLiteral  = "true"
	FalseLiteral = "false"
)

// ConditionError is a guard that rendered to something other than `true` or
// `false`. It is separate from a render failure so the message can show what
// the guard actually produced, which is the whole diagnosis: a guard that
// renders `<no value>` is reading a field that is not there, and one that
// renders `0` is written in a language vincent does not speak.
type ConditionError struct {
	Field  string
	Output string
}

func (e *ConditionError) Error() string {
	shown := e.Output
	if shown == "" {
		shown = "an empty string"
	} else {
		shown = fmt.Sprintf("%q", shown)
	}
	return fmt.Sprintf("%s must render to %q or %q, got %s",
		e.Field, TrueLiteral, FalseLiteral, shown)
}

// Evaluate renders a guard and demands exactly `true` or `false`.
//
// Strictness rather than a truthiness table (decision 4): a permissive rule
// would accept `<no value>` and the empty string, both of which mean the
// guard is reading something that is not there — the one case where guessing
// a verdict is worse than refusing to have one.
func Evaluate(name, expr string, rc RenderContext) (bool, error) {
	out, err := Render(name, expr, rc)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(out) {
	case TrueLiteral:
		return true, nil
	case FalseLiteral:
		return false, nil
	default:
		return false, &ConditionError{Field: name, Output: strings.TrimSpace(out)}
	}
}

// Guarded reports whether the step carries a guard at all. An unguarded step
// runs, which is what every step did before task 015.
func (s Step) Guarded() bool { return s.If != "" }

// Guarded reports whether the lane carries a guard (§7.6).
func (l Lane) Guarded() bool { return l.If != "" }
