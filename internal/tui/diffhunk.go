package tui

import (
	"strconv"
	"time"
)

// The hunk level of the diff pane (task 169 decisions 4 and 9). The pane folds
// by lane and file (diffgroup.go); under a file sit its hunks, and a second
// cursor walks those — across files, in row order — so a reader steps through
// a change one `@@` at a time instead of paging through it.
//
// Nothing here binds a key. The task workspace's Diff tab and the chat's diff
// pane drive these methods, and the chat's pane also feeds the renderer
// structured files (setFiles) whose hunks carry the daemon's ids and review
// marks. The view is unified only; side by side is deliberately not built.

// diffHunkSelected marks the hunk under the hunk cursor. A glyph, as the fold
// marks are: the selected style is a background, and a background alone does
// not survive NO_COLOR (§15 Colour).
const diffHunkSelected = "▶ "

// diffHunkAccepted marks a hunk the reader accepted, for the same reason: the
// dimming says it too, and dimming alone is colour.
const diffHunkAccepted = "✓ "

// diffFileHunk is one hunk of a file: where its `@@` line sits in the file's body,
// with the lines up to the next hunk's start belonging to it.
type diffFileHunk struct {
	start int
	// id is the caller's handle on the hunk (setFiles); empty for a hunk
	// parsed out of a task's diff, which has only its position.
	id       string
	accepted bool
}

// diffHunkInput is one hunk of a structured file. body is the hunk's lines
// after its `@@` header.
type diffHunkInput struct {
	id, header string
	body       []string
	accepted   bool
}

// diffFileInput is one file handed to setFiles. note, when set, is a dimmed
// line under the file's row — why the file cannot be rejected, say.
type diffFileInput struct {
	path string
	// status is git's letter for what happened to the file (M, A, D or R),
	// drawn on its row as a fetched diff's is; 0 draws none.
	status         byte
	added, removed int
	binary         bool
	note           string
	hunks          []diffHunkInput
}

// diffHunkRef names the hunk under the cursor to a caller. index is the hunk's
// position in its file.
type diffHunkRef struct {
	path, id, header string
	index            int
	accepted         bool
}

// diffHunkCursor is the hunk cursor. Like the folds and the file cursor it is
// kept by identity — the file's key, and the hunk's id or, for a hunk with
// none, its index — so a refresh that re-parses the diff leaves it where it
// was. sec and file are that identity resolved against the current sections,
// redone only when the sections change (keepHunk), never per rebuild.
type diffHunkCursor struct {
	on        bool
	key, id   string
	index     int
	sec, file int
}

// reviewed is a file's reviewed/total hunk count, as its row shows it.
func (f *diffFile) reviewed() string {
	n := 0
	for _, h := range f.hunks {
		if h.accepted {
			n++
		}
	}
	return strconv.Itoa(n) + "/" + strconv.Itoa(len(f.hunks))
}

// hunkEnd is one past the last body line of hunk h.
func (f *diffFile) hunkEnd(h int) int {
	if h+1 < len(f.hunks) {
		return f.hunks[h+1].start
	}
	return len(f.body)
}

// fileIndent is what a file's row is indented by: a level under its lane when
// the lanes render, nothing otherwise.
func (p *diffPane) fileIndent() string {
	if p.grouped {
		return "  "
	}
	return ""
}

// setFiles replaces the content with one ungrouped section built from
// structured files — no task, no fetch — and switches review drawing on (task
// 169 decision 9). Folds, the file cursor and the hunk cursor are kept by
// identity, exactly as a task's refresh keeps them.
//
// A binary file and a file with a note keep whatever lines they were given but
// get no hunks: there is nothing in them for the cursor to walk or a mark to
// sit on.
func (p *diffPane) setFiles(files []diffFileInput, asOf time.Time) {
	p.fetching, p.err, p.loaded = false, nil, true
	p.asOf = asOf
	p.review, p.grouped, p.truncated = true, false, false
	p.lines = nil

	sec := diffSection{remainder: true}
	// The same cap a fetched diff gets, spent the same way: it bounds the
	// terminal, so the hunk that crosses it is cut and what follows is left
	// to the truncation notice.
	budget := maxDiffLines
	for _, in := range files {
		f := diffFile{path: in.path, status: in.status, added: in.added, removed: in.removed, binary: in.binary, note: in.note}
		walkable := !in.binary && in.note == ""
		for _, h := range in.hunks {
			if budget <= 0 {
				p.truncated = true
				break
			}
			if walkable {
				f.hunks = append(f.hunks, diffFileHunk{start: len(f.body), id: h.id, accepted: h.accepted})
			}
			body := h.body
			if len(body) > budget-1 {
				body = body[:budget-1]
				p.truncated = true
			}
			f.body = append(append(f.body, h.header), body...)
			budget -= 1 + len(body)
		}
		p.lines = append(p.lines, f.body...)
		sec.files = append(sec.files, f)
		sec.styledBody = append(sec.styledBody, colorDiffLines(f.body))
		sec.added += f.added
		sec.removed += f.removed
		if p.truncated {
			break
		}
	}
	p.sections = []diffSection{sec}
	for f := range sec.files {
		for h, hk := range sec.files[f].hunks {
			if hk.accepted {
				p.restyleHunk(0, f, h)
			}
		}
	}
	p.keepFolds()
}

// restyleHunk redoes the cached lines of one hunk after its mark changed:
// dimmed when accepted, coloured as a diff otherwise. Styling stays a per-load
// cost — a flip pays for its own hunk and nothing else.
func (p *diffPane) restyleHunk(sec, file, h int) {
	f := &p.sections[sec].files[file]
	styled := p.sections[sec].styledBody[file]
	for i := f.hunks[h].start + 1; i < f.hunkEnd(h); i++ {
		if f.hunks[h].accepted {
			styled[i] = styleDim.Render(f.body[i])
		} else {
			styled[i] = colorDiffLine(f.body[i])
		}
	}
}

// hunkHeaderRow draws one hunk's `@@` line. The marks are glyphs ahead of the
// header, through the icon helper so tui.icons modes keep working, and the
// styling only repeats what they say.
func hunkHeaderRow(header string, accepted, selected bool) string {
	mark := ""
	if accepted {
		mark = diffHunkAccepted
	}
	switch {
	case selected:
		return styleSelected.Render(ic(diffHunkSelected+mark) + header)
	case accepted:
		return styleDim.Render(ic(mark) + header)
	default:
		return styleDiffHunk.Render(header)
	}
}

// setHunk puts the hunk cursor on a hunk and records its identity.
func (p *diffPane) setHunk(sec, file, h int) {
	p.hunk = diffHunkCursor{
		on:  true,
		key: p.fileKey(sec, file), id: p.sections[sec].files[file].hunks[h].id,
		index: h, sec: sec, file: file,
	}
	p.built = false
}

// keepHunk carries the hunk cursor across new sections. The file is found by
// key and the hunk by id; a hunk with no id, or whose id is gone, keeps its
// index, clamped — so when the selected hunk went away the cursor is on what
// slid into its place. A file that went away takes the cursor with it.
func (p *diffPane) keepHunk() {
	if !p.hunk.on {
		return
	}
	was := p.hunk
	p.hunk = diffHunkCursor{}
	p.built = false
	for i := range p.sections {
		for f := range p.sections[i].files {
			if p.fileKey(i, f) != was.key {
				continue
			}
			hunks := p.sections[i].files[f].hunks
			if len(hunks) == 0 {
				return
			}
			idx := min(was.index, len(hunks)-1)
			if was.id != "" {
				for h := range hunks {
					if hunks[h].id == was.id {
						idx = h
						break
					}
				}
			}
			p.setHunk(i, f, idx)
			return
		}
	}
}

// hunkPos is where the hunk cursor stands, and false when it stands nowhere a
// reader can see: no hunk selected, or one inside a file or lane that has
// since been folded shut. A hidden selection is not acted on, and reopening
// the fold shows it again.
func (p *diffPane) hunkPos() (sec, file, h int, ok bool) {
	c := p.hunk
	if !c.on || !p.open[c.key] {
		return 0, 0, 0, false
	}
	if p.grouped && !p.open[p.sectionKey(c.sec)] {
		return 0, 0, 0, false
	}
	return c.sec, c.file, c.index, true
}

// selectedHunk is the hunk under the hunk cursor.
func (p *diffPane) selectedHunk() (diffHunkRef, bool) {
	sec, file, h, ok := p.hunkPos()
	if !ok {
		return diffHunkRef{}, false
	}
	f := &p.sections[sec].files[file]
	hk := f.hunks[h]
	return diffHunkRef{path: f.path, id: hk.id, header: f.body[hk.start], index: h, accepted: hk.accepted}, true
}

// moveHunk steps the hunk cursor: +1 to the next hunk, -1 to the previous,
// across files and lanes in row order. A folded file — and its lane — is
// opened when the cursor lands in it, the file cursor follows, and the hunk's
// header is scrolled into view. It reports false, and changes nothing, when
// there is no hunk that way.
//
// The walk starts from the hunk cursor while the file cursor is still on its
// file, and from the file cursor's row otherwise: a reader who walked the file
// list somewhere else means the hunks there, not the next one after a
// selection they left behind.
func (p *diffPane) moveHunk(delta int) bool {
	if delta == 0 || len(p.nodes) == 0 {
		return false
	}
	n := p.nodes[min(max(p.cursor, 0), len(p.nodes)-1)]
	sec, file, h := n.section, n.file, -1
	if hs, hf, hi, ok := p.hunkPos(); ok && hs == sec && hf == file {
		h = hi
	}
	sec, file, h, ok := p.stepHunk(sec, file, h, delta > 0)
	if !ok {
		return false
	}
	if p.grouped {
		p.open[p.sectionKey(sec)] = true
	}
	key := p.fileKey(sec, file)
	p.open[key] = true
	p.rebuildNodes()
	p.pinCursor(key)
	p.setHunk(sec, file, h)
	p.revealHunk = true
	return true
}

// stepHunk finds the hunk after or before a position. file is -1 for a lane's
// own row and h is -1 for a file's own row, both of which sort ahead of what
// is under them. Files without hunks — binary, noted — are stepped over.
func (p *diffPane) stepHunk(sec, file, h int, forward bool) (s, f, hunk int, ok bool) {
	if forward {
		for si := sec; si < len(p.sections); si++ {
			files := p.sections[si].files
			fi := 0
			if si == sec {
				fi = max(file, 0)
			}
			for ; fi < len(files); fi++ {
				hi := 0
				if si == sec && fi == file {
					hi = h + 1
				}
				if hi < len(files[fi].hunks) {
					return si, fi, hi, true
				}
			}
		}
		return 0, 0, 0, false
	}
	for si := sec; si >= 0; si-- {
		files := p.sections[si].files
		fi := len(files) - 1
		if si == sec {
			fi = file
		}
		for ; fi >= 0; fi-- {
			hi := len(files[fi].hunks) - 1
			if si == sec && fi == file {
				hi = h - 1
			}
			if hi >= 0 {
				return si, fi, hi, true
			}
		}
	}
	return 0, 0, 0, false
}

// setHunkAccepted flips one hunk's review mark in place, without a refetch,
// and reports whether there is such a hunk. Only structured files carry ids,
// so on a task's diff there is never one.
func (p *diffPane) setHunkAccepted(path, id string, accepted bool) bool {
	if !p.review {
		return false
	}
	for s := range p.sections {
		for f := range p.sections[s].files {
			file := &p.sections[s].files[f]
			if file.path != path {
				continue
			}
			for h := range file.hunks {
				if file.hunks[h].id != id {
					continue
				}
				if file.hunks[h].accepted != accepted {
					file.hunks[h].accepted = accepted
					p.restyleHunk(s, f, h)
					p.built = false
				}
				return true
			}
		}
	}
	return false
}
