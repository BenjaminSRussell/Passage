package domain_test

import (
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

func ptr(s string) *string { return &s }

func TestDisplayName(t *testing.T) {
	tests := []struct {
		name      string
		partyType string
		fields    domain.PartyFields
		want      string
	}{
		{"organization uses legal name", domain.TypeOrganization, domain.PartyFields{LegalName: ptr("Acme GmbH"), TradeName: ptr("Acme")}, "Acme GmbH"},
		{"person joins given and family", domain.TypePerson, domain.PartyFields{GivenName: ptr("Ada"), MiddleName: ptr("M"), FamilyName: ptr("Lovelace")}, "Ada Lovelace"},
		{"person skips empty parts", domain.TypePerson, domain.PartyFields{GivenName: ptr("Ada")}, "Ada"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.DisplayName(tc.partyType, tc.fields); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateParty(t *testing.T) {
	long := ptr(strings.Repeat("x", 201))
	tests := []struct {
		name         string
		partyType    string
		fields       domain.PartyFields
		requireNames bool
		wantFields   []string
	}{
		{"valid organization", domain.TypeOrganization, domain.PartyFields{LegalName: ptr("Acme")}, true, nil},
		{"organization needs legal name", domain.TypeOrganization, domain.PartyFields{}, true, []string{"legalName"}},
		{"person needs both names", domain.TypePerson, domain.PartyFields{}, true, []string{"familyName", "givenName"}},
		{"unknown type", "company", domain.PartyFields{}, true, []string{"partyType"}},
		{"name too long", domain.TypeOrganization, domain.PartyFields{LegalName: long}, true, []string{"legalName"}},
		{"person fields on organization", domain.TypeOrganization, domain.PartyFields{LegalName: ptr("Acme"), GivenName: ptr("Ada")}, true, []string{"givenName"}},
		{"organization fields on person", domain.TypePerson, domain.PartyFields{GivenName: ptr("A"), FamilyName: ptr("B"), LegalName: ptr("Acme")}, true, []string{"legalName"}},
		{"bad dob", domain.TypePerson, domain.PartyFields{GivenName: ptr("A"), FamilyName: ptr("B"), DOB: ptr("2020-13-40")}, true, []string{"dob"}},
		{"future dob", domain.TypePerson, domain.PartyFields{GivenName: ptr("A"), FamilyName: ptr("B"), DOB: ptr("2999-01-01")}, true, []string{"dob"}},
		{"good dob", domain.TypePerson, domain.PartyFields{GivenName: ptr("A"), FamilyName: ptr("B"), DOB: ptr("1990-02-03")}, true, nil},
		{"owner not a uuid", domain.TypeOrganization, domain.PartyFields{LegalName: ptr("Acme"), OwnerUserID: ptr("bob")}, true, []string{"ownerUserId"}},
		{"patch skips required names", domain.TypePerson, domain.PartyFields{}, false, nil},
		{"combined person name too long", domain.TypePerson, domain.PartyFields{GivenName: ptr(strings.Repeat("a", 150)), FamilyName: ptr(strings.Repeat("b", 100))}, true, []string{"familyName"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := domain.ValidateParty(tc.partyType, tc.fields, tc.requireNames)
			got := []string{}
			for f := range errs {
				got = append(got, f)
			}
			if len(got) != len(tc.wantFields) {
				t.Fatalf("got %v, want fields %v", errs, tc.wantFields)
			}
			for _, f := range tc.wantFields {
				if _, ok := errs[f]; !ok {
					t.Fatalf("got %v, want field %s", errs, f)
				}
			}
		})
	}
}

func TestCleanTrimsAndDropsBlank(t *testing.T) {
	got := domain.PartyFields{LegalName: ptr("  Acme "), TradeName: ptr("   ")}.Clean()
	if *got.LegalName != "Acme" || got.TradeName != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestApplyPatch(t *testing.T) {
	tests := []struct {
		name  string
		patch domain.PartyFields
		want  []string
	}{
		{"same value is no change", domain.PartyFields{GivenName: ptr("Ada")}, []string{}},
		{"changed fields are sorted", domain.PartyFields{TimeZone: ptr("UTC"), GivenName: ptr("Grace"), Gender: ptr("f")}, []string{"gender", "givenName", "timeZone"}},
		{"empty clears an optional field", domain.PartyFields{MiddleName: ptr("")}, []string{"middleName"}},
		{"nothing sent", domain.PartyFields{}, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cur := domain.Party{PartyType: domain.TypePerson, PartyFields: domain.PartyFields{GivenName: ptr("Ada"), MiddleName: ptr("M"), FamilyName: ptr("L")}}
			if got := domain.ApplyPatch(&cur, tc.patch); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	cur := domain.Party{PartyFields: domain.PartyFields{MiddleName: ptr("M")}}
	domain.ApplyPatch(&cur, domain.PartyFields{MiddleName: ptr("")})
	if cur.MiddleName != nil {
		t.Fatalf("middle name not cleared: %v", *cur.MiddleName)
	}
}

func TestCheckTransition(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"active", "inactive", true}, {"inactive", "active", true}, {"active", "active", true},
		{"active", "merged", false}, {"merged", "active", false}, {"active", "draft", false}, {"active", "", false},
	}
	for _, tc := range tests {
		if got := domain.CheckTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("%s -> %s: got %v", tc.from, tc.to, got)
		}
	}
}

func TestMaskDOB(t *testing.T) {
	if domain.MaskDOB(nil, false) != nil {
		t.Fatal("unset dob must stay absent")
	}
	if got := domain.MaskDOB(ptr("1990-02-03"), true); *got != "1990-02-03" {
		t.Fatalf("got %q", *got)
	}
	if got := domain.MaskDOB(ptr("1990-02-03"), false); *got != domain.MaskedDOB {
		t.Fatalf("got %q", *got)
	}
}

func TestMaskDOBNeverLeaksWithoutPermission(t *testing.T) {
	leaks := func(dob string) bool {
		if dob == "" || dob == domain.MaskedDOB {
			return false
		}
		return *domain.MaskDOB(&dob, false) == dob
	}
	if err := quick.Check(func(dob string) bool { return !leaks(dob) }, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSearchNameIsNormalizedName(t *testing.T) {
	property := func(given, family string) bool {
		f := domain.PartyFields{GivenName: &given, FamilyName: &family}
		name := domain.DisplayName(domain.TypePerson, f)
		return domain.Normalize(name) == domain.Normalize(strings.TrimSpace(given+" "+family))
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRequestHashDiffersByBody(t *testing.T) {
	a := domain.RequestHash(map[string]string{"n": "a"})
	if a != domain.RequestHash(map[string]string{"n": "a"}) || a == domain.RequestHash(map[string]string{"n": "b"}) {
		t.Fatal("hash must be stable and body-sensitive")
	}
}

func TestPublicFieldsDropsPersonalFields(t *testing.T) {
	got := domain.PublicFields([]string{"dob", "gender", "givenName", "timeZone"})
	if !reflect.DeepEqual(got, []string{"givenName", "timeZone"}) {
		t.Fatalf("got %v", got)
	}
}
