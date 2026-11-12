package main

import (
	"crypto/fips140"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
)

// checkFIPSStartup is the fail-closed FIPS hook (#599). It runs before any child starts. The FIPS build variant is compiled with
// GOFIPS140=v1.0.0, which cloudprofile.CheckFIPS detects through the build info, so such a binary refuses to start when
// crypto/fips140.Enabled() is false. requireFIPS in passage.yaml (or REQUIRE_FIPS) forces the same check on an ordinary build, and a
// PASSAGE_CLOUD_PROFILE that needs FIPS does too. Children inherit PASSAGE_CLOUD_PROFILE and GODEBUG and run their own check.
func checkFIPSStartup(cfg Config, getenv func(string) string) error {
	if cfg.RequireFIPS && !fips140.Enabled() {
		return cloudprofile.ErrFIPSRequired
	}
	if cfg.CloudProfile != "" {
		env := func(k string) string {
			if k == cloudprofile.EnvProfile {
				return cfg.CloudProfile
			}
			return getenv(k)
		}
		return cloudprofile.CheckFIPS(env)
	}
	return cloudprofile.CheckFIPS(getenv)
}

func fipsEnabled() bool { return fips140.Enabled() }

func fipsBuilt() bool { return cloudprofile.BuiltWithFIPS() }
