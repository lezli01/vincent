package agent

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// Catalogs maps adapter name → catalog: the input of the §8.2 cross-catalog
// check.
type Catalogs map[string]Options

// Finding is one §8.2 catalog check result, error or warning.
type Finding struct {
	Field   string // "model" | "effort"
	Message string
}

// Check validates a resolved selection against the catalogs (§8.2): a value
// in the resolved adapter's own catalog is valid; a value found only in
// another adapter's catalog is an error (a claude-only effort must never
// reach a codex step); a value in no catalog at all passes with a warning —
// the CLI stays the final authority at run time. An agent missing from the
// catalogs is skipped entirely: unknown agents are someone else's check.
func (c Catalogs) Check(sel Selection) (errs, warns []Finding) {
	for _, f := range []struct {
		field string
		value string
		list  func(Options) []Option
	}{
		{"model", sel.Model, func(o Options) []Option { return o.Models }},
		{"effort", sel.Effort, func(o Options) []Option { return o.Efforts }},
	} {
		if f.value == "" {
			continue
		}
		own, known := c[sel.Agent]
		if !known {
			continue
		}
		if containsOption(f.list(own), f.value) {
			continue
		}
		if owner := c.ownerOf(f.list, f.value, sel.Agent); owner != "" {
			errs = append(errs, Finding{Field: f.field, Message: fmt.Sprintf(
				"%s %q is not valid for agent %s (it belongs to %s's catalog)",
				f.field, f.value, sel.Agent, owner)})
			continue
		}
		warns = append(warns, Finding{Field: f.field, Message: fmt.Sprintf(
			"%s %q is not in %s's catalog; passing through (the CLI is the final authority)",
			f.field, f.value, sel.Agent)})
	}
	return errs, warns
}

// InputEverPossible reports whether the named adapter could ever take mid-run
// input (task 013). An adapter missing from the catalogs answers true: unknown
// agents are someone else's check, the same rule Check applies.
func (c Catalogs) InputEverPossible(name string) bool {
	opts, known := c[name]
	if !known {
		return true
	}
	return opts.InputEverPossible()
}

// RestrictedPossible reports whether the named adapter could ever run a
// `permission_mode: restricted` step on this host (task 041). It reads the
// static catalog level only, so it never probes and never needs an installed
// binary — the answer does not depend on one. An adapter missing from the
// catalogs answers true: unknown agents are someone else's check, the same
// rule Check and InputEverPossible apply.
func (c Catalogs) RestrictedPossible(name string) bool {
	opts, known := c[name]
	if !known {
		return true
	}
	return opts.RestrictedEverPossible()
}

// ownerOf names an adapter other than exclude whose catalog contains value.
func (c Catalogs) ownerOf(list func(Options) []Option, value, exclude string) string {
	for name, opts := range c {
		if name != exclude && containsOption(list(opts), value) {
			return name
		}
	}
	return ""
}

func containsOption(opts []Option, value string) bool {
	for _, o := range opts {
		if o.Value == value {
			return true
		}
	}
	return false
}

// InputVerdict is what the daemon knows *right now* about an adapter's
// ability to stop a run and take a human answer (task 013). It is the one
// answer the creation-time gate and the engine's pre-flight both consult, so
// "can this agent be asked a question" has a single source.
type InputVerdict string

// Input verdicts (task 013).
const (
	// InputUnknown is "nobody can say": the binary is not installed, the
	// probe did not answer, or the adapter is not registered. Callers must
	// let the work through — §9.6's degrade-never-block rule outranks a gate
	// built on a probe that failed.
	InputUnknown InputVerdict = "unknown"
	// InputSupported is an installed binary that reported input support.
	InputSupported InputVerdict = "supported"
	// InputUnsupported is a positive no: an adapter that can never support
	// input, or an installed one whose version falls outside the verified
	// family. Only this verdict refuses anything.
	InputUnsupported InputVerdict = "unsupported"
)

// InputVerdict reports whether the named adapter can take mid-run input,
// combining the static catalog capability with what the last probe saw
// (task 013). The static "never" is decisive without an installed binary;
// everything else needs one, and an absent or unprobed binary is unknown.
func (c *CatalogCache) InputVerdict(ctx context.Context, name string) InputVerdict {
	if c == nil {
		return InputUnknown
	}
	e, ok := c.Entry(ctx, name, false)
	if !ok {
		return InputUnknown
	}
	return e.InputVerdict()
}

// CatalogEntry is one adapter's cached probe result (spec §9.6): the §9.5
// availability plus the merged option catalog.
type CatalogEntry struct {
	Availability Availability
	Options      Options
	ProbedAt     time.Time
	ProbeError   string // "" = clean probe
	// AuthCheckedAt is when Detect last ran for this entry — the full probe,
	// or a Detect-only auth refresh (task 026). It is what authTTL is measured
	// from, and is deliberately separate from ProbedAt, which dates the
	// *catalog*: the options are still exactly as fresh as the binary.
	AuthCheckedAt time.Time
	// AuthError records a Detect-only auth refresh that failed. The entry
	// keeps its previous availability when that happens, including its
	// `logged_in` — see redetect. It is not served on the wire: §9.6's
	// `probe_error` means "the option probe failed and you are reading the
	// curated catalog", and widening it to cover a freshness check would
	// change what every existing client thinks it is being told.
	AuthError string
}

// InputVerdict is this entry's answer to "can this adapter be asked a
// question" (task 013). The static catalog level is decisive without an
// installed binary — an adapter with no control channel has none whether or
// not it is on this machine — and everything else needs one: an absent or
// unprobed binary is unknown, never a refusal.
func (e CatalogEntry) InputVerdict() InputVerdict {
	switch {
	case !e.Options.InputEverPossible():
		return InputUnsupported
	case !e.Availability.Found:
		return InputUnknown
	case e.Availability.SupportsInput:
		return InputSupported
	default:
		return InputUnsupported
	}
}

// RestrictedVerdict is what the daemon knows about an adapter's ability to
// run a `restricted` step on this host (task 041) — the
// permission-compatibility facet of §9.5 health.
type RestrictedVerdict string

// Restricted verdicts (task 041).
const (
	// RestrictedUnknown is "nobody can say": the adapter is not registered,
	// or its catalog states no level. Callers must let the work through.
	RestrictedUnknown RestrictedVerdict = "unknown"
	// RestrictedSupported is an adapter that can restrict here.
	RestrictedSupported RestrictedVerdict = "supported"
	// RestrictedUnsupported is a positive no — cursor on Windows is the one
	// case vincent has (§9.7). Only this verdict refuses anything.
	RestrictedUnsupported RestrictedVerdict = "unsupported"
)

// RestrictedVerdict is this entry's answer to "can this adapter run a
// restricted step here" (task 041).
//
// Unlike InputVerdict it never consults Availability: restricted support is a
// fact about the adapter and the OS, so an absent binary changes nothing about
// the answer. That is what makes the creation-time refusal safe on a machine
// where nothing is installed — the same argument that makes InputNever
// decisive without a binary.
func (e CatalogEntry) RestrictedVerdict() RestrictedVerdict {
	return RestrictedVerdictFor(e.Options)
}

// RestrictedVerdictFor is the verdict for a catalog on its own, for callers
// that hold one without a cache entry — `vincent doctor`, which reads
// Curated() and never runs an option probe.
func RestrictedVerdictFor(o Options) RestrictedVerdict {
	switch o.RestrictedSupport {
	case RestrictedNever:
		return RestrictedUnsupported
	case RestrictedAlways:
		return RestrictedSupported
	default:
		return RestrictedUnknown
	}
}

// catalogKey is the binary identity a cache entry is keyed by: resolved path
// plus mtime. Help output is a pure function of the binary, so an unchanged
// key means an unchanged catalog (§9.6); the version is recorded in the
// entry, not the key — an unchanged file cannot change version.
type catalogKey struct {
	path  string // "" when the binary does not resolve
	mtime int64  // unix nanos; 0 when unresolved or unstattable
}

// catalogSlot holds one adapter's cache line. probeMu serializes probes so
// concurrent requests never double-spawn; dataMu guards the fields so
// readers (Catalogs, primed lookups) never wait behind a running probe.
type catalogSlot struct {
	probeMu sync.Mutex
	dataMu  sync.RWMutex
	valid   bool
	key     catalogKey
	entry   CatalogEntry
}

// failureTTL is how long a *failed* probe is trusted.
//
// The binary-identity key (§9.6) is only sound for a probe that answered: help
// output is a pure function of the binary, a failure is not. A cold logon timed
// out `codex --version` once and the daemon then served "codex is unavailable —
// exit status 1" for its whole lifetime against a healthy CLI, because nothing
// about the binary had changed since the moment it was too slow (T4.22).
//
// A minute is chosen so the failure survives a burst of requests — the TUI's
// board, detail and new-task views each ask — and does not survive the user
// noticing and looking again. Re-probing an agent that is genuinely absent
// costs no subprocess at all: an unresolved path fails in Detect before it
// spawns anything.
const failureTTL = time.Minute

// authTTL is how long a *clean* entry's `logged_in` is trusted before Detect
// is re-run for it alone (task 026, amending §9.6).
//
// Binary identity is exact for help output and only a *floor* for auth state:
// nothing about the binary changes when a user logs in, so a cached `false`
// otherwise survives until the CLI is upgraded or `?refresh=true` arrives.
// §9.6 already records that gap and names the per-field TTL as the better fix
// than doctor's unconditional refresh, which only repairs one surface.
//
// Five minutes is chosen the way failureTTL was: long enough that a board, a
// detail view and a new-task form asking in the same second cost one probe,
// short enough that a user who logs in and looks again is told the truth. Only
// the entries that can answer are re-checked — an adapter whose `logged_in` is
// nil has no auth state to go stale (§9.5) — and only Detect re-runs, because
// the option catalog genuinely is a pure function of the binary.
const authTTL = 5 * time.Minute

// CatalogCache caches Detect+Options per adapter, keyed by binary identity.
// Only Entry probes (and only when the identity changed, the last probe failed
// more than failureTTL ago, or refresh is set); validation paths read Catalogs,
// which never spawns a subprocess (§8.2, §9.6 — T2.11 decisions).
type CatalogCache struct {
	reg   *Registry
	now   func() time.Time
	slots map[string]*catalogSlot
}

// NewCatalogCache returns a cache over the registry's adapters.
func NewCatalogCache(reg *Registry) *CatalogCache {
	slots := make(map[string]*catalogSlot, len(reg.Names()))
	for _, n := range reg.Names() {
		slots[n] = &catalogSlot{}
	}
	return &CatalogCache{reg: reg, now: time.Now, slots: slots}
}

// Names returns the adapter names the cache serves, in registration order.
func (c *CatalogCache) Names() []string { return c.reg.Names() }

// Entry returns the adapter's catalog entry, probing only when the binary
// identity changed, the cached entry is a stale failure, or refresh is set. The
// bool is false for an unknown adapter. A request-time call costs a path
// resolution and one stat.
func (c *CatalogCache) Entry(ctx context.Context, name string, refresh bool) (CatalogEntry, bool) {
	slot, ok := c.slots[name]
	if !ok {
		return CatalogEntry{}, false
	}
	a, _ := c.reg.Get(name)
	slot.probeMu.Lock()
	defer slot.probeMu.Unlock()
	key := identity(a)
	now := c.now()
	slot.dataMu.RLock()
	hit := slot.valid && !refresh && key == slot.key && !staleFailure(slot.entry, now)
	entry := slot.entry
	slot.dataMu.RUnlock()
	if hit {
		if !staleAuth(entry, now) {
			return entry, true
		}
		// The catalog is still exact — the binary has not changed — so only
		// the half binary identity cannot vouch for is asked again (task 026).
		entry = redetect(ctx, a, entry, now)
		slot.dataMu.Lock()
		slot.entry = entry
		slot.dataMu.Unlock()
		return entry, true
	}
	entry = probe(ctx, a, now)
	slot.dataMu.Lock()
	slot.valid, slot.key, slot.entry = true, key, entry
	slot.dataMu.Unlock()
	return entry, true
}

// Catalogs returns adapter → catalog for §8.2 validation: the cached options
// when primed, the curated catalog otherwise. It never probes and never
// waits behind a running probe — probing only ever adds values to curated,
// so a verdict can soften but never harden once the probe lands.
func (c *CatalogCache) Catalogs() Catalogs {
	out := make(Catalogs, len(c.slots))
	for _, name := range c.reg.Names() {
		slot := c.slots[name]
		slot.dataMu.RLock()
		valid, opts := slot.valid, slot.entry.Options
		slot.dataMu.RUnlock()
		if !valid {
			a, _ := c.reg.Get(name)
			opts = a.Curated()
		}
		out[name] = opts
	}
	return out
}

// staleFailure reports whether an entry that did not fully answer is old enough
// to be worth asking again. Either half counts: an adapter can be available and
// still have failed its options probe (cursor's network catalog), and a stale
// curated fallback is the same kind of wrong answer as a stale "unavailable".
func staleFailure(e CatalogEntry, now time.Time) bool {
	if e.Availability.Found && e.ProbeError == "" {
		return false
	}
	return now.Sub(e.ProbedAt) >= failureTTL
}

// staleAuth reports whether a clean entry's `logged_in` is old enough to be
// worth asking again (task 026). Only an entry that *has* an answer qualifies:
// an adapter that cannot cheaply tell (§9.5) returns nil forever, and spawning
// a subprocess every five minutes to be told nothing again is pure cost.
func staleAuth(e CatalogEntry, now time.Time) bool {
	if !e.Availability.Found || e.Availability.LoggedIn == nil {
		return false
	}
	return now.Sub(e.AuthCheckedAt) >= authTTL
}

// redetect re-runs Detect alone against an entry whose catalog is still exact.
//
// A failure keeps the previous availability untouched and records the error.
// That is the T4.22 rule applied to the field this TTL exists for: a Windows
// deadline is `TerminateProcess(pid, 1)`, and turning that into "not
// authenticated" is a false accusation against a logged-in account — worse
// than the stale value the refresh was trying to improve on. The clock is
// stamped either way, so a persistently failing probe costs one subprocess per
// authTTL rather than one per request.
func redetect(ctx context.Context, a Adapter, prev CatalogEntry, now time.Time) CatalogEntry {
	e := prev
	e.AuthCheckedAt = now
	av, err := a.Detect(ctx)
	if err != nil {
		e.AuthError = err.Error()
		return e
	}
	e.AuthError = ""
	if av.LoggedIn == nil {
		// Detect answered but declined to say. Same rule, softer failure:
		// keep the answer we had rather than downgrade it to "unknown".
		av.LoggedIn = prev.Availability.LoggedIn
	}
	e.Availability = av
	return e
}

// identity resolves the binary identity without spawning it.
func identity(a Adapter) catalogKey {
	p, err := a.Path()
	if err != nil {
		return catalogKey{}
	}
	fi, err := os.Stat(p)
	if err != nil {
		return catalogKey{path: p}
	}
	return catalogKey{path: p, mtime: fi.ModTime().UnixNano()}
}

// probe runs the full Detect+Options pass. Failures degrade: availability
// carries its own error, and an Options error lands in ProbeError while the
// returned catalog is still served (curated fallback, §9.6).
func probe(ctx context.Context, a Adapter, now time.Time) CatalogEntry {
	e := CatalogEntry{ProbedAt: now, AuthCheckedAt: now}
	av, err := a.Detect(ctx)
	if err != nil {
		av = Availability{Error: err.Error()}
	}
	e.Availability = av
	opts, err := a.Options(ctx)
	if err != nil {
		e.ProbeError = err.Error()
	}
	e.Options = opts
	return e
}
