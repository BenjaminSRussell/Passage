// Package migrations embeds the assets service SQL, applied in order by dbx.Migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
