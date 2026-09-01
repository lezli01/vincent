package tui

import (
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The assistant document model (#291).
//
// Task 073 read one `agent.output` record as one Markdown document. That is
// wrong for a message the adapter split across records: consecutive records
// already render with no blank line between them, so they *look* like one
// message, and a table, list or fence spanning two of them parsed as two
// broken documents. Here a run of consecutive `agent.output` records is one
// document instead.
//
// What closes a document is any other record — reasoning, tool use, tool
// result, command output, `agent.raw`, a result line — and a run or turn
// boundary, which falls out for free because the task pane renders one
// attempt's records and the chat renders one turn's. The rule is deliberately
// independent of the verbosity level: a level that hides a record must not
// also change how the prose around it parses.
//
// This is a parse-scope change. The blank-line rule between a document and a
// record that is not prose is untouched, which is what keeps the pane's shape
// the same everywhere a message was never split.
//
// What is **not** here is the stable-prefix classifier #291 proposed. This
// wire never carries a partial Markdown document: claude runs message-level
// `stream-json` with no `--include-partial-messages` (the T1.7 decision,
// `docs/history/v0-tasks.md:142`), codex emits `agent_message` only on
// `item.completed`, and cursor delivers content blocks whole. A record is
// present whole or is not present, so there is no unfinished tail to
// classify. Joining does reintroduce a growing tail at *record* granularity,
// and the bound this file guarantees instead is the weaker, true one: a
// record boundary is a message boundary, the parse is deterministic from the
// accumulated source, and nothing above the last block of the previous
// document moves when a record arrives.

// assistantDoc is one run of consecutive `agent.output` records, read as a
// single Markdown document.
type assistantDoc struct {
	// seq is the document's identity: the client-assigned identity of its
	// first record. It is not an index — see seqStamp — so it survives the
	// maxRecords front-prune.
	seq  int64
	text string
	// first and last bound the run in the record window it was derived
	// from. They are render-time positions and are not identity.
	first, last int
}

// assistantDocs splits a record window into the documents it contains, in
// order. seqs is the window's parallel identity slice; a caller that has none
// (a test rendering a literal window) passes nil and gets positional
// identities, which is enough for one render but not across a prune.
func assistantDocs(recs []apiclient.TranscriptRecord, seqs []int64) []assistantDoc {
	out := make([]assistantDoc, 0, 4)
	for i := 0; i < len(recs); i++ {
		if recs[i].Type != recTypeOutput {
			continue
		}
		j := i
		var b strings.Builder
		for j < len(recs) && recs[j].Type == recTypeOutput {
			if j > i {
				// A record ends at a line: joining with a newline is what
				// reconstructs the source a splitting adapter started with,
				// so a table header in one record and its delimiter row in
				// the next parse as the one table they were written as.
				b.WriteByte('\n')
			}
			b.WriteString(recs[j].Text)
			j++
		}
		out = append(out, assistantDoc{seq: seqAt(seqs, i), text: b.String(), first: i, last: j - 1})
		i = j - 1
	}
	return out
}

// recTypeOutput is the one record type ever read as Markdown (task 073
// decision 5).
const recTypeOutput = "agent.output"

// seqStamp hands out the client-side identities records arrive without.
//
// apiclient.TranscriptRecord carries no id and no offset, and #291 changes no
// wire format, so identity is assigned on ingest instead. It is monotonic and
// never an index into the record slice: the whole point is that it survives
// the maxRecords front-prune, which shifts every index down.
type seqStamp struct{ next int64 }

// take returns n fresh identities.
func (s *seqStamp) take(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		s.next++
		out[i] = s.next
	}
	return out
}

// fit returns seqs stamped to cover exactly n records. The common paths stamp
// on ingest and this is a no-op; a window installed by any other route — a
// test, a path that has not been through applyTranscript — is stamped whole on
// first use, which costs that window its anchor and nothing else.
func (s *seqStamp) fit(seqs []int64, n int) []int64 {
	if len(seqs) == n {
		return seqs
	}
	return s.take(n)
}

// seqAt is the identity of record i, or a positional stand-in when the caller
// passed no identities at all.
func seqAt(seqs []int64, i int) int64 {
	if i < len(seqs) {
		return seqs[i]
	}
	return int64(i) + 1
}

// lineAnchor is what one rendered pane line came from, which is what lets a
// paused reader keep their place across a rebuild.
//
// Four things move a paused reader today, and none of them is streaming
// reflow: a resize re-wraps every line, a front-prune shifts them all up, and
// the verbosity and raw toggles rebuild the pane wholesale. Capturing the
// topmost visible block and restoring it afterwards fixes all four. Follow
// mode is untouched — it keeps the bottom anchor.
type lineAnchor struct {
	// rec is the identity of the record the line came from; for assistant
	// prose it is the document's seq, which is its first record's. Zero
	// means the line belongs to no record (a truncation note, an
	// unrecognized-line count) and cannot be anchored to.
	rec int64
	// block is the ordinal of the Markdown block within the document, and 0
	// for every other line.
	block int
	// off is the line's ordinal within that block.
	off int
}

// anchorAt is the anchor of the line at the given viewport offset.
func anchorAt(anchors []lineAnchor, y int) lineAnchor {
	if y < 0 || y >= len(anchors) {
		return lineAnchor{}
	}
	return anchors[y]
}

// anchorIndex finds where a captured anchor landed in a freshly built pane.
//
// It resolves in three tiers, which is what makes it survive a rebuild that
// changed the block structure rather than only the wrapping: the exact line,
// then the block's first line, then the document's first line. The last tier
// is what carries a paused reader across the raw toggle, where the rendered
// blocks a captured ordinal names do not exist.
func anchorIndex(anchors []lineAnchor, want lineAnchor) (int, bool) {
	if want.rec == 0 {
		return 0, false
	}
	block, doc := -1, -1
	for i, a := range anchors {
		if a.rec != want.rec {
			continue
		}
		if doc < 0 {
			doc = i
		}
		if a.block != want.block {
			continue
		}
		if block < 0 {
			block = i
		}
		if a.off == want.off {
			return i, true
		}
	}
	switch {
	case block >= 0:
		return block, true
	case doc >= 0:
		return doc, true
	}
	return 0, false
}
