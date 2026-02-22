// Package ui contains embedded frontend assets.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distDir embed.FS

// DistFS returns the dist subdirectory as a filesystem.
func DistFS() (fs.FS, error) {
	return fs.Sub(distDir, "dist")
}
