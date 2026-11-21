package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// A chat's image attachments (task 170, §5.5, §13.2). A client uploads the
// bytes and puts the returned chip in the message; the daemon resolves it
// for the agent when the turn starts.

// ChatImage is one image stored on a chat. Chip is the text that names it in
// a message, `[image N]`.
type ChatImage struct {
	Number    int    `json:"number"`
	Chip      string `json:"chip"`
	MediaType string `json:"media_type"`
	Bytes     int64  `json:"bytes"`
	Name      string `json:"name,omitempty"`
}

// AddChatImage is POST /v1/chats/{id}/images: data is the image's bytes and
// name its original file name, informational. The daemon accepts PNG, JPEG,
// GIF and WebP by content, up to 10 MiB, and only on a chat whose agent can
// be given images (Agent.SupportsImages, and never a linked chat on a
// containerized task); anything else is a 422 validation_failed.
func (c *Client) AddChatImage(ctx context.Context, chatID int64, name string, data []byte) (*ChatImage, error) {
	path := fmt.Sprintf("/v1/chats/%d/images", chatID)
	if name != "" {
		path += "?" + url.Values{"name": {name}}.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.rest.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, decodeError(resp)
	}
	var out ChatImage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", path, err)
	}
	return &out, nil
}

// ChatImages is GET /v1/chats/{id}/images: the chat's images in number
// order.
func (c *Client) ChatImages(ctx context.Context, chatID int64) ([]ChatImage, error) {
	var out struct {
		Images []ChatImage `json:"images"`
	}
	if err := c.get(ctx, fmt.Sprintf("/v1/chats/%d/images", chatID), &out); err != nil {
		return nil, err
	}
	return out.Images, nil
}
