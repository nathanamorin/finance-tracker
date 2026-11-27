// Package web embeds the built frontend (web/dist, produced by `npm run build`
// in this directory). dist/.keep is committed so the Go build works before
// the frontend has been built; the server then answers 404 for the UI.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built frontend rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
