package chatrun

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// A chat's image attachments (task 170, spec §5.5).
//
// A client uploads the bytes; the daemon stores them per chat, numbered from
// 1, and a message names one with the chip `[image N]`. When the turn starts
// the chip is resolved to the stored file and handed to the adapter in
// RunSpec.Images — the message itself goes to the agent as typed. Whether an
// adapter takes images is agent.ImageAttacher's answer and is never emulated.

// MaxImageBytes is the largest image a chat stores.
const MaxImageBytes = 10 << 20

// maxImageNameBytes bounds the informational file name kept with an image.
const maxImageNameBytes = 255

// ImageRefusal is an image, or a message naming one, that the chat cannot
// take: not an image vincent stores, too large, a chip with no image behind
// it, or an agent that cannot be given images. The API answers it as a
// validation failure with the message as written.
type ImageRefusal struct{ Message string }

func (e *ImageRefusal) Error() string { return e.Message }

func refuseImage(format string, args ...any) error {
	return &ImageRefusal{Message: fmt.Sprintf(format, args...)}
}

// imageTypes are the formats a chat stores, by sniffed media type, with the
// extension the stored file gets.
var imageTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// SniffImage reports the media type of data when it is a PNG, JPEG, GIF or
// WebP image. It reads the content alone — never a file name or a
// Content-Type header, both of which the uploader chooses.
func SniffImage(data []byte) (mediaType string, ok bool) {
	mediaType = http.DetectContentType(data)
	_, ok = imageTypes[mediaType]
	return mediaType, ok
}

// imageChip matches the composer's chip for image N.
var imageChip = regexp.MustCompile(`\[image (\d+)\]`)

// ImageChip is the chip that names image n in a message.
func ImageChip(n int) string { return "[image " + strconv.Itoa(n) + "]" }

// ParseImageChips returns the image numbers text names, each once, in the
// order they first appear.
func ParseImageChips(text string) []int {
	var out []int
	seen := map[int]bool{}
	for _, m := range imageChip.FindAllStringSubmatch(text, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// imageDir is where a chat's images live: under the chat's own directory in
// the data dir, beside its transcripts, so a permanent delete removes them.
func (r *Runner) imageDir(chatID int64) string {
	return filepath.Join(r.deps.DataDir, "transcripts", worktree.ChatOwner(chatID).Dir(), "images")
}

// ImagesUnavailable is why chat cannot be given images, or "" when it can.
// Two things decide it: the adapter's own capability, and — for a chat
// linked to a task whose workflow runs in a container — that the stored file
// is in the daemon's data directory, which is not mounted in there. Nothing
// is copied into a container to make it work.
func (r *Runner) ImagesUnavailable(ctx context.Context, chat *store.Chat) (string, error) {
	adapter, ok := r.deps.Agents.Get(chat.Agent)
	if !ok {
		return fmt.Sprintf("no adapter named %q", chat.Agent), nil
	}
	if !agent.CanAttachImages(adapter) {
		return fmt.Sprintf("the %s agent cannot be given images", chat.Agent), nil
	}
	if chat.Linked() && r.deps.InContainer != nil {
		in, err := r.deps.InContainer(ctx, *chat.LinkedTaskID)
		if err != nil {
			return "", err
		}
		if in {
			return fmt.Sprintf("the %s agent cannot be given images in this chat: its turns run in "+
				"task %d's container, where a stored image is not reachable", chat.Agent, *chat.LinkedTaskID), nil
		}
	}
	return "", nil
}

// AddImage stores data as the chat's next image. It refuses a terminal chat
// with store.ErrInvalidChatAction, and with an *ImageRefusal anything that
// is not a PNG, JPEG, GIF or WebP by content, anything over MaxImageBytes,
// and any chat that cannot be given images.
func (r *Runner) AddImage(ctx context.Context, chat *store.Chat, name string, data []byte) (*store.ChatImage, error) {
	if chatstate.Terminal(chat.State) {
		return nil, fmt.Errorf("chat %d: %w", chat.ID, store.ErrInvalidChatAction)
	}
	if len(data) > MaxImageBytes {
		return nil, refuseImage("the image is larger than %d MiB", MaxImageBytes>>20)
	}
	mediaType, ok := SniffImage(data)
	if !ok {
		return nil, refuseImage("the upload is not a PNG, JPEG, GIF or WebP image")
	}
	why, err := r.ImagesUnavailable(ctx, chat)
	if err != nil {
		return nil, err
	}
	if why != "" {
		return nil, refuseImage("%s", why)
	}
	dir := r.imageDir(chat.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chat %d images: %w", chat.ID, err)
	}
	img, err := r.deps.Store.AddChatImage(ctx, chat.ID, mediaType, cleanImageName(name), int64(len(data)),
		func(n int) string { return filepath.Join(dir, strconv.Itoa(n)+imageTypes[mediaType]) })
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(img.Path, data, 0o600); err != nil {
		// The number is given back: a row with no file behind it would be a
		// chip that resolves to nothing.
		if rmErr := r.deps.Store.RemoveChatImage(r.persistCtx(), chat.ID, img.Number); rmErr != nil {
			r.deps.Logger.Error("chat image row left behind", "chat", chat.ID, "number", img.Number, "error", rmErr)
		}
		return nil, fmt.Errorf("store chat %d image: %w", chat.ID, err)
	}
	return img, nil
}

// cleanImageName is the informational name kept with an image: the last
// path element, valid UTF-8, bounded.
func cleanImageName(name string) string {
	name = strings.ToValidUTF8(name, "")
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	for len(name) > maxImageNameBytes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

// ResolveImages resolves the `[image N]` chips in text to the chat's stored
// files, as absolute paths in chip order.
//
// Only a match naming an image this chat has is a chip (review F4 of task
// 170): anything else — `[image 1]` in pasted Markdown, a log line, prose
// about the feature, or a number the chat never had — is the human's text
// and goes to the agent as typed, because the composer's text is never
// interpreted (§5.5). A message naming no image of the chat resolves to
// nothing and asks nothing of the adapter. A chat that has images it can no
// longer give — its task now runs in a container, say — is refused with an
// *ImageRefusal naming the agent rather than sent without them.
//
// It is the one function a send's validation and a turn's start both run,
// so the two cannot disagree — and a queued message is resolved when its
// turn starts, which is when it reaches the agent.
func (r *Runner) ResolveImages(ctx context.Context, chat *store.Chat, text string) ([]string, error) {
	_, paths, err := r.resolveImages(ctx, chat, text)
	return paths, err
}

// resolveImages is ResolveImages with the numbers of the chips it resolved.
func (r *Runner) resolveImages(ctx context.Context, chat *store.Chat, text string) ([]int, []string, error) {
	named := ParseImageChips(text)
	if len(named) == 0 {
		return nil, nil, nil
	}
	images, err := r.deps.Store.ListChatImages(ctx, chat.ID)
	if err != nil {
		return nil, nil, err
	}
	byNumber := make(map[int]string, len(images))
	for _, img := range images {
		byNumber[img.Number] = img.Path
	}
	var (
		chips []int
		paths []string
	)
	for _, n := range named {
		if path, ok := byNumber[n]; ok {
			chips = append(chips, n)
			paths = append(paths, path)
		}
	}
	if len(chips) == 0 {
		return nil, nil, nil
	}
	why, err := r.ImagesUnavailable(ctx, chat)
	if err != nil {
		return nil, nil, err
	}
	if why != "" {
		return nil, nil, refuseImage("%s, and the message names %s", why, ImageChip(chips[0]))
	}
	return chips, paths, nil
}

// IsImageRefusal reports whether err is an *ImageRefusal, and its message.
func IsImageRefusal(err error) (string, bool) {
	var refusal *ImageRefusal
	if errors.As(err, &refusal) {
		return refusal.Message, true
	}
	return "", false
}

// persistCtx is the context a write that must survive shutdown uses.
func (r *Runner) persistCtx() context.Context {
	if r.persist != nil {
		return r.persist
	}
	return context.Background()
}
