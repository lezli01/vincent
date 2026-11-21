package agent

// ImageAttacher is the optional capability an adapter implements when its
// CLI takes image files for a run by path (task 170, spec §9.1). It is an
// optional interface for the reason Resumer is: an adapter that says nothing
// cannot be given an image, and the capability is never emulated — vincent
// does not inline an image into the prompt, nor tell the agent to go and read
// the file.
//
// It is true only for an adapter whose CLI documents an argv spelling for a
// local image path, pinned to a help fixture named for the CLI version:
// codex's `-i, --image <FILE>` (testdata/help_exec_0.169.0.txt and
// help_exec_resume_0.169.0.txt) and copilot's `--attachment <path>`
// (testdata/help_1.0.105.txt). claude 2.1.330's `--help` names no flag that
// takes a local image (`--file` downloads by file id), and cursor-agent
// 2026.09.18's names none at all, so neither implements this.
type ImageAttacher interface {
	// SupportsImages reports whether Start honors RunSpec.Images.
	SupportsImages() bool
}

// CanAttachImages reports whether a takes RunSpec.Images (see ImageAttacher).
// It is what a chat consults before storing an image and before a send that
// names one.
func CanAttachImages(a Adapter) bool {
	i, ok := a.(ImageAttacher)
	return ok && i.SupportsImages()
}
