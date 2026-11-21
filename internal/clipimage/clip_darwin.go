package clipimage

import (
	"context"
	"encoding/hex"
	"strings"
)

// osascriptClasses are the clipboard flavours asked for, in order: PNG first,
// which is what a screenshot is, then the two others a browser's "copy image"
// may leave without a PNG beside it.
var osascriptClasses = []string{"PNGf", "JPEG", "GIFf"}

// osascriptArgs is the AppleScript, one `-e` per line: each flavour is tried
// and the first the clipboard can be coerced to is returned; when none can,
// the script errors, and osascript exits non-zero.
//
// osascript prints a data value as text — `«data PNGf89504E47…»` — which is
// why this needs no temporary file: the bytes arrive on standard output, in
// hexadecimal, and parseOsascript reads them back.
func osascriptArgs() []string {
	var args []string
	for _, class := range osascriptClasses {
		args = append(args,
			"-e", "try",
			"-e", "return (the clipboard as «class "+class+"»)",
			"-e", "end try",
		)
	}
	return append(args, "-e", `error "no image on the clipboard"`)
}

// osascriptLimit is the most osascript may print: two hexadecimal digits a
// byte, and the wrapper around them.
const osascriptLimit = 2*MaxBytes + 64

func read(ctx context.Context) ([]byte, error) {
	if !findHelper("osascript") {
		return nil, ErrNoImage
	}
	out, err := runHelper(ctx, osascriptLimit, "osascript", osascriptArgs()...)
	if err != nil {
		return nil, err
	}
	return parseOsascript(out)
}

// parseOsascript reads `«data XXXX<hex>»` back into bytes, XXXX being the
// four-character flavour. Anything else — a string, a file reference, a
// truncated value — is no image.
func parseOsascript(out []byte) ([]byte, error) {
	s := strings.TrimSpace(string(out))
	body, ok := strings.CutPrefix(s, "«data ")
	if !ok {
		return nil, ErrNoImage
	}
	body, ok = strings.CutSuffix(body, "»")
	if !ok || len(body) < 4 {
		return nil, ErrNoImage
	}
	data, err := hex.DecodeString(body[4:])
	if err != nil {
		return nil, ErrNoImage
	}
	return data, nil
}
