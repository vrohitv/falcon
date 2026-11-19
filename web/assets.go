// Package webassets embeds the built UI.
package webassets

import "embed"

//go:embed all:dist
var Dist embed.FS
