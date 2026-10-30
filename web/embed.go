// Package web embeds the built UI (web/dist) into the firstlume binary.
//
// Build it with `make web` before `go build`; without it the server still
// runs and answers UI routes with a hint to build it.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI, rooted at its index.html.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory exists
	}
	return sub
}
