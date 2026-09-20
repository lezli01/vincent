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
	// Skills and Problems are exactly what the served clean probe returned:
	// the CLI's order, duplicates kept, nothing synthesized. nil when none.
	Skills   []Skill
	Problems []SkillProblem
	// ProbedAt is when the served clean answer was obtained. Zero when there
	// is none.
	ProbedAt time.Time
	// ProbeError is the latest probe's error text when that probe failed.
	// "" when it answered.
	ProbeError string
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
	return &SkillCache{now: time.Now, slots: make(map[skillKey]*skillSlot)}
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
	s := c.slot(skillKey{name: a.Name(), bin: identity(a), dir: dir})
	if !refresh {
		if ans, fresh := s.fresh(c.now()); fresh {
			return ans
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
		return ans
	}
	if !refresh {
		if ans, fresh := s.fresh(c.now()); fresh {
			return ans
		}
	}
	return c.probe(ctx, lister, s)
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
