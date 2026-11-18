// Package nerdfont installs the one Nerd Font vincent offers for `tui.icons:
// nerd` (task 151), on macOS and Linux.
//
// Nothing is bundled: the release is downloaded from the Nerd Fonts project and
// must match the size and SHA-256 pinned here before it is opened, and each face
// taken from it must match its own pinned hash. Those hashes also answer "is it
// installed" and "is this file ours", so no manifest is kept: uninstalling
// removes a file only when its content is exactly what vincent would have
// written, and a font someone else put there under the same name is left alone.
//
// Installing is per-user and needs no administrator rights. It runs in the
// client, never the daemon, because a font belongs to the machine with the
// screen.
//
// Installing a font does not make a terminal use it, and a program cannot
// reliably find out which font its terminal draws with. Terminal therefore only
// advises, from the environment, and the caller asks the person whether the
// icons look right before turning them on.
//
// It is a leaf: it imports the standard library only.
package nerdfont
