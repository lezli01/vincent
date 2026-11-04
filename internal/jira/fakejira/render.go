package fakejira

import (
	"strconv"
	"strings"
)

// navigable is every field the fake renders, in Jira's field ids.
var navigable = []string{
	"summary", "description", "status", "issuetype", "project", "priority",
	"labels", "assignee", "reporter", "created", "updated", "resolution",
	"comment",
}

// fieldSet is a parsed `fields` parameter.
type fieldSet struct {
	all   bool
	names map[string]bool
}

func (f fieldSet) has(name string) bool { return f.all || f.names[name] }

// parseFields reads `fields` — comma-separated, repeatable, `*all` and
// `*navigable` meaning every field. Absent, dflt says whether every field is
// meant (GET issue, bulkfetch) or none but the id (search/jql).
func parseFields(vals []string, dflt bool) fieldSet {
	fs := fieldSet{names: map[string]bool{}}
	seen := false
	for _, v := range vals {
		for _, f := range strings.Split(v, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			seen = true
			if f == "*all" || f == "*navigable" {
				fs.all = true
			}
			fs.names[f] = true
		}
	}
	if !seen {
		fs.all = dflt
	}
	return fs
}

func hasExpand(vals []string, want string) bool {
	for _, v := range vals {
		for _, e := range strings.Split(v, ",") {
			if strings.TrimSpace(e) == want {
				return true
			}
		}
	}
	return false
}

// categoryShapes are Jira Cloud's three status categories, global ids and
// all (GET /rest/api/3/statuscategory).
var categoryShapes = map[string]struct {
	id          int
	name, color string
}{
	CategoryNew:           {2, "To Do", "blue-gray"},
	CategoryIndeterminate: {4, "In Progress", "yellow"},
	CategoryDone:          {3, "Done", "green"},
}

func (h *handler) renderStatus(p *Project, id string) map[string]any {
	st := p.status(id)
	if st == nil {
		return map[string]any{"id": id}
	}
	cat := categoryShapes[st.Category]
	return map[string]any{
		"self":        h.base + apiPrefix + "status/" + st.ID,
		"id":          st.ID,
		"name":        st.Name,
		"description": "",
		"statusCategory": map[string]any{
			"self":      h.base + apiPrefix + "statuscategory/" + strconv.Itoa(cat.id),
			"id":        cat.id,
			"key":       st.Category,
			"colorName": cat.color,
			"name":      cat.name,
		},
	}
}

func (h *handler) renderUser(u *User) any {
	if u == nil {
		return nil
	}
	out := map[string]any{
		"self":        h.base + apiPrefix + "user?accountId=" + u.AccountID,
		"accountId":   u.AccountID,
		"displayName": u.DisplayName,
		"active":      true,
		"accountType": "atlassian",
	}
	if u.EmailAddress != "" {
		out["emailAddress"] = u.EmailAddress
	}
	return out
}

// render is an issue in REST v3's IssueBean shape: `id`, `key`, `self`,
// `fields` restricted to fs and, with rendered, `renderedFields` carrying
// the description's HTML. `_fake` is never part of it.
func (h *handler) render(is *Issue, fs fieldSet, rendered bool) map[string]any {
	out := map[string]any{
		"expand": "renderedFields,names,schema,operations,editmeta,changelog,versionedRepresentations",
		"id":     is.ID,
		"key":    is.Key,
		"self":   h.base + apiPrefix + "issue/" + is.ID,
	}
	p := h.c.project(is.Project)
	fields := map[string]any{}
	for _, name := range navigable {
		if !fs.has(name) {
			continue
		}
		switch name {
		case "summary":
			fields[name] = is.Summary
		case "description":
			if len(is.Description) > 0 {
				fields[name] = is.Description
			} else {
				fields[name] = nil
			}
		case "status":
			if p != nil {
				fields[name] = h.renderStatus(p, is.Status)
			}
		case "issuetype":
			fields[name] = map[string]any{"name": or(is.IssueType, "Task"), "subtask": false}
		case "project":
			if p != nil {
				fields[name] = map[string]any{
					"self": h.base + apiPrefix + "project/" + p.ID, "id": p.ID, "key": p.Key,
					"name": p.Name, "projectTypeKey": "software",
				}
			}
		case "priority":
			if is.Priority != "" {
				fields[name] = map[string]any{"name": is.Priority}
			} else {
				fields[name] = nil
			}
		case "labels":
			labels := is.Labels
			if labels == nil {
				labels = []string{}
			}
			fields[name] = labels
		case "assignee":
			fields[name] = h.renderUser(is.Assignee)
		case "reporter":
			fields[name] = h.renderUser(is.Reporter)
		case "created":
			fields[name] = is.Created
		case "updated":
			fields[name] = is.Updated
		case "comment":
			fields[name] = h.renderCommentField(is)
		case "resolution":
			if is.Resolution != nil {
				fields[name] = map[string]any{
					"self": h.base + apiPrefix + "resolution/" + is.Resolution.ID,
					"id":   is.Resolution.ID, "name": is.Resolution.Name,
				}
			} else {
				fields[name] = nil
			}
		}
	}
	if len(fields) > 0 || fs.all {
		out["fields"] = fields
	}
	if rendered {
		rf := map[string]any{}
		if fs.has("description") {
			if is.DescriptionHTML != "" {
				rf["description"] = is.DescriptionHTML
			} else {
				rf["description"] = nil
			}
		}
		out["renderedFields"] = rf
	}
	return out
}

func or(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

// renderCommentField is an issue's `comment` field: REST v3's PageOfComments
// with every comment and its `total`, rendered without renderedBody (an
// issue read's expand=renderedFields does not reach into it).
func (h *handler) renderCommentField(is *Issue) map[string]any {
	thread := []map[string]any{}
	for i := range h.c.Comments {
		if h.c.Comments[i].Issue == is.ID {
			thread = append(thread, h.renderComment(is, &h.c.Comments[i], false))
		}
	}
	return map[string]any{
		"self":       h.base + apiPrefix + "issue/" + is.ID + "/comment",
		"comments":   thread,
		"maxResults": len(thread),
		"total":      len(thread),
		"startAt":    0,
	}
}

// renderComment is REST v3's Comment shape; renderedBody only when asked.
func (h *handler) renderComment(is *Issue, cm *Comment, rendered bool) map[string]any {
	out := map[string]any{
		"self":         h.base + apiPrefix + "issue/" + is.ID + "/comment/" + cm.ID,
		"id":           cm.ID,
		"author":       h.renderUser(&cm.Author),
		"updateAuthor": h.renderUser(&cm.Author),
		"body":         cm.Body,
		"created":      cm.Created,
		"updated":      cm.Updated,
		"jsdPublic":    true,
	}
	if rendered {
		out["renderedBody"] = cm.BodyHTML
	}
	if cm.Visibility != nil {
		out["visibility"] = map[string]any{"type": cm.Visibility.Type, "value": cm.Visibility.Value, "identifier": cm.Visibility.Value}
	}
	return out
}
