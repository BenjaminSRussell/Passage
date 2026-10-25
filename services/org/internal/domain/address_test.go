package domain_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

var (
	usRule = domain.CountryRule{RegionRequired: true, PostalRegex: `^[0-9]{5}(-[0-9]{4})?$`}
	caRule = domain.CountryRule{RegionRequired: true, PostalRegex: `^[A-Z][0-9][A-Z] ?[0-9][A-Z][0-9]$`}
	gbRule = domain.CountryRule{PostalRegex: `^[A-Z]{1,2}[0-9][A-Z0-9]? ?[0-9][A-Z]{2}$`}
	deRule = domain.CountryRule{PostalRegex: `^[0-9]{5}$`}
)

func TestCheckCountryRules(t *testing.T) {
	tests := []struct {
		name string
		addr domain.PostalAddress
		rule domain.CountryRule
		want map[string]string
	}{
		{"US without region", domain.PostalAddress{CountryCode: "US", PostalCode: "94105"}, usRule, map[string]string{"regionCode": domain.CodeRegionRequired}},
		{"US short postal", domain.PostalAddress{CountryCode: "US", RegionCode: "CA", PostalCode: "9410"}, usRule, map[string]string{"postalCode": domain.CodePostalFormat}},
		{"US without region and short postal", domain.PostalAddress{CountryCode: "US", PostalCode: "9410"}, usRule, map[string]string{"regionCode": domain.CodeRegionRequired, "postalCode": domain.CodePostalFormat}},
		{"US five digits", domain.PostalAddress{CountryCode: "US", RegionCode: "CA", PostalCode: "94105"}, usRule, map[string]string{}},
		{"US zip plus four", domain.PostalAddress{CountryCode: "US", RegionCode: "CA", PostalCode: "94105-1234"}, usRule, map[string]string{}},
		{"US empty postal", domain.PostalAddress{CountryCode: "US", RegionCode: "CA"}, usRule, map[string]string{"postalCode": domain.CodePostalFormat}},
		{"CA lower-case postal", domain.PostalAddress{CountryCode: "CA", RegionCode: "ON", PostalCode: " k1a 0b1 "}, caRule, map[string]string{}},
		{"GB valid", domain.PostalAddress{CountryCode: "GB", PostalCode: "SW1A 1AA"}, gbRule, map[string]string{}},
		{"GB digits", domain.PostalAddress{CountryCode: "GB", PostalCode: "12345"}, gbRule, map[string]string{"postalCode": domain.CodePostalFormat}},
		{"DE needs no region", domain.PostalAddress{CountryCode: "DE", PostalCode: "10115"}, deRule, map[string]string{}},
		{"no regex accepts anything", domain.PostalAddress{CountryCode: "NL", PostalCode: "whatever"}, domain.CountryRule{}, map[string]string{}},
		{"no regex accepts empty", domain.PostalAddress{CountryCode: "NL"}, domain.CountryRule{}, map[string]string{}},
		{"uncompilable regex is no check", domain.PostalAddress{CountryCode: "XX", PostalCode: "1"}, domain.CountryRule{PostalRegex: "("}, map[string]string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.CheckCountryRules(tc.addr, tc.rule); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("CheckCountryRules = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		name string
		addr domain.PostalAddress
		want string
	}{
		{"US", domain.PostalAddress{CountryCode: "US", RegionCode: "IL", City: "Springfield", PostalCode: "62701", Street1: "1 Main St", Street2: "Suite 4"}, "1 Main St\nSuite 4\nSpringfield, IL 62701\nUS"},
		{"DE", domain.PostalAddress{CountryCode: "DE", City: "Berlin", PostalCode: "10115", Street1: "Invalidenstrasse 1"}, "Invalidenstrasse 1\n10115 Berlin\nDE"},
		{"IN with building", domain.PostalAddress{CountryCode: "IN", RegionCode: "MH", City: "Mumbai", PostalCode: "400001", Street1: "1 MG Road", Building: "Tower B"}, "1 MG Road\nTower B\nMumbai 400001\nMH\nIN"},
		{"generic with PO box", domain.PostalAddress{CountryCode: "NL", City: "Amsterdam", PostalCode: "1011 ab", Street1: "Damrak 1", POBox: "PO 99"}, "Damrak 1\nPO 99\n1011 AB Amsterdam\nNL"},
		{"generic with region", domain.PostalAddress{CountryCode: "ZZ", RegionCode: "R1", City: "Town", Street1: "Road"}, "Road\nTown\nR1\nZZ"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.Format(tc.addr); got != tc.want {
				t.Fatalf("Format = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidPurpose(t *testing.T) {
	for _, purpose := range domain.AddressPurposes {
		if !domain.ValidPurpose(purpose) {
			t.Fatalf("%s rejected", purpose)
		}
	}
	if domain.ValidPurpose("billing") || len(domain.AddressPurposes) != 7 {
		t.Fatalf("billing accepted or purposes = %v", domain.AddressPurposes)
	}
}

func TestNullGeocoder(t *testing.T) {
	got, err := domain.NullGeocoder{}.Geocode(context.Background(), domain.PostalAddress{CountryCode: "US"})
	if err != nil || got.Found {
		t.Fatalf("NullGeocoder = %+v, %v; want not found and no error", got, err)
	}
}
