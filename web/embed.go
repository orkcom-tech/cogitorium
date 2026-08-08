// Package web embeds the built frontend (web/dist) into the server binary.
// Build the frontend first (`make ui`); a binary built without it serves a
// 503 explaining that, nothing else.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
