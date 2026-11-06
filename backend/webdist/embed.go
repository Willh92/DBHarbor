package webdist

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var rawFS embed.FS

// DistFS returns the embedded frontend dist filesystem.
func DistFS() (fs.FS, error) {
	return fs.Sub(rawFS, "dist")
}
