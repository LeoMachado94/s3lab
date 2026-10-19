// Package web embeds the static console UI.
package web

import "embed"

// Static holds the console UI files under the "static" directory.
//
//go:embed static
var Static embed.FS
