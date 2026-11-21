package chatnote

import "testing"

func TestAdd(t *testing.T) {
	const hd = Heading
	line := Line("a/b.go", "@@ -1 +1 @@")
	for name, tc := range map[string]struct{ draft, want string }{
		"empty draft":         {"", hd + "\n- a/b.go @@ -1 +1 @@\n\n"},
		"plain draft":         {"fix it", hd + "\n- a/b.go @@ -1 +1 @@\n\nfix it"},
		"existing block":      {hd + "\n- x.go @@ -9 +9 @@\n\nfix it", hd + "\n- x.go @@ -9 +9 @@\n- a/b.go @@ -1 +1 @@\n\nfix it"},
		"two lines already":   {hd + "\n- x @@ 1\n- y @@ 2\n\nrest\n- not ours", hd + "\n- x @@ 1\n- y @@ 2\n- a/b.go @@ -1 +1 @@\n\nrest\n- not ours"},
		"block at the end":    {hd + "\n- x @@ 1", hd + "\n- x @@ 1\n- a/b.go @@ -1 +1 @@"},
		"heading alone":       {hd, hd + "\n- a/b.go @@ -1 +1 @@"},
		"heading, no entries": {hd + "\n\nrest", hd + "\n- a/b.go @@ -1 +1 @@\n\nrest"},
		"edited above":        {"hey\n" + hd + "\n- x @@ 1\n", hd + "\n- a/b.go @@ -1 +1 @@\n\nhey\n" + hd + "\n- x @@ 1\n"},
		"heading as a prefix": {hd + " and more", hd + "\n- a/b.go @@ -1 +1 @@\n\n" + hd + " and more"},
	} {
		if got := Add(tc.draft, line); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.want)
		}
	}
}

// A CRLF file's header can carry the line's CR; the note does not.
func TestLineDropsATrailingCR(t *testing.T) {
	if got := Add("", Line("a", "@@ -1 +1 @@ x\r")); got != Heading+"\n- a @@ -1 +1 @@ x\n\n" {
		t.Errorf("CR header: %q", got)
	}
}
