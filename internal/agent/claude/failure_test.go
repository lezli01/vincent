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
			// The wording 2.1.329 actually prints, verbatim from
			// testdata/stream_usage_limit_2.1.329.jsonl.
			name: "hit your 5-hour limit",
			res:  agent.RunResult{IsError: true, ExitCode: 1, ResultText: "You've hit your 5-hour limit · resets 1:30pm (Europe/Budapest)"},
			want: agent.FailureUsageLimit,
		},
		{
			name: "hit your weekly limit, typographic apostrophe",
			res:  agent.RunResult{IsError: true, ErrorMessage: "You’ve hit your weekly limit"},
			want: agent.FailureUsageLimit,
		},
		{
			// A clean run quoting the new wording is still not a quota stop.
			name: "hit-your-limit wording in a successful run",
			res:  agent.RunResult{ResultText: "the CLI says \"You've hit your 5-hour limit\" when the window is spent"},
			want: "",
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
		{
			// The incident the IsError guard exists for: a step that
			// succeeded while writing *about* quota stops. Classifying it
			// interrupts a step that finished its work and re-queues the
			// task behind a hold, which the next attempt earns again.
			name: "a successful run quoting every marker is not classified",
			res: agent.RunResult{ResultText: "the wordings are `usage limit reached`, " +
				"`5-hour limit reached` and `weekly limit reached`"},
		},
		{
			name:   "a successful run whose stderr quotes a marker is not classified",
			res:    agent.RunResult{ResultText: "done"},
			stderr: "failure.go:32:\t\"usage limit reached\",",
		},
		{
			// The guard is about the run's verdict, not its exit code: a
			// terminal error event with exit 0 is still a failed run.
			name: "an error result with exit 0 is still classified",
			res:  agent.RunResult{IsError: true, ResultText: "Claude AI usage limit reached"},
			want: agent.FailureUsageLimit,
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

// TestCapturedUsageLimitStopIsClassified parses a genuine quota stop, captured
// from claude 2.1.329 when an account ran out mid-run. It is the fixture the
// markers above never had: the sentence matches none of the three original
// wordings, and what identifies the stop is the result line's `api_error`.
func TestCapturedUsageLimitStopIsClassified(t *testing.T) {
	res := finalResult(t, "stream_usage_limit_2.1.329.jsonl")
	if !res.IsError {
		t.Fatal("IsError = false, want the captured result's is_error")
	}
	if res.Failure == nil || res.Failure.Kind != agent.FailureUsageLimit {
		t.Fatalf("Failure = %+v, want usage_limit from api_error", res.Failure)
	}
	// The captured reset is in the past by now, and a past reset is never
	// acted on: the interval fallback decides instead.
	if res.Failure.RetryAfter != nil {
		t.Errorf("RetryAfter = %s, want nil for a reset already past", res.Failure.RetryAfter)
	}
	// The wording alone is enough too, for a stop that reaches classify
	// without the structured code.
	if f := classify(agent.RunResult{IsError: true, ExitCode: 1, ResultText: res.ResultText}, ""); f == nil || f.Kind != agent.FailureUsageLimit {
		t.Errorf("classify(%q) = %+v, want usage_limit", res.ResultText, f)
	}
}

// TestResultLineAPIErrorNeedsAnErrorResult holds the structured code to the
// rule the wording is held to: only a result that is an error is a stop.
func TestResultLineAPIErrorNeedsAnErrorResult(t *testing.T) {
	evs := parseAll([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"done","api_error":"usage_limit_reached"}`))
	if got := evs[0].Result; got == nil || got.Failure != nil {
		t.Fatalf("Result = %+v, want a result with no Failure", got)
	}
}

// TestWaitClassifiesUsageLimitFromAPIError is the wiring check for the
// 2.1.329 shape: no epoch in the sentence, the reset in the result line.
func TestWaitClassifiesUsageLimitFromAPIError(t *testing.T) {
	for name, env := range map[string][]string{
		"with a reset":    {"FAKEAGENT_USAGE_LIMIT_SHAPE=api-error", "FAKEAGENT_USAGE_LIMIT_RESET=600"},
		"without a reset": {"FAKEAGENT_USAGE_LIMIT_SHAPE=api-error"},
	} {
		t.Run(name, func(t *testing.T) {
			h := startRun(t, "usage-limit", env...)
			drain(t, h)
			res, err := h.Wait()
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if res.Failure == nil || res.Failure.Kind != agent.FailureUsageLimit {
				t.Fatalf("Failure = %+v, want usage_limit", res.Failure)
			}
			wantReset := len(env) == 2
			if got := res.Failure.RetryAfter != nil; got != wantReset {
				t.Fatalf("RetryAfter = %v, want set = %v", res.Failure.RetryAfter, wantReset)
			}
			if wantReset && !res.Failure.RetryAfter.After(time.Now()) {
				t.Errorf("RetryAfter = %s, want a future instant", res.Failure.RetryAfter)
			}
		})
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
// must not start labelling runs it has no evidence about. `limit-prose` is the
// sharp end of that — a run that succeeded while quoting every wording the
// classifier matches, which is what a step documenting this machinery writes.
func TestWaitLeavesOrdinaryFailuresUnclassified(t *testing.T) {
	for _, scenario := range []string{"success", "error-event", "nonzero-exit", "limit-prose"} {
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
