package main

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

// The markup is edited with anchored regular expressions rather than parsed:
// the trees are Jekyll output from a handful of known layouts, and a parse
// and re-render would rewrite every page's bytes rather than the few tags
// this program owns. Every anchor has a fallback, so a layout lacking one
// degrades instead of failing (task 131 decision 2).
var (
	headClose   = regexp.MustCompile(`(?i)</head\s*>`)
	bodyClose   = regexp.MustCompile(`(?i)</body\s*>`)
	bodyOpen    = regexp.MustCompile(`(?i)<body\b[^>]*>`)
	contentMain = regexp.MustCompile(`(?is)<main\b[^>]*\bclass\s*=\s*["'][^"']*\bcontent-card\b[^"']*["'][^>]*>`)
	canonical   = regexp.MustCompile(`(?is)<link\b[^>]*?\brel\s*=\s*["']canonical["'][^>]*>`)
	contentAttr = regexp.MustCompile(`(?is)\bcontent\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

func metaTag(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?is)<meta\b[^>]*?\b(?:name|property)\s*=\s*["']` + regexp.QuoteMeta(key) + `["'][^>]*>`)
}

var (
	ogURL      = metaTag("og:url")
	twitterURL = metaTag("twitter:url")
	robots     = metaTag("robots")
	imageTags  = []struct {
		attr, key string
		re        *regexp.Regexp
	}{
		{"property", "og:image", metaTag("og:image")},
		{"property", "og:image:secure_url", metaTag("og:image:secure_url")},
		{"name", "twitter:image", metaTag("twitter:image")},
	}
)

// noindex is what every page outside latest tells a crawler: follow its
// links, index the latest copy its canonical names instead.
const noindex = "noindex,follow"

// transform is everything sitever does to one page of one version.
func (s *site) transform(v *version, rel, doc string) string {
	doc = s.rootSitemap(v, doc)
	if v != s.primary {
		doc = s.redirectSEO(v, rel, doc)
	}
	doc = insertHead(doc, chromeStyle)
	if banner := s.banner(v, rel); banner != "" {
		if loc := contentMain.FindStringIndex(doc); loc != nil {
			doc = doc[:loc[1]] + "\n" + banner + doc[loc[1]:]
		} else {
			doc = insertBodyStart(doc, banner)
		}
	}
	// The selector is fixed in place, so it goes last in the document and
	// the layout's skip link stays the first thing a keyboard reaches.
	return insertBodyEnd(doc, s.selector(v, rel))
}

// redirectSEO points a non-latest page's canonical, social URLs and images
// at latest, and keeps the page itself out of the index.
func (s *site) redirectSEO(v *version, rel, doc string) string {
	target := esc(s.origin + s.linkInto(s.primary, rel))
	doc = setTag(doc, canonical, `<link rel="canonical" href="`+target+`">`)
	doc = setTag(doc, ogURL, `<meta property="og:url" content="`+target+`">`)
	doc = setTag(doc, twitterURL, `<meta name="twitter:url" content="`+target+`">`)
	doc = setTag(doc, robots, `<meta name="robots" content="`+noindex+`">`)
	from := s.base + "/" + v.name + "/"
	to := s.base + "/" + s.primary.name + "/"
	for _, t := range imageTags {
		loc := t.re.FindStringIndex(doc)
		if loc == nil {
			continue // an image is only rewritten, never invented
		}
		m := contentAttr.FindStringSubmatch(doc[loc[0]:loc[1]])
		if m == nil {
			continue
		}
		src := html.UnescapeString(m[1] + m[2])
		src = strings.Replace(src, from, to, 1)
		doc = doc[:loc[0]] + `<meta ` + t.attr + `="` + t.key + `" content="` + esc(src) + `">` + doc[loc[1]:]
	}
	return doc
}

// setTag replaces every tag re matches with tag, or inserts tag into <head>
// when the layout never emitted one.
func setTag(doc string, re *regexp.Regexp, tag string) string {
	if re.MatchString(doc) {
		return re.ReplaceAllLiteralString(doc, tag)
	}
	return insertHead(doc, tag)
}

// insertHead puts s at the end of <head>, falling back to the start of
// <body>, then the start of the document.
func insertHead(doc, s string) string {
	if loc := headClose.FindStringIndex(doc); loc != nil {
		return doc[:loc[0]] + s + "\n" + doc[loc[0]:]
	}
	return insertBodyStart(doc, s)
}

func insertBodyEnd(doc, s string) string {
	if loc := bodyClose.FindAllStringIndex(doc, -1); loc != nil {
		at := loc[len(loc)-1][0]
		return doc[:at] + s + "\n" + doc[at:]
	}
	return doc + "\n" + s
}

func insertBodyStart(doc, s string) string {
	if loc := bodyOpen.FindStringIndex(doc); loc != nil {
		return doc[:loc[1]] + "\n" + s + doc[loc[1]:]
	}
	return s + "\n" + doc
}

// selector is the badge naming v and the list of every version, each linked
// to rel there or to its homepage.
func (s *site) selector(v *version, rel string) string {
	var b strings.Builder
	b.WriteString(`<nav class="sitever" aria-label="Documentation version"><details><summary>`)
	b.WriteString(`<span class="sitever-badge">` + esc(v.label) + `</span></summary><ul>`)
	for _, o := range s.versions {
		b.WriteString(`<li><a href="` + esc(s.linkInto(o, rel)) + `"`)
		if o == v {
			b.WriteString(` aria-current="page"`)
		}
		b.WriteString(`>` + esc(o.label) + `</a></li>`)
	}
	b.WriteString(`</ul></details></nav>`)
	return b.String()
}

// banner is the warning above an old tag's or dev's content, and nothing on
// latest or on the newest tag's own copy (task 131 decision 3).
func (s *site) banner(v *version, rel string) string {
	if v.name == latestName || v.name == s.newest {
		return ""
	}
	link := func(text string) string {
		if s.primary.name != latestName {
			return "" // the dev-only smoke build has no release to point at
		}
		return `<a href="` + esc(s.linkInto(s.primary, rel)) + `">` + text + `</a>.`
	}
	var text string
	if v.name == devName {
		text = "Unreleased docs — may describe features not in any release yet. " + link("See the latest release")
	} else {
		text = "You are viewing docs for " + esc(v.name) + " — " + link("see the latest release")
	}
	return `<div class="sitever-banner" role="note">` + strings.TrimSpace(strings.TrimSuffix(text, " — ")) + `</div>`
}

// chromeStyle ships with the markup so an old tag's stylesheet needs no
// change; the colours are the site's own gruvbox palette.
const chromeStyle = `<style id="sitever-style">
.sitever{position:fixed;right:1rem;bottom:1rem;z-index:1000;font:500 .8rem/1.4 "Roboto Mono",ui-monospace,monospace}
.sitever summary{cursor:pointer;list-style:none}
.sitever summary::-webkit-details-marker{display:none}
.sitever-badge{display:inline-block;padding:.3rem .65rem;border:1px solid #fabd2f;border-radius:4px;background:#282828;color:#fabd2f}
.sitever-badge::after{content:" \25BE"}
.sitever ul{position:absolute;right:0;bottom:2.2rem;margin:0;padding:.35rem 0;min-width:12rem;max-height:60vh;overflow:auto;list-style:none;border:1px solid #504945;border-radius:4px;background:#282828}
.sitever li{margin:0}
.sitever a{display:block;padding:.25rem .8rem;color:#ebdbb2;text-decoration:none}
.sitever a:hover,.sitever a:focus{background:#3c3836;color:#fabd2f}
.sitever a[aria-current]{color:#fabd2f}
.sitever-banner{margin:0 0 1rem;padding:.6rem .9rem;border:1px solid #fabd2f;border-radius:4px;background:#3c3836;color:#ebdbb2;font:500 .85rem/1.5 "Roboto Mono",ui-monospace,monospace}
.sitever-banner a{color:#fabd2f}
</style>`

// redirectStub is a page that sends its reader to target, keeping the
// fragment: meta refresh for readers without script, location.replace (with
// location.hash) for everyone else, and a canonical so a crawler records the
// target rather than the stub.
func redirectStub(target, origin string) string {
	js, _ := json.Marshal(target) // a string never fails to marshal; json escapes '<'
	t := esc(target)
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Redirecting…</title>
<link rel="canonical" href="` + esc(origin) + t + `">
<meta name="robots" content="noindex">
<script>location.replace(` + string(js) + ` + location.hash);</script>
<meta http-equiv="refresh" content="0; url=` + t + `">
</head>
<body>
<p>This page has moved to <a href="` + t + `">` + t + `</a>.</p>
</body>
</html>
`
}

func esc(s string) string { return html.EscapeString(s) }
