//go:build !linux

package service

// Install is not supported on this platform yet.
func Install(o Options) (string, error) { return "", ErrUnsupported }

// Uninstall is not supported on this platform yet.
func Uninstall(name string) error { return ErrUnsupported }

// Status is not supported on this platform yet.
func Status(name string) (string, error) { return "", ErrUnsupported }

// LogsArgs is not supported on this platform yet.
func LogsArgs(name string) []string { return nil }
