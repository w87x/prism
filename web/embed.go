// Package web embeds the built Svelte UI.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
