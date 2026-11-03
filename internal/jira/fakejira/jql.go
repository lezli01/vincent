package fakejira

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Query is a parsed JQL query in the fake's subset.
type Query struct {
	// ProjectKey or ProjectID, from `project = "KEY"` or `project = ID`.
	ProjectKey string
	ProjectID  string
	// UpdatedSince, from `updated >= <epoch ms>`; zero when absent.
	UpdatedSince    time.Time
	HasUpdatedSince bool
	// NotDone, from `statusCategory != Done`.
	NotDone bool
	// IDAfter, from `id > N`; 0 when absent.
	IDAfter    int64
	HasIDAfter bool
	// OrderByUpdated is `ORDER BY updated ASC, id ASC`; otherwise the order
	// is `ORDER BY id ASC`, which is also the default.
	OrderByUpdated bool
}

// ParseJQL parses the subset of JQL the fake answers: clauses joined by AND
// out of `project = "KEY"`, `project = ID`, `updated >= <epoch ms>`,
// `statusCategory != Done` and `id > N`, then optionally `ORDER BY updated
// ASC, id ASC` or `ORDER BY id ASC`. Keywords and field names are
// case-insensitive. Anything else — another field, operator or ordering, an
// OR, parentheses, an empty query (Jira Cloud's search/jql refuses unbounded
// queries) — is an error, which the route answers 400 (decision 5): a client
// that strays from the subset should fail its tests, not be answered.
func ParseJQL(jql string) (Query, error) {
	toks, err := tokenize(jql)
	if err != nil {
		return Query{}, err
	}
	var q Query
	i := 0
	clauses := 0
	for i < len(toks) && !toks[i].is("order") {
		if clauses > 0 {
			if !toks[i].is("and") {
				return Query{}, fmt.Errorf("expected AND, got %q", toks[i].text)
			}
			i++
		}
		if i+2 >= len(toks) {
			return Query{}, fmt.Errorf("incomplete clause")
		}
		field, op, val := toks[i], toks[i+1], toks[i+2]
		i += 3
		if err := q.clause(field, op, val); err != nil {
			return Query{}, err
		}
		clauses++
	}
	if clauses == 0 {
		return Query{}, fmt.Errorf("unbounded JQL queries are not allowed")
	}
	if i < len(toks) {
		rest := toks[i:]
		switch {
		case matches(rest, "order", "by", "updated", "asc", ",", "id", "asc"):
			q.OrderByUpdated = true
		case matches(rest, "order", "by", "id", "asc"):
		default:
			return Query{}, fmt.Errorf("unsupported ORDER BY")
		}
	}
	return q, nil
}

func (q *Query) clause(field, op, val token) error {
	switch {
	case field.is("project") && op.text == "=":
		if val.quoted {
			q.ProjectKey = val.text
			return nil
		}
		if _, err := strconv.ParseInt(val.text, 10, 64); err == nil {
			q.ProjectID = val.text
			return nil
		}
		return fmt.Errorf("project must be a quoted key or a numeric id")
	case field.is("updated") && op.text == ">=" && !val.quoted:
		ms, err := strconv.ParseInt(val.text, 10, 64)
		if err != nil {
			return fmt.Errorf("updated must be compared with epoch milliseconds")
		}
		q.UpdatedSince, q.HasUpdatedSince = time.UnixMilli(ms).UTC(), true
		return nil
	case field.is("statuscategory") && op.text == "!=" && strings.EqualFold(val.text, "done"):
		q.NotDone = true
		return nil
	case field.is("id") && op.text == ">" && !val.quoted:
		n, err := strconv.ParseInt(val.text, 10, 64)
		if err != nil {
			return fmt.Errorf("id must be compared with a number")
		}
		q.IDAfter, q.HasIDAfter = n, true
		return nil
	}
	return fmt.Errorf("unsupported clause %s %s %s", field.text, op.text, val.text)
}

type token struct {
	text   string
	quoted bool
}

func (t token) is(word string) bool { return !t.quoted && strings.EqualFold(t.text, word) }

func matches(toks []token, words ...string) bool {
	if len(toks) != len(words) {
		return false
	}
	for i, w := range words {
		if !toks[i].is(w) {
			return false
		}
	}
	return true
}

func tokenize(s string) ([]token, error) {
	var out []token
	r := []rune(s)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(r) && r[j] != c {
				j++
			}
			if j == len(r) {
				return nil, fmt.Errorf("unterminated string")
			}
			out = append(out, token{text: string(r[i+1 : j]), quoted: true})
			i = j + 1
		case c == ',':
			out = append(out, token{text: ","})
			i++
		case c == '=' || c == '!' || c == '<' || c == '>':
			j := i + 1
			if j < len(r) && r[j] == '=' {
				j++
			}
			out = append(out, token{text: string(r[i:j])})
			i = j
		case unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' || c == '-':
			j := i
			for j < len(r) && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_' || r[j] == '-') {
				j++
			}
			out = append(out, token{text: string(r[i:j])})
			i = j
		default:
			return nil, fmt.Errorf("unsupported character %q", c)
		}
	}
	return out, nil
}

// match reports whether an issue view satisfies the query.
func (q Query) match(c *Corpus, is *Issue) bool {
	p := c.project(is.Project)
	if q.ProjectKey != "" && is.Project != q.ProjectKey {
		return false
	}
	if q.ProjectID != "" && (p == nil || p.ID != q.ProjectID) {
		return false
	}
	if q.HasUpdatedSince {
		t, err := time.Parse(TimeLayout, is.Updated)
		if err != nil || t.Before(q.UpdatedSince) {
			return false
		}
	}
	if q.NotDone && p != nil {
		if st := p.status(is.Status); st != nil && st.Category == CategoryDone {
			return false
		}
	}
	if q.HasIDAfter && numericID(is.ID) <= q.IDAfter {
		return false
	}
	return true
}

func (q Query) sort(issues []Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		if q.OrderByUpdated {
			ti, _ := time.Parse(TimeLayout, issues[i].Updated)
			tj, _ := time.Parse(TimeLayout, issues[j].Updated)
			if !ti.Equal(tj) {
				return ti.Before(tj)
			}
		}
		return numericID(issues[i].ID) < numericID(issues[j].ID)
	})
}

func numericID(id string) int64 {
	n, _ := strconv.ParseInt(id, 10, 64)
	return n
}
