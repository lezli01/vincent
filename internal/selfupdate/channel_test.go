package selfupdate

import "testing"

// Channel detection over synthetic executable paths per GOOS. It runs the
// whole table on every platform because classify is pure — that is the reason
// Detect is split into an os.Executable half and this one, since a Windows
// path can only be exercised on Linux if nothing touches the filesystem.
func TestClassify(t *testing.T) {
	env := map[string]string{
		"HOME":            "/Users/ada",
		"USERPROFILE":     `C:\Users\ada`,
		"LOCALAPPDATA":    `C:\Users\ada\AppData\Local`,
		"GOPATH":          "/Users/ada/go",
		"HOMEBREW_PREFIX": "/opt/homebrew",
	}
	getenv := func(k string) string { return env[k] }

	for _, tc := range []struct {
		name string
		goos string
		exe  string
		want Channel
	}{
		{
			"homebrew cellar", "darwin",
			"/opt/homebrew/Cellar/vincent/0.4.1/bin/vincent", ChannelHomebrew,
		},
		{"homebrew bin", "darwin", "/opt/homebrew/bin/vincent", ChannelHomebrew},
		{
			"linuxbrew", "linux",
			"/home/linuxbrew/.linuxbrew/Cellar/vincent/0.4.1/bin/vincent", ChannelHomebrew,
		},
		{"deb or rpm in /usr/bin", "linux", "/usr/bin/vincent", ChannelSystemPackage},
		{
			"scoop apps", "windows",
			`C:\Users\ada\scoop\apps\vincent\current\vincent.exe`, ChannelScoop,
		},
		{
			"winget packages", "windows",
			`C:\Users\ada\AppData\Local\Microsoft\WinGet\Packages\lezli01.Vincent_x\vincent.exe`,
			ChannelWinGet,
		},
		{
			"mise installs", "linux",
			"/Users/ada/.local/share/mise/installs/vincent/0.4.1/vincent", ChannelMise,
		},
		{"gopath bin", "linux", "/Users/ada/go/bin/vincent", ChannelGoInstall},
		{"direct download in home", "linux", "/Users/ada/bin/vincent", ChannelSelf},
		{"hand-placed in /usr/local/bin", "linux", "/usr/local/bin/vincent", ChannelSelf},
		// The near-miss the whole-segment comparison exists for: a directory
		// whose name merely starts with a prefix is not inside it.
		{
			"homebrew-lookalike prefix", "darwin",
			"/opt/homebrew-staging/bin/vincent", ChannelSelf,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.exe, getenv, tc.goos); got != tc.want {
				t.Errorf("classify(%q, %s) = %q, want %q", tc.exe, tc.goos, got, tc.want)
			}
		})
	}
}

// A path matching nothing must resolve to package-managed, never to self —
// the conservative direction the issue asks for. The cost of guessing "self"
// wrongly is a clobbered file the next package upgrade reverts.
func TestClassifyEmptyPathIsUnknown(t *testing.T) {
	if got := classify("", func(string) string { return "" }, "linux"); got != ChannelUnknown {
		t.Errorf("classify(\"\") = %q, want %q", got, ChannelUnknown)
	}
	if ChannelUnknown.Owned() {
		t.Error("an unidentifiable install reported itself as vincent-owned")
	}
}

// Every package-managed channel that has a command prints one, and every
// channel vincent may replace prints none — a channel with both would be a
// contradiction on screen.
func TestUpgradeCommands(t *testing.T) {
	for _, c := range []Channel{ChannelHomebrew, ChannelScoop, ChannelWinGet, ChannelMise, ChannelGoInstall} {
		if c.UpgradeCommand() == "" {
			t.Errorf("%s has no upgrade command", c)
		}
		if c.Owned() {
			t.Errorf("%s reported itself as vincent-owned", c)
		}
	}
	if !ChannelSelf.Owned() {
		t.Error("the direct-download channel is not vincent-owned")
	}
	if ChannelSelf.UpgradeCommand() != "" {
		t.Error("the direct-download channel printed somebody else's upgrade command")
	}
}
