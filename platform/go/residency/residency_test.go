package residency_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
	"github.com/BenjaminSRussell/passage/platform/residency"
)

func TestCheck(t *testing.T) {
	for _, c := range []struct {
		code, region string
		ok           bool
	}{
		{"EU", "westeurope", true},
		{"EU", "swedencentral", true},
		{"eu", " WestEurope ", true},
		{"EU", "eu-west", true},
		{"EU", "eu-central", true},
		{"EU", "eastus2", false},
		{"EU", "us-east", false},
		{"EU", "uksouth", false},
		{"EU", "", false},
		{"EU", "mars", false},
		{"US", "eastus2", true},
		{"US", "us-east", true},
		{"US", "westeurope", false},
		{"UK", "uksouth", true},
		{"UK", "northeurope", false},
		{"GLOBAL", "westeurope", true},
		{"GLOBAL", "us-east", true},
		{"GLOBAL", "anything", true},
		{"GLOBAL", "", false},
		{"", "eastus", true}, // no residency is the default, GLOBAL
		{"ATLANTIS", "westeurope", false},
	} {
		err := residency.Check(c.code, c.region)
		if (err == nil) != c.ok {
			t.Errorf("Check(%q, %q) = %v, want ok=%v", c.code, c.region, err, c.ok)
		}
		if err != nil {
			var e *residency.Error
			if !errors.As(err, &e) || e.Code != "OPS-1705" {
				t.Errorf("Check(%q, %q) = %v, want an *Error with code OPS-1705", c.code, c.region, err)
			}
		}
	}
}

func TestValidAndCodes(t *testing.T) {
	if got := residency.Codes(); !slices.Equal(got, []string{"EU", "GLOBAL", "UK", "US"}) {
		t.Fatalf("Codes = %v", got)
	}
	if !residency.Valid("") || !residency.Valid("eu") || residency.Valid("XX") {
		t.Fatal("Valid is wrong")
	}
	if regions, ok := residency.Regions("GLOBAL"); !ok || regions != nil {
		t.Fatalf("GLOBAL regions = %v, %v; want nil, true (any)", regions, ok)
	}
	if _, ok := residency.Regions("XX"); ok {
		t.Fatal("XX is unknown")
	}
}

func TestResidenciesOf(t *testing.T) {
	if got := residency.ResidenciesOf("westeurope"); !slices.Equal(got, []string{"EU"}) {
		t.Fatalf("westeurope in %v", got)
	}
	if got := residency.ResidenciesOf("nowhere"); len(got) != 0 {
		t.Fatalf("nowhere in %v", got)
	}
}

// The EU residency is exactly the eu-data-boundary overlay's allowedRegions plus the legacy EU ids, and every US
// region of the gcc profile is in US, so the profile table and the residency table cannot drift apart.
func TestTableMatchesTheCloudProfile(t *testing.T) {
	eu, err := cloudprofile.Resolve("commercial", "eu-data-boundary")
	if err != nil {
		t.Fatal(err)
	}
	if len(eu.AllowedRegions) == 0 {
		t.Fatal("the eu-data-boundary overlay lists no regions")
	}
	for _, r := range eu.AllowedRegions {
		if !residency.Allows(residency.EU, r) {
			t.Errorf("EU residency does not allow %s, which the eu-data-boundary overlay does", r)
		}
	}
	regions, _ := residency.Regions(residency.EU)
	for _, r := range regions {
		if !slices.Contains(eu.AllowedRegions, r) && !slices.Contains([]string{"eu-west", "eu-central", "eu-north"}, r) {
			t.Errorf("EU residency allows %s, which is neither in the eu-data-boundary overlay nor a legacy EU id", r)
		}
	}
	gcc, err := cloudprofile.Resolve("gcc")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range gcc.AllowedRegions {
		if !residency.Allows(residency.US, r) {
			t.Errorf("US residency does not allow %s, which the gcc profile does", r)
		}
		if residency.Allows(residency.EU, r) {
			t.Errorf("EU residency allows the US region %s", r)
		}
	}
}

func TestNoRegionIsInTwoRestrictedResidencies(t *testing.T) {
	for _, code := range residency.Codes() {
		regions, _ := residency.Regions(code)
		for _, r := range regions {
			if got := residency.ResidenciesOf(r); len(got) != 1 {
				t.Errorf("region %s is in %v, want exactly one residency", r, got)
			}
		}
	}
}
