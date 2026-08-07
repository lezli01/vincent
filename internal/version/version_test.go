package version

import (
	"strings"
	"testing"
)

func TestStringNeverPrintsEmptyFields(t *testing.T) {
	got := String()
	if !strings.HasPrefix(got, "vincent version ") {
		t.Fatalf("unexpected prefix in version string: %q", got)
	}
	for _, forbidden := range []string{"  ", "commit ,", "built )"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("empty field leaked into version string: %q", got)
		}
	}
}
