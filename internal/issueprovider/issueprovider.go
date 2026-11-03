package issueprovider

import "slices"

// The provider ids, as issue_remotes.provider stores them.
const (
	// GitHub is a GitHub issue imported by task 130's sync.
	GitHub = "github"
	// Jira is a Jira Cloud issue imported by task 142's sync.
	Jira = "jira"
)

// provider is one registry entry.
type provider struct {
	id         string
	writesBack bool
}

// registry is every known provider, in All's order.
var registry = []provider{
	{id: GitHub, writesBack: true},
	{id: Jira, writesBack: true},
}

// Known reports whether id is a provider vincent knows.
func Known(id string) bool {
	return slices.ContainsFunc(registry, func(p provider) bool { return p.id == id })
}

// WritesBack reports whether a live remote of provider id has its state
// changes and granted comments carried back to the provider. An unknown id
// does not.
func WritesBack(id string) bool {
	return slices.ContainsFunc(registry, func(p provider) bool { return p.id == id && p.writesBack })
}

// All returns every known provider id in a fixed order. The slice is fresh.
func All() []string {
	out := make([]string, len(registry))
	for i, p := range registry {
		out[i] = p.id
	}
	return out
}
