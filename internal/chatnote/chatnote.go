package chatnote

import "strings"

// Heading opens the block.
const Heading = "The user reverted these hunks; do not reapply them:"

// Line is one rejected hunk's line of the block: "- <path> <@@ header>".
func Line(path, header string) string {
	// A hunk of a CRLF file can carry the line's CR on its header.
	return "- " + path + " " + strings.TrimRight(header, "\r")
}

// Add puts line into the block at the very top of draft: the heading, one
// line per hunk, a blank line, then what the draft held. A draft that already
// starts with the heading gets the line appended to the end of that block;
// one the user edited so it no longer does gets a new block.
func Add(draft, line string) string {
	rest, ok := strings.CutPrefix(draft, Heading)
	if !ok || (rest != "" && rest[0] != '\n') {
		return Heading + "\n" + line + "\n\n" + draft
	}
	// end is where the block stops: the index of the "\n" ending its last
	// line, or the draft's end.
	end := len(Heading)
	for end < len(draft) {
		next := draft[end+1:]
		if !strings.HasPrefix(next, "- ") {
			break
		}
		nl := strings.IndexByte(next, '\n')
		if nl < 0 {
			end = len(draft)
			break
		}
		end += 1 + nl
	}
	return draft[:end] + "\n" + line + draft[end:]
}
