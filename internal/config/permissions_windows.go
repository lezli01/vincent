//go:build windows

package config

import "os"

// sharedBits is empty on Windows: the mode bits carry no access control there,
// and access to {config_dir} comes from the per-user ACL %APPDATA% inherits —
// the same story as {data_dir}/token, which grew no DACL code either (T1.3).
// A mode-based warning would be advice a reader cannot act on: there is no
// chmod to run.
const sharedBits os.FileMode = 0
