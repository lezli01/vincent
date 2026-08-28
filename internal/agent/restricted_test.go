package agent

import "testing"

// TestRestrictedVerdict pins the same asymmetry the input gate rests on: only
// a positive "never" refuses anything, and — unlike the input verdict — the
// answer never consults Availability, because restricted support is a fact
// about the adapter and the OS rather than about the installed build.
func TestRestrictedVerdict(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry CatalogEntry
		want  RestrictedVerdict
	}{
		{"never adapter, nothing installed", CatalogEntry{
			Options: Options{RestrictedSupport: RestrictedNever},
		}, RestrictedUnsupported},
		{"never adapter, installed", CatalogEntry{
			Options:      Options{RestrictedSupport: RestrictedNever},
			Availability: Availability{Found: true},
		}, RestrictedUnsupported},
		{"always adapter, nothing installed", CatalogEntry{
			Options: Options{RestrictedSupport: RestrictedAlways},
		}, RestrictedSupported},
		{"unjudged catalog", CatalogEntry{
			Availability: Availability{Found: true},
		}, RestrictedUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.RestrictedVerdict(); got != tc.want {
				t.Errorf("RestrictedVerdict() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCatalogsRestrictedPossibleNeverProbes is the property the creation-time
// gate depends on: the API answers it on every POST /v1/tasks, so an answer
// that cost a subprocess would put an agent CLI in the request path.
func TestCatalogsRestrictedPossibleNeverProbes(t *testing.T) {
	bin := fakeBinary(t)
	stub := &stubAdapter{
		name: "cursor", path: bin,
		av:      Availability{Found: true, Path: bin},
		opts:    Options{RestrictedSupport: RestrictedNever},
		curated: Options{RestrictedSupport: RestrictedNever},
	}
	c := NewCatalogCache(NewRegistry(stub))

	if c.Catalogs().RestrictedPossible("cursor") {
		t.Error("RestrictedPossible = true for an adapter that can never restrict")
	}
	if stub.detects != 0 || stub.options != 0 {
		t.Errorf("probed (%d detects, %d options); the gate must never spawn", stub.detects, stub.options)
	}
	// An unknown adapter is someone else's check: the catalogs have nothing
	// to say about it, so it must not be refused on their account.
	if !c.Catalogs().RestrictedPossible("not-an-adapter") {
		t.Error("RestrictedPossible = false for an unregistered agent; unknown must stay permissive")
	}
	// A catalog that states no level is unjudged, not a refusal.
	if !(Catalogs{"future": Options{}}).RestrictedPossible("future") {
		t.Error("RestrictedPossible = false for an unjudged catalog; silence is not evidence")
	}
}
