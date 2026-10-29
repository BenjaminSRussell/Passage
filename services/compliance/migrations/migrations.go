// Package migrations embeds the compliance service's SQL migrations, applied in order by dbx.Migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
