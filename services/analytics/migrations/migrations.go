// Package migrations embeds the analytics database schema, applied by dbx.Migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
