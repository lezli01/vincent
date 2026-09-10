// Package skills embeds the agent skills this repository publishes, so the
// binary can reuse their text instead of keeping a second copy of it.
//
// It lives here rather than under internal/ because `go:embed` reads only
// from the embedding package's own directory tree: a package elsewhere cannot
// name `skills/vincent-workflows/SKILL.md` at all. Nothing but the embedded
// files belongs in it — it depends on no other package in this module and
// must stay that way, so any package may import it.
//
// The published skill is the single source: editing
// `vincent-workflows/SKILL.md` changes the built-in `create-workflow` and
// `update-workflows` prompts at the next build, with no Go change. That
// coupling is deliberate (task 024 decision 7) and is why the consumers escape
// and re-indent the text rather than assuming anything about its shape.
package skills

import "embed"

// VincentWorkflows is `skills/vincent-workflows/SKILL.md` verbatim, YAML
// front matter included. Callers that want only the instructions strip it.
//
//go:embed vincent-workflows/SKILL.md
var VincentWorkflows string

// FS is every published skill's `SKILL.md`, keyed by its path inside this
// directory (`vincent-workflows/SKILL.md`). It is the *set* of what this
// repository publishes, which is why nothing enumerates the names in Go: a
// second directory under `skills/` is picked up by the glob, and every
// surface that reports installation state — `vincent doctor`, `vincent
// skills`, the daemon view — reports it with no code change (task 095
// decision 10).
//
// Only `SKILL.md` is embedded, not `references/`, `LICENSE.txt` or
// `agents/openai.yaml`. Installation shells out to the `skills` CLI, which
// fetches the whole published directory from git; what the binary needs from
// the tree is the front matter — the name, the description and
// `metadata.version` it compares an installed copy against.
//
//go:embed */SKILL.md
var FS embed.FS
