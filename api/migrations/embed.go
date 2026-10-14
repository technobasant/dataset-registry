// Package migrations embeds the goose migrations so the binary carries its schema.
package migrations

import "embed"

// FS holds every migration.
//
//go:embed *.sql
var FS embed.FS
