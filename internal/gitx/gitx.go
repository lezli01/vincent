// Package gitx is the single door to the git CLI (phase 1 decision): every
// git invocation in vincent goes through Run, which captures output and maps
// failures to a typed *Error. vincent never links a git library.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Timeouts for the shapes of git work vincent does (T1.5/T1.6 decision):
// queries are near-instant; worktree checkouts scale with repo size.
const (
	QueryTimeout    = 30 * time.Second
	WorktreeTimeout = 5 * time.Minute
	// RemoteTimeout bounds a git call that talks to a network remote:
	// `push --delete` on archive (§10, task 008) and the base-branch `fetch`
	// that precedes a worktree creation (§10, task 056). It is its own constant
	// because neither of the others fits: a query timeout is sized for a local
	// object-database read and would fail every push over a slow link, while a
	// worktree timeout would park the archive's caller behind an unreachable
	// host for five minutes. A remote that does not answer inside this is
	// logged and the branch survives, which is the pre-008 behaviour; a fetch
	// that does not answer inside it is logged and the task is created from
	// the local base, which is the pre-056 behaviour.
	RemoteTimeout = 60 * time.Second
)

// MinMajor and MinMinor are the git version below which the daemon warns at
// startup (phase 1 decision).
const (
	MinMajor = 2
	MinMinor = 31
)

// Error is a failed git invocation. ExitCode is -1 when the process could not
// run at all (binary missing, context canceled); Err carries that cause.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *Error) Error() string {
	cmd := "git " + strings.Join(e.Args, " ")
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", cmd, e.Err)
	}
	if e.Stderr != "" {
		return fmt.Sprintf("%s: exit %d: %s", cmd, e.ExitCode, e.Stderr)
	}
	return fmt.Sprintf("%s: exit %d", cmd, e.ExitCode)
}

func (e *Error) Unwrap() error { return e.Err }

// Git runs git commands via the CLI.
type Git struct {
	path string
}

// New returns a runner invoking "git" from PATH.
func New() *Git { return &Git{path: "git"} }

// Run executes git with args in dir and returns trimmed stdout. Failures are
// returned as *Error with stderr captured.
func (g *Git) Run(ctx context.Context, dir string, args ...string) (string, error) {
	return g.run(ctx, dir, nil, args...)
}

func (g *Git) run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	// G204: g.path is "git" (or a test's stand-in) and args is an argument
	// slice assembled by callers in this repository — no shell, so no quoting
	// or metacharacter question arises for the branch and path values in it.
	cmd := exec.CommandContext(ctx, g.path, args...) //nolint:gosec // G204: see above
	hideConsole(cmd)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		e := &Error{Args: args, ExitCode: -1, Stderr: strings.TrimSpace(stderr.String())}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			e.ExitCode = exit.ExitCode()
		} else {
			e.Err = err
		}
		if ctx.Err() != nil {
			e.Err = ctx.Err()
		}
		return "", e
	}
	return strings.TrimSpace(stdout.String()), nil
}

// CheckRefFormat reports whether name is usable as a branch name, returning a
// *Error when it is not.
//
// It asks git rather than reimplementing git's grammar (task 001 decision). That
// grammar rejects `..`, `~^:?*[\`, control characters, `@{`, a `.lock` suffix, a
// leading `-`, a trailing `.`, `//` and the name `HEAD` — a list a hand-rolled
// matcher would have to reproduce and then keep correct on three platforms as git
// evolves. git is also the thing that will ultimately accept or reject the name,
// so any second opinion is one that can disagree.
//
// Two behaviours worth knowing, both verified against git 2.x rather than
// assumed. It needs no repository, so it runs with no working directory — which
// also means `--branch`'s shorthand expansion (`@{-1}`) has nothing to resolve
// against and is simply rejected, exactly what vincent wants when it is about to
// store the literal. And `refs/heads/x` is *accepted*: git considers it a legal
// branch name, which would produce `refs/heads/refs/heads/x`. That is git's call
// to make, and inventing a rule against it is the thing this delegation avoids.
func (g *Git) CheckRefFormat(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	_, err := g.Run(ctx, "", "check-ref-format", "--branch", name)
	return err
}

// Version returns the raw `git version` line and the parsed major/minor.
func (g *Git) Version(ctx context.Context) (raw string, major, minor int, err error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	raw, err = g.Run(ctx, "", "version")
	if err != nil {
		return "", 0, 0, err
	}
	major, minor, ok := parseVersion(raw)
	if !ok {
		return raw, 0, 0, fmt.Errorf("unrecognized git version output %q", raw)
	}
	return raw, major, minor, nil
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)`)

// parseVersion extracts major.minor from output like
// "git version 2.43.0.windows.1".
func parseVersion(raw string) (major, minor int, ok bool) {
	m := versionRe.FindStringSubmatch(raw)
	if m == nil {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	return major, minor, err1 == nil && err2 == nil
}

// RunEnv is Run with extra environment variables layered over the daemon's
// own (task 069). It exists for exactly one caller — the non-forcing branch
// push, which sets `GIT_TERMINAL_PROMPT=0` so a credential helper that wants
// a terminal fails instead of parking a request handler on a prompt nobody
// can answer — and it is a separate method rather than a field on Git because
// every other invocation in vincent wants the user's environment untouched
// (spec §2).
//
// The daemon's own environment is inherited first and env is appended, which
// is exec's own last-wins rule: a user who has already set GIT_TERMINAL_PROMPT
// does not get two conflicting entries, they get this one.
func (g *Git) RunEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return g.run(ctx, dir, env, args...)
}
