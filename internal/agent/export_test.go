package agent

import "time"

// This file exports internals to the external agent_test package, which is
// where a test must live to use agenttest's stubs: agenttest imports agent,
// so a test inside package agent cannot import it back.

// The skill cache's fixed numbers (§9.6, task 124, 124.9, #505), so a test
// moves its clock by the constant rather than by a copy of it.
const (
	SkillTTL        = skillTTL
	SkillFailureTTL = skillFailureTTL
	SkillCacheMax   = skillCacheMax
	BundledMax      = bundledMax
)

// BundledSetCount is how many binaries' bundled-skill sets the cache holds
// (task 124.16), so a test can prove the registry is bounded without
// exporting the registry itself.
func BundledSetCount(c *SkillCache) int {
	c.bundledMu.Lock()
	defer c.bundledMu.Unlock()
	return len(c.bundledSets)
}

// SetSkillCacheClock replaces the cache's clock, as catalog_test.go replaces
// CatalogCache.now. Call it before the cache is shared.
func SetSkillCacheClock(c *SkillCache, now func() time.Time) { c.now = now }
