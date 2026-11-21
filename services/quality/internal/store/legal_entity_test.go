package store

import (
	"testing"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

func TestSingleLegalEntity(t *testing.T) {
	const a, b = "00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"
	cases := map[string]struct{ scope, want string }{
		"tenant-wide":  {"", ""},
		"one entity":   {a, a},
		"two entities": {a + "," + b, ""},
		"nil uuid":     {"00000000-0000-0000-0000-000000000000", ""},
	}
	for name, c := range cases {
		if got := singleLegalEntity(tenant.Principal{LegalEntityID: c.scope}); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}
