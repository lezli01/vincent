package nerdfont

import "strings"

// TerminalInfo is what the environment suggests about the terminal in use.
type TerminalInfo struct {
	// Name is the terminal, empty when it was not recognized.
	Name string
	// Bundled is a terminal that draws Nerd Font icons with no font installed.
	Bundled bool
	// Remote is a session over SSH: a font installed here would never reach the
	// screen, which is on the other end.
	Remote bool
	// Steps says where this terminal's font is chosen.
	Steps string
}

// Terminal reads the environment through env.
func Terminal(env func(string) string) TerminalInfo {
	info := TerminalInfo{Remote: env("SSH_CONNECTION") != "" || env("SSH_TTY") != ""}
	program, term := strings.ToLower(env("TERM_PROGRAM")), env("TERM")
	switch {
	case program == "ghostty" || term == "xterm-ghostty":
		info.Name, info.Bundled = "Ghostty", true
	case program == "wezterm":
		info.Name, info.Bundled = "WezTerm", true
	case program == "iterm.app":
		info.Name, info.Steps = "iTerm2", "Settings → Profiles → Text → Font"
	case program == "apple_terminal":
		info.Name, info.Steps = "Terminal", "Settings → Profiles → Text → Font → Change"
	case program == "vscode":
		info.Name, info.Steps = "VS Code", "the terminal.integrated.fontFamily setting"
	case term == "xterm-kitty" || env("KITTY_WINDOW_ID") != "":
		info.Name, info.Steps = "kitty", "font_family in kitty.conf"
	case term == "alacritty" || env("ALACRITTY_WINDOW_ID") != "":
		info.Name, info.Steps = "Alacritty", "[font.normal] family in alacritty.toml"
	default:
		info.Steps = "your terminal's font or profile settings"
	}
	return info
}
