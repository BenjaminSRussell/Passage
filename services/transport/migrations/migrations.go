// Package migrations embeds the transport service's SQL, applied in order by dbx.Migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
