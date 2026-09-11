package trigger

import (
	"fmt"
	"sort"
	"strings"
)

// matchEvent runs the `match:` prefilter (decision 3): every key is a dotted
// path into the event, and every one must hold. It is deliberately small —
// equality only — because anything richer is what `if:` is for, and a second
// expression language is what decision 3 beat.
//
// The comparison is on rendered scalars so "42" in the file equals 42 in the
// event. A list on the match side means "any of these"; an array on the event
// side means "contains", which is how `labels: vincent` reads against an
// event carrying `labels: [bug, vincent]`. A path the event does not have is
// a miss, not an error: a prefilter that errored on a sparse event would
// fill the ledger with `error` rows for events nobody asked about.
//
// The first failing key is returned so a dry run can say which one.
func matchEvent(match map[string]any, event map[string]any) (bool, string) {
	keys := make([]string, 0, len(match))
	for k := range match {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		got, ok := lookup(event, k)
		if !ok || !matchOne(match[k], got) {
			return false, k
		}
	}
	return true, ""
}

// lookup walks a dotted path through nested JSON objects.
func lookup(event map[string]any, path string) (any, bool) {
	var cur any = event
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func matchOne(want, got any) bool {
	wants := []any{want}
	if l, ok := want.([]any); ok {
		wants = l
	}
	gots := []any{got}
	if l, ok := got.([]any); ok {
		gots = l
	}
	for _, w := range wants {
		for _, g := range gots {
			if scalar(g) && scalarString(w) == scalarString(g) {
				return true
			}
		}
	}
	return false
}

// scalarString renders a match or event scalar the one way both sides are
// compared: JSON numbers arrive as float64 and YAML integers as uint64 or
// int, and "42" must equal 42 whichever side it came from.
func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return formatFloat(t)
	case float32:
		return formatFloat(float64(t))
	default:
		return fmt.Sprint(t)
	}
}

func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprint(f)
}
