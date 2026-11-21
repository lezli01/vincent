package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/clipimage"
)

// Image paste in the chat composer (task 170).
//
// ctrl+v reads the clipboard for an image first. One is uploaded to the
// chat — POST /v1/chats/{id}/images — and the daemon's chip, `[image N]`,
// goes into the draft at the cursor as plain text; the daemon resolves the
// chip when the message is sent. No image falls back to today's text paste,
// silently, so ctrl+v over text behaves exactly as it did. A pasted path to
// an image file — what a terminal pastes for a file dropped on it — is
// uploaded the same way, and its chip replaces the path.
//
// The client holds nothing of an image: the chip is text, and the daemon
// owns the bytes.

// clipImageRead is the clipboard read, a variable so tests inject a fake
// clipboard instead of shelling out to the platform's helper.
var clipImageRead = clipimage.Read

// readImageFile reads a pasted image path, a variable for the same reason.
var readImageFile = os.ReadFile

// Image messages.
type (
	// chatImageClipMsg is an image the clipboard or a pasted path gave up,
	// on its way to the upload. name is the file's base name when it came
	// from a path, "" from the clipboard.
	chatImageClipMsg struct {
		chatID int64
		name   string
		data   []byte
	}
	// chatImageAddedMsg reports the upload.
	chatImageAddedMsg struct {
		chatID int64
		image  *apiclient.ChatImage
		err    error
	}
)

// takesImagePaste reports whether ctrl+v is the composer's: no popup or
// layer above it owns the keyboard.
func (v *chatView) takesImagePaste() bool {
	return v.form == nil && !v.convFocus && !v.diff.open && !v.shell.prompt &&
		v.picker == nil && !v.history.open && v.composer.Focused()
}

// pasteImageCmd is ctrl+v in the composer: the clipboard's image if it has
// one, else the clipboard's text exactly as readClipboardCmd delivers it.
func (v *chatView) pasteImageCmd() tea.Cmd {
	if !v.takesImagePaste() {
		return readClipboardCmd()
	}
	id := v.chatID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		data, err := clipImageRead(ctx)
		if err != nil || len(data) == 0 {
			// ErrNoImage, or a helper that failed: either way there is no
			// image to offer, and the text paste is what ctrl+v always did.
			return readClipboardCmd()()
		}
		return chatImageClipMsg{chatID: id, data: data}
	}
}

// pasteImagePathCmd is a pasted text that names an image file. A file that
// cannot be read is pasted as the text it was, which is what it was before
// this feature.
func (v *chatView) pasteImagePathCmd(path, text string) tea.Cmd {
	id := v.chatID
	return func() tea.Msg {
		data, err := readImageFile(path)
		if err != nil || len(data) == 0 {
			return chatImagePathFailedMsg{chatID: id, text: text}
		}
		return chatImageClipMsg{chatID: id, name: filepath.Base(path), data: data}
	}
}

// chatImagePathFailedMsg sends a pasted path that could not be read back to
// the composer as text.
type chatImagePathFailedMsg struct {
	chatID int64
	text   string
}

// imagesRefused is the note for an agent that says it takes no images, or ""
// when it takes them or has not said. An agent not yet known is let through:
// the daemon refuses it with a 422 that names the agent anyway.
func (v *chatView) imagesRefused() string {
	if v.chat == nil {
		return ""
	}
	if a, ok := v.agents[v.chat.Agent]; ok && a.SupportsImages != nil && !*a.SupportsImages {
		return v.chat.Agent + " cannot take images — nothing was pasted"
	}
	return ""
}

// applyImageClip uploads an image, after refusing locally for an agent that
// cannot take one.
func (v *chatView) applyImageClip(msg chatImageClipMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	if why := v.imagesRefused(); why != "" {
		v.note, v.noteBad = why, true
		return nil
	}
	client, id := v.client, v.chatID
	if client == nil {
		return nil
	}
	v.note, v.noteBad = "uploading the image…", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		img, err := client.AddChatImage(ctx, id, msg.name, msg.data)
		return chatImageAddedMsg{chatID: id, image: img, err: err}
	}
}

// applyImageAdded puts the chip in the draft at the cursor, or the daemon's
// refusal in the note with nothing inserted.
func (v *chatView) applyImageAdded(msg chatImageAddedMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	if msg.err != nil || msg.image == nil {
		v.note, v.noteBad = "image: "+errString(cmpErr(msg.err)), true
		return nil
	}
	v.note, v.noteBad = "", false
	before := v.composer.Value()
	v.composer.InsertString(msg.image.Chip)
	return v.draftEdited(before)
}

// cmpErr names a nil error with no image, which only a daemon answering 2xx
// with an empty body can produce.
func cmpErr(err error) error {
	if err == nil {
		return errors.New("the daemon returned no image")
	}
	return err
}
