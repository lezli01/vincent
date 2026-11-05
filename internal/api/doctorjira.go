package api

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/jira"
	"github.com/lezli01/vincent/internal/store"
)

// Doctor's Jira row (task 142.15). It is the GitHub row's counterpart and,
// like it, information and never a Problem.
//
// Three rules shape it, all settled with the issue's author:
//
//   - One credentialed call per distinct site, GET /rest/api/3/myself, and
//     nothing else (decision 1). The Jira client sends Basic auth on every
//     request — serverInfo included — so GET /v1/projects/{id}/jira's
//     three-call sequence would present a bad token two or three times per
//     site, and repeated refusals are how Jira's CAPTCHA lockout is tripped.
//     There is no fallback after a 403 and no retry on anything.
//   - The network is touched only when probe=true (decision 2): the TUI's
//     daemon panel asks with probe=false on every refresh, and must not
//     hit Jira — task 029 decision 4, and the lockout again.
//   - No call when there is nothing to call (decision 5): the switch off, no
//     project bound to Jira, or a credential variable unset.

// jiraDoctorUnauthorized is the 401 sentence. Doctor's own rather than
// jiraMessages' because doctor is where a user goes to learn what to do.
const jiraDoctorUnauthorized = "token rejected; Jira Cloud tokens expire — create a new one " +
	"(or jira.user_env names the wrong e-mail)"

// fillJira fills the Jira row from the live config, the daemon's
// environment and the store. The credential's values are read only to hand
// to the probe's client; the row carries the variable names and whether each
// is set.
func (s *Server) fillJira(ctx context.Context, rep *doctor.Report, probe bool) {
	cfg := s.deps.Config()
	j := doctor.JiraConfig(cfg)
	environ := s.deps.Environ
	if environ == nil {
		environ = os.Environ
	}
	env := cfg.Environment.Resolve(environ())
	userSet, tokenSet := jira.EnvSet(env, cfg.Jira.UserEnv), jira.EnvSet(env, cfg.Jira.TokenEnv)
	j.Credential.UserPresent, j.Credential.TokenPresent = &userSet, &tokenSet
	j.Sync = rep.Jira.Sync

	for _, site := range s.jiraSites(ctx) {
		j.Sites = append(j.Sites, doctor.JiraSite{URL: site})
	}
	if len(j.Sites) == 0 {
		rep.Jira = j
		return
	}
	switch {
	case !cfg.Jira.Enabled:
		for i := range j.Sites {
			j.Sites[i].Reason, j.Sites[i].Message = syncReasonJiraDisabled, jiraMessages[syncReasonJiraDisabled]
		}
	case !userSet || !tokenSet:
		j.Message = jiraMissingCredential(cfg.Jira.UserEnv, userSet, cfg.Jira.TokenEnv, tokenSet)
		for i := range j.Sites {
			j.Sites[i].Reason, j.Sites[i].Message = jira.ReasonNoCredential, jiraMessages[jira.ReasonNoCredential]
		}
	case probe:
		user, token, err := jira.EnvCredentials(env, cfg.Jira.UserEnv, cfg.Jira.TokenEnv)
		if err != nil {
			// EnvSet said both are set; EnvCredentials uses the same lookup.
			break
		}
		s.probeJiraSites(ctx, j.Sites, user, token)
	}
	rep.Jira = j
}

// jiraSites is the distinct, normalized sites of the projects whose issue
// source resolves to jira, sorted. Resolving jira needs no git: derive never
// yields it, so origin is not read.
func (s *Server) jiraSites(ctx context.Context) []string {
	if s.deps.Store == nil {
		return nil
	}
	projects, err := s.deps.Store.ListProjects(ctx)
	if err != nil {
		s.deps.Logger.Warn("doctor: list projects", "error", err)
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for i := range projects {
		res := issuesource.ResolveWith(projects[i], github.Repo{}, false)
		if res.Provider != store.IssueProviderJira || strings.TrimSpace(res.JiraSite) == "" {
			continue
		}
		site := jira.New(res.JiraSite, nil, nil).Site()
		if !seen[site] {
			seen[site] = true
			out = append(out, site)
		}
	}
	sort.Strings(out)
	return out
}

// probeJiraSites asks each site one question, in parallel, under one
// budget no larger than the project probe's.
func (s *Server) probeJiraSites(ctx context.Context, sites []doctor.JiraSite, user, token string) {
	ctx, cancel := context.WithTimeout(ctx, jiraProbeBudget)
	defer cancel()
	var wg sync.WaitGroup
	for i := range sites {
		wg.Add(1)
		go func(site *doctor.JiraSite) {
			defer wg.Done()
			client := jira.New(site.URL, s.deps.JiraHTTP, nil)
			client.Creds = func() (string, string, error) { return user, token, nil }
			me, err := client.Myself(ctx)
			s.jiraSiteAnswer(site, me, err)
		}(&sites[i])
	}
	wg.Wait()
}

// jiraSiteAnswer maps one myself call onto the site entry. The error is
// logged at debug — jira.Error never carries the token or the Authorization
// header — and only the reason and a sentence reach the report.
func (s *Server) jiraSiteAnswer(site *doctor.JiraSite, me jira.Myself, err error) {
	yes, no := true, false
	if err == nil {
		site.Reachable, site.Authenticated, site.As = &yes, &yes, me.DisplayName
		return
	}
	reason := jira.ReasonOf(err)
	if reason == "" {
		reason = jira.ReasonBadResponse
	}
	if s.deps.Logger != nil {
		s.deps.Logger.Debug("doctor jira probe", "site", site.URL, "reason", reason, "error", err)
	}
	site.Reason, site.Message = reason, jiraMessages[reason]
	switch reason {
	case jira.ReasonUnreachable:
		site.Reachable = &no
	case jira.ReasonUnauthorized:
		site.Reachable, site.Authenticated = &yes, &no
		site.Message = jiraDoctorUnauthorized
	default:
		site.Reachable = &yes
	}
}

// jiraMissingCredential names the unset variable(s) and the fix. The
// daemon's environment is not the login shell's, least of all as a service,
// which is the usual reason a variable set in a terminal is missing here.
func jiraMissingCredential(userEnv string, userSet bool, tokenEnv string, tokenSet bool) string {
	var missing []string
	if !userSet {
		missing = append(missing, jiraVarName(userEnv))
	}
	if !tokenSet {
		missing = append(missing, jiraVarName(tokenEnv))
	}
	verb, it := "is", "it"
	if len(missing) > 1 {
		verb, it = "are", "them"
	}
	return strings.Join(missing, " and ") + " " + verb + " not set in the daemon's environment; " +
		"the daemon (especially under `vincent service install`) does not inherit your login shell, " +
		"so set " + it + " under environment.set in config.yaml"
}

func jiraVarName(name string) string {
	if name == "" {
		return "(unnamed variable)"
	}
	return name
}
