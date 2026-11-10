// Package webui is the Workspace Web App (Blueprint 13A.4, P5a, ADR-0041): a
// static single-page app served by the Core. It is a client of the public API
// only; every action is authorized server-side. All data is rendered as text
// (never as HTML), and a strict Content-Security-Policy forbids inline script.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// FS is the app's files (index.html, app.js, app.css).
func FS() fs.FS {
	sub, _ := fs.Sub(files, "static")
	return sub
}
