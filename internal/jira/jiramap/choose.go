package jiramap

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lezli01/vincent/internal/issuestate"
)

// Failure codes, the snake_case strings the outbox stores.
const (
	CodeNoTransition          = "no_transition"
	CodeAmbiguousTransition   = "ambiguous_transition"
	CodeTransitionNeedsFields = "transition_needs_fields"
)

// Sentinels for errors.Is; each ChooseError wraps exactly one.
var (
	ErrNoTransition          = errors.New("no transition achieves the request")
	ErrAmbiguousTransition   = errors.New("several transitions achieve the request")
	ErrTransitionNeedsFields = errors.New("the transition needs fields vincent does not set")
)

// ChooseError is a typed chooser failure.
type ChooseError struct {
	// Code is CodeNoTransition, CodeAmbiguousTransition or
	// CodeTransitionNeedsFields.
	Code   string
	Action issuestate.Action
	Reason issuestate.Reason
	// Detail names what was considered: the override, the candidate
	// transitions, or the fields they require.
	Detail string
	err    error
}

func (e *ChooseError) Error() string {
	what := string(e.Action)
	if e.Reason != "" {
		what += " as " + string(e.Reason)
	}
	msg := fmt.Sprintf("%s: %s", what, e.err.Error())
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	return msg
}

// Unwrap returns the sentinel.
func (e *ChooseError) Unwrap() error { return e.err }

// Outcome is a §5.6 state and close reason.
type Outcome struct {
	State  issuestate.State
	Reason issuestate.Reason
}

// Choice is the transition to perform. Resolution is the resolution to set
// on its screen, "" for none. Achieved is what FromJira reads after it, and
// always equals the request.
type Choice struct {
	TransitionID string
	Resolution   string
	Achieved     Outcome
}

// candidate is a transition the rule accepted, with the resolution it sets.
type candidate struct {
	t   Transition
	res string
}

// ChooseTransition picks the one available transition that carries a
// vincent close (with reason) or reopen to Jira, by the rules in doc.go: an
// override first, else the per-reason heuristic; required fields other than
// the resolution exclude a transition; a reason is never degraded.
func ChooseTransition(action issuestate.Action, reason issuestate.Reason, ts []Transition, c Config) (Choice, error) {
	reason, err := issuestate.ResolveReason(action, reason)
	if err != nil {
		return Choice{}, err
	}
	want := Outcome{State: issuestate.Closed, Reason: reason}
	switch action {
	case issuestate.Close:
	case issuestate.Reopen:
		want = Outcome{State: issuestate.Open}
	default:
		// remote_closed and remote_reopened are sync reporting what Jira
		// says; there is nothing to write back for them.
		return Choice{}, fmt.Errorf("%s is sync's report of the remote, not a write", action)
	}
	fail := func(code string, sentinel error, detail string) (Choice, error) {
		return Choice{}, &ChooseError{Code: code, Action: action, Reason: reason, Detail: detail, err: sentinel}
	}

	var tiers [][]candidate
	if ov := c.override(action, reason); ov != "" {
		var named []Transition
		for _, t := range ts {
			if fold(t.Name) == fold(ov) || fold(t.To.Name) == fold(ov) {
				named = append(named, t)
			}
		}
		switch len(named) {
		case 0:
			return fail(CodeNoTransition, ErrNoTransition, fmt.Sprintf("override %q is not available now", ov))
		case 1:
		default:
			return fail(CodeAmbiguousTransition, ErrAmbiguousTransition, fmt.Sprintf("override %q matches %s", ov, names(named)))
		}
		tiers = [][]candidate{c.accept(want, named)}
	} else {
		tiers = c.heuristic(want, ts)
	}

	for _, tier := range tiers {
		if len(tier) == 0 {
			continue
		}
		var usable []candidate
		var blocked []string
		for _, cd := range tier {
			if missing := needsFields(cd); len(missing) > 0 {
				blocked = append(blocked, fmt.Sprintf("%q requires %s", cd.t.Name, strings.Join(missing, ", ")))
				continue
			}
			usable = append(usable, cd)
		}
		switch len(usable) {
		case 0:
			return fail(CodeTransitionNeedsFields, ErrTransitionNeedsFields, strings.Join(blocked, "; "))
		case 1:
			cd := usable[0]
			return Choice{TransitionID: cd.t.ID, Resolution: cd.res, Achieved: c.achieved(cd)}, nil
		default:
			amb := make([]Transition, len(usable))
			for i, cd := range usable {
				amb[i] = cd.t
			}
			return fail(CodeAmbiguousTransition, ErrAmbiguousTransition, "candidates "+names(amb))
		}
	}
	return fail(CodeNoTransition, ErrNoTransition, fmt.Sprintf("none of %s achieves it", names(ts)))
}

// override is the configured override for the request, "" for none.
func (c Config) override(a issuestate.Action, r issuestate.Reason) string {
	if a == issuestate.Reopen {
		return c.Overrides.Reopen
	}
	switch r {
	case issuestate.NotPlanned:
		return c.Overrides.CloseNotPlanned
	case issuestate.Duplicate:
		return c.Overrides.CloseDuplicate
	default:
		return c.Overrides.CloseCompleted
	}
}

// heuristic splits the available transitions into preference tiers of
// accepted candidates; the first non-empty tier is the one counted.
func (c Config) heuristic(want Outcome, ts []Transition) [][]candidate {
	byCategory := func(key string) []Transition {
		var out []Transition
		for _, t := range ts {
			if t.To.StatusCategory.Key == key {
				out = append(out, t)
			}
		}
		return out
	}
	if want.State == issuestate.Open {
		return [][]candidate{
			c.accept(want, byCategory(CategoryNew)),
			c.accept(want, byCategory(CategoryIndeterminate)),
		}
	}
	done := byCategory(CategoryDone)
	switch want.Reason {
	case issuestate.NotPlanned:
		return c.byName(want, done, c.readsNotPlanned)
	case issuestate.Duplicate:
		// A "Duplicate" transition first; then, since a company-managed
		// workflow offers Duplicate on both "Done" and "Won't do", the done
		// transitions not named for another close.
		tiers := c.byName(want, done, func(n string) bool { return inSet(n, c.duplicateSet()) })
		var plain, rest []candidate
		for _, cd := range tiers[1] {
			if c.readsNotPlanned(cd.t.Name) || c.readsNotPlanned(cd.t.To.Name) {
				rest = append(rest, cd)
			} else {
				plain = append(plain, cd)
			}
		}
		return [][]candidate{tiers[0], plain, rest}
	default:
		var clean []Transition
		for _, t := range done {
			if !c.readsNotCompleted(t.Name) && !c.readsNotCompleted(t.To.Name) {
				clean = append(clean, t)
			}
		}
		return [][]candidate{c.accept(want, clean)}
	}
}

// byName prefers transitions whose name or target name matches, then the
// rest: "Won't do → Cancelled" beats a "Done" whose resolution list merely
// offers Won't do.
func (c Config) byName(want Outcome, ts []Transition, match func(string) bool) [][]candidate {
	var named, rest []Transition
	for _, t := range ts {
		if match(t.Name) || match(t.To.Name) {
			named = append(named, t)
		} else {
			rest = append(rest, t)
		}
	}
	return [][]candidate{c.accept(want, named), c.accept(want, rest)}
}

// readsNotCompleted reports whether a name marks a transition or status as
// a not-planned or duplicate close, which close/completed never picks.
func (c Config) readsNotCompleted(name string) bool {
	return c.readsNotPlanned(name) || inSet(name, c.duplicateSet())
}

// accept keeps the transitions that can achieve want, each with the
// resolution it would set. A transition is accepted only when FromJira,
// reading its target status and that resolution, gives exactly want — which
// is what makes Achieved equal the request on every success.
func (c Config) accept(want Outcome, ts []Transition) []candidate {
	var out []candidate
	for _, t := range ts {
		res, ok := c.resolutionFor(want, t)
		if !ok {
			continue
		}
		cd := candidate{t: t, res: res}
		if c.achieved(cd) != want {
			continue
		}
		out = append(out, cd)
	}
	return out
}

// resolutionFor is the resolution a transition would be given for want, and
// whether the transition can carry want at all.
func (c Config) resolutionFor(want Outcome, t Transition) (string, bool) {
	if want.State == issuestate.Open {
		// Never set a resolution on reopen, even when the screen offers it.
		return "", true
	}
	field, offered := t.Fields[resolutionField]
	switch want.Reason {
	case issuestate.NotPlanned:
		if !offered {
			// Case (b): FromJira's null-resolution fallback reads the
			// target name, which accept checks.
			return "", true
		}
		return pick(field.AllowedValues, c.notPlannedSet())
	case issuestate.Duplicate:
		if !offered {
			return "", false
		}
		return pick(field.AllowedValues, c.duplicateSet())
	default:
		if !offered {
			return "", true
		}
		if len(field.AllowedValues) == 0 {
			return "Done", true
		}
		for _, v := range field.AllowedValues {
			if fold(v.Name) == "done" {
				return v.Name, true
			}
		}
		for _, v := range field.AllowedValues {
			if !c.readsNotCompleted(v.Name) {
				return v.Name, true
			}
		}
		return "", false
	}
}

// pick returns the allowed value earliest in set (so the set's order is the
// preference: Won't do first by default). With no allowed values listed it
// returns set's first name, as Jira accepts any resolution then.
func pick(allowed []Named, set []string) (string, bool) {
	if len(allowed) == 0 {
		return set[0], true
	}
	for _, s := range set {
		for _, v := range allowed {
			if fold(v.Name) == fold(s) {
				return v.Name, true
			}
		}
	}
	return "", false
}

// achieved is FromJira over the transition's target and the resolution set.
func (c Config) achieved(cd candidate) Outcome {
	f := Fields{Status: &Status{Name: cd.t.To.Name, StatusCategory: cd.t.To.StatusCategory}}
	if cd.res != "" {
		f.Resolution = &Named{Name: cd.res}
	}
	s, r, _ := FromJira(f, c)
	return Outcome{State: s, Reason: r}
}

// needsFields lists, sorted, the screen fields that are required, have no
// default and that vincent leaves unset.
func needsFields(cd candidate) []string {
	var out []string
	for name, m := range cd.t.Fields {
		if !m.Required || m.HasDefaultValue {
			continue
		}
		if name == resolutionField && cd.res != "" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func names(ts []Transition) string {
	q := make([]string, len(ts))
	for i, t := range ts {
		q[i] = fmt.Sprintf("%q", t.Name)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
