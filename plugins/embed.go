// Package plugins embeds the agent plugins that nitipan installs on request.
//
// The sources live in this tree so they can be read and checked as files. `nitipan setup` writes
// them to a directory the user names, with nitipan's own absolute path filled in.
package plugins

import "embed"

// FS holds the opencode plugin under opencode/nitipan.
//
//go:embed opencode
var FS embed.FS
