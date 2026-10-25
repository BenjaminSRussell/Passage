package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

//go:embed reference/*.csv
var Reference embed.FS
