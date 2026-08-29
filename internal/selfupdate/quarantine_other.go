//go:build !darwin

package selfupdate

// clearQuarantine is a no-op off macOS: com.apple.quarantine is a Gatekeeper
// attribute and no other platform has one.
func clearQuarantine(string) error { return nil }
