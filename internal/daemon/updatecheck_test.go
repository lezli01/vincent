package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/release"
)

// The acceptance criterion, written as an assertion rather than as a duration
// somebody waits out: with the check off, the feed handler calls t.Fatal if it
// is ever hit. Both spellings of "off" are covered, because
// `update.poll_interval: 0` is documented to have the same effect as
// `update.check: false`.
func TestUpdateCheckMakesNoRequestWhenDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Update
	}{
		{"check false", config.Update{Check: false, PollInterval: config.Duration(time.Hour)}},
		{"poll_interval zero", config.Update{Check: true, PollInterval: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Errorf("the release feed was called with the check disabled")
			}))
			t.Cleanup(srv.Close)

			cfg := config.Default()
			cfg.Update = tc.cfg
			u := NewUpdateCheck(func() config.Config { return cfg },
				release.New(release.Options{BaseURL: srv.URL}), nil)
			u.Tick(t.Context())

			got := u.Result()
			if got.Enabled {
				t.Error("a disabled check reported itself enabled")
			}
			if !got.CheckedAt.IsZero() {
				t.Errorf("a disabled check stamped checked_at: %s", got.CheckedAt)
			}
		})
	}
}

func TestUpdateCheckCachesLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","published_at":"2026-08-21T09:31:07Z",
			"html_url":"https://example.invalid/v9.9.9"}`))
	}))
	t.Cleanup(srv.Close)

	u := NewUpdateCheck(enabledUpdateConfig, release.New(release.Options{BaseURL: srv.URL}), nil)
	u.Tick(t.Context())

	got := u.Result()
	if !got.Enabled {
		t.Error("an enabled check reported itself disabled")
	}
	if got.Latest.Version != "v9.9.9" {
		t.Errorf("latest = %q, want v9.9.9", got.Latest.Version)
	}
	if got.CheckedAt.IsZero() {
		t.Error("a successful check left checked_at zero")
	}
	if got.Error != "" {
		t.Errorf("a successful check carried an error: %s", got.Error)
	}
}

// A failed tick keeps what the previous one learned. A check that forgets
// because a laptop was on a train is worse than one that says nothing — and
// the reason is carried on the result, where `vincent doctor` finds it.
func TestUpdateCheckKeepsPreviousAnswerOnFailure(t *testing.T) {
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","published_at":"2026-08-21T09:31:07Z"}`))
	}))
	t.Cleanup(srv.Close)

	u := NewUpdateCheck(enabledUpdateConfig, release.New(release.Options{BaseURL: srv.URL}), nil)
	u.Tick(t.Context())
	first := u.Result()

	fail = true
	u.Tick(t.Context())
	after := u.Result()

	if after.Latest.Version != first.Latest.Version {
		t.Errorf("a failed tick discarded the cached release: %q -> %q",
			first.Latest.Version, after.Latest.Version)
	}
	if !after.CheckedAt.Equal(first.CheckedAt) {
		t.Error("a failed tick moved checked_at, which must mark the last success")
	}
	if after.Error == "" {
		t.Error("a failed tick carried no reason")
	}
}

func enabledUpdateConfig() config.Config {
	cfg := config.Default()
	cfg.Update = config.Update{Check: true, PollInterval: config.Duration(time.Hour)}
	return cfg
}
