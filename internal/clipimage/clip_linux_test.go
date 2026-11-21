package clipimage

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
)

var allHelpers = []string{"wl-paste", "xclip"}

func TestPickImageType(t *testing.T) {
	for _, tc := range []struct{ listed, want string }{
		{"text/plain\nimage/jpeg\nimage/png\n", "image/png"},
		{"TARGETS\r\nimage/gif\r\n", "image/gif"},
		{"image/webp", "image/webp"},
		{"text/plain\nUTF8_STRING\n", ""},
		{"", ""},
	} {
		if got := pickImageType(tc.listed); got != tc.want {
			t.Errorf("pickImageType(%q) = %q, want %q", tc.listed, got, tc.want)
		}
	}
}

type call struct {
	name  string
	args  []string
	limit int64
}

func TestReadLinuxPrefersWlPaste(t *testing.T) {
	var calls []call
	fakeHelpers(t, allHelpers, func(limit int64, name string, args []string) ([]byte, error) {
		calls = append(calls, call{name, args, limit})
		if slices.Contains(args, "--list-types") {
			return []byte("text/plain\nimage/jpeg\n"), nil
		}
		return jpegHead, nil
	})
	data, err := Read(context.Background())
	if err != nil || !bytes.Equal(data, jpegHead) {
		t.Fatalf("Read = %q, %v", data, err)
	}
	want := []call{
		{"wl-paste", []string{"--list-types"}, typesLimit},
		{"wl-paste", []string{"--type", "image/jpeg"}, MaxBytes},
	}
	if !equalCalls(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestReadLinuxFallsBackToXclip(t *testing.T) {
	var calls []call
	fakeHelpers(t, allHelpers, func(limit int64, name string, args []string) ([]byte, error) {
		calls = append(calls, call{name, args, limit})
		switch {
		case name == "wl-paste":
			return nil, errors.New("no wayland display")
		case slices.Contains(args, "TARGETS"):
			return []byte("TARGETS\nimage/png\n"), nil
		}
		return pngHead, nil
	})
	data, err := Read(context.Background())
	if err != nil || !bytes.Equal(data, pngHead) {
		t.Fatalf("Read = %q, %v", data, err)
	}
	want := []call{
		{"wl-paste", []string{"--list-types"}, typesLimit},
		{"xclip", []string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, typesLimit},
		{"xclip", []string{"-selection", "clipboard", "-t", "image/png", "-o"}, MaxBytes},
	}
	if !equalCalls(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestReadLinuxNoImageTarget(t *testing.T) {
	fakeHelpers(t, []string{"xclip"}, func(_ int64, _ string, args []string) ([]byte, error) {
		if slices.Contains(args, "TARGETS") {
			return []byte("UTF8_STRING\ntext/plain\n"), nil
		}
		t.Fatal("fetched a target the clipboard does not offer")
		return nil, nil
	})
	if _, err := Read(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("Read = %v, want ErrNoImage", err)
	}
}

func equalCalls(a, b []call) bool {
	return slices.EqualFunc(a, b, func(x, y call) bool {
		return x.name == y.name && x.limit == y.limit && slices.Equal(x.args, y.args)
	})
}
