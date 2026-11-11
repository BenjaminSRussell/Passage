package cloudprofile

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func env(profile string) func(string) string {
	return func(k string) string {
		if k == EnvProfile {
			return profile
		}
		return ""
	}
}

func TestCheckFIPSDecisionTable(t *testing.T) {
	for _, c := range []struct {
		name         string
		profile      string
		built, fips  bool
		wantRefusal  bool
		wantAnyError bool
	}{
		{"commercial, ordinary build, no FIPS", "", false, false, false, false},
		{"commercial, FIPS build, FIPS off at runtime", "commercial", true, false, true, true},
		{"gcc, FIPS active", "gcc", true, true, false, false},
		{"gcc, ordinary image deployed", "gcc", false, false, true, true},
		{"gcc, FIPS image but GODEBUG=fips140=off", "gcc", true, false, true, true},
		{"gcc+hipaa, FIPS off", "gcc+hipaa", false, false, true, true},
		{"gcc-high, FIPS off", "gcc-high", false, false, true, true},
		{"china does not require FIPS", "china", false, false, false, false},
		{"unknown profile fails closed", "azure-stack", false, true, false, true},
	} {
		err := checkFIPS(env(c.profile), c.built, c.fips)
		if (err != nil) != c.wantAnyError {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if c.wantRefusal && !errors.Is(err, ErrFIPSRequired) {
			t.Errorf("%s: want ErrFIPSRequired, got %v", c.name, err)
		}
	}
}

// TestFIPSVariantBootsAndRefuses builds cmd/fipscheck the way the Dockerfiles do (GOFIPS140=v1.0.0) and runs it:
// it starts under gcc, and refuses to start when FIPS mode is switched off or when an ordinary build runs under gcc.
func TestFIPSVariantBootsAndRefuses(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	dir := t.TempDir()
	build := func(name, fipsEnv string) string {
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, "./cmd/fipscheck")
		cmd.Env = append(os.Environ(), "GOFIPS140="+fipsEnv, "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build GOFIPS140=%s: %v\n%s", fipsEnv, err, b)
		}
		return out
	}
	fipsBin, plainBin := build("fips", "v1.0.0"), build("plain", "off")
	run := func(bin string, extra ...string) (string, error) {
		cmd := exec.Command(bin)
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, extra...)
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	if out, err := run(fipsBin, EnvProfile+"=gcc"); err != nil || !strings.Contains(out, "fips140.Enabled=true") {
		t.Errorf("FIPS build under gcc must start: %v %s", err, out)
	}
	if out, err := run(fipsBin, EnvProfile+"=gcc", "GODEBUG=fips140=off"); err == nil || !strings.Contains(out, "refusing to start") {
		t.Errorf("FIPS build with FIPS mode switched off must refuse: %v %s", err, out)
	}
	if out, err := run(fipsBin, "GODEBUG=fips140=off"); err == nil {
		t.Errorf("a FIPS build must refuse to run with FIPS off even without a profile: %s", out)
	}
	if out, err := run(plainBin, EnvProfile+"=gcc"); err == nil || !strings.Contains(out, "refusing to start") {
		t.Errorf("an ordinary build under gcc must refuse: %v %s", err, out)
	}
	if out, err := run(plainBin, EnvProfile+"=commercial"); err != nil {
		t.Errorf("an ordinary build under commercial must start: %v %s", err, out)
	}
}
