//go:build !windows

package config

import "os"

// sharedBits are the mode bits that grant access to anyone but the owner.
const sharedBits os.FileMode = 0o077
