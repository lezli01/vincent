package jira

import (
	"fmt"
	"runtime"
	"strings"
)

// EnvCredentials looks up the user (the account's email) and the API token in
// a resolved K=V environment, by the names a binding's jira.user_env and
// jira.token_env give (spec §12.3, task 142 decision 4). A later entry wins,
// as it does for a child process, and names are matched case-insensitively on
// Windows, where the environment is. Either variable unset or empty is a
// no_credential *Error naming the variable — never a value.
func EnvCredentials(env []string, userEnv, tokenEnv string) (user, token string, err error) {
	user, uok := lookupEnv(env, userEnv)
	token, tok := lookupEnv(env, tokenEnv)
	switch {
	case !uok:
		return "", "", noCredential(userEnv)
	case !tok:
		return "", "", noCredential(tokenEnv)
	}
	return user, token, nil
}

func noCredential(name string) *Error {
	if name == "" {
		return &Error{Op: "credentials", Reason: ReasonNoCredential, Err: fmt.Errorf("no environment variable is named for it")}
	}
	return &Error{Op: "credentials", Reason: ReasonNoCredential, Err: fmt.Errorf("environment variable %s is not set", name)}
}

// lookupEnv is name's last non-empty-named value in env; ok is false when it
// is absent or empty.
func lookupEnv(env []string, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	fold := runtime.GOOS == "windows"
	val, found := "", false
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == name || (fold && strings.EqualFold(k, name)) {
			val, found = v, true
		}
	}
	return val, found && val != ""
}
