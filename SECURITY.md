# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue for security vulnerabilities.

Use GitHub's private vulnerability reporting: go to the repository's
**Security** tab → **Report a vulnerability**, or use this direct link:
<https://github.com/lezli01/vincent/security/advisories/new>. Reports submitted
this way are private and visible only to the maintainers, and are acknowledged
on a best-effort basis.

## Scope notes

vincent executes AI agents in **full-auto mode by default** — agents can run
arbitrary commands as the invoking user, and git worktrees provide collision
isolation, not security isolation. This is a documented design decision (see
[the spec](docs/versions/v0/spec.md), §16), not a vulnerability. Reports about
sandbox escapes are only in scope for the opt-in `restricted` permission mode.
