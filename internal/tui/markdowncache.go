package tui

import "crypto/sha256"

// The rendered-document memo (#291).
//
// Every live chunk marks the pane dirty, and the rebuild re-rendered every
// record — up to maxRecords of them — at the daemon's ~10 Hz coalescing rate
// (§13.3). Keying the render on the document instead means a chunk re-renders
// the one document it extended: O(document) rather than O(pane), which is the
// honest cost of reading a run of records as one document.
//
// The key is the source's digest plus every input that changes the result:
// the pane width, the verbosity level and the raw toggle. The issue's "theme"
// and "completion state" are not in it — this TUI has no theme concept, and
// with whole records there is no partial state to key on. There is no
// client-side throttle and no second timer either; the daemon already
// coalesces.
type mdCacheKey struct {
	digest [sha256.Size]byte
	width  int
	level  outputLevel
	raw    bool
}

type mdCacheEntry struct {
	lines  []string
	blocks []int
	gen    uint64
}

// mdCache is one pane's memo. It is swept per render pass rather than bounded
// by count: what a pane can hold is already bounded by maxRecords, and an
// entry no pass touched belongs to a document that is no longer on screen.
type mdCache struct {
	entries map[mdCacheKey]mdCacheEntry
	gen     uint64
	// renders counts the renders that actually ran, which is what the tests
	// assert on. Counting renders rather than timing them is the only way to
	// state the property without making the suite a benchmark.
	renders int
}

// begin opens a render pass.
func (c *mdCache) begin() {
	if c == nil {
		return
	}
	c.gen++
}

// sweep closes one, dropping what the pass did not touch.
func (c *mdCache) sweep() {
	if c == nil {
		return
	}
	for k, e := range c.entries {
		if e.gen != c.gen {
			delete(c.entries, k)
		}
	}
}

// lines renders one document, from the memo when it can. A nil cache renders
// every time, which is what the call sites that have no pane behind them want.
func (c *mdCache) lines(text string, width int, level outputLevel, raw bool) ([]string, []int) {
	if c == nil {
		return assistantBlockLines(text, width, raw)
	}
	key := mdCacheKey{digest: sha256.Sum256([]byte(text)), width: width, level: level, raw: raw}
	if e, ok := c.entries[key]; ok {
		e.gen = c.gen
		c.entries[key] = e
		return e.lines, e.blocks
	}
	lines, blocks := assistantBlockLines(text, width, raw)
	c.renders++
	if c.entries == nil {
		c.entries = make(map[mdCacheKey]mdCacheEntry, 8)
	}
	c.entries[key] = mdCacheEntry{lines: lines, blocks: blocks, gen: c.gen}
	return lines, blocks
}
