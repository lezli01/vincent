//go:build !linux

package container

// HostUser is empty everywhere but Linux. macOS Docker Desktop maps ownership
// itself through its file-sharing layer, so forcing a uid there would break
// images with a baked user for no gain (task 061 decision 5). Windows never
// reaches here: a containerized task is refused at creation on a Windows
// daemon (decision 2).
func HostUser() string { return "" }
