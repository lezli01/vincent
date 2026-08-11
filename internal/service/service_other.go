//go:build !windows && !darwin && !linux

package service

import "context"

// Platforms with no backend still compile: vincent builds on more than the
// three OSes CI runs, and a build failure is a worse answer than a clear
// "not supported here".

func install(context.Context, Options) error { return ErrUnsupported }

func uninstall(context.Context) error { return ErrUnsupported }

func query(context.Context) (Status, error) { return Status{}, ErrUnsupported }

// LingerFailed is never true where nothing installs.
func LingerFailed(error) bool { return false }
