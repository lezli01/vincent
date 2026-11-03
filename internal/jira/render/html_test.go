package render

import (
	"flag"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden .md files under testdata")

func site(t testing.TB) *url.URL {
	t.Helper()
	u, err := url.Parse("https://acme.atlassian.net/")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestHTMLToMarkdown pins one case per construct #906's acceptance criteria
// name, plus the degradations the package doc promises.
func TestHTMLToMarkdown(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"whitespace only", " \n\t&nbsp; ", ""},
		{"headings", "<h1>One</h1><h3>Three <em>x</em></h3><h6>Six</h6>", "# One\n\n### Three *x*\n\n###### Six"},
		{"empty heading dropped", "<h2> </h2><p>x</p>", "x"},
		{"paragraphs and divs", "<p>a</p><div>b</div>c", "a\n\nb\n\nc"},
		{"whitespace collapsed", "<p>  a \n\n  b\t c&nbsp;&nbsp;d  </p>", "a b c d"},
		{"entities decoded", "<p>&lt;tag&gt; &amp; &quot;q&quot; &#x263A;</p>", `<tag> & "q" ☺`},
		{"br", "<p>one<br>two<br/><br/><br/>three</p>", "one\ntwo\n\nthree"},
		{"strong and em", "<p><strong>s</strong> <b>b</b> <em>e</em> <i>i</i></p>", "**s** **b** *e* *i*"},
		{"emphasis edges moved outside", "<p>a<b> bold </b>c</p>", "a **bold** c"},
		{"empty emphasis", "<p>a<b> </b>c</p>", "a c"},
		{"text-only inline", "<p><del>d</del> <s>s</s> <ins>i</ins> <u>u</u> x<sup>2</sup> H<sub>2</sub>O <span>sp</span> <font color=red>f</font></p>", "d s i u x2 H2O sp f"},
		{"inline code", "<p>run <code>go test</code> or <tt>make</tt></p>", "run `go test` or `make`"},
		{"inline code with backticks", "<p><code>a`b</code> <code>`x</code></p>", "``a`b`` `` `x ``"},
		{"block code", "<pre>line 1\n  line 2\n</pre>", "```\nline 1\n  line 2\n```"},
		{"block code with language class", `<pre><code class="language-python">print(1)</code></pre>`, "```python\nprint(1)\n```"},
		{"jira code panel", `<div class="code panel"><div class="codeContent panelContent"><pre class="code-java">a &lt; b</pre></div></div>`, "```java\na < b\n```"},
		{"code fence longer than content run", "<pre>```\nnested\n```</pre>", "````\n```\nnested\n```\n````"},
		{"code keeps markup as text", "<pre><b>x</b><br>y</pre>", "```\nx\ny\n```"},
		{"bullet list", "<ul><li>a</li><li>b</li></ul>", "- a\n- b"},
		{"ordered list with start", `<ol start="3"><li>c</li><li>d</li></ol>`, "3. c\n4. d"},
		{
			"nested lists",
			"<ol><li>one<ul><li>a<ol><li>deep</li></ol></li><li>b</li></ul></li><li>two</li></ol>",
			"1. one\n   - a\n     1. deep\n   - b\n2. two",
		},
		{"list directly in list", "<ul><li>a</li><ul><li>b</li></ul></ul>", "- a\n  - b"},
		{"empty items skipped", "<ol><li></li><li>a</li><li> </li><li>b</li></ol>", "1. a\n2. b"},
		{"item with paragraphs", "<ul><li><p>a</p><p>b</p></li></ul>", "- a\n  b"},
		{"absolute link", `<p><a href="https://example.com/x?a=1&amp;b=2">docs</a></p>`, "[docs](https://example.com/x?a=1&b=2)"},
		{"relative link absolutized", `<a href="/browse/ACME-1">ACME-1</a>`, "[ACME-1](https://acme.atlassian.net/browse/ACME-1)"},
		{"mailto link", `<a href="mailto:a@b.c">mail</a>`, "[mail](mailto:a@b.c)"},
		{"link destination encoded", `<a href="https://e.com/a b(c)">x</a>`, "[x](https://e.com/a%20b%28c%29)"},
		{"link without text", `<a href="https://e.com/"></a>`, "[https://e.com/](https://e.com/)"},
		{"link without href", `<a name="anchor">t</a>`, "t"},
		{"javascript link", `<a href="javascript:alert(1)">click</a>`, "click"},
		{"data link", `<a href="data:text/html,x">click</a>`, "click"},
		{"image", `<img src="/i.png" alt="pic">`, "![pic](https://acme.atlassian.net/i.png)"},
		{"data image", `<img src="data:image/png;base64,AA" alt="pic">`, "pic"},
		{"emoticon", `<p>ok <img class="emoticon" src="/s.png" alt="(smile)"></p>`, "ok (smile)"},
		{
			"mention link",
			`<p>cc <a href="/secure/ViewProfile.jspa?accountId=557058:abc" class="user-hover" rel="557058:abc" data-account-id="557058:abc">Ada Lovelace</a></p>`,
			"cc Ada Lovelace",
		},
		{"mention people link", `<a href="https://acme.atlassian.net/jira/people/557058:abc">@Ada</a>`, "@Ada"},
		{"mention span", `<span class="mention" data-mention-id="557058:abc">@Ada</span>`, "@Ada"},
		{"blockquote", "<blockquote><p>a</p><p>b</p></blockquote>", "> a\n>\n> b"},
		{"nested blockquote", "<blockquote>a<blockquote>b</blockquote></blockquote>", "> a\n>\n> > b"},
		{"hr", "<p>a</p><hr><p>b</p>", "a\n\n---\n\nb"},
		{
			"table with th",
			"<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>",
			"| A | B |\n| --- | --- |\n| 1 | 2 |",
		},
		{
			"table without th, ragged, pipes and newlines",
			"<table><tbody><tr><td>x|y</td><td>a<br>b</td></tr><tr><td>only</td></tr></tbody></table>",
			"| x¦y | a b |\n| --- | --- |\n| only |  |",
		},
		{"table caption", "<table><caption>Cap</caption><tr><td>a</td></tr></table>", "Cap\n\n| a |\n| --- |"},
		{"empty table", "<table></table><p>x</p>", "x"},
		{
			"attachment image",
			`<span class="image-wrap"><a href="/rest/api/3/attachment/content/1" file-preview-type="image"><img src="/rest/api/3/attachment/thumbnail/1" alt="s.png"></a></span>`,
			"![s.png](https://acme.atlassian.net/rest/api/3/attachment/content/1)",
		},
		{
			"attachment file link",
			`<span class="nobr"><a href="/secure/attachment/2/t.log">t.log<sup><img class="rendericon" src="/i.gif" alt=""></sup></a></span>`,
			"[t.log](https://acme.atlassian.net/secure/attachment/2/t.log)",
		},
		{"media without url", `<div data-attachment-name="d.pdf" data-attachment-type="file"></div>`, "[attachment: d.pdf]"},
		{"media without name", `<div data-media-services-id="abc"></div>`, "[attachment]"},
		{"media named link", `<div data-attachment-name="d.pdf"><a href="/a/3"></a></div>`, "[d.pdf](https://acme.atlassian.net/a/3)"},
		{"dropped elements", "<style>p{}</style><script>alert(1)</script><p>kept</p><noscript>no</noscript>", "kept"},
		{"comments dropped", "<p>a<!-- hidden -->b</p>", "ab"},
		{"unknown elements degrade to text", "<p><blink>x</blink> <custom-el>y</custom-el></p><marquee>z</marquee>", "x y\n\nz"},
		{"inline wrapping blocks", "<span><p>a</p><p>b</p></span>", "a\n\nb"},
		// The HTML5 tree builder reopens an unclosed b inside the list item,
		// which is what a browser shows too.
		{"unclosed tags", "<p><b>bold<ul><li>item", "**bold**\n\n- **item**"},
		{"stray closers", "</div></p>text</b>", "text"},
		{"markdown punctuation not escaped", "<p>*not* _em_ # 1. [x]</p>", "*not* _em_ # 1. [x]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HTMLToMarkdown(tc.in, site(t))
			if got != tc.want {
				t.Errorf("HTMLToMarkdown(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestHTMLToMarkdownNilBase(t *testing.T) {
	got := HTMLToMarkdown(`<a href="/browse/A-1">A-1</a> <img src="i.png" alt="i"> <a href="https://e.com">e</a> <a href="javascript:x()">j</a>`, nil)
	want := "[A-1](/browse/A-1) ![i](i.png) [e](https://e.com) j"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestMentionNeverLeaksAccountID is the privacy half of "mentions": the
// account id appears nowhere in the output, link or text.
func TestMentionNeverLeaksAccountID(t *testing.T) {
	const id = "5b10ac8d82e05b22cc7d4ef5"
	for _, f := range goldenFixtures(t) {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), id) {
			continue
		}
		if got := HTMLToMarkdown(string(src), site(t)); strings.Contains(got, id) {
			t.Errorf("%s: account id %s leaked into\n%s", f, id, got)
		}
	}
}

func TestDeeplyNestedLists(t *testing.T) {
	const depth = 200
	in := strings.Repeat("<ul><li>x", depth) + strings.Repeat("</li></ul>", depth)
	got := HTMLToMarkdown(in, nil)
	lines := strings.Split(got, "\n")
	if len(lines) != depth {
		t.Fatalf("got %d lines, want %d", len(lines), depth)
	}
	last := lines[depth-1]
	if want := strings.Repeat("  ", depth-1) + "- x"; last != want {
		t.Errorf("deepest line %q, want %q", last, want)
	}
}

func goldenFixtures(t testing.TB) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "*.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures under testdata")
	}
	return files
}

// TestGolden converts each testdata/*.html fixture, twice, and compares it
// with its .md golden file. Byte-identical output across runs is what lets
// the importer (#908) treat an unchanged body as unchanged.
//
// The fixtures are hand-written in Jira Cloud's documented rendered-HTML
// shape; bodies captured from a real site (#899) join them named after the
// capture, the way adapter testdata is.
func TestGolden(t *testing.T) {
	for _, f := range goldenFixtures(t) {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			got := HTMLToMarkdown(string(src), site(t))
			if again := HTMLToMarkdown(string(src), site(t)); again != got {
				t.Fatalf("not deterministic:\n%s\n---\n%s", got, again)
			}
			golden := strings.TrimSuffix(f, ".html") + ".md"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			// A checkout with autocrlf would hand the golden back CRLF.
			if w := strings.ReplaceAll(string(want), "\r\n", "\n"); got != w {
				t.Errorf("%s differs from golden\n got:\n%s\nwant:\n%s", f, got, w)
			}
		})
	}
}

func FuzzHTMLToMarkdown(f *testing.F) {
	for _, s := range []string{
		"", "<p>a</p>", "<ul><li>a<ol><li>b", "<table><tr><td>|</td>", "<pre>```</pre>",
		"<a href='javascript:x'>y</a>", "<b><i>x</b></i>", "<blockquote><blockquote>",
		`<span class="image-wrap"><a href="/x"><img src="/y"></a></span>`, "&nbsp;<br><br>",
	} {
		f.Add(s)
	}
	base, _ := url.Parse("https://acme.atlassian.net/")
	f.Fuzz(func(t *testing.T, in string) {
		got := HTMLToMarkdown(in, base)
		if again := HTMLToMarkdown(in, base); again != got {
			t.Fatalf("not deterministic for %q:\n%q\n%q", in, got, again)
		}
		if got != strings.TrimSpace(got) {
			t.Fatalf("untrimmed output for %q: %q", in, got)
		}
		for _, l := range strings.Split(got, "\n") {
			if l != strings.TrimRight(l, " \t") {
				t.Fatalf("trailing space for %q: %q", in, l)
			}
		}
		if strings.Contains(got, "javascript:") && !strings.Contains(in, "javascript:") {
			t.Fatalf("invented a javascript: URL for %q", in)
		}
	})
}
