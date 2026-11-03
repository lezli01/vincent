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
// write-back drain are handed the providers they talk to. GitHub is the only
// one, built over the daemon's one *github.Client; issuesync.go and
// issueoutbox.go never name that client themselves. Which projects bind to
// which provider is task 142's later work (#902, #903).

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
	o.sources = o.githubOnly
	return o
}

// githubOnly is the default registry: the GitHub source, enabled exactly
// when the importer's gates are open — github.enabled and poll_interval,
// and a client wired.
func (o *IssueOutbox) githubOnly(provider string) (issuesource.Source, string) {
	switch {
	case provider != issuesource.ProviderGitHub:
		return nil, outboxReasonNoClient
	case !o.cfg().GitHub.Polls():
		return nil, outboxReasonDisabled
	case o.gh == nil:
		return nil, outboxReasonNoClient
	}
	return o.gh, ""
}
