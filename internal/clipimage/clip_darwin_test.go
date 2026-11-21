package clipimage

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

var allHelpers = []string{"osascript"}

func TestOsascriptArgs(t *testing.T) {
	args := osascriptArgs()
	if len(args)%2 != 0 {
		t.Fatalf("args are not -e pairs: %q", args)
	}
	var lines []string
	for i := 0; i < len(args); i += 2 {
		if args[i] != "-e" {
			t.Fatalf("args[%d] = %q, want -e", i, args[i])
		}
		lines = append(lines, args[i+1])
	}
	script := strings.Join(lines, "\n")
	png := strings.Index(script, "«class PNGf»")
	jpeg := strings.Index(script, "«class JPEG»")
	if png < 0 || jpeg < 0 || png > jpeg {
		t.Fatalf("PNG is not asked for first:\n%s", script)
	}
	if !strings.HasPrefix(lines[len(lines)-1], "error ") {
		t.Fatalf("script does not fail without an image:\n%s", script)
	}
}

func TestParseOsascript(t *testing.T) {
	good := "«data PNGf" + strings.ToUpper(hex.EncodeToString(pngHead)) + "»\n"
	got, err := parseOsascript([]byte(good))
	if err != nil || !bytes.Equal(got, pngHead) {
		t.Fatalf("parseOsascript = %q, %v", got, err)
	}
	for _, bad := range []string{"", "hello", "«data PNGf", "«data PNGfZZ»", "«data PN»", "file Macintosh HD:a.png"} {
		if _, err := parseOsascript([]byte(bad)); !errors.Is(err, ErrNoImage) {
			t.Errorf("parseOsascript(%q) = %v, want ErrNoImage", bad, err)
		}
	}
}

func TestReadDarwin(t *testing.T) {
	var gotName string
	var gotLimit int64
	fakeHelpers(t, allHelpers, func(limit int64, name string, _ []string) ([]byte, error) {
		gotName, gotLimit = name, limit
		return []byte("«data PNGf" + hex.EncodeToString(pngHead) + "»"), nil
	})
	data, err := Read(context.Background())
	if err != nil || !bytes.Equal(data, pngHead) {
		t.Fatalf("Read = %q, %v", data, err)
	}
	if gotName != "osascript" || gotLimit != osascriptLimit {
		t.Fatalf("ran %s with limit %d", gotName, gotLimit)
	}
}

func TestReadDarwinNotAnImage(t *testing.T) {
	fakeHelpers(t, allHelpers, func(int64, string, []string) ([]byte, error) {
		return []byte("«data PNGf" + hex.EncodeToString([]byte("plain text")) + "»"), nil
	})
	if _, err := Read(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("Read = %v, want ErrNoImage", err)
	}
}
