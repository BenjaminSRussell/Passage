// Package migrations embeds the service's SQL migrations for dbx.Migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
