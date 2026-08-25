// Package templates declares the embeds for the HTML templates and the static
// assets built into the binary. What renders and what serves them take a
// filesystem as a parameter.
package templates

import (
	"embed"
	"io/fs"
)

//go:embed layouts pages
var templateFiles embed.FS

// The stylesheet under assets/css is generated, so favicon.svg is what keeps
// this embed valid: go:embed fails on a directory matching no files, and it
// skips dot names, so a .gitkeep would not have worked.
//
//go:embed assets
var assetFiles embed.FS

// Templates carries the layouts/ and pages/ prefixes the renderer globs on.
func Templates() fs.FS {
	return templateFiles
}

// Assets is rooted at the asset directory itself, so a path in a template
// ("css/app.css") is what a browser asks for.
func Assets() (fs.FS, error) {
	return fs.Sub(assetFiles, "assets")
}
