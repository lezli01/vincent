package jira

import "net/url"

// Every Jira REST path the client calls, pinned in one place (task 142
// decision 5). Listing is pathSearchJQL alone: /rest/api/3/search was
// removed upstream (CHANGE-2046), and no path here may name it.
const (
	apiRoot = "/rest/api/3/"

	pathSearchJQL  = apiRoot + "search/jql"
	pathBulkFetch  = apiRoot + "issue/bulkfetch"
	pathMyself     = apiRoot + "myself"
	pathServerInfo = apiRoot + "serverInfo"
)

// pathIssue is GET /rest/api/3/issue/{issueIdOrKey}.
func pathIssue(idOrKey string) string { return apiRoot + "issue/" + url.PathEscape(idOrKey) }

// pathComments is GET /rest/api/3/issue/{issueIdOrKey}/comment.
func pathComments(idOrKey string) string { return pathIssue(idOrKey) + "/comment" }

// pathProject is GET /rest/api/3/project/{projectIdOrKey}.
func pathProject(idOrKey string) string { return apiRoot + "project/" + url.PathEscape(idOrKey) }

// The page sizes asked for. search/jql caps a page at 100 once fields beyond
// the id are named (5000 is the id-only cap); the comment page's cap is 100.
const (
	searchPageSize  = 100
	commentPageSize = 100
)

// Fixed query values.
const (
	expandRenderedFields = "renderedFields"
	expandRenderedBody   = "renderedBody"
	commentOrder         = "created"
)
