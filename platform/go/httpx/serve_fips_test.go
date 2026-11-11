package httpx

import (
	"context"
	"crypto/fips140"
	"errors"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
)

// Serve is the one start-up path every service goes through; a FIPS profile on a process that is not in FIPS
// 140-3 mode must not open a listener (#599, #601).
func TestServeRefusesToStartInAFIPSProfileWithoutFIPSMode(t *testing.T) {
	if fips140.Enabled() {
		t.Skip("this test binary already runs in FIPS 140-3 mode")
	}
	t.Setenv(cloudprofile.EnvProfile, "gcc")
	err := Serve(context.Background(), "127.0.0.1:0", http.NotFoundHandler())
	if !errors.Is(err, cloudprofile.ErrFIPSRequired) {
		t.Fatalf("Serve under gcc without FIPS = %v, want ErrFIPSRequired", err)
	}
}
