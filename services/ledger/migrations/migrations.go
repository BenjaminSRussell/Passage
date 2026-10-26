// Package migrations embeds the ledger service's SQL migrations.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
