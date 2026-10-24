package migrations

import "embed"

// FS contains the metadata registry schema.
//
//go:embed *.sql
var FS embed.FS
