// Package migrations embeds the goose SQL migrations so the binary carries its schema.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
