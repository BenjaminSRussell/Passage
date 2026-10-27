// Package migrations embeds the costacct SQL migrations applied by dbx.Migrate.
package migrations

import "embed"

// FS holds the ordered NNNN_name.sql files.
//
//go:embed *.sql
var FS embed.FS
