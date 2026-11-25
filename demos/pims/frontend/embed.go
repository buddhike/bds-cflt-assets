// Package frontend holds the built React app, for the server binary. The desktop
// app embeds the same directory from its own main package.
package frontend

import "embed"

// Dist is the output of `npm run build`. A fresh checkout holds only a
// placeholder, so Go builds before the UI has been built, and the server then
// serves a page that says so.
//
//go:embed all:dist
var Dist embed.FS
