//go:build !webdist

package main

import "io/fs"

// webFS is nil in a build without -tags webdist: the gateway then serves the API only and passage-server doctor says so.
func webFS() fs.FS { return nil }

const webEmbedded = false
