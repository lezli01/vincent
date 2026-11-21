package clipimage

import (
	"bytes"
	"context"
	"encoding/base64"
)

// powershellScript puts the clipboard's image on standard output as base64
// PNG. Base64 because PowerShell's pipeline is text: raw bytes written to it
// are re-encoded on the way out. PNG always, because .NET hands back a bitmap
// whatever was copied and PNG is the lossless way to write one. No image
// exits 1 with nothing written.
const powershellScript = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms, System.Drawing
$image = [System.Windows.Forms.Clipboard]::GetImage()
if ($null -eq $image) { exit 1 }
$stream = New-Object System.IO.MemoryStream
$image.Save($stream, [System.Drawing.Imaging.ImageFormat]::Png)
[Console]::Out.Write([Convert]::ToBase64String($stream.ToArray()))`

// powershells are the hosts tried, in order: Windows PowerShell, which every
// Windows has and which loads Windows Forms without help, then PowerShell 7.
var powershells = []string{"powershell", "pwsh"}

// powershellArgs runs the script without a profile, which could print to the
// output or take seconds, and in a single-threaded apartment, which the
// clipboard's COM interface requires and PowerShell 7 does not default to.
func powershellArgs() []string {
	return []string{"-NoProfile", "-NonInteractive", "-STA", "-Command", powershellScript}
}

// powershellLimit is the most the script may print: base64 is four characters
// for every three bytes.
const powershellLimit = MaxBytes/3*4 + 64

func read(ctx context.Context) ([]byte, error) {
	for _, shell := range powershells {
		if !findHelper(shell) {
			continue
		}
		out, err := runHelper(ctx, powershellLimit, shell, powershellArgs()...)
		if err != nil {
			return nil, err
		}
		return parsePowershell(out)
	}
	return nil, ErrNoImage
}

// parsePowershell decodes the script's base64. Empty or malformed output is
// no image.
func parsePowershell(out []byte) ([]byte, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, ErrNoImage
	}
	data := make([]byte, base64.StdEncoding.DecodedLen(len(out)))
	n, err := base64.StdEncoding.Decode(data, out)
	if err != nil {
		return nil, ErrNoImage
	}
	return data[:n], nil
}
