package daemon

import (
	"log/slog"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/store"
)

// The daemon's issue sources (task 142.4): where the importer and the
// write-back drain are handed the providers they talk to. issuesync.go and
// issueoutbox.go never name a provider's client themselves.
//
// The drain's registry is keyed by provider (task 142.7), and each
// provider's rows drain under that provider's own gates: a Jira row is never
// sent under GitHub's switches, and a GitHub row is never held by
// `jira.enabled: false`.

// NewIssueOutbox builds the drain. It performs no I/O.
func NewIssueOutbox(st *store.Store, cfg func() config.Config, client *github.Client, logger *slog.Logger) *IssueOutbox {
	o := &IssueOutbox{
		store: st, cfg: cfg, logger: logger,
		now: time.Now, sleep: sleepCtx,
		wake: make(chan struct{}, 1),
	}
	if client != nil {
		// The outbox addresses each row by its remote and binds nothing,
		// so the source takes no git runner.
		o.gh = issuesource.NewGitHub(client, nil)
	}
	o.sources = o.byProvider
	return o
}

// WithJiraSource wires the Jira rows' source (#908); until then every Jira
// row waits with no_client while Jira is switched on.
func (o *IssueOutbox) WithJiraSource(src issuesource.Source) *IssueOutbox {
	o.jira = src
	return o
}

// byProvider is the default registry: each provider's source, enabled
// exactly when that provider's importer gates are open — its enabled
// switch and poll_interval, and a source wired. Jira writes back too
// (issueprovider.WritesBack, task 142.5) but has no source wired yet, so
// its rows wait no_client and are never failed; a provider it does not
// know waits with no_client too.
func (o *IssueOutbox) byProvider(provider string) (issuesource.Source, string) {
	cfg := o.cfg()
	switch provider {
	case issuesource.ProviderGitHub:
		return gated(cfg.GitHub.Polls(), o.gh)
	case issuesource.ProviderJira:
		return gated(cfg.Jira.Polls(), o.jira)
	}
	return nil, outboxReasonNoClient
}

// gated is one provider's entry: disabled when its switches are off,
// no_client when they are on and no source is wired.
func gated(polls bool, src issuesource.Source) (issuesource.Source, string) {
	switch {
	case !polls:
		return nil, outboxReasonDisabled
	case src == nil:
		return nil, outboxReasonNoClient
	}
	return src, ""
}
