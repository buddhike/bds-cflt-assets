// Package frontend holds the built Svelte app, for the server binary.
package frontend

import "embed"

// Dist is the output of `npm run build`. A fresh checkout holds only a
// placeholder, so Go builds before the UI has been built.
//
//go:embed all:dist
var Dist embed.FS
