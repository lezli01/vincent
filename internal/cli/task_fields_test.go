package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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

// TestReadFieldsFileAcceptsAnObjectOfStrings covers the shape --fields-file
// exists for: a generated JSON document, from a file and from stdin, whose
// values may carry the spaces, newlines, Unicode and `=` a command line makes
// awkward.
func TestReadFieldsFileAcceptsAnObjectOfStrings(t *testing.T) {
	const doc = `{
	  "ticket": "OPS-42",
	  "url": "https://example.test/?a=b",
	  "note": "two words\nand a second line",
	  "who": "안나",
	  "empty": ""
	}`
	want := map[string]string{
		"ticket": "OPS-42",
		"url":    "https://example.test/?a=b",
		"note":   "two words\nand a second line",
		"who":    "안나",
		"empty":  "",
	}

	path := filepath.Join(t.TempDir(), "fields.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write fields file: %v", err)
	}
	got, err := readFieldsFile(path, nil)
	if err != nil {
		t.Fatalf("readFieldsFile(file): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("from file = %v, want %v", got, want)
	}

	// `-` reads the same document from stdin, which is the piped-from-jq form.
	got, err = readFieldsFile("-", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("readFieldsFile(-): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("from stdin = %v, want %v", got, want)
	}

	// An object with a key repeated inside it is last-wins, matching --field.
	got, err = readFieldsFile("-", strings.NewReader(`{"ticket":"OPS-1","ticket":"OPS-2"}`))
	if err != nil {
		t.Fatalf("readFieldsFile(duplicate key): %v", err)
	}
	if got["ticket"] != "OPS-2" {
		t.Errorf("duplicate key = %q, want the later value OPS-2", got["ticket"])
	}
}

// TestReadFieldsFileRejections: every rejection names the offending key at
// most, and never the value behind it (task 045 decision 4) — an error message
// is scrollback and CI logs.
func TestReadFieldsFileRejections(t *testing.T) {
	secret := "hunter2-do-not-print"
	cases := []struct {
		name string
		doc  string
		key  string // named in the message when the key is what is wrong
	}{
		{"number value", `{"retries": 3}`, "retries"},
		{"boolean value", `{"dry-run": true}`, "dry-run"},
		{"null value", `{"owner": null}`, "owner"},
		{"array value", `{"labels": ["` + secret + `"]}`, "labels"},
		{"object value", `{"meta": {"token": "` + secret + `"}}`, "meta"},
		{"empty key", `{"": "` + secret + `"}`, ""},
		{"blank key", `{"   ": "` + secret + `"}`, ""},
		{"not an object", `["` + secret + `"]`, ""},
		{"null document", `null`, ""},
		{"trailing content", `{"ticket":"OPS-42"} {"ticket":"OPS-43"}`, ""},
		{"trailing junk", `{"ticket":"OPS-42"} nonsense`, ""},
		{"empty document", ``, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readFieldsFile("-", strings.NewReader(tc.doc))
			if err == nil {
				t.Fatalf("readFieldsFile(%s) succeeded with %v", tc.doc, got)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error echoes a field value: %v", err)
			}
			if tc.key != "" && !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error does not name the key %q: %v", tc.key, err)
			}
		})
	}
}

// TestReadFieldsFileBound: stdin can be an unbounded pipe, so the read is
// capped at the API's own large-body bound and the message says which.
func TestReadFieldsFileBound(t *testing.T) {
	// One byte past the bound: an object whose single value fills the rest.
	value := strings.Repeat("x", maxFieldsFileBytes)
	_, err := readFieldsFile("-", strings.NewReader(`{"big":"`+value+`"}`))
	if err == nil {
		t.Fatal("an over-bound document was accepted")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(maxFieldsFileBytes)) {
		t.Errorf("error does not name the limit: %v", err)
	}
	if strings.Contains(err.Error(), "xxxx") {
		t.Errorf("error echoes the body: %v", err)
	}

	// Exactly at the bound still parses: the cap is inclusive.
	fit := `{"big":"` + strings.Repeat("x", maxFieldsFileBytes-10) + `"}`
	if len(fit) != maxFieldsFileBytes {
		t.Fatalf("fixture is %d bytes, want exactly %d", len(fit), maxFieldsFileBytes)
	}
	if _, err := readFieldsFile("-", strings.NewReader(fit)); err != nil {
		t.Errorf("a document exactly at the bound was rejected: %v", err)
	}
}

func TestReadFieldsFileMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-file.json")
	if _, err := readFieldsFile(missing, nil); err == nil {
		t.Fatal("a missing --fields-file was accepted")
	}
}

// TestMergeFields: the file supplies the base map and each --field overrides
// its own key (task 045 decision 2), and neither flag leaves the request's
// field map nil so an unchanged command line sends what it always sent.
func TestMergeTaskFields(t *testing.T) {
	cases := []struct {
		name        string
		file, flags map[string]string
		want        map[string]string
	}{
		{"neither", nil, nil, nil},
		{"flags only", nil, map[string]string{"a": "1"}, map[string]string{"a": "1"}},
		{"file only", map[string]string{"a": "1"}, nil, map[string]string{"a": "1"}},
		{
			"disjoint keys union",
			map[string]string{"a": "1"},
			map[string]string{"b": "2"},
			map[string]string{"a": "1", "b": "2"},
		},
		{
			"--field wins the shared key",
			map[string]string{"a": "file", "b": "2"},
			map[string]string{"a": "flag"},
			map[string]string{"a": "flag", "b": "2"},
		},
		{"empty file object", map[string]string{}, nil, map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeTaskFields(tc.file, tc.flags)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeTaskFields = %v, want %v", got, tc.want)
			}
			// nil is not the same as empty here: it is what keeps a task
			// created with neither flag byte for byte unchanged on the wire.
			if (got == nil) != (tc.want == nil) {
				t.Errorf("mergeTaskFields nil-ness = %v, want %v", got == nil, tc.want == nil)
			}
		})
	}
}

// TestFieldsSummary: names and a count, sorted, never a value.
func TestFieldsSummary(t *testing.T) {
	if got := fieldsSummary(nil); got != "" {
		t.Errorf("fieldsSummary(nil) = %q, want empty", got)
	}
	if got := fieldsSummary(map[string]string{}); got != "" {
		t.Errorf("fieldsSummary(empty) = %q, want empty", got)
	}
	got := fieldsSummary(map[string]string{
		"ticket": "OPS-42", "owner": "ana", "api-key": "hunter2",
	})
	if want := "fields: api-key, owner, ticket (3)"; got != want {
		t.Errorf("fieldsSummary = %q, want %q", got, want)
	}
	for _, value := range []string{"OPS-42", "ana", "hunter2"} {
		if strings.Contains(got, value) {
			t.Errorf("fieldsSummary echoes the value %q: %q", value, got)
		}
	}
}
