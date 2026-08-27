// Package web embeds the frontend static files directly into the Plumb
// executable, so the distribution is one file plus its sidecars — no
// separate assets directory to keep track of.
package web

import "embed"

//go:embed index.html app.js styles.css
var FS embed.FS
