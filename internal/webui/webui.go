// Package webui embeds the web front's build (internal/webui/dist).
//
// The front's sources live in web/; `make web` builds them and copies
// web/dist here (go:embed cannot reach outside its package). Until the front
// exists, dist holds a static placeholder page with no data.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded front, rooted inside dist (index.html at its top).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil { // only on an invalid path literal: a build-time constant
		panic(err)
	}
	return sub
}
