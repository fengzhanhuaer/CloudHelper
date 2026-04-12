// Package web embeds the compiled frontend dist into the binary.
// Run `npm run build` inside frontend/ before `go build`.
package web

import "embed"

//go:embed all:dist
var FS embed.FS
