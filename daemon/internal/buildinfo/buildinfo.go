// Package buildinfo holds the version stamped at link time. It has no
// imports so every binary in the module can use it without dragging in the
// daemon's runtime.
package buildinfo

// Version is set by the build with -ldflags "-X .../buildinfo.Version=...".
var Version = "dev"
