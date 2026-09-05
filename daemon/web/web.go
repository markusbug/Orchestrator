// Package web embeds the browser debug client.
package web

import "embed"

// FS holds index.html, app.js and vendored xterm assets.
//
//go:embed index.html app.js static/*
var FS embed.FS
