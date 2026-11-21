package clipimage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"testing"
)

var allHelpers = []string{"powershell", "pwsh"}

func TestPowershellArgs(t *testing.T) {
	args := powershellArgs()
	for _, flag := range []string{"-NoProfile", "-NonInteractive", "-STA", "-Command"} {
		if !slices.Contains(args, flag) {
			t.Errorf("args lack %s: %q", flag, args)
		}
	}
	if args[len(args)-1] != powershellScript {
		t.Fatalf("the script is not the last argument: %q", args)
	}
}

func TestParsePowershell(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString(pngHead)
	got, err := parsePowershell([]byte(enc + "\r\n"))
	if err != nil || !bytes.Equal(got, pngHead) {
		t.Fatalf("parsePowershell = %q, %v", got, err)
	}
	for _, bad := range []string{"", "  \r\n", "not base64!"} {
		if _, err := parsePowershell([]byte(bad)); !errors.Is(err, ErrNoImage) {
			t.Errorf("parsePowershell(%q) = %v, want ErrNoImage", bad, err)
		}
	}
}

func TestReadWindows(t *testing.T) {
	var names []string
	fakeHelpers(t, []string{"pwsh"}, func(limit int64, name string, _ []string) ([]byte, error) {
		names = append(names, name)
		if limit != powershellLimit {
			t.Errorf("limit = %d, want %d", limit, powershellLimit)
		}
		return []byte(base64.StdEncoding.EncodeToString(gifHead)), nil
	})
	data, err := Read(context.Background())
	if err != nil || !bytes.Equal(data, gifHead) {
		t.Fatalf("Read = %q, %v", data, err)
	}
	if !slices.Equal(names, []string{"pwsh"}) {
		t.Fatalf("ran %q, want pwsh alone", names)
	}
}
