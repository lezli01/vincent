package clipimage

import (
	"context"
	"strings"
)

// imageTypes are the clipboard targets asked for, best first: PNG is what
// every screenshot tool offers, and the others are what a browser may offer
// alone.
var imageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// typesLimit bounds a helper's list of targets, which is a few lines of text.
const typesLimit = 64 << 10

// clipTool is one clipboard program: how to ask it which targets the
// clipboard offers, and how to ask it for one of them.
type clipTool struct {
	name  string
	types []string
	fetch func(mime string) []string
}

// clipTools is the order they are tried in: wl-paste first, because under
// Wayland xclip reaches the clipboard only through XWayland and may see a
// stale or empty one; xclip second, for X11 and for a Wayland session
// without wl-clipboard.
var clipTools = []clipTool{
	{
		name:  "wl-paste",
		types: []string{"--list-types"},
		fetch: func(mime string) []string { return []string{"--type", mime} },
	},
	{
		name:  "xclip",
		types: []string{"-selection", "clipboard", "-t", "TARGETS", "-o"},
		fetch: func(mime string) []string { return []string{"-selection", "clipboard", "-t", mime, "-o"} },
	},
}

// read asks each installed tool in turn. The targets are listed before
// anything is fetched: asking either tool for a target the clipboard does
// not offer is an error at best, and listing says which image format to ask
// for instead of guessing four times.
func read(ctx context.Context) ([]byte, error) {
	for _, tool := range clipTools {
		if !findHelper(tool.name) {
			continue
		}
		listed, err := runHelper(ctx, typesLimit, tool.name, tool.types...)
		if err != nil {
			continue
		}
		mime := pickImageType(string(listed))
		if mime == "" {
			continue
		}
		data, err := runHelper(ctx, MaxBytes, tool.name, tool.fetch(mime)...)
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, ErrNoImage
}

// pickImageType is the best image target in a tool's listing, one target a
// line, or "" when it lists none.
func pickImageType(listed string) string {
	offered := map[string]bool{}
	for _, line := range strings.Split(listed, "\n") {
		offered[strings.TrimSpace(line)] = true
	}
	for _, mime := range imageTypes {
		if offered[mime] {
			return mime
		}
	}
	return ""
}
