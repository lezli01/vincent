package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This repository's own resolver workflows write the pull request title into
// `.vincent-issue/pr-title.txt` and hand it to `gh pr create`, so the sentence
// that tells the agent what to put in that file decides whether the pull
// request opens green or red. `.github/workflows/pr-title.yml` is a required
// check that *fails* a pull request whose title carries a Conventional Commit
// prefix — CLAUDE.md's rule, because GitHub copies the title into the merge
// commit and Release Please would then record the change twice (issue #450).
//
// This is a drift check of the same shape as the docs-claims tests in
// `internal/cli`: the enforcing regular expression is lifted out of the
// workflow that enforces it rather than restated here, so the day the check
// changes, this test changes with it.
const (
	prTitleFile  = ".vincent-issue/pr-title.txt"
	prTitleGuard = ".github/workflows/pr-title.yml"
	// The rule's name in CLAUDE.md, CONTRIBUTING.md, README.md and in the
	// error the guard prints ("Use a plain-language PR title"). An
	// instruction that does not put the agent on it is not an instruction
	// the agent can follow.
	prTitleVocabulary = `(?i)plain[- ]language`
	// Words that turn a mention of a rejected style into a warning against
	// it. "never `feat: add workflow fields`" is the contrast CLAUDE.md draws
	// and is exactly what the instruction should carry; "Conventional Commits
	// style" on its own is the defect.
	prTitleNegation = `(?i)\b(never|not|no|rather than|instead of|avoid)\b`
	// How far back a negation may sit and still govern the mention. One
	// clause, not one paragraph.
	prTitleNegationWindow = 60
)

// TestResolveWorkflowsAskForAPlainLanguagePRTitle holds every workflow that
// writes `pr-title.txt` to the title rule its own repository enforces. A
// workflow that asks its agent for a Conventional Commits title reliably
// produces a pull request that a required check rejects, which is a failure
// no step in the workflow can see and a human has to notice on GitHub.
func TestResolveWorkflowsAskForAPlainLanguagePRTitle(t *testing.T) {
	reject := prTitleRejectPattern(t)
	plain := regexp.MustCompile(prTitleVocabulary)

	root := filepath.Join("..", "..", ".vincent", "workflows")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}

	checked := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		src, readErr := os.ReadFile(filepath.Join(root, e.Name()))
		if readErr != nil {
			t.Fatalf("read %s: %v", e.Name(), readErr)
		}
		wf, _, parseErr := Parse(src, curatedOptions())
		if parseErr != nil {
			t.Fatalf("%s does not parse: %v", e.Name(), parseErr)
		}
		for _, placed := range allSteps(wf) {
			bullet, ok := prTitleInstruction(placed.Step.Prompt)
			if !ok {
				continue
			}
			checked++
			t.Run(e.Name()+"/"+placed.Step.ID, func(t *testing.T) {
				if !plain.MatchString(bullet) {
					t.Errorf("the instruction for %s never tells the agent the title is plain language,\n"+
						"which is the one thing %s requires of it:\n\n%s",
						prTitleFile, prTitleGuard, bullet)
				}
				for _, mention := range mentionsOf(bullet, "conventional commit") {
					if !negated(bullet, mention) {
						t.Errorf("the instruction for %s asks for a Conventional Commits title,\n"+
							"which %s rejects with %q:\n\n%s",
							prTitleFile, prTitleGuard, reject, bullet)
					}
				}
				for _, example := range titleExamples(bullet) {
					if reject.MatchString(example) && !negated(bullet, strings.Index(bullet, example)) {
						t.Errorf("the instruction for %s shows %q as a title to write,\n"+
							"and %s rejects it:\n\n%s", prTitleFile, example, prTitleGuard, bullet)
					}
				}
			})
		}
	}
	if checked < 2 {
		t.Fatalf("found %d instructions writing %s; the resolver workflows carry two, "+
			"so this test would pass vacuously", checked, prTitleFile)
	}
}

// prTitleRejectPattern lifts the pattern `.github/workflows/pr-title.yml`
// matches a title against out of the guard itself, so this test asserts what
// the required check actually does rather than a copy of it that can drift.
// The guard's ERE spells its whitespace class `[[:space:]]`, which Go's
// regexp accepts unchanged.
func prTitleRejectPattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	path := filepath.Join("..", "..", filepath.FromSlash(prTitleGuard))
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", prTitleGuard, err)
	}
	m := regexp.MustCompile(`(?m)^\s*pattern='([^']+)'\s*$`).FindSubmatch(src)
	if m == nil {
		t.Fatalf("no pattern='…' assignment in %s", prTitleGuard)
	}
	re, err := regexp.Compile(string(m[1]))
	if err != nil {
		t.Fatalf("pattern from %s does not compile in Go: %v", prTitleGuard, err)
	}
	// The lifted pattern is only worth asserting against if it still says
	// what this test believes it says, on the very title task 287 shipped
	// and the hand-repaired one that replaced it (issue #450).
	if !re.MatchString("feat(cli): add vincent github pr link, unlink, show and checks") {
		t.Fatalf("pattern from %s no longer rejects a Conventional Commits title: %s", prTitleGuard, re)
	}
	if re.MatchString("Add vincent github pr link, unlink, show and checks") {
		t.Fatalf("pattern from %s rejects a plain-language title: %s", prTitleGuard, re)
	}
	return re
}

// prTitleInstruction returns the markdown bullet that tells the agent what to
// write into `pr-title.txt` — the bullet naming the file plus the lines
// indented under it, and nothing of the bullets around it. Scoping matters:
// the same prompt correctly tells the agent that *commits* are Conventional
// Commits, and that sentence must not be mistaken for this one.
func prTitleInstruction(prompt string) (string, bool) {
	lines := strings.Split(prompt, "\n")
	for i, line := range lines {
		body := strings.TrimLeft(line, " \t")
		if !strings.Contains(line, prTitleFile) || !strings.HasPrefix(body, "- ") {
			continue
		}
		indent := len(line) - len(body)
		out := []string{line}
		for _, next := range lines[i+1:] {
			trimmed := strings.TrimLeft(next, " \t")
			if trimmed == "" || len(next)-len(trimmed) <= indent {
				break
			}
			out = append(out, next)
		}
		return strings.Join(out, "\n"), true
	}
	return "", false
}

// mentionsOf returns the offsets at which needle appears in text, matched
// without regard to case.
func mentionsOf(text, needle string) []int {
	lower := strings.ToLower(text)
	var out []int
	for from := 0; ; {
		i := strings.Index(lower[from:], needle)
		if i < 0 {
			return out
		}
		out = append(out, from+i)
		from += i + len(needle)
	}
}

// negated reports whether the clause leading up to at warns against what
// follows rather than asking for it.
func negated(text string, at int) bool {
	if at < 0 {
		return false
	}
	from := max(at-prTitleNegationWindow, 0)
	return regexp.MustCompile(prTitleNegation).MatchString(text[from:at])
}

// titleExamples returns the backticked spans of an instruction that could be
// a pull request title rather than a path or an identifier: the ones carrying
// a space. `Add workflow fields` is one, `.vincent-issue/pr-title.txt` is not.
func titleExamples(bullet string) []string {
	var out []string
	for _, span := range regexp.MustCompile("`([^`\n]+)`").FindAllStringSubmatch(bullet, -1) {
		if strings.Contains(span[1], " ") {
			out = append(out, span[1])
		}
	}
	return out
}

// TestPRTitleInstructionVerdicts holds the drift check above to its own
// meaning: the instruction issue #450 reports fails it, the wording CLAUDE.md
// asks for passes it, and the contrast CLAUDE.md draws — naming the rejected
// style in order to warn against it — is not mistaken for asking for it.
func TestPRTitleInstructionVerdicts(t *testing.T) {
	reject := prTitleRejectPattern(t)
	plain := regexp.MustCompile(prTitleVocabulary)

	cases := []struct {
		name   string
		bullet string
		want   bool
	}{
		{
			name: "as shipped",
			bullet: "- `" + prTitleFile + "` — one line, Conventional Commits style,\n" +
				"  no trailing prose.",
			want: false,
		},
		{
			name: "plain language with the contrast",
			bullet: "- `" + prTitleFile + "` — one line, plain language, no trailing\n" +
				"  prose: `Add workflow fields`, never `feat: add workflow fields`.\n" +
				"  GitHub copies the title into the merge commit, so a Conventional\n" +
				"  Commits title makes Release Please record the change twice.",
			want: true,
		},
		{
			name:   "plain language alone",
			bullet: "- `" + prTitleFile + "` — one line, plain-language, no trailing prose.",
			want:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok := plain.MatchString(tc.bullet)
			for _, mention := range mentionsOf(tc.bullet, "conventional commit") {
				ok = ok && negated(tc.bullet, mention)
			}
			for _, example := range titleExamples(tc.bullet) {
				if reject.MatchString(example) {
					ok = ok && negated(tc.bullet, strings.Index(tc.bullet, example))
				}
			}
			if ok != tc.want {
				t.Errorf("instruction accepted = %v, want %v:\n\n%s", ok, tc.want, tc.bullet)
			}
		})
	}
}
