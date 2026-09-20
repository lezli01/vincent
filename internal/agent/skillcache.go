package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// skillTTL is how long a *clean* skill listing is trusted (§9.6, task 124,
// 124.9, #505, settling the task's open question 7) — a list, or
// ErrSkillsUnsupported's positive no about the build.
//
// Binary identity is in the key, but unlike help output a listing is not a
// pure function of the binary: a person adds a skill to `.claude/skills` or
// installs a plugin and nothing about the CLI changes. So a TTL is what
// expires an answer about the directory, the argument authTTL makes for
// `logged_in`, and five minutes is authTTL's number for authTTL's reason: a
// chat view, `vincent chat skills` and a composer asking in the same second
// cost one probe, and a human who changes something and looks again is told
// the truth. A turn is the one change vincent sees happen, and the chat runner
// invalidates the directory when one ends; `?refresh=true` bypasses the TTL
// outright.
//
// There is no config key, because no other §9.6 TTL has one.
const skillTTL = 5 * time.Minute

// skillFailureTTL is how long a *failed* listing probe is trusted (§9.6,
// task 124, 124.9, #505, settling the task's open question 7).
//
// It is failureTTL's number for failureTTL's reason (T4.22): a probe that
// timed out once must not be served for the life of the daemon against a CLI
// that is healthy now, yet a burst of requests must not cost a subprocess
// each. A minute survives the burst and not the user looking again. There is
// no config key, for the same reason skillTTL has none.
const skillFailureTTL = time.Minute

// skillCacheMax bounds how many keys the cache holds (§9.6, task 124, 124.9,
// #505). The directory is in the key and worktrees come and go for the life of
// the daemon, so without a bound the cache would grow with every chat and
// task ever listed.
//
// It is not coupled to `max_parallel_chats`: a probe is not a chat turn and
// holds no slot, so how many turns may run at once says nothing about how
// many directories are being looked at. Sixty-four is far more directories
// than anyone watches at once, and a key costs one listing. There is no config
// key, for the same reason the TTLs have none.
const skillCacheMax = 64

// bundledMax bounds how many binaries' bundled-skill sets the cache holds
// (§9.6, task 124.16, #512). A set is keyed by binary identity alone — path
// plus mtime — so it grows only when a person installs another agent CLI or
// upgrades one, which is far slower than worktrees come and go. It is
// skillCacheMax's number because it is skillCacheMax's kind of bound: a
// ceiling nothing reaches in practice, there so that nothing can grow without
// limit for the life of the daemon. A set costs a few dozen short names.
const bundledMax = skillCacheMax

// BundledState says what became of the `builtin` rows a served listing held
// (§9.6, §5.5, task 124.16, #512). claude marks its bundled skills
// (`simplify`, `loop`, `run`) and its built-in commands (`clear`, `compact`)
// alike, and only a turn's init line separates the two — so before any turn
// has run on a binary, the whole set is withheld rather than risk offering
// `/clear`, which resets a conversation vincent still shows the history of.
type BundledState string

// Bundled states (task 124.16, #512).
const (
	// BundledNotApplicable is the question not arising: a listing that is not
	// a list at all, or one holding no `builtin` row — which is every codex
	// and cursor listing, neither CLI having such a concept.
	BundledNotApplicable BundledState = ""
	// BundledListed is a turn on this binary having named its skills, so the
	// `builtin` rows it named are served and the rest are still dropped.
	BundledListed BundledState = "listed"
	// BundledAfterFirstTurn is a listing holding `builtin` rows that no turn
	// on this binary has classified yet. They are withheld, and this says the
	// first turn will bring them back — in any directory and any chat, since
	// what is bundled is a property of the installed CLI, not of a place.
	BundledAfterFirstTurn BundledState = "after_first_turn"
)

// SkillAnswer is what the skill cache knows about one (adapter, binary,
// directory), in the shape the chat skills route serves (§9.6, §13.2,
// task 124, 124.9, #505).
//
// Its slices are shared with the cache and with every other caller served
// the same answer: read them, never modify them.
type SkillAnswer struct {
	// Verdict: InputSupported (Skills is a real list, possibly kept from an
	// earlier probe, see ProbeError), InputUnsupported (a positive no), or
	// InputUnknown (no clean answer yet, and the last probe failed).
	Verdict InputVerdict
	// Reason is ErrSkillsUnsupported's wrapped error text (err.Error()) when
	// that is the answer being served. Otherwise "".
	Reason string
	// Skills and Problems are what the served clean probe returned: the
	// CLI's order, duplicates kept, nothing synthesized. nil when none.
	//
	// The one thing removed is a `builtin` row this binary's turns have not
	// named — see Bundled and SkillCache.serve. Nothing is ever added or
	// reordered.
	Skills   []Skill
	Problems []SkillProblem
	// ProbedAt is when the served clean answer was obtained. Zero when there
	// is none.
	ProbedAt time.Time
	// ProbeError is the latest probe's error text when that probe failed.
	// "" when it answered.
	ProbeError string
	// Bundled says what became of the probe's `builtin` rows: served,
	// withheld until the first turn, or no such row at all (task 124.16).
	// Skills is already filtered accordingly — this only says why.
	Bundled BundledState
}

// SkillCache caches what SkillLister.ListSkills reports for an (adapter,
// binary, directory), and is how the daemon consumes SkillLister (§9.1, §9.6,
// task 124 decision 5, 124.9, #505): the chat skills route serves every
// listing from it, and the chat runner invalidates a directory when a turn
// ends there. It is in memory only, like the catalog cache it copies: a list
// nothing stores has no row to migrate and no event to publish.
//
// It follows CatalogCache's rules wherever they apply:
//
//   - **Key.** The adapter's name, its binary identity (catalogKey: resolved
//     path plus mtime, found without spawning anything) and the cleaned
//     directory. An upgraded CLI is a new key, so it is asked at once rather
//     than after the TTL.
//   - **TTLs.** skillTTL for a clean answer, skillFailureTTL for a failed
//     probe; refresh bypasses both.
//   - **Single flight.** Each key's probes are serialized, and a caller queued
//     behind a probe that finished after it arrived is served that probe's
//     answer rather than probing again, refresh or not. A reader that finds a
//     fresh answer never waits behind a running probe.
//   - **A failure keeps the previous answer** (T4.22). A failed probe records
//     its error beside the last clean answer, never in place of it, and only
//     ErrSkillsUnsupported is ever read as a no.
//   - **Bounded.** skillCacheMax keys, least recently used evicted first.
//
// Beside the listings it holds one more thing, on its own key: per *binary*
// identity, the skill names a turn's init line reported (ReportBundled, task
// 124.16). That is the only signal separating claude's bundled skills from
// its built-in commands, both of which its listing marks `builtin`; a row so
// marked is served only once a turn has named it. The registry is in memory
// like the rest, bounded at bundledMax, and nothing about it is persisted or
// published.
//
// The cache spawns nothing itself: a probe is the adapter's ListSkills, run
// on the host with the daemon's environment. A probe is not a chat turn
// either — it takes no `max_parallel_chats` slot, touches no store row and
// emits no event.
type SkillCache struct {
	now func() time.Time

	// probes numbers every stored probe, cache-wide. A Lookup reads it before
	// anything else, so "a probe that finished after this caller arrived" is
	// one comparison against the number its slot's last store took. It is
	// cache-wide rather than per slot because the slot is only known once the
	// key's binary identity has been resolved, and arrival is the call's start.
	probes atomic.Uint64

	mu    sync.Mutex
	slots map[skillKey]*skillSlot
	// tick orders slots by recency: a slot's used is the tick of the last
	// Lookup that asked for it or the last probe that stored into it.
	tick uint64

	// bundled is the classification half (task 124.16, #512): per binary
	// identity, the skill names a turn's init line reported. Its own mutex
	// because it is written by chat turns and read by lookups, on paths that
	// share nothing else — a report must never queue behind an eviction scan.
	bundledMu   sync.Mutex
	bundledSets map[catalogKey]*bundledSet
	bundledTick uint64
}

// bundledSet is one binary's reported skill names (task 124.16). used orders
// the sets by recency the way skillSlot.used orders the slots.
type bundledSet struct {
	key   catalogKey
	used  uint64
	names map[string]bool
}

// skillKey is what one cached listing is an answer about (§9.6, task 124
// decision 5). A listing is a function of the directory as well as of the
// binary — codex's `skills/list` is per cwd — which is why this cache is not
// the catalog cache's per-adapter slot.
type skillKey struct {
	name string
	bin  catalogKey
	dir  string // filepath.Clean'd
}

// skillSlot is one key's cache line. probeMu serializes probes so concurrent
// requests never double-spawn; dataMu guards the answer so a reader never
// waits behind a running probe — catalogSlot's split, for catalogSlot's
// reason.
//
// A slot evicted or invalidated while a probe runs is only unlinked from the
// cache: the probe still stores into it, and the callers already holding it
// are served from it, but no later Lookup can find it. That is what keeps an
// answer begun before an Invalidate from surviving it, with nothing left
// behind to grow as worktrees churn.
type skillSlot struct {
	key  skillKey
	used uint64 // guarded by SkillCache.mu

	probeMu sync.Mutex

	dataMu    sync.RWMutex
	valid     bool      // a probe has stored here
	checkedAt time.Time // when the last stored probe returned
	answer    SkillAnswer
	stored    uint64 // SkillCache.probes as of the last store
}

// NewSkillCache returns an empty cache. It spawns nothing until asked.
func NewSkillCache() *SkillCache {
	return &SkillCache{
		now:         time.Now,
		slots:       make(map[skillKey]*skillSlot),
		bundledSets: make(map[catalogKey]*bundledSet),
	}
}

// Lookup answers which skills a's CLI would load for a run in workDir.
//
// A fresh answer is served as it stands; otherwise, or when refresh is set,
// the adapter is asked, unless a probe for the same key finished after this
// call began, whose answer is served instead. An adapter that does not
// implement SkillLister answers InputUnsupported with no Reason and is never
// asked: the route words that case itself, and normally never gets here with
// one.
func (c *SkillCache) Lookup(ctx context.Context, a Adapter, workDir string, refresh bool) SkillAnswer {
	lister, ok := a.(SkillLister)
	if !ok {
		return SkillAnswer{Verdict: InputUnsupported}
	}
	// Marked before the key is resolved: arrival is the call's start.
	arrived := c.probes.Load()
	dir := filepath.Clean(workDir)
	bin := identity(a)
	s := c.slot(skillKey{name: a.Name(), bin: bin, dir: dir})
	if !refresh {
		if ans, fresh := s.fresh(c.now()); fresh {
			return c.serve(bin, ans)
		}
	}
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	s.dataMu.RLock()
	joined := s.stored > arrived
	s.dataMu.RUnlock()
	if joined {
		// The probe this caller queued behind answered it, refresh or not:
		// N refreshes arriving together cost one subprocess.
		ans, _ := s.fresh(c.now())
		return c.serve(bin, ans)
	}
	if !refresh {
		if ans, fresh := s.fresh(c.now()); fresh {
			return c.serve(bin, ans)
		}
	}
	return c.serve(bin, c.probe(ctx, lister, s))
}

// ReportBundled records the skill names a turn on bin's CLI loaded, which is
// what tells that binary's bundled skills from its built-in commands (§9.6,
// task 124.16, #512). The chat runner calls it with a turn's init line; an
// adapter whose stream carries no such line — codex, cursor — never does.
//
// The classification is stored against the *binary* and not the directory: a
// bundled skill ships with the installed CLI, so the first turn anywhere
// restores the rows everywhere, and a chat created a moment ago is not
// penalised for being new. It does not touch the listings themselves, which
// the turn's ending invalidates unconditionally either way.
//
// Safe on a nil receiver, the way Invalidate is. An empty names is ignored:
// it is a build that sends no `skills` array, not a CLI claiming it bundles
// nothing.
func (c *SkillCache) ReportBundled(a Adapter, names []string) {
	if c == nil || a == nil || len(names) == 0 {
		return
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	bin := identity(a)
	c.bundledMu.Lock()
	defer c.bundledMu.Unlock()
	c.bundledTick++
	if b, ok := c.bundledSets[bin]; ok {
		b.used, b.names = c.bundledTick, set
		return
	}
	if len(c.bundledSets) >= bundledMax {
		// A linear scan over a map this small, for skillCache.slot's reason.
		var oldest *bundledSet
		for _, b := range c.bundledSets {
			if oldest == nil || b.used < oldest.used {
				oldest = b
			}
		}
		delete(c.bundledSets, oldest.key)
	}
	c.bundledSets[bin] = &bundledSet{key: bin, used: c.bundledTick, names: set}
}

// serve filters a stored answer's `builtin` rows down to the ones a turn on
// this binary named, and says which situation the caller is in (task 124.16,
// #512).
//
// It runs at serve time rather than at probe time so that a directory probed
// before the first turn is not frozen into its unclassified answer for a
// whole skillTTL: the same stored listing answers `after_first_turn` now and
// serves its bundled rows the moment a turn reports them.
//
// ans is a copy, and the filtered Skills is a fresh slice: the cache's own
// slice is shared with every other caller and is never modified.
func (c *SkillCache) serve(bin catalogKey, ans SkillAnswer) SkillAnswer {
	builtins := 0
	for _, s := range ans.Skills {
		if s.Builtin {
			builtins++
		}
	}
	if builtins == 0 {
		// Every codex and cursor listing, and every claude one from a build
		// that marks nothing: there is no question to answer.
		return ans
	}
	names, known := c.bundledNames(bin)
	ans.Bundled = BundledAfterFirstTurn
	if known {
		ans.Bundled = BundledListed
	}
	kept := make([]Skill, 0, len(ans.Skills))
	for _, s := range ans.Skills {
		if s.Builtin && !names[s.Name] {
			continue
		}
		kept = append(kept, s)
	}
	ans.Skills = nilIfEmpty(kept)
	return ans
}

// bundledNames is bin's reported set, and whether any turn has reported one.
// The returned map is the stored one: read it, never write it — ReportBundled
// replaces a set rather than mutating it, so a reader holding an old one
// still sees a consistent answer.
func (c *SkillCache) bundledNames(bin catalogKey) (map[string]bool, bool) {
	c.bundledMu.Lock()
	defer c.bundledMu.Unlock()
	b, ok := c.bundledSets[bin]
	if !ok {
		return nil, false
	}
	c.bundledTick++
	b.used = c.bundledTick
	return b.names, true
}

// Invalidate drops every entry for workDir, across adapter names and binary
// identities (§9.6, task 124, 124.9, #505). A probe already running for it
// still answers its own callers, but stores nothing a later Lookup can see,
// so the next Lookup probes again. Safe on a nil receiver, where it does
// nothing.
func (c *SkillCache) Invalidate(workDir string) {
	if c == nil {
		return
	}
	dir := filepath.Clean(workDir)
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.slots {
		if k.dir == dir {
			delete(c.slots, k)
		}
	}
}

// slot returns k's slot, creating it — and evicting the least recently used
// slot when the cache is full — if there is none. Either way k becomes the
// most recently used key.
func (c *SkillCache) slot(k skillKey) *skillSlot {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick++
	if s, ok := c.slots[k]; ok {
		s.used = c.tick
		return s
	}
	if len(c.slots) >= skillCacheMax {
		// A linear scan, not a list: at sixty-four slots it costs less than
		// the path resolution and stat every Lookup already pays for.
		var oldest *skillSlot
		for _, s := range c.slots {
			if oldest == nil || s.used < oldest.used {
				oldest = s
			}
		}
		delete(c.slots, oldest.key)
	}
	s := &skillSlot{key: k, used: c.tick, answer: SkillAnswer{Verdict: InputUnknown}}
	c.slots[k] = s
	return s
}

// touch marks s most recently used, if it is still in the cache. A slot
// evicted or invalidated while its probe ran stays out.
func (c *SkillCache) touch(s *skillSlot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slots[s.key] == s {
		c.tick++
		s.used = c.tick
	}
}

// probe asks the adapter and stores what it said. The caller holds
// s.probeMu.
//
// A list, and ErrSkillsUnsupported wrapped or not, are clean answers about
// this binary and replace whatever was there. Any other error is a failed
// probe: it is recorded beside the previous clean answer, which is kept
// exactly as it was (T4.22), and never read as a no. A failure whose caller
// has gone away is that caller's alone and is not stored — otherwise one
// client hanging up would have every other one told "failed" for a minute.
func (c *SkillCache) probe(ctx context.Context, lister SkillLister, s *skillSlot) SkillAnswer {
	list, err := lister.ListSkills(ctx, SkillQuery{WorkDir: s.key.dir})
	now := c.now()
	s.dataMu.Lock()
	next := s.answer
	switch {
	case err == nil:
		next = SkillAnswer{
			Verdict:  InputSupported,
			Skills:   nilIfEmpty(list.Skills),
			Problems: nilIfEmpty(list.Problems),
			ProbedAt: now,
		}
	case errors.Is(err, ErrSkillsUnsupported):
		next = SkillAnswer{Verdict: InputUnsupported, Reason: err.Error(), ProbedAt: now}
	default:
		next.ProbeError = err.Error()
		if ctx.Err() != nil {
			s.dataMu.Unlock()
			return next
		}
	}
	s.valid, s.checkedAt, s.answer = true, now, next
	s.stored = c.probes.Add(1)
	s.dataMu.Unlock()
	c.touch(s)
	return next
}

// fresh returns s's answer and whether it is still trusted at now: while
// checkedAt plus the TTL for the last probe's outcome is after now.
func (s *skillSlot) fresh(now time.Time) (SkillAnswer, bool) {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	if !s.valid {
		return s.answer, false
	}
	ttl := skillTTL
	if s.answer.ProbeError != "" {
		ttl = skillFailureTTL
	}
	return s.answer, s.checkedAt.Add(ttl).After(now)
}

// nilIfEmpty keeps "none" one value on the wire, whether the CLI reported an
// empty list or no list.
func nilIfEmpty[T any](v []T) []T {
	if len(v) == 0 {
		return nil
	}
	return v
}
