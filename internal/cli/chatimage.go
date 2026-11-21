package cli

// `vincent chat image` — a chat's image attachments over POST and GET
// /v1/chats/{id}/images (§5.5, §13.2, task 170). An upload returns the chip,
// `[image N]`, that names the image in a message; the daemon resolves the
// chip for the agent when the turn starts.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

func newChatImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Attach images to a chat and list them",
	}
	cmd.AddCommand(newChatImageAddCmd(), newChatImageListCmd())
	return cmd
}

func newChatImageAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <chat-id> <file>",
		Short: "Upload an image to a chat and print its chip",
		Long: "Upload <file> to the chat and print the chip that names it, `[image N]`. Put " +
			"the chip in a message (`vincent chat send`) and the agent is given the image " +
			"with that turn.\n\n" +
			"The daemon accepts PNG, JPEG, GIF and WebP, judged by content, up to 10 MiB, " +
			"and only on a chat whose agent can be given images; anything else is refused. " +
			"--json prints the image object instead. Exit 1 on a refusal; 2 with no daemon.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			data, err := os.ReadFile(args[1])
			if err != nil {
				return fmt.Errorf("read image: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				img, err := c.AddChatImage(ctx, id, filepath.Base(args[1]), data)
				if err != nil {
					return chatFailure(cmd, err)
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), img)
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), img.Chip)
				return nil
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

func newChatImageListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls <chat-id>",
		Aliases: []string{"list"},
		Short:   "List a chat's images in number order",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				imgs, err := c.ChatImages(ctx, id)
				if err != nil {
					return chatFailure(cmd, err)
				}
				if imgs == nil {
					imgs = []apiclient.ChatImage{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), imgs)
				}
				rows := make([][]string, 0, len(imgs))
				for _, img := range imgs {
					rows = append(rows, []string{
						img.Chip, img.MediaType, strconv.FormatInt(img.Bytes, 10), dash(img.Name),
					})
				}
				return table(cmd.OutOrStdout(), []string{"CHIP", "TYPE", "BYTES", "NAME"}, rows)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}
