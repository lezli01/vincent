// Package builtin is the one place that lists the agent adapters vincent
// ships, in registry order (spec §9.1). Adding an adapter is one arm here and
// one in ConfigPaths, not an edit to every consumer.
//
// Three callers need that list: the daemon's production registry, `vincent
// doctor`'s daemonless DetectAgents, and `vincent workflow validate`'s local
// catalogs. Two of them cannot import internal/daemon — doctor by task 005
// decision 6, the CLI by the one-way dependency direction — so the list lives
// below all three. It imports internal/agent, the adapter packages and
// internal/config, and nothing from the daemon side.
//
// The switch from adapter name to config.Agents field lives here too, rather
// than in internal/config, so config stays a leaf (#854).
package builtin
