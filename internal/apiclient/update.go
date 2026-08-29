package apiclient

import (
	"context"
	"time"
)

// UpdateStatus is the GET /v1/update body: the daemon's cached release check
// (task 055, §13.2).
//
// The endpoint serves the cache and never polls, so a client refreshing this
// is asking the daemon what it already knows. `vincent update --check` is the
// path that goes to GitHub, and it does so directly rather than through here.
type UpdateStatus struct {
	// Enabled is whether the daemon's background poll is configured to run.
	// False means `update.check: false` or `update.poll_interval: 0`, not
	// that anything is broken.
	Enabled bool `json:"enabled"`
	// CurrentVersion is the **daemon's** build, which is not necessarily the
	// build of the binary that made this request — that difference is the
	// whole point of the mismatch line on `vincent daemon status`.
	CurrentVersion string `json:"current_version"`
	// LatestVersion is the newest stable release the daemon has seen, empty
	// until a poll succeeds. Prereleases never appear here.
	LatestVersion string `json:"latest_version"`
	// UpdateAvailable is the daemon's verdict, not a comparison the client
	// re-derives.
	UpdateAvailable bool       `json:"update_available"`
	PublishedAt     *time.Time `json:"published_at"`
	ReleaseURL      string     `json:"release_url"`
	// CheckedAt is nil until a poll succeeds. Nil with Enabled true is the
	// never-polled state — distinct from "no update available", and rendered
	// differently.
	CheckedAt *time.Time `json:"checked_at"`
	// Error is why the last poll failed, empty when it worked.
	Error string `json:"error"`
}

// Checked reports whether a poll has ever succeeded on this daemon.
func (u UpdateStatus) Checked() bool { return u.CheckedAt != nil }

// Update fetches the daemon's cached release check. It makes no outbound
// call beyond localhost.
func (c *Client) Update(ctx context.Context) (UpdateStatus, error) {
	var out UpdateStatus
	if err := c.get(ctx, "/v1/update", &out); err != nil {
		return UpdateStatus{}, err
	}
	return out, nil
}
