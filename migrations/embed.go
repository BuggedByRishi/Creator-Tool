// Package migrations embeds the goose-format SQL migrations so the binary
// can migrate itself on start-up. The same files work with the goose CLI.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
