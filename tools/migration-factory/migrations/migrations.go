// Package migrations embeds the migration-factory SQL migrations applied by dbx.Migrate.
package migrations

import "embed"

// FS holds every NNNN_name.sql file in apply order.
//
//go:embed *.sql
var FS embed.FS
