// Package service installs the daemon as a per-user background service.
package service

import "errors"

// ErrUnsupported is returned on platforms without service support yet.
var ErrUnsupported = errors.New("service install is not supported on this platform yet")

// Options for installation.
type Options struct {
	Exe  string // absolute path to the orchestrator binary
	Path string // PATH to embed so `claude` resolves under the service
	Name string // service name (default orchestrator)
}
