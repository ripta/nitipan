// Package version carries the build version, which the Makefile injects through ldflags.
package version

// Version is the build version. It stays "dev" for a build that does not inject one.
var Version = "dev"
