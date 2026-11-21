//go:build !darwin && !linux && !windows

package clipimage

import "context"

// read has no helper to run on a platform vincent does not ship for; the
// clipboard there has no image as far as a paste is concerned.
func read(context.Context) ([]byte, error) { return nil, ErrNoImage }
