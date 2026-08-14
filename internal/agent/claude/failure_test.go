package claude

import (
	"strconv"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// TestClassify is the table the whole conservative-parse argument rests on.
// The last group matters most: output the adapter does not recognize must
// yield a nil Failure, so a run that used to fail `nonzero_exit`/`agent_error`
// still does.
func TestClassify(t *testing.T) {
	tests := []struct {
		name   string
		res    agent.RunResult
		stderr string
		want   agent.FailureKind // "" = nil Failure
	}{
		{
			name: "usage limit in the result text",
			res:  agent.RunResult{IsError: true, ResultText: "Claude AI usage limit reached"},
			want: agent.FailureUsageLimit,
		},
		{
			name: "usage limit in the error message",
			res:  agent.RunResult{IsError: true, ErrorMessage: "Claude AI usage limit reached|0"},
			want: agent.FailureUsageLimit,
		},
		{
			name:   "usage limit only on stderr",
			res:    agent.RunResult{ExitCode: 1},
			stderr: "5-hour limit reached, try again later",
			want:   agent.FailureUsageLimit,
		},
		{
			name: "weekly limit",
			res:  agent.RunResult{IsError: true, ResultText: "Weekly limit reached for this account"},
			want: agent.FailureUsageLimit,
		},
		{
			name: "invalid api key",
			res:  agent.RunResult{IsError: true, ResultText: "Invalid API key · Please run /login"},
			want: agent.FailureUnauthenticated,
		},
		{
			name:   "logged out on stderr",
			res:    agent.RunResult{ExitCode: 1},
			stderr: "You are not logged in.",
			want:   agent.FailureUnauthenticated,
		},
		{
			name: "expired oauth token",
			res:  agent.RunResult{IsError: true, ErrorMessage: "OAuth token has expired"},
			want: agent.FailureUnauthenticated,
		},
		{
			// The precedence case: a quota stop is recoverable by waiting, so
			// it wins over a phrase that would send the reader to re-login.
			name: "both phrasings present prefers the recoverable one",
			res:  agent.RunResult{IsError: true, ResultText: "usage limit reached; please run /login to switch accounts"},
			want: agent.FailureUsageLimit,
		},

		// Everything below is today's behaviour, unchanged.
		{
			name: "an ordinary agent failure is not classified",
			res:  agent.RunResult{IsError: true, ResultText: "the tests did not pass"},
		},
		{
			name: "a plain nonzero exit is not classified",
			res:  agent.RunResult{ExitCode: 3},
		},
		{
			name:   "an unrelated rate limit is not a usage limit",
			res:    agent.RunResult{ExitCode: 1},
			stderr: "curl: (429) rate limit exceeded talking to api.example.com",
		},
		{
			name: "a successful run is not classified",
			res:  agent.RunResult{ResultText: "done"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.res, tc.stderr)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("classify = %+v, want nil (unrecognized output must keep today's reason)", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("classify = nil, want kind %s", tc.want)
			}
			if got.Kind != tc.want {
				t.Fatalf("kind = %s, want %s", got.Kind, tc.want)
			}
		})
	}
}

// TestClassifyParsesResetTime covers the RetryAfter leg: a usable timestamp is
// parsed, and anything else stays nil so the interval fallback decides rather
// than a guess.
func TestClassifyParsesResetTime(t *testing.T) {
	now := time.Now()
	in90m := now.Add(90 * time.Minute).Truncate(time.Second)

	got := classify(agent.RunResult{
		IsError:    true,
		ResultText: "Claude AI usage limit reached|" + strconv.FormatInt(in90m.Unix(), 10),
	}, "")
	if got == nil || got.RetryAfter == nil {
		t.Fatalf("classify = %+v, want a parsed RetryAfter", got)
	}
	if !got.RetryAfter.Equal(in90m.UTC()) {
		t.Errorf("RetryAfter = %s, want %s", got.RetryAfter, in90m.UTC())
	}

	unusable := map[string]string{
		"no timestamp at all":  "Claude AI usage limit reached",
		"not a number":         "Claude AI usage limit reached|later-today",
		"already in the past":  "Claude AI usage limit reached|1000000000",
		"implausibly far away": "Claude AI usage limit reached|" + strconv.FormatInt(now.Add(60*24*time.Hour).Unix(), 10),
	}
	for name, text := range unusable {
		t.Run(name, func(t *testing.T) {
			f := classify(agent.RunResult{IsError: true, ResultText: text}, "")
			if f == nil || f.Kind != agent.FailureUsageLimit {
				t.Fatalf("classify = %+v, want a usage-limit failure", f)
			}
			if f.RetryAfter != nil {
				t.Errorf("RetryAfter = %s, want nil (never a guessed reset time)", f.RetryAfter)
			}
		})
	}
}

// TestWaitClassifiesUsageLimit runs the real adapter against the fake CLI, so
// the wiring from the stream through Wait is exercised rather than assumed.
func TestWaitClassifiesUsageLimit(t *testing.T) {
	h := startRun(t, "usage-limit", "FAKEAGENT_USAGE_LIMIT_RESET=600")
	drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Failure == nil || res.Failure.Kind != agent.FailureUsageLimit {
		t.Fatalf("Failure = %+v, want usage_limit", res.Failure)
	}
	if res.Failure.RetryAfter == nil {
		t.Fatal("RetryAfter = nil, want the reset time the CLI reported")
	}
	if !res.Failure.RetryAfter.After(time.Now()) {
		t.Errorf("RetryAfter = %s, want a future instant", res.Failure.RetryAfter)
	}
}

// TestWaitClassifiesUnauthenticated is the same wiring check for the auth half.
func TestWaitClassifiesUnauthenticated(t *testing.T) {
	h := startRun(t, "unauthenticated")
	drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Failure == nil || res.Failure.Kind != agent.FailureUnauthenticated {
		t.Fatalf("Failure = %+v, want unauthenticated", res.Failure)
	}
}

// TestWaitLeavesOrdinaryFailuresUnclassified is the regression: the adapter
// must not start labelling runs it has no evidence about.
func TestWaitLeavesOrdinaryFailuresUnclassified(t *testing.T) {
	for _, scenario := range []string{"success", "error-event", "nonzero-exit"} {
		t.Run(scenario, func(t *testing.T) {
			h := startRun(t, scenario)
			drain(t, h)
			res, err := h.Wait()
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if res.Failure != nil {
				t.Errorf("Failure = %+v, want nil for scenario %s", res.Failure, scenario)
			}
		})
	}
}
