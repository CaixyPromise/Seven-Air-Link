// Package web embeds the Bridge collaboration console; no network/CDN build dependencies.
package web

import "embed"

//go:embed index.html app.js collaboration.js maintenance.js notes.js auth.js style.css
var Assets embed.FS
