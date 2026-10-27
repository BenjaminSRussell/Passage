// Package migrations embeds the purchasing SQL migrations applied by dbx.Migrate.
package migrations

import "embed"

// FS holds the *.sql migrations in lexical order.
//
//go:embed *.sql
var FS embed.FS
