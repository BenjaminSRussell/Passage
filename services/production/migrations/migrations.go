// Package migrations embeds the SQL migrations applied by dbx.Migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
