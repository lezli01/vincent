package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	latestName = "latest"
	devName    = "dev"
)

// version is one built tree: its URL segment, the label the selector shows
// for it, and every file it publishes, as slash-separated paths relative to
// its root.
type version struct {
	name  string
	label string
	dir   string
	files map[string]bool
}

func (v *version) has(rel string) bool { return v.files[rel] }

func (v *version) sortedFiles() []string {
	out := make([]string, 0, len(v.files))
	for f := range v.files {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// site is every version, in selector order (latest, dev, tags newest first),
// plus the one every other version points its readers and crawlers at.
type site struct {
	base     string // "/vincent"
	origin   string // "https://lezli01.is-a.dev"
	versions []*version
	primary  *version // latest, or dev when no release tree was built
	newest   string   // the newest tag, "" without one
	dev      *version
}

// loadSite reads in's subdirectories. A tag tree without a latest tree, or
// the reverse, is refused rather than half-assembled: latest *is* the newest
// tag, so one without the other means the deploy built the wrong set.
func loadSite(in, base, origin string) (*site, error) {
	entries, err := os.ReadDir(in)
	if err != nil {
		return nil, err
	}
	s := &site{base: base, origin: origin}
	var latest *version
	var tagNames []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		switch name := e.Name(); name {
		case latestName, devName:
			v, err := readVersion(filepath.Join(in, name), name)
			if err != nil {
				return nil, err
			}
			if name == latestName {
				latest = v
			} else {
				s.dev = v
			}
		default:
			if _, ok := parseTag(name); !ok {
				return nil, fmt.Errorf("%s: not latest, dev or a release tag", filepath.Join(in, name))
			}
			tagNames = append(tagNames, name)
		}
	}
	tagNames = filterTags(tagNames)
	switch {
	case latest == nil && s.dev == nil:
		return nil, fmt.Errorf("%s holds neither a latest nor a dev tree", in)
	case latest != nil && len(tagNames) == 0:
		return nil, fmt.Errorf("%s has a latest tree but no tag tree to say which release it is", in)
	case latest == nil && len(tagNames) > 0:
		return nil, fmt.Errorf("%s has tag trees but no latest tree", in)
	}
	if latest != nil {
		s.newest = tagNames[0]
		latest.label = latestName + " (" + s.newest + ")"
		s.versions = append(s.versions, latest)
		s.primary = latest
	}
	if s.dev != nil {
		s.versions = append(s.versions, s.dev)
		if s.primary == nil {
			s.primary = s.dev
		}
	}
	for _, t := range tagNames {
		v, err := readVersion(filepath.Join(in, t), t)
		if err != nil {
			return nil, err
		}
		s.versions = append(s.versions, v)
	}
	return s, nil
}

func readVersion(dir, name string) (*version, error) {
	v := &version{name: name, label: name, dir: dir, files: map[string]bool{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		v.files[filepath.ToSlash(rel)] = true
		return nil
	})
	return v, err
}

// pagePath is the site-relative URL Jekyll gives a built file: an index.html
// is served as its directory.
func pagePath(rel string) string {
	if rel == "index.html" {
		return ""
	}
	if strings.HasSuffix(rel, "/index.html") {
		return strings.TrimSuffix(rel, "index.html")
	}
	return rel
}

// url is the base-relative URL of rel in v.
func (s *site) url(v *version, rel string) string {
	return s.base + "/" + v.name + "/" + pagePath(rel)
}

// linkInto is the URL of rel in v when v publishes it, and v's homepage when
// it does not — the selector's and the banner's one fallback rule.
func (s *site) linkInto(v *version, rel string) string {
	if v.has(rel) {
		return s.url(v, rel)
	}
	return s.url(v, "index.html")
}

func isHTML(rel string) bool { return strings.EqualFold(path.Ext(rel), ".html") }

func (s *site) assemble(out string) error {
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty", out)
	}
	for _, v := range s.versions {
		for _, rel := range v.sortedFiles() {
			// Only one sitemap is published, at the root, so a crawler
			// never indexes a version a canonical points away from.
			if rel == "sitemap.xml" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(v.dir, filepath.FromSlash(rel)))
			if err != nil {
				return err
			}
			if isHTML(rel) {
				data = []byte(s.transform(v, rel, string(data)))
			} else if rel == "robots.txt" {
				data = []byte(s.rootSitemap(v, string(data)))
			}
			if err := writeFile(out, v.name+"/"+rel, data); err != nil {
				return err
			}
		}
	}
	return s.writeRoot(out)
}

// rootSitemap points a reference to v's own sitemap at the root one.
func (s *site) rootSitemap(v *version, doc string) string {
	return strings.ReplaceAll(doc, s.base+"/"+v.name+"/sitemap.xml", s.base+"/sitemap.xml")
}

// writeRoot writes what lives above the version trees: latest's sitemap,
// robots.txt and 404 page, its docs/assets for image URLs a redirect cannot
// carry, and the redirect stubs for every pre-versioning path.
func (s *site) writeRoot(out string) error {
	p := s.primary
	for _, rel := range p.sortedFiles() {
		copyIt := rel == "sitemap.xml" || rel == "robots.txt" || rel == "404.html" ||
			(strings.HasPrefix(rel, "docs/assets/") && !isHTML(rel))
		if !copyIt {
			continue
		}
		src := filepath.Join(p.dir, filepath.FromSlash(rel))
		if rel == "robots.txt" || rel == "404.html" {
			// Already rewritten for the root sitemap, and the 404 page
			// already carries the selector.
			src = filepath.Join(out, p.name, filepath.FromSlash(rel))
		}
		data, err := os.ReadFile(src) //nolint:gosec // G304: a file of a tree this program was told to assemble
		if err != nil {
			return err
		}
		if err := writeFile(out, rel, data); err != nil {
			return err
		}
	}
	for _, rel := range p.sortedFiles() {
		if err := s.stub(out, p, rel); err != nil {
			return err
		}
	}
	if s.dev != nil && s.dev != p {
		for _, rel := range s.dev.sortedFiles() {
			if p.has(rel) {
				continue
			}
			if err := s.stub(out, s.dev, rel); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *site) stub(out string, target *version, rel string) error {
	if !isHTML(rel) || rel == "404.html" || s.isVersionDir(rel) {
		return nil
	}
	return writeFile(out, rel, []byte(redirectStub(s.url(target, rel), s.origin)))
}

// isVersionDir reports whether rel would land inside a version tree at the
// root, where a stub would overwrite that version's own page.
func (s *site) isVersionDir(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	for _, v := range s.versions {
		if first == v.name {
			return true
		}
	}
	return false
}

func writeFile(out, rel string, data []byte) error {
	p := filepath.Join(out, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // G301: a public web site, published world-readable
		return err
	}
	return os.WriteFile(p, data, 0o644) //nolint:gosec // a public web site, published world-readable
}
