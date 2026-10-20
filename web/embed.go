// Package web contains the embedded, dependency-free browser UI.
package web

import "embed"

// FS contains the static UI assets served by the hub.
//
//go:embed index.html app.js style.css admin.html admin.js
var FS embed.FS
