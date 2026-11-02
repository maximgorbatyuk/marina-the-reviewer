// Package buildinfo exposes link-time release identity for commands and persisted review artifacts.
package buildinfo

// Channel selects fixed development or production storage at link time.
var Channel = "dev"

// Version can be replaced at link time to identify the built release.
var Version = "dev"

// Commit can be replaced at link time to identify the built source revision.
var Commit = "unknown"

// Date can be replaced at link time to identify when the binary was built.
var Date = "unknown"
