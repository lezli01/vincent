package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The issue-source flags of `project add` and `project edit` (task 142.6).
// The binding is daemon state, edited only through the API; the daemon is
// the one validator, so these flags assemble a request and leave every rule
// about sites, keys and JQL to it.

// projectSourceFlags are the issue-source flags, in help order.
var projectSourceFlags = []string{"issue-source", "jira-site", "jira-project", "jira-jql", "jira-mapping"}

// jiraSourceFlags are the flags that only mean something beside jira.
var jiraSourceFlags = projectSourceFlags[1:]

func addIssueSourceFlags(cmd *cobra.Command) {
	cmd.Flags().String("issue-source", "",
		"Where the project's issues come from: derive (from origin), none, github or jira")
	cmd.Flags().String("jira-site", "",
		"Jira site base URL, e.g. https://example.atlassian.net (with --issue-source jira)")
	cmd.Flags().String("jira-project", "", "Jira project key, e.g. ENG (with --issue-source jira)")
	cmd.Flags().String("jira-jql", "",
		`Optional JQL clause narrowing the Jira project's issues; "" removes it`)
	cmd.Flags().String("jira-mapping", "",
		`Jira mapping override: a JSON file, or - for stdin; "" removes it`)
}

// issueSourceFlagsChanged reports whether any issue-source flag was given.
func issueSourceFlagsChanged(cmd *cobra.Command) bool {
	for _, f := range projectSourceFlags {
		if cmd.Flags().Changed(f) {
			return true
		}
	}
	return false
}

// issueSourceFromFlags builds the binding the flags ask for. current is the
// project's binding for an edit (nil for add): the request replaces the whole
// binding, so a Jira field not named keeps its current value, and changing
// only --jira-jql on a jira project needs no other flag. It returns nil when
// no issue-source flag was given.
func issueSourceFromFlags(cmd *cobra.Command, current *apiclient.ProjectIssueSource) (*apiclient.ProjectIssueSourceRequest, error) {
	if !issueSourceFlagsChanged(cmd) {
		return nil, nil
	}
	flags := cmd.Flags()
	provider := ""
	if current != nil {
		provider = current.Provider
	}
	if flags.Changed("issue-source") {
		v, _ := flags.GetString("issue-source")
		provider = strings.TrimSpace(v)
	}
	if provider == "" {
		provider = apiclient.IssueProviderDerive
	}
	req := &apiclient.ProjectIssueSourceRequest{Provider: provider}
	if provider != apiclient.IssueProviderJira {
		for _, f := range jiraSourceFlags {
			if flags.Changed(f) {
				return nil, fmt.Errorf("--%s needs --issue-source jira", f)
			}
		}
		return req, nil
	}
	j := apiclient.ProjectIssueSourceJira{}
	if current != nil && current.Provider == apiclient.IssueProviderJira && current.Jira != nil {
		j = *current.Jira
	}
	if flags.Changed("jira-site") {
		j.Site, _ = flags.GetString("jira-site")
	}
	if flags.Changed("jira-project") {
		j.ProjectKey, _ = flags.GetString("jira-project")
	}
	if flags.Changed("jira-jql") {
		j.JQL, _ = flags.GetString("jira-jql")
	}
	if flags.Changed("jira-mapping") {
		src, _ := flags.GetString("jira-mapping")
		m, err := readJiraMapping(cmd, src)
		if err != nil {
			return nil, err
		}
		j.Mapping = m
	}
	req.Jira = &j
	return req, nil
}

// readJiraMapping reads --jira-mapping: "" removes the override, - reads
// stdin, anything else is a file. Only JSON well-formedness is checked here,
// because a malformed document cannot be sent at all; its fields are the
// daemon's to judge.
func readJiraMapping(cmd *cobra.Command, src string) (json.RawMessage, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, nil
	}
	var (
		data []byte
		err  error
	)
	if src == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		// G304: the path is the invoking user's own flag, read with their
		// own permissions; there is no boundary to cross.
		data, err = os.ReadFile(src) //nolint:gosec // G304: see above
	}
	if err != nil {
		return nil, fmt.Errorf("--jira-mapping: %w", err)
	}
	if !json.Valid(data) {
		return nil, errors.New("--jira-mapping: not valid JSON")
	}
	return json.RawMessage(data), nil
}
