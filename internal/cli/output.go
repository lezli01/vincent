package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
)

// jsonFlag adds --json to a command. Every data subcommand carries it (PR U
// decision): the table is for a human reading a terminal, the JSON is for the
// script that inevitably wraps it, and a tool that only has the former gets
// parsed with awk by someone eventually.
func jsonFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("json", false, "Emit JSON instead of a table")
}

func wantJSON(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

// emitJSON writes v as indented JSON. `null` is never emitted for an empty
// collection — a script doing `| jq '.[]'` should see an empty list, not a
// type error — so callers pass an initialized slice.
func emitJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// table writes aligned columns. Empty rows still print the header, so an
// empty result looks like an empty table rather than a broken command.
func table(w io.Writer, header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(header, "\t")); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := fmt.Fprintln(tw, strings.Join(r, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// errDaemonUnreachable is exit code 2: the request was never made because no
// daemon answered. It is deliberately distinct from exit 1 (the daemon
// answered and said no), so a script can tell "start the daemon" from "fix
// your request" without parsing stderr (PR U decision).
var errDaemonUnreachable = exitError{code: 2}

// client resolves the running daemon's API client.
//
// It never auto-starts one. Bare `vincent` starts a daemon because a TUI is
// an interactive session the user asked for; a `vincent task ls` inside a
// shell loop silently spawning a background daemon is a surprise, and the
// surprise is worse than the error (PR U decision).
func client(cmd *cobra.Command) (*apiclient.Client, error) {
	dirs, err := config.ResolveDirs()
	if err != nil {
		return nil, err
	}
	c, err := apiclient.Discover(dirs.Data)
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
			"no running daemon found — start one with `vincent daemon start`")
		return nil, errDaemonUnreachable
	}
	// Discover only reads daemon.json; a stale file from an unclean shutdown
	// parses fine and then fails on first use. Probing here turns that into
	// the same "no daemon" answer rather than a confusing transport error at
	// an arbitrary point later.
	if _, err := c.Health(cmd.Context()); err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
			"a daemon is recorded but not responding — start one with `vincent daemon start`")
		return nil, errDaemonUnreachable
	}
	return c, nil
}

// withClient runs fn against a live daemon, mapping unreachability to exit 2.
func withClient(cmd *cobra.Command, fn func(context.Context, *apiclient.Client) error) error {
	c, err := client(cmd)
	if err != nil {
		return err
	}
	return fn(cmd.Context(), c)
}

// dash renders an unset optional value. A column that would otherwise be
// blank reads as a rendering bug; "-" reads as "not set".
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// deref reads an optional string. Nil and empty collapse to the same cell —
// a table has no way to show the difference, and no reader wants one.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// apiMessage unwraps the daemon's error envelope for display, falling back to
// the transport error. Exit code stays 1: the daemon answered, it said no.
func apiMessage(err error) string {
	var apiErr *apiclient.Error
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return err.Error()
}
