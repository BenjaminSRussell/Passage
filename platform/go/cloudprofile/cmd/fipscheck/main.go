// Command fipscheck runs the cloud profile's fail-closed FIPS startup check and exits 0 when the process may
// start. CI builds it with GOFIPS140=v1.0.0 and boots it under each profile; it also shows an operator what a
// Passage service will do under a given PASSAGE_CLOUD_PROFILE.
package main

import (
	"crypto/fips140"
	"fmt"
	"os"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
)

func main() {
	if err := cloudprofile.CheckFIPS(os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "refusing to start:", err)
		os.Exit(1)
	}
	fmt.Printf("ok profile=%q fips140.Enabled=%v builtWithFIPS=%v\n", os.Getenv(cloudprofile.EnvProfile), fips140.Enabled(), cloudprofile.BuiltWithFIPS())
}
