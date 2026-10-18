// Package web embeds the Vite production build. Run `bun run build` first.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built frontend rooted at dist/.
func Assets() (fs.FS, error) { return fs.Sub(dist, "dist") }
