// Package migrations embeds the pricing SQL migrations applied by dbx.Migrate.
package migrations

import "embed"

// FS holds the *.sql migrations in order.
//
//go:embed *.sql
var FS embed.FS
