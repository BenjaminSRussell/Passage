package cloudprofile

import (
	"crypto/fips140"
	"errors"
	"fmt"
	"runtime/debug"
)

// BuiltWithFIPS reports whether this binary was built with GOFIPS140 set to the validated module (a value other
// than "off"), which the Go toolchain records in the build info.
func BuiltWithFIPS() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "GOFIPS140" {
			return s.Value != "" && s.Value != "off"
		}
	}
	return false
}

// ErrFIPSRequired is returned by CheckFIPS when FIPS 140-3 mode is required and not active.
var ErrFIPSRequired = errors.New("cloudprofile: FIPS 140-3 mode is required but crypto/fips140.Enabled() is false")

// CheckFIPS is the fail-closed startup check. It returns an error when the profile named by
// PASSAGE_CLOUD_PROFILE needs FIPS, or when this binary was built with GOFIPS140, and the FIPS 140-3
// module is not active (for example GODEBUG=fips140=off, or an image built without GOFIPS140 deployed
// under a gcc profile). An unknown profile name is an error too. A commercial deployment of an ordinary
// build passes.
func CheckFIPS(getenv func(string) string) error {
	return checkFIPS(getenv, BuiltWithFIPS(), fips140.Enabled())
}

func checkFIPS(getenv func(string) string, built, enabled bool) error {
	p, err := FromEnv(getenv)
	if err != nil {
		return err
	}
	switch {
	case enabled:
		return nil
	case p.FIPS:
		return fmt.Errorf("%w: profile %s requires it; build with GOFIPS140=v1.0.0 and do not set GODEBUG=fips140=off", ErrFIPSRequired, p.Selector())
	case built:
		return fmt.Errorf("%w: this binary was built with GOFIPS140; do not set GODEBUG=fips140=off", ErrFIPSRequired)
	}
	return nil
}
