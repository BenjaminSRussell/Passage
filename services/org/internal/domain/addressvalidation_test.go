package domain_test

import (
	"context"
	"encoding/csv"
	"os"
	"reflect"
	"testing"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func fixtureRows(t *testing.T, name string) [][]string {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows[1:]
}

func countryRules(t *testing.T) map[string]domain.CountryRule {
	t.Helper()
	f, err := migrations.Reference.Open("reference/countries.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]domain.CountryRule{}
	for _, r := range rows[1:] {
		rules[r[0]] = domain.CountryRule{RegionRequired: r[2] == "true", PostalRegex: r[3]}
	}
	return rules
}

func TestRuleAddressValidator_SevenCountryFixture(t *testing.T) {
	rules := countryRules(t)
	rows := fixtureRows(t, "../../testdata/addresses-7-countries.csv")
	valid, invalid, suggested := 0, 0, 0
	for _, r := range rows {
		in := domain.PostalAddress{CountryCode: r[0], RegionCode: r[1], City: r[2], Street1: r[3], PostalCode: r[4]}
		normalized, err := domain.RuleAddressValidator{}.Normalize(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		problems := domain.CheckCountryRules(normalized, rules[r[0]])
		known, adapterValid := domain.PostalValid(normalized.CountryCode, normalized.PostalCode)
		if !known || adapterValid != (len(problems) == 0) {
			t.Errorf("%s %q: adapter says known %v valid %v but the country rule reports %v", r[0], r[4], known, adapterValid, problems)
		}
		suggestions, err := domain.RuleAddressValidator{}.Suggest(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if len(suggestions) > 0 && r[6] == "true" {
			t.Errorf("%s %q: valid address got suggestions %v", r[0], r[4], suggestions)
		}
		for _, s := range suggestions {
			suggested++
			if p := domain.CheckCountryRules(s.Address, rules[r[0]]); len(p) != 0 {
				t.Errorf("%s %q: suggestion %q fails the country rule %v", r[0], r[4], s.Address.PostalCode, p)
			}
		}
		switch r[6] {
		case "true":
			valid++
			if len(problems) != 0 || normalized.PostalCode != r[5] {
				t.Errorf("%s %q: normalized %q, problems %v; want %q and none", r[0], r[4], normalized.PostalCode, problems, r[5])
			}
		default:
			invalid++
			if problems["postalCode"] != domain.CodePostalFormat {
				t.Errorf("%s %q: normalized %q passed the postal rule, want %s", r[0], r[4], normalized.PostalCode, domain.CodePostalFormat)
			}
		}
	}
	if valid != 140 || invalid != 70 {
		t.Fatalf("fixture has %d valid and %d invalid rows, want 140 and 70", valid, invalid)
	}
	if suggested < 3 {
		t.Fatalf("invalid rows produced %d suggestions, want at least 3", suggested)
	}
}

func TestRuleAddressValidator_Normalize(t *testing.T) {
	cases := []struct {
		name string
		in   domain.PostalAddress
		want domain.PostalAddress
	}{
		{"uk alias and gb layout", domain.PostalAddress{CountryCode: " uk ", City: "  London ", PostalCode: "sw1a1aa", Street1: "10  Downing   St"},
			domain.PostalAddress{CountryCode: "GB", City: "London", PostalCode: "SW1A 1AA", Street1: "10 Downing St"}},
		{"us zip plus four", domain.PostalAddress{CountryCode: "us", RegionCode: "ca", PostalCode: "941051234"},
			domain.PostalAddress{CountryCode: "US", RegionCode: "CA", PostalCode: "94105-1234"}},
		{"iso3 aliases", domain.PostalAddress{CountryCode: "can", PostalCode: "k1a0b1"}, domain.PostalAddress{CountryCode: "CA", PostalCode: "K1A 0B1"}},
		{"unknown country keeps shape", domain.PostalAddress{CountryCode: "NL", PostalCode: " 1011 ab "},
			domain.PostalAddress{CountryCode: "NL", PostalCode: "1011 AB"}},
	}
	for _, c := range cases {
		got, err := domain.RuleAddressValidator{}.Normalize(context.Background(), c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
}

func TestRuleAddressValidator_Suggest(t *testing.T) {
	cases := []struct {
		name       string
		in         domain.PostalAddress
		wantPostal []string
	}{
		{"us missing leading zero", domain.PostalAddress{CountryCode: "US", PostalCode: "2108"}, []string{"02108"}},
		{"de missing leading zero", domain.PostalAddress{CountryCode: "DE", PostalCode: "1067"}, []string{"01067"}},
		{"ca letter o for zero", domain.PostalAddress{CountryCode: "CA", PostalCode: "K1A OB1"}, []string{"K1A 0B1"}},
		{"valid has none", domain.PostalAddress{CountryCode: "FR", PostalCode: "75001"}, nil},
		{"hopeless has none", domain.PostalAddress{CountryCode: "IN", PostalCode: "abc"}, nil},
		{"unknown country has none", domain.PostalAddress{CountryCode: "NL", PostalCode: "x"}, nil},
	}
	for _, c := range cases {
		got, err := domain.RuleAddressValidator{}.Suggest(context.Background(), c.in)
		if err != nil {
			t.Fatal(err)
		}
		var postals []string
		for _, s := range got {
			postals = append(postals, s.Address.PostalCode)
			if s.Reason == "" {
				t.Errorf("%s: suggestion without a reason", c.name)
			}
		}
		if !reflect.DeepEqual(postals, c.wantPostal) {
			t.Errorf("%s: suggested %v, want %v", c.name, postals, c.wantPostal)
		}
	}
}

func TestNullAddressValidator_ChangesNothing(t *testing.T) {
	in := domain.PostalAddress{CountryCode: "uk", PostalCode: " sw1a1aa "}
	got, err := domain.NullAddressValidator{}.Normalize(context.Background(), in)
	suggestions, suggestErr := domain.NullAddressValidator{}.Suggest(context.Background(), in)
	if err != nil || suggestErr != nil || got != in || suggestions != nil {
		t.Fatalf("null validator changed %+v to %+v, suggestions %v", in, got, suggestions)
	}
}
