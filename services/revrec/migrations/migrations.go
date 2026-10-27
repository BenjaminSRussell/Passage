// Package migrations embeds the revrec SQL migrations applied in order by dbx.Migrate.
package migrations

import "embed"

// FS holds the NNNN_name.sql migration files.
//
//go:embed *.sql
var FS embed.FS
