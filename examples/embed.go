// Package examples embeds the example workflows this repository ships, so
// `vincent workflow init --from <example>` can hand one over from any working
// directory — no checkout, no daemon, no network.
//
// It lives here rather than under internal/ because `go:embed` reads only
// from the embedding package's own directory tree: a package elsewhere cannot
// name `examples/feature-pr.yaml` at all. That constraint is the point — the
// embed reads the very files the repository publishes and the release archive
// ships (`.goreleaser.yaml` globs `examples/*.yaml`), so an embedded copy
// cannot drift from the documented one. It is the same reason `skills/embed.go`
// sits where it does.
//
// Nothing but the embedded files belongs in it: it depends on no other package
// in this module and must stay that way, so any package may import it.
package examples

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.yaml
var files embed.FS

// Ext is the extension every example carries, and the one an installed copy
// keeps. It is exported so a caller naming a destination file does not have
// to spell the string a second time.
const Ext = ".yaml"

// Names lists the examples by the name `--from` accepts — the file name
// without its extension — sorted. It is derived from the embedded filesystem
// rather than from a list kept beside it: a hardcoded list would silently
// fail to offer a newly added example and would drift from the directory.
func Names() []string {
	des, err := fs.ReadDir(files, ".")
	if err != nil {
		// Reading an embed.FS root cannot fail — the tree is fixed at build
		// time — so there is no error worth propagating to every caller.
		return nil
	}
	out := make([]string, 0, len(des))
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		out = append(out, strings.TrimSuffix(de.Name(), Ext))
	}
	sort.Strings(out)
	return out
}

// Read returns one example's source verbatim, by the name Names reports.
// The bytes are the file as published, comments included: the comments are
// most of what makes an example worth copying.
func Read(name string) ([]byte, error) {
	// Only a bare name is a name. Rejecting a separator here keeps a value
	// that arrived from a command line from naming anything but an example,
	// even though an embed.FS has nothing above its root to reach.
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return nil, fmt.Errorf("unknown example %q", name)
	}
	src, err := files.ReadFile(name + Ext)
	if err != nil {
		return nil, fmt.Errorf("unknown example %q", name)
	}
	return src, nil
}
