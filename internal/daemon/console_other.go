//go:build !windows

package daemon

// DetachConsole is a no-op off Windows: there is no console window to release,
// and a launchd agent or systemd user unit never had one. It exists so `vincent
// daemon --hide-console` parses everywhere and the command surface does not
// differ by platform (T4.20, T4.21).
func DetachConsole() string { return "" }
