// Package migrations embeds the billing SQL migrations applied by dbx.Migrate.
package migrations

import "embed"

// FS holds every NNNN_name.sql file in this directory.
//
//go:embed *.sql
var FS embed.FS
