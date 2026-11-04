package apiclient

import (
	"context"
	"strconv"
)

// JiraStatus is GET /v1/projects/{id}/jira — the Jira probe (task 142.12).
// Like GitHubStatus it separates "switched off", "not a Jira project" and
// "on Jira but unusable right now": Enabled is the config switch, Reason the
// first check that failed.
type JiraStatus struct {
	Enabled   bool `json:"enabled"`
	Available bool `json:"available"`
	// Reason is the daemon's named reason, empty when Available: disabled,
	// not_jira, no_credential, unreachable, unauthorized, forbidden,
	// not_found, rate_limited, bad_request or bad_response. Message is the
	// sentence to show. Neither carries Jira's error text or the credential.
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
	// Site and ProjectKey are the project's Jira binding; ProjectID is
	// Jira's id for the key, once the project check answered.
	Site       string `json:"site,omitempty"`
	ProjectKey string `json:"project_key,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
	// Server is set once the site answered, Account once the credential was
	// accepted.
	Server  *JiraServer  `json:"server,omitempty"`
	Account *JiraAccount `json:"account,omitempty"`
}

// JiraServer is the probe's serverInfo summary.
type JiraServer struct {
	Version        string `json:"version,omitempty"`
	DeploymentType string `json:"deployment_type,omitempty"`
}

// JiraAccount is who the credential authenticates as.
type JiraAccount struct {
	DisplayName string `json:"display_name,omitempty"`
}

// Unavailable renders why Jira is not usable, and "" when it is.
func (j JiraStatus) Unavailable() string {
	if j.Available {
		return ""
	}
	if j.Message != "" {
		return j.Message
	}
	if j.Reason != "" {
		return j.Reason
	}
	return "Jira is not available for this project"
}

// ProjectJira probes one project's Jira binding. The daemon makes the Jira
// calls; the client never talks to Jira.
func (c *Client) ProjectJira(ctx context.Context, projectID int64) (JiraStatus, error) {
	var out JiraStatus
	path := "/v1/projects/" + strconv.FormatInt(projectID, 10) + "/jira"
	if err := c.get(ctx, path, &out); err != nil {
		return JiraStatus{}, err
	}
	return out, nil
}
