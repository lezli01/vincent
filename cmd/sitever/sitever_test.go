package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const (
	testOrigin = "https://lezli01.is-a.dev"
	testBase   = "/vincent"
)

// modernPage is the current default layout reduced to the tags sitever
// touches: every SEO tag present, a skip link, the content-card main.
func modernPage(name, rel string) string {
	u := testOrigin + testBase + "/" + name + "/" + pagePath(rel)
	img := testOrigin + testBase + "/" + name + "/docs/assets/opengraph.png"
	return `<!doctype html>
<html lang="en">
<head>
  <meta name="robots" content="index,follow,max-image-preview:large">
  <link rel="canonical" href="` + u + `">
  <link rel="sitemap" type="application/xml" title="Sitemap" href="` + testOrigin + testBase + "/" + name + `/sitemap.xml">
  <meta property="og:url" content="` + u + `">
  <meta property="og:image" content="` + img + `">
  <meta property="og:image:secure_url" content="` + img + `">
  <meta name="twitter:url" content="` + u + `">
  <meta name="twitter:image" content="` + img + `">
</head>
<body>
  <a class="skip-link" href="#content">Skip to content</a>
  <main id="content" class="content-card framed"><article>` + rel + `</article></main>
  <script src="` + testBase + "/" + name + `/assets/js/search.js" defer></script>
</body>
</html>
`
}

// oldPage is a pre-task-120 layout: no search, no robots tag, no
// content-card main.
func oldPage(name, rel string) string {
	u := testOrigin + testBase + "/" + name + "/" + pagePath(rel)
	return `<!doctype html>
<html lang="en">
<head>
  <link rel="canonical" href="` + u + `">
  <meta property="og:url" content="` + u + `">
  <meta property="og:image" content="` + testOrigin + testBase + "/" + name + `/docs/assets/opengraph.png">
</head>
<body>
  <main id="content"><article>` + rel + `</article></main>
</body>
</html>
`
}

// barePage carries no canonical, robots or social tag at all.
func barePage(_, rel string) string {
	return "<html><head><title>" + rel + "</title></head><body><p>" + rel + "</p></body></html>\n"
}

type fixture struct {
	name  string
	page  func(name, rel string) string
	pages []string
	extra map[string]string // non-HTML files
}

var commonPages = []string{"index.html", "404.html", "docs/index.html", "docs/features.html"}

func fixtures() []fixture {
	common := func(name string) map[string]string {
		return map[string]string{
			"sitemap.xml":                "<urlset>" + name + "</urlset>",
			"robots.txt":                 "Sitemap: " + testOrigin + testBase + "/" + name + "/sitemap.xml\n",
			"docs/assets/opengraph.png":  "png-" + name,
			"assets/css/style.css":       "css-" + name,
			"docs/assets/social/why.png": "why-" + name,
		}
	}
	modern := append([]string{"docs/guides/triggers.html"}, commonPages...)
	return []fixture{
		{latestName, modernPage, modern, common(latestName)},
		{devName, modernPage, append([]string{"docs/new.html"}, modern...), common(devName)},
		{"v0.11.0", modernPage, modern, common("v0.11.0")},
		{"v0.10.1", modernPage, commonPages, common("v0.10.1")},
		{"v0.9.0", oldPage, commonPages, common("v0.9.0")},
		{"v0.6.0", barePage, commonPages, nil},
	}
}

func writeFixtures(t *testing.T, fx []fixture) string {
	t.Helper()
	in := t.TempDir()
	for _, f := range fx {
		files := map[string]string{}
		for _, p := range f.pages {
			files[p] = f.page(f.name, p)
		}
		for p, c := range f.extra {
			files[p] = c
		}
		for p, c := range files {
			if err := writeFile(filepath.Join(in, f.name), p, []byte(c)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return in
}

func assembleFixtures(t *testing.T, fx []fixture) string {
	t.Helper()
	in := writeFixtures(t, fx)
	out := filepath.Join(t.TempDir(), "site")
	if err := run([]string{"assemble", "-in", in, "-out", out, "-base", testBase, "-url", testOrigin}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return out
}

func read(t *testing.T, out, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(out, rel string) bool {
	_, err := os.Stat(filepath.Join(out, filepath.FromSlash(rel)))
	return err == nil
}

var hrefs = regexp.MustCompile(`<li><a href="([^"]*)"(?: aria-current="page")?>([^<]*)</a></li>`)

func TestSelectorOnEveryPage(t *testing.T) {
	fx := fixtures()
	out := assembleFixtures(t, fx)
	labels := []string{"latest (v0.11.0)", "dev", "v0.11.0", "v0.10.1", "v0.9.0", "v0.6.0"}
	for _, f := range fx {
		for _, rel := range f.pages {
			doc := read(t, out, f.name+"/"+rel)
			if n := strings.Count(doc, `<nav class="sitever"`); n != 1 {
				t.Fatalf("%s/%s: %d selectors, want 1", f.name, rel, n)
			}
			ms := hrefs.FindAllStringSubmatch(doc, -1)
			var got []string
			for _, m := range ms {
				got = append(got, m[2])
			}
			if !reflect.DeepEqual(got, labels) {
				t.Fatalf("%s/%s: selector %q, want %q", f.name, rel, got, labels)
			}
		}
	}

	// Same path where the target has it, the target's homepage where not.
	doc := read(t, out, "latest/docs/guides/triggers.html")
	want := []string{
		"/vincent/latest/docs/guides/triggers.html",
		"/vincent/dev/docs/guides/triggers.html",
		"/vincent/v0.11.0/docs/guides/triggers.html",
		"/vincent/v0.10.1/",
		"/vincent/v0.9.0/",
		"/vincent/v0.6.0/",
	}
	var got []string
	for _, m := range hrefs.FindAllStringSubmatch(doc, -1) {
		got = append(got, m[1])
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selector links %q, want %q", got, want)
	}
	if doc := read(t, out, "v0.9.0/docs/index.html"); !strings.Contains(doc, `<a href="/vincent/v0.9.0/docs/" aria-current="page">v0.9.0</a>`) {
		t.Fatalf("own version not marked current, or a directory index not linked as its directory:\n%s", doc)
	}
	if doc := read(t, out, "latest/index.html"); !strings.Contains(doc, `<span class="sitever-badge">latest (v0.11.0)</span>`) {
		t.Fatalf("latest badge missing:\n%s", doc)
	}
	// The selector comes after the skip link, so the skip link stays first.
	if doc := read(t, out, "dev/index.html"); strings.Index(doc, "skip-link") > strings.Index(doc, `class="sitever"`) {
		t.Fatalf("selector placed before the skip link:\n%s", doc)
	}
}

func TestBanners(t *testing.T) {
	out := assembleFixtures(t, fixtures())
	cases := []struct {
		rel, want string
	}{
		{"latest/docs/features.html", ""},
		{"v0.11.0/docs/features.html", ""}, // the newest tag's own copy
		{"dev/docs/features.html", `Unreleased docs — may describe features not in any release yet. <a href="/vincent/latest/docs/features.html">See the latest release</a>.`},
		{"dev/docs/new.html", `<a href="/vincent/latest/">See the latest release</a>`},
		{"v0.10.1/docs/features.html", `You are viewing docs for v0.10.1 — <a href="/vincent/latest/docs/features.html">see the latest release</a>.`},
		{"v0.9.0/docs/features.html", `You are viewing docs for v0.9.0`},
		{"v0.6.0/index.html", `You are viewing docs for v0.6.0 — <a href="/vincent/latest/">see the latest release</a>.`},
	}
	for _, c := range cases {
		doc := read(t, out, c.rel)
		has := strings.Contains(doc, `class="sitever-banner"`)
		if c.want == "" {
			if has {
				t.Errorf("%s: unexpected banner", c.rel)
			}
			continue
		}
		if !has || !strings.Contains(doc, c.want) {
			t.Errorf("%s: banner missing %q:\n%s", c.rel, c.want, doc)
		}
	}
	// Anchored inside the content card where the layout has one, at the
	// start of <body> where it does not.
	if doc := read(t, out, "v0.10.1/docs/features.html"); !regexp.MustCompile(`content-card framed">\s*<div class="sitever-banner"`).MatchString(doc) {
		t.Errorf("banner not at the top of the content card:\n%s", doc)
	}
	if doc := read(t, out, "v0.9.0/docs/features.html"); !regexp.MustCompile(`<body>\s*<div class="sitever-banner"`).MatchString(doc) {
		t.Errorf("banner not at the start of body:\n%s", doc)
	}
}

func TestNonLatestPointsCrawlersAtLatest(t *testing.T) {
	fx := fixtures()
	out := assembleFixtures(t, fx)
	for _, f := range fx {
		for _, rel := range f.pages {
			doc := read(t, out, f.name+"/"+rel)
			if f.name == latestName {
				// Untouched apart from the chrome and the sitemap link.
				orig := modernPage(latestName, rel)
				for _, tag := range regexp.MustCompile(`<(?:link rel="canonical"|meta)[^>]*>`).FindAllString(orig, -1) {
					if !strings.Contains(doc, tag) {
						t.Errorf("latest/%s lost %s", rel, tag)
					}
				}
				continue
			}
			target := testOrigin + "/vincent/latest/" + pagePath(rel)
			if f.name == devName && rel == "docs/new.html" {
				target = testOrigin + "/vincent/latest/"
			}
			for _, want := range []string{
				`<link rel="canonical" href="` + target + `">`,
				`<meta property="og:url" content="` + target + `">`,
				`<meta name="twitter:url" content="` + target + `">`,
				`<meta name="robots" content="noindex,follow">`,
			} {
				if strings.Count(doc, want) != 1 {
					t.Errorf("%s/%s: want exactly one %s:\n%s", f.name, rel, want, doc)
				}
			}
			if strings.Contains(doc, `index,follow,max-image`) || strings.Count(doc, `rel="canonical"`) != 1 {
				t.Errorf("%s/%s: old robots or a second canonical survived:\n%s", f.name, rel, doc)
			}
			if f.name != "v0.6.0" && !strings.Contains(doc, `<meta property="og:image" content="`+testOrigin+`/vincent/latest/docs/assets/opengraph.png">`) {
				t.Errorf("%s/%s: og:image not served from latest:\n%s", f.name, rel, doc)
			}
			if f.name == "v0.6.0" && strings.Contains(doc, "og:image") {
				t.Errorf("%s/%s: an image was invented:\n%s", f.name, rel, doc)
			}
			if strings.Contains(doc, "/"+f.name+"/sitemap.xml") {
				t.Errorf("%s/%s: still links its own sitemap", f.name, rel)
			}
		}
	}
	if doc := read(t, out, "v0.11.0/docs/features.html"); !strings.Contains(doc, `<meta name="twitter:image" content="`+testOrigin+`/vincent/latest/docs/assets/opengraph.png">`) {
		t.Errorf("twitter:image not served from latest:\n%s", doc)
	}
}

func TestOnlyLatestSitemapAtRoot(t *testing.T) {
	fx := fixtures()
	out := assembleFixtures(t, fx)
	for _, f := range fx {
		if exists(out, f.name+"/sitemap.xml") {
			t.Errorf("%s/sitemap.xml published", f.name)
		}
	}
	if got := read(t, out, "sitemap.xml"); got != "<urlset>latest</urlset>" {
		t.Errorf("root sitemap %q, want latest's", got)
	}
	if got := read(t, out, "robots.txt"); got != "Sitemap: "+testOrigin+"/vincent/sitemap.xml\n" {
		t.Errorf("root robots.txt %q", got)
	}
	if got := read(t, out, "docs/assets/opengraph.png"); got != "png-latest" {
		t.Errorf("root asset %q, want latest's", got)
	}
	if got := read(t, out, "docs/assets/social/why.png"); got != "why-latest" {
		t.Errorf("root social card %q, want latest's", got)
	}
	if exists(out, "assets/css/style.css") {
		t.Error("non-docs asset copied to the root")
	}
	if got := read(t, out, "404.html"); !strings.Contains(got, `<nav class="sitever"`) || strings.Contains(got, "Redirecting") {
		t.Error("root 404 is not latest's page")
	}
}

func TestRedirectStubs(t *testing.T) {
	out := assembleFixtures(t, fixtures())
	cases := map[string]string{
		"index.html":                "/vincent/latest/",
		"docs/index.html":           "/vincent/latest/docs/",
		"docs/features.html":        "/vincent/latest/docs/features.html",
		"docs/guides/triggers.html": "/vincent/latest/docs/guides/triggers.html",
		"docs/new.html":             "/vincent/dev/docs/new.html",
	}
	for rel, target := range cases {
		doc := read(t, out, rel)
		for _, want := range []string{
			`<link rel="canonical" href="` + testOrigin + target + `">`,
			`<meta http-equiv="refresh" content="0; url=` + target + `">`,
			`location.replace("` + target + `" + location.hash)`,
		} {
			if !strings.Contains(doc, want) {
				t.Errorf("%s: stub lacks %s:\n%s", rel, want, doc)
			}
		}
	}
}

func TestDevOnlyAssembly(t *testing.T) {
	fx := fixtures()[1:2]
	out := assembleFixtures(t, fx)
	doc := read(t, out, "dev/docs/features.html")
	if !strings.Contains(doc, `<span class="sitever-badge">dev</span>`) {
		t.Fatalf("no selector:\n%s", doc)
	}
	if !strings.Contains(doc, `<div class="sitever-banner" role="note">Unreleased docs — may describe features not in any release yet.</div>`) {
		t.Fatalf("dev banner should stand without a release link:\n%s", doc)
	}
	if !strings.Contains(read(t, out, "index.html"), `url=/vincent/dev/"`) {
		t.Fatal("root does not redirect to dev")
	}
}

func TestLoadSiteRefusesMismatchedTrees(t *testing.T) {
	all := fixtures()
	for name, fx := range map[string][]fixture{
		"tag without latest": {all[1], all[2]},
		"latest without tag": {all[0], all[1]},
		"neither":            {all[3]},
	} {
		in := writeFixtures(t, fx)
		if _, err := loadSite(in, testBase, testOrigin); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	in := writeFixtures(t, all)
	if err := os.MkdirAll(filepath.Join(in, "v0.12.0-rc1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSite(in, testBase, testOrigin); err == nil {
		t.Error("a prerelease tree was accepted")
	}
}

func TestVersions(t *testing.T) {
	in := "v0.1.0\nv0.1.0-rc1\nv0.5.0\nv0.6.0\nv0.9.0\nv0.10.1\nv0.10.0\nv0.11.0-rc1\nv1.0.0\nnot-a-tag\nv0.10.1\n"
	var out bytes.Buffer
	if err := run([]string{"versions"}, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	want := "v1.0.0\nv0.10.1\nv0.10.0\nv0.9.0\nv0.6.0\n"
	if out.String() != want {
		t.Fatalf("versions:\n%s\nwant:\n%s", out.String(), want)
	}
}
