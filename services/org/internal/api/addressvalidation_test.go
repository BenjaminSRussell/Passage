package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

const normalizePath = "/v1/org/addresses:normalize"

type failingValidator struct{ domain.NullAddressValidator }

func (failingValidator) Suggest(context.Context, domain.PostalAddress) ([]domain.AddressSuggestion, error) {
	return nil, errors.New("down")
}

func TestAddressNormalize(t *testing.T) {
	tests := []struct {
		name       string
		validator  domain.AddressValidator
		body       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"rules normalize a gb address", domain.RuleAddressValidator{}, `{"countryCode":"uk","city":"London","postalCode":"sw1a1aa","street1":"10 Downing St"}`, "org.read", true, http.StatusOK, `"countryCode":"GB","city":"London","postalCode":"SW1A 1AA"`},
		{"rules suggest a leading zero", domain.RuleAddressValidator{}, `{"countryCode":"US","regionCode":"MA","city":"Boston","postalCode":"2108","street1":"1 Beacon St"}`, "org.read", true, http.StatusOK, `"postalCode":"02108","street1":"1 Beacon St"},"reason":"postal code is missing a leading zero"`},
		{"rules return formatted text and no suggestions", domain.RuleAddressValidator{}, `{"countryCode":"FR","city":"Paris","postalCode":"75001","street1":"1 Rue de Rivoli"}`, "org.read", true, http.StatusOK, `"formatted":"1 Rue de Rivoli\n75001 Paris\nFR","suggestions":[]`},
		{"null default echoes the input", domain.NullAddressValidator{}, `{"countryCode":"GB","postalCode":"sw1a1aa"}`, "org.read", true, http.StatusOK, `"postalCode":"sw1a1aa"`},
		{"missing country", domain.RuleAddressValidator{}, `{"postalCode":"75001"}`, "org.read", true, http.StatusBadRequest, `"countryCode"`},
		{"unknown field", domain.RuleAddressValidator{}, `{"countryCode":"US","surprise":1}`, "org.read", true, http.StatusBadRequest, "COM-0001"},
		{"adapter failure", failingValidator{}, `{"countryCode":"US"}`, "org.read", true, http.StatusInternalServerError, ""},
		{"without duty", domain.RuleAddressValidator{}, `{"countryCode":"US"}`, "", true, http.StatusForbidden, "AUTH-0003"},
		{"without tenant", domain.RuleAddressValidator{}, `{"countryCode":"US"}`, "", false, http.StatusUnauthorized, "AUTH-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, a := newRouter(&fakeStore{})
			a.SetAddressValidator(tc.validator)
			rec := do(r, http.MethodPost, normalizePath, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
		})
	}
}
