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

// CatalogEntry is one adapter's cached probe result (spec §9.6): the §9.5
// availability plus the merged option catalog.
type CatalogEntry struct {
	Availability Availability
	Options      Options
	ProbedAt     time.Time
	ProbeError   string // "" = clean probe
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
	e := CatalogEntry{ProbedAt: now}
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
