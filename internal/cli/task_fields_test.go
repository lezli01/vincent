package cli

import (
	"reflect"
	"testing"
)

func TestParseFieldFlags(t *testing.T) {
	got, err := parseFieldFlags([]string{
		"ticket=OPS-42",
		"url=https://example.test/?a=b",
		"ticket=OPS-43",
	})
	if err != nil {
		t.Fatalf("parseFieldFlags: %v", err)
	}
	want := map[string]string{
		"ticket": "OPS-43", // later values win
		"url":    "https://example.test/?a=b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}

	for _, value := range []string{"ticket", "=value", "  =value"} {
		if _, err := parseFieldFlags([]string{value}); err == nil {
			t.Errorf("parseFieldFlags(%q) succeeded", value)
		}
	}
}
