//go:build webdist

package main

import (
	"embed"
	"io/fs"
)

// The web build is copied to internal/web/dist by CI (npm run build in apps/web) before compiling with -tags webdist.
// The directory is not committed.
//
//go:embed all:internal/web/dist
var distFS embed.FS

const webEmbedded = true

func webFS() fs.FS {
	sub, err := fs.Sub(distFS, "internal/web/dist")
	if err != nil {
		return nil
	}
	return sub
}
