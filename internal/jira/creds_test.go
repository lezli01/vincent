package jira

import (
	"runtime"
	"strings"
	"testing"
)

func TestEnvCredentials(t *testing.T) {
	env := []string{"JIRA_USER=first@example.com", "OTHER=x", "JIRA_USER=bot@example.com", "JIRA_TOKEN=s3cret-value", "EMPTY="}
	user, token, err := EnvCredentials(env, "JIRA_USER", "JIRA_TOKEN")
	if err != nil || user != "bot@example.com" || token != "s3cret-value" {
		t.Fatalf("got %q %q %v; the last entry wins", user, token, err)
	}
	for _, tc := range []struct{ userEnv, tokenEnv, names string }{
		{"JIRA_USER", "MISSING", "MISSING"},
		{"NOPE", "JIRA_TOKEN", "NOPE"},
		{"JIRA_USER", "EMPTY", "EMPTY"},
		{"JIRA_USER", "", "no environment variable"},
	} {
		_, _, err := EnvCredentials(env, tc.userEnv, tc.tokenEnv)
		if ReasonOf(err) != ReasonNoCredential || !strings.Contains(err.Error(), tc.names) {
			t.Errorf("%s/%s: %v", tc.userEnv, tc.tokenEnv, err)
		}
		if err != nil && (strings.Contains(err.Error(), "s3cret-value") || strings.Contains(err.Error(), "bot@example.com")) {
			t.Errorf("error names a value: %v", err)
		}
	}
	_, _, err = EnvCredentials([]string{"jira_user=a", "jira_token=b"}, "JIRA_USER", "JIRA_TOKEN")
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Errorf("windows names are case-insensitive: %v", err)
		}
	} else if ReasonOf(err) != ReasonNoCredential {
		t.Errorf("names are case-sensitive off windows: %v", err)
	}
}
