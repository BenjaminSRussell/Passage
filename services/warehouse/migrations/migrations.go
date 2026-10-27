// Package migrations embeds the warehouse SQL migrations applied by dbx.Migrate.
package migrations

import "embed"

// FS holds every *.sql file in this directory, applied in name order.
//
//go:embed *.sql
var FS embed.FS
