// Package migrations embeds the treasury SQL migrations applied by dbx.Migrate.
package migrations

import "embed"

// FS holds every *.sql migration in lexical order.
//
//go:embed *.sql
var FS embed.FS
