package api

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/jira"
	"github.com/lezli01/vincent/internal/store"
)

// The Jira probe (task 142.12, settled decision 2; task 142 open question 7):
// GET /v1/projects/{id}/jira, the Jira counterpart of GET
// /v1/projects/{id}/github. It is a read that asks Jira three questions on
// demand and stores nothing — the importer is still the only thing that
// syncs, and the sync row is still what GET /v1/projects/{id}/issues/sync
// reads.

// jiraProbeBudget bounds the probe's three Jira calls together. It sits
// well under the loopback client's ten-second deadline (apiclient's
// requestTimeout): a site that blackholes the connection, or is just slow,
// would otherwise hold the handler for the Jira client's 30 s per call, and
// the CLI would give up first and print a deadline error in place of the
// unreachable answer the probe exists to give (review F3 on #943) — the
// trap apiclient's probeTimeout describes for `vincent doctor`. An expired
// budget fails the call in flight as a transport error: unreachable. A var
// so a test can shorten it.
var jiraProbeBudget = 8 * time.Second

// jiraReasonNotJira is a project whose issue source does not resolve to
// jira: bound to none or github, or derived (derive never yields jira). No
// sync reason fits — not_github and source_not_github say the opposite — so
// the probe has its own, and it never calls Jira for such a project.
const jiraReasonNotJira = "not_jira"

// jiraResponse is GET /v1/projects/{id}/jira.
//
//   - enabled: the config.yaml `jira.enabled` switch (§12.3), reported apart
//     from available so "you turned it off" and "this cannot reach Jira" read
//     differently.
//   - available: every check passed — the site answered, the credential is
//     accepted and the bound project exists and is visible to it.
//   - reason: "" when available; otherwise the first check that failed, in
//     the sync row's vocabulary: disabled (jira.enabled false), not_jira,
//     no_credential, unreachable, unauthorized, forbidden, not_found,
//     rate_limited, bad_request or bad_response (jira.Reason*).
//   - message: reason as a sentence for a human; never Jira's own error text.
//   - site, project_key: the binding's, set whenever the project is on Jira.
//   - project_id: Jira's numeric id for project_key, once the project check
//     answered.
//   - server: {version, deployment_type} from serverInfo, once reachable.
//   - account: {display_name} of the credential's user, once accepted.
//
// Nothing in it — or in any error this route writes — carries the API token,
// the value of the user variable, or an Authorization header: the body is
// built from Jira's answers and the reason vocabulary only.
type jiraResponse struct {
	Enabled    bool            `json:"enabled"`
	Available  bool            `json:"available"`
	Reason     string          `json:"reason"`
	Message    string          `json:"message,omitempty"`
	Site       string          `json:"site,omitempty"`
	ProjectKey string          `json:"project_key,omitempty"`
	ProjectID  string          `json:"project_id,omitempty"`
	Server     *jiraServerBody `json:"server,omitempty"`
	Account    *jiraAccount    `json:"account,omitempty"`
}

type jiraServerBody struct {
	Version        string `json:"version,omitempty"`
	DeploymentType string `json:"deployment_type,omitempty"`
}

type jiraAccount struct {
	DisplayName string `json:"display_name,omitempty"`
}

// jiraMessages render the probe's reasons. A reason missing here is shown
// as itself by the clients.
var jiraMessages = map[string]string{
	syncReasonJiraDisabled: "the Jira integration is turned off (jira.enabled: false)",
	jiraReasonNotJira:      "this project's issue source is not Jira",
	jira.ReasonNoCredential: "no Jira credential: set the variables jira.user_env and jira.token_env name " +
		"(default JIRA_USER and JIRA_API_TOKEN)",
	jira.ReasonUnreachable:  "the Jira site could not be reached",
	jira.ReasonUnauthorized: "Jira rejected the credential",
	jira.ReasonForbidden:    "the credential may not read this Jira project",
	jira.ReasonNotFound:     "the bound Jira project does not exist or is not visible to the credential",
	jira.ReasonRateLimited:  "Jira is rate limiting this credential",
	jira.ReasonBadRequest:   "Jira refused the request",
	jira.ReasonBadResponse:  "Jira answered with something vincent could not read",
}

// handleProjectJira implements GET /v1/projects/{id}/jira.
//
// The checks run in order and the first to fail is the answer: the project
// must resolve to jira (else not_jira, and no call is made); `jira.enabled`
// must be true (else disabled — the choice here is the GitHub probe's, not
// "probe anyway": a switched-off integration makes no call, which is what
// keeps the switch a literal promise, and the sync status names the same
// reason); the credential variables must be set (else no_credential, still
// no call); then serverInfo (reachability), myself (the credential) and
// project/{key} (the binding). Every answer is 200 — a probe that found Jira
// unusable has still answered — and an unknown project id is the usual 404.
//
// The credential is resolved per request from the live config, through
// environment.set/inherit like an agent's environment, and handed to a
// client that lives for this request only.
func (s *Server) handleProjectJira(w http.ResponseWriter, r *http.Request) {
	project, ok := s.projectFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.jiraProbe(r.Context(), project))
}

func (s *Server) jiraProbe(ctx context.Context, project *store.Project) jiraResponse {
	cfg := s.deps.Config()
	out := jiraResponse{Enabled: cfg.Jira.Enabled}
	res := s.issueResolution(ctx, project)
	if res.Provider != store.IssueProviderJira {
		return out.fail(jiraReasonNotJira)
	}
	client := jira.New(res.JiraSite, s.deps.JiraHTTP, nil)
	out.Site, out.ProjectKey = client.Site(), res.JiraProjectKey
	if !cfg.Jira.Enabled {
		return out.fail(issuesource.ReasonDisabled)
	}
	user, token, err := s.jiraCredentials(cfg)
	if err != nil {
		return out.fail(jira.ReasonNoCredential)
	}
	client.Creds = func() (string, string, error) { return user, token, nil }
	ctx, cancel := context.WithTimeout(ctx, jiraProbeBudget)
	defer cancel()

	info, err := client.ServerInfo(ctx)
	if err != nil {
		return out.failWith(s, "server info", err)
	}
	out.Server = &jiraServerBody{Version: info.Version, DeploymentType: info.DeploymentType}
	me, err := client.Myself(ctx)
	if err != nil {
		return out.failWith(s, "myself", err)
	}
	out.Account = &jiraAccount{DisplayName: me.DisplayName}
	p, err := client.Project(ctx, res.JiraProjectKey)
	if err != nil {
		return out.failWith(s, "project", err)
	}
	out.ProjectID = p.ID
	if p.Key != "" {
		out.ProjectKey = p.Key
	}
	out.Available = true
	return out
}

// jiraCredentials resolves the Jira user and token from the daemon's
// environment as config.yaml's environment block shapes it. The error, when
// a variable is unset, names the variable and never a value.
func (s *Server) jiraCredentials(cfg config.Config) (user, token string, err error) {
	environ := s.deps.Environ
	if environ == nil {
		environ = os.Environ
	}
	return jira.EnvCredentials(cfg.Environment.Resolve(environ()), cfg.Jira.UserEnv, cfg.Jira.TokenEnv)
}

func (o jiraResponse) fail(reason string) jiraResponse {
	o.Available, o.Reason, o.Message = false, reason, jiraMessages[reason]
	return o
}

// failWith maps a failed Jira call onto its reason. The error is logged —
// jira.Error never carries the token or the Authorization header — and only
// the reason reaches the client.
func (o jiraResponse) failWith(s *Server, op string, err error) jiraResponse {
	reason := jira.ReasonOf(err)
	if reason == "" {
		reason = jira.ReasonBadResponse
	}
	if s.deps.Logger != nil {
		s.deps.Logger.Debug("jira probe", "op", op, "site", o.Site, "reason", reason, "error", err)
	}
	return o.fail(reason)
}
