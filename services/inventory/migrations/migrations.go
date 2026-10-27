// Package migrations embeds the inventory service's SQL migrations.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
