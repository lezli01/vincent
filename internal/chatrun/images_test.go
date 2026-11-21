package chatrun

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// Minimal headers of each stored format: the sniff reads content, so these
// are images as far as the daemon can tell.
var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpegBytes = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	gifBytes  = []byte("GIF89a\x01\x00\x01\x00")
	webpBytes = []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
)

func refused(t *testing.T, err error, want string) {
	t.Helper()
	msg, ok := IsImageRefusal(err)
	if !ok || !strings.Contains(msg, want) {
		t.Errorf("error = %v, want an image refusal naming %q", err, want)
	}
}

func TestAddImageSniffsNumbersAndBounds(t *testing.T) {
	h := newHarness(t)
	c := h.chatOn(t, "codex")
	for i, tc := range []struct {
		data  []byte
		media string
		ext   string
	}{
		{pngBytes, "image/png", ".png"},
		{jpegBytes, "image/jpeg", ".jpg"},
		{gifBytes, "image/gif", ".gif"},
		{webpBytes, "image/webp", ".webp"},
	} {
		// The name lies about the type on purpose: only the content counts.
		img, err := h.runner.AddImage(t.Context(), c, "dir/shot.txt", tc.data)
		if err != nil {
			t.Fatalf("AddImage(%s): %v", tc.media, err)
		}
		if img.Number != i+1 || img.MediaType != tc.media || img.Bytes != int64(len(tc.data)) || img.Name != "shot.txt" {
			t.Errorf("image %d = %+v", i+1, img)
		}
		if filepath.Ext(img.Path) != tc.ext || !filepath.IsAbs(img.Path) {
			t.Errorf("stored at %q, want an absolute %s path", img.Path, tc.ext)
		}
		if got, err := os.ReadFile(img.Path); err != nil || !bytes.Equal(got, tc.data) {
			t.Errorf("stored bytes differ (%v)", err)
		}
	}

	_, err := h.runner.AddImage(t.Context(), c, "notes.png", []byte("just some text"))
	refused(t, err, "not a PNG, JPEG, GIF or WebP")
	_, err = h.runner.AddImage(t.Context(), c, "page.png", []byte("%PDF-1.7 not an image"))
	refused(t, err, "not a PNG, JPEG, GIF or WebP")
	big := append(slices.Clone(pngBytes), make([]byte, MaxImageBytes)...)
	_, err = h.runner.AddImage(t.Context(), c, "big.png", big)
	refused(t, err, "larger than 10 MiB")
	if imgs, _ := h.store.ListChatImages(t.Context(), c.ID); len(imgs) != 4 {
		t.Errorf("a refused upload took a number: %d images", len(imgs))
	}

	if _, err := h.store.SetChatState(t.Context(), c.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}
	c.State = chatstate.Archived
	if _, err := h.runner.AddImage(t.Context(), c, "", pngBytes); !errors.Is(err, store.ErrInvalidChatAction) {
		t.Errorf("AddImage on an archived chat = %v, want ErrInvalidChatAction", err)
	}
}

// TestImageChipsReachTheAdapter runs a turn on each adapter that has the
// capability and reads back what the fake CLI was passed.
func TestImageChipsReachTheAdapter(t *testing.T) {
	for _, agentName := range []string{"codex", "copilot"} {
		t.Run(agentName, func(t *testing.T) {
			h := newHarness(t)
			record := filepath.Join(t.TempDir(), "images")
			t.Setenv("FAKEAGENT_IMAGES_FILE", record)
			c := h.chatOn(t, agentName)
			one, err := h.runner.AddImage(t.Context(), c, "one.png", pngBytes)
			if err != nil {
				t.Fatalf("AddImage: %v", err)
			}
			two, err := h.runner.AddImage(t.Context(), c, "two.gif", gifBytes)
			if err != nil {
				t.Fatalf("AddImage: %v", err)
			}
			// Chip order, not upload order, and a repeated chip once.
			msg := "compare [image 2] with [image 1], then [image 2] again"
			turn := h.sendAndWait(t, c.ID, msg)
			if turn.State != chatstate.TurnDone || turn.Prompt != msg {
				t.Fatalf("turn = %s %q (%s)", turn.State, turn.Prompt, turn.ErrorMessage)
			}
			data, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("the %s dialect recorded no image: %v", agentName, err)
			}
			got := strings.Fields(strings.ReplaceAll(string(data), "\r", ""))
			if !slices.Equal(got, []string{two.Path, one.Path}) {
				t.Errorf("%s was passed %q, want [%s %s]", agentName, got, two.Path, one.Path)
			}

			// A message naming no image passes none.
			if err := os.Remove(record); err != nil {
				t.Fatal(err)
			}
			h.sendAndWait(t, c.ID, "thanks")
			if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a turn with no chip passed an image (%v)", err)
			}

			// A number the chat never had is the human's text, not a
			// chip (review F4): it resolves to nothing, beside a real one.
			paths, err := h.runner.ResolveImages(t.Context(), c, "see [image 9] and [image 1]")
			if err != nil || !slices.Equal(paths, []string{one.Path}) {
				t.Errorf("ResolveImages beside an unknown number = %v, %v; want [%s]", paths, err, one.Path)
			}
		})
	}
}

func TestImagesRefusedWithoutTheCapability(t *testing.T) {
	h := newHarness(t)
	for _, agentName := range []string{"claude", "cursor"} {
		c := h.chatOn(t, agentName)
		_, err := h.runner.AddImage(t.Context(), c, "a.png", pngBytes)
		refused(t, err, "the "+agentName+" agent cannot be given images")
		// Such a chat can hold no image, so `[image 1]` in its message is
		// the human's own text and is sent as typed (review F4); so is a
		// message with no chip at all.
		for _, msg := range []string{"look at [image 1]", "no image here"} {
			if paths, err := h.runner.ResolveImages(t.Context(), c, msg); err != nil || paths != nil {
				t.Errorf("%s: ResolveImages(%q) = %v, %v", agentName, msg, paths, err)
			}
		}
		// The send is accepted and the agent gets the text verbatim.
		msg := "the docs say `[image 1]` names a pasted image"
		if turn := h.sendAndWait(t, c.ID, msg); turn.State != chatstate.TurnDone || turn.Prompt != msg {
			t.Errorf("%s: a send naming [image 1] in prose = %s %q (%s)", agentName, turn.State, turn.Prompt, turn.ErrorMessage)
		}
	}
}

// TestImagesRefusedInATaskContainer is the container case: the stored file
// is in the daemon's data directory, which the task's container does not
// mount, so the capability is unsupported there and nothing is copied in.
func TestImagesRefusedInATaskContainer(t *testing.T) {
	h := newHarness(t)
	task, c := h.linkedTask(t, h.repo)
	// What decides it is the chat the caller holds, so the agent is set on
	// that: the store's row stays the linked chat it was opened as.
	c.Agent = "codex"
	contained := true
	h.runner.deps.InContainer = func(_ context.Context, taskID int64) (bool, error) {
		if taskID != task.ID {
			t.Errorf("InContainer asked about task %d, want %d", taskID, task.ID)
		}
		return contained, nil
	}
	_, err := h.runner.AddImage(t.Context(), c, "a.png", pngBytes)
	refused(t, err, "container")
	refused(t, err, "codex")

	// The same chat on a host task takes the image.
	contained = false
	if _, err := h.runner.AddImage(t.Context(), c, "a.png", pngBytes); err != nil {
		t.Errorf("AddImage on a host task: %v", err)
	}
	// An image the chat holds, once its task runs in a container, cannot be
	// given: the chip naming it is refused rather than sent without it.
	contained = true
	_, err = h.runner.ResolveImages(t.Context(), c, "[image 1]")
	refused(t, err, "container")
}

func TestParseImageChips(t *testing.T) {
	got := ParseImageChips("a [image 3] b [image 1][image 3] [image x] [image] [Image 2]")
	if !slices.Equal(got, []int{3, 1}) {
		t.Errorf("ParseImageChips = %v, want [3 1]", got)
	}
}
