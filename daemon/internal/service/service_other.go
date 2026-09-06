//go:build !linux && !darwin

package service

// Install is not supported on this platform yet.
func Install(o Options) (string, error) { return "", ErrUnsupported }

// Uninstall is not supported on this platform yet.
func Uninstall(name string) error { return ErrUnsupported }

// Status is not supported on this platform yet.
func Status(name string) (string, error) { return "", ErrUnsupported }

// Enabled is not supported on this platform yet.
func Enabled(name string) (bool, error) { return false, ErrUnsupported }

// LogsArgs is not supported on this platform yet.
func LogsArgs(name string) []string { return nil }

// Start is not supported on this platform yet.
func Start(name string) error { return ErrUnsupported }

// Stop is not supported on this platform yet.
func Stop(name string) error { return ErrUnsupported }

// SetEnabled is not supported on this platform yet.
func SetEnabled(name string, on bool) error { return ErrUnsupported }

// Installed is not supported on this platform yet.
func Installed(name string) (bool, error) { return false, ErrUnsupported }
