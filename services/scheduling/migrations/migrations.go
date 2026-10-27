// Package migrations embeds the scheduling service's SQL migrations.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
