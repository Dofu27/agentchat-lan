// Package web contains the embedded, dependency-free browser UI.
package web

import "embed"

// FS contains the static UI assets served by the hub.
//
//go:embed index.html app.js style.css
var FS embed.FS
