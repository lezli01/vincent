package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/jira"
	"github.com/lezli01/vincent/internal/jira/fakejira"
	"github.com/lezli01/vincent/internal/store"
)

// Task 142.15: doctor's Jira row, against the fake Jira over httptest.

// loggedSite is fakeSite with the fake's request log, so a test can count
// exactly what doctor asked Jira for.
func loggedSite(t *testing.T) (site, scenario, log string) {
	t.Helper()
	dir := t.TempDir()
	scenario, log = filepath.Join(dir, "scenario"), filepath.Join(dir, "requests.log")
	ts := httptest.NewServer(&fakejira.Store{
		ScenarioFile: scenario, RequestLog: log, User: jiraProbeUser, Token: jiraProbeToken,
	})
	t.Cleanup(ts.Close)
	return ts.URL, scenario, log
}

func requestsTo(t *testing.T, log string) []fakejira.LoggedRequest {
	t.Helper()
	reqs, err := fakejira.Requests(log)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	return reqs
}

// wantOnlyMyself asserts the log holds exactly one request, to myself.
func wantOnlyMyself(t *testing.T, log string) {
	t.Helper()
	reqs := requestsTo(t, log)
	if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Path != "rest/api/3/myself" {
		t.Errorf("requests = %+v, want exactly one GET rest/api/3/myself", reqs)
	}
}

// doctor fetches GET /v1/doctor and decodes the Jira row, failing the test
// if either credential value appears anywhere in the body.
func (h *jiraHarness) doctor(t *testing.T, probe bool) (doctor.Jira, *doctor.Report) {
	t.Helper()
	path := "/v1/doctor"
	if !probe {
		path += "?probe=false"
	}
	resp, body := doRequest(t, h.ts, http.MethodGet, path, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("doctor = %d: %s", resp.StatusCode, body)
	}
	for _, secret := range []string{jiraProbeToken, jiraProbeUser} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("the doctor report carries a credential value %q", secret)
		}
	}
	var rep doctor.Report
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rep.Jira, &rep
}

func wantNoJiraProblem(t *testing.T, rep *doctor.Report) {
	t.Helper()
	for _, p := range rep.Problems {
		if strings.Contains(strings.ToLower(p.Group+" "+p.Message), "jira") {
			t.Errorf("a Jira finding became a Problem: %+v", p)
		}
	}
}

func isTrue(b *bool) bool  { return b != nil && *b }
func isFalse(b *bool) bool { return b != nil && !*b }

// One call per distinct site, and nothing but myself: two projects on one
// site — one spelled with a trailing slash — and one on a second site.
func TestDoctorJiraOneMyselfPerSite(t *testing.T) {
	siteA, _, logA := loggedSite(t)
	siteB, _, logB := loggedSite(t)
	h := newJiraHarness(t, config.Default(), probeEnv())
	h.project(t, jiraBinding(siteA, "VIN"))
	h.project(t, jiraBinding(siteA+"/", "OPS"))
	h.project(t, jiraBinding(siteB, "VIN"))
	h.project(t, store.IssueSource{Provider: store.IssueProviderGitHub})

	j, rep := h.doctor(t, true)
	if len(j.Sites) != 2 {
		t.Fatalf("sites = %+v, want two", j.Sites)
	}
	for _, s := range j.Sites {
		if !isTrue(s.Reachable) || !isTrue(s.Authenticated) || s.As == "" || s.Reason != "" {
			t.Errorf("site = %+v, want reachable and authenticated", s)
		}
	}
	if !j.Enabled || !isTrue(j.Credential.UserPresent) || !isTrue(j.Credential.TokenPresent) || j.Message != "" {
		t.Errorf("row = %+v", j)
	}
	if j.Credential.UserEnv != "JIRA_USER" || j.Credential.TokenEnv != "JIRA_API_TOKEN" {
		t.Errorf("credential = %+v", j.Credential)
	}
	wantOnlyMyself(t, logA)
	wantOnlyMyself(t, logB)
	wantNoJiraProblem(t, rep)
}

// A refused token is refused once per site — never two or three times — and
// the sentence says what to do.
func TestDoctorJiraUnauthorizedOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scenario string
		env      []string
	}{
		{name: "scenario", scenario: fakejira.ScenarioUnauthorized, env: probeEnv()},
		{name: "wrong token", env: []string{"JIRA_USER=" + jiraProbeUser, "JIRA_API_TOKEN=nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site, scenario, log := loggedSite(t)
			if tc.scenario != "" {
				if err := os.WriteFile(scenario, []byte(tc.scenario+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			h := newJiraHarness(t, config.Default(), tc.env)
			h.project(t, jiraBinding(site, "VIN"))
			j, rep := h.doctor(t, true)
			if len(j.Sites) != 1 {
				t.Fatalf("sites = %+v", j.Sites)
			}
			s := j.Sites[0]
			if !isTrue(s.Reachable) || !isFalse(s.Authenticated) || s.Reason != jira.ReasonUnauthorized {
				t.Errorf("site = %+v, want reachable, not authenticated, unauthorized", s)
			}
			if !strings.HasPrefix(s.Message, "token rejected; Jira Cloud tokens expire — create a new one") {
				t.Errorf("message = %q", s.Message)
			}
			wantOnlyMyself(t, log)
			wantNoJiraProblem(t, rep)
		})
	}
}

// POST /v1/doctor/fix composes two reports, before and after the repair,
// and still asks each site exactly one question (review F1).
func TestDoctorFixAsksJiraOncePerSite(t *testing.T) {
	site, _, log := loggedSite(t)
	h := newJiraHarness(t, config.Default(), probeEnv())
	h.project(t, jiraBinding(site, "VIN"))
	resp, body := doRequest(t, h.ts, http.MethodPost, "/v1/doctor/fix", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("doctor fix = %d: %s", resp.StatusCode, body)
	}
	var res doctor.FixResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Report == nil || len(res.Report.Jira.Sites) != 1 || !isTrue(res.Report.Jira.Sites[0].Authenticated) {
		t.Errorf("fix report jira = %+v, want the one site probed and authenticated", res.Report)
	}
	wantOnlyMyself(t, log)
}

// probe=false — the TUI panel's refresh — makes no call, and still fills
// the credential block, the sites and the sync rows, reachability unknown.
func TestDoctorJiraProbeFalseMakesNoCall(t *testing.T) {
	site, _, log := loggedSite(t)
	h := newJiraHarness(t, config.Default(), probeEnv())
	id := h.project(t, jiraBinding(site, "VIN"))
	if err := h.st.PutIssueSyncState(t.Context(), store.IssueSyncState{
		ProjectID: id, Provider: store.IssueProviderJira, OK: true,
	}); err != nil {
		t.Fatalf("PutIssueSyncState: %v", err)
	}
	j, _ := h.doctor(t, false)
	if len(j.Sites) != 1 || j.Sites[0].URL != site || j.Sites[0].Reachable != nil || j.Sites[0].Authenticated != nil {
		t.Errorf("sites = %+v, want one unknown", j.Sites)
	}
	if !isTrue(j.Credential.UserPresent) || !isTrue(j.Credential.TokenPresent) {
		t.Errorf("credential = %+v", j.Credential)
	}
	if len(j.Sync) != 1 || j.Sync[0].ProjectID != id {
		t.Errorf("sync = %+v", j.Sync)
	}
	if reqs := requestsTo(t, log); len(reqs) != 0 {
		t.Errorf("probe=false called Jira: %+v", reqs)
	}
}

// The switch off, or no project on Jira: no call.
func TestDoctorJiraDisabledOrUnboundMakesNoCall(t *testing.T) {
	site, _, log := loggedSite(t)
	cfg := config.Default()
	cfg.Jira.Enabled = false
	h := newJiraHarness(t, cfg, probeEnv())
	h.project(t, jiraBinding(site, "VIN"))
	j, rep := h.doctor(t, true)
	if j.Enabled || len(j.Sites) != 1 || j.Sites[0].Reachable != nil || j.Sites[0].Reason != syncReasonJiraDisabled {
		t.Errorf("disabled row = %+v", j)
	}
	wantNoJiraProblem(t, rep)

	h2 := newJiraHarness(t, config.Default(), probeEnv())
	h2.project(t, store.IssueSource{Provider: store.IssueProviderGitHub})
	h2.project(t, store.IssueSource{Provider: store.IssueProviderDerive})
	j, _ = h2.doctor(t, true)
	if len(j.Sites) != 0 || j.Message != "" {
		t.Errorf("unbound row = %+v, want no sites and no message", j)
	}
	if reqs := requestsTo(t, log); len(reqs) != 0 {
		t.Errorf("doctor called Jira: %+v", reqs)
	}
}

// A missing variable is named, with the environment.set fix, and no call
// is made.
func TestDoctorJiraMissingVariable(t *testing.T) {
	for _, tc := range []struct {
		name               string
		env                []string
		userSet, tokenSet  bool
		mentions, excludes string
	}{
		{name: "no token", env: []string{"JIRA_USER=" + jiraProbeUser}, userSet: true, mentions: "JIRA_API_TOKEN is not set", excludes: "JIRA_USER and"},
		{name: "neither", mentions: "JIRA_USER and JIRA_API_TOKEN are not set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site, _, log := loggedSite(t)
			h := newJiraHarness(t, config.Default(), tc.env)
			h.project(t, jiraBinding(site, "VIN"))
			j, rep := h.doctor(t, true)
			c := j.Credential
			if c.UserPresent == nil || *c.UserPresent != tc.userSet || c.TokenPresent == nil || *c.TokenPresent != tc.tokenSet {
				t.Errorf("credential = %+v", c)
			}
			if !strings.Contains(j.Message, tc.mentions) || !strings.Contains(j.Message, "environment.set") {
				t.Errorf("message = %q, want %q and environment.set", j.Message, tc.mentions)
			}
			if tc.excludes != "" && strings.Contains(j.Message, tc.excludes) {
				t.Errorf("message = %q names a variable that is set", j.Message)
			}
			if len(j.Sites) != 1 || j.Sites[0].Reason != jira.ReasonNoCredential || j.Sites[0].Reachable != nil {
				t.Errorf("sites = %+v", j.Sites)
			}
			if reqs := requestsTo(t, log); len(reqs) != 0 {
				t.Errorf("doctor called Jira without a credential: %+v", reqs)
			}
			wantNoJiraProblem(t, rep)
		})
	}
}

// A closed port and a site that never answers both read unreachable, inside
// the budget.
func TestDoctorJiraUnreachable(t *testing.T) {
	defer func(b time.Duration) { jiraProbeBudget = b }(jiraProbeBudget)
	jiraProbeBudget = 200 * time.Millisecond

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String()
	_ = ln.Close()
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) })

	h := newJiraHarness(t, config.Default(), probeEnv())
	h.project(t, jiraBinding(closed, "VIN"))
	h.project(t, jiraBinding(hang.URL, "VIN"))
	start := time.Now()
	j, rep := h.doctor(t, true)
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("doctor took %v, want the probe held to its budget", took)
	}
	if len(j.Sites) != 2 {
		t.Fatalf("sites = %+v", j.Sites)
	}
	for _, s := range j.Sites {
		if !isFalse(s.Reachable) || s.Authenticated != nil || s.Reason != jira.ReasonUnreachable {
			t.Errorf("site = %+v, want unreachable", s)
		}
	}
	wantNoJiraProblem(t, rep)
}

// The split (task 142 open question 9): github.sync holds the GitHub
// project only, as before, and jira.sync the Jira project only.
func TestDoctorSplitsIssueSyncByProvider(t *testing.T) {
	site, _, _ := loggedSite(t)
	h := newJiraHarness(t, config.Default(), probeEnv())
	gh := h.project(t, store.IssueSource{Provider: store.IssueProviderGitHub})
	jp := h.project(t, jiraBinding(site, "VIN"))
	now := time.Now().UTC()
	for _, st := range []store.IssueSyncState{
		{ProjectID: gh, Provider: store.IssueProviderGitHub, Repo: "o/r", LastAttemptAt: &now, Reason: "not_found"},
		{ProjectID: jp, Provider: store.IssueProviderJira, LastAttemptAt: &now, Reason: jira.ReasonUnauthorized},
	} {
		if err := h.st.PutIssueSyncState(t.Context(), st); err != nil {
			t.Fatalf("PutIssueSyncState: %v", err)
		}
	}
	j, rep := h.doctor(t, false)
	if len(rep.GitHub.Sync) != 1 || rep.GitHub.Sync[0].ProjectID != gh || rep.GitHub.Sync[0].Reason != "not_found" {
		t.Errorf("github.sync = %+v", rep.GitHub.Sync)
	}
	if len(j.Sync) != 1 || j.Sync[0].ProjectID != jp || j.Sync[0].OK || j.Sync[0].Reason != jira.ReasonUnauthorized {
		t.Errorf("jira.sync = %+v", j.Sync)
	}
}
