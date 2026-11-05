package partydata_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/partydata"
)

const (
	vendorID = "11111111-1111-4111-8111-111111111111"
	partyID  = "22222222-2222-4222-8222-222222222222"
	leID     = "33333333-3333-4333-8333-333333333333"
	addrID   = "44444444-4444-4444-8444-444444444444"
)

func servers(t *testing.T, partyStatus int) (*httptest.Server, *httptest.Server) {
	t.Helper()
	party := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if partyStatus != http.StatusOK {
			w.WriteHeader(partyStatus)
			return
		}
		switch r.URL.Path {
		case "/v1/party/vendors/" + vendorID:
			_, _ = w.Write([]byte(`{"id":"` + vendorID + `","partyId":"` + partyID + `"}`))
		case "/v1/party/parties/" + partyID:
			_, _ = w.Write([]byte(`{"name":"Acme","legalName":"Acme Supplies GmbH","registrationNumber":"HRB 123"}`))
		default:
			t.Errorf("party path %s", r.URL.Path)
		}
	}))
	org := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/org/addresses" && strings.Contains(r.URL.RawQuery, "ownerType=party") && strings.Contains(r.URL.RawQuery, "ownerId="+partyID):
			_, _ = w.Write([]byte(`{"items":[{"id":"` + addrID + `","ownerType":"party","countryCode":"DE","formatted":"Hauptstr 1, 10115 Berlin","purposes":[{"purpose":"business","primary":true}]}]}`))
		case r.URL.Path == "/v1/org/legal-entities/"+leID:
			_, _ = w.Write([]byte(`{"name":"Passage Werke AG","country":"DE","registrationNumber":"HRB 999","primaryAddressId":"` + addrID + `"}`))
		case r.URL.Path == "/v1/org/addresses/"+addrID:
			_, _ = w.Write([]byte(`{"id":"` + addrID + `","countryCode":"DE","formatted":"Ring 5, 80331 Muenchen","purposes":[{"purpose":"invoice","primary":true},{"purpose":"business","primary":false}]}`))
		default:
			t.Errorf("org path %s?%s", r.URL.Path, r.URL.RawQuery)
		}
	}))
	t.Cleanup(party.Close)
	t.Cleanup(org.Close)
	return party, org
}

func TestSelfBillParties_ReadsSupplierAndCustomerFromTheMasters(t *testing.T) {
	party, org := servers(t, http.StatusOK)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leID, UserID: "alice"})
	got, err := partydata.New(party.URL, org.URL, nil).SelfBillParties(ctx, leID, vendorID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Supplier.Name != "Acme Supplies GmbH" || got.Supplier.Address != "Hauptstr 1, 10115 Berlin" || got.Supplier.Country != "DE" || got.Supplier.RegistrationNumber != "HRB 123" {
		t.Fatalf("supplier %+v", got.Supplier)
	}
	if got.Customer.Name != "Passage Werke AG" || got.Customer.Address != "Ring 5, 80331 Muenchen" || got.Customer.Country != "DE" || got.Customer.RegistrationNumber != "HRB 999" {
		t.Fatalf("customer %+v", got.Customer)
	}
}

func TestSelfBillParties_FailsWhenAMasterAnswersAnError(t *testing.T) {
	party, org := servers(t, http.StatusInternalServerError)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leID, UserID: "alice"})
	if _, err := partydata.New(party.URL, org.URL, nil).SelfBillParties(ctx, leID, vendorID); err == nil {
		t.Fatal("an unavailable party master produced a document")
	}
	if _, err := partydata.New(party.URL, org.URL, nil).SelfBillParties(context.Background(), leID, vendorID); err == nil {
		t.Fatal("a call without a principal succeeded")
	}
}

func TestSelfBillParties_RefusesASupplierWithoutAnAddressAndPrefersTheInvoiceAddress(t *testing.T) {
	var items string
	party := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/party/vendors/") {
			_, _ = w.Write([]byte(`{"partyId":"` + partyID + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"legalName":"Acme"}`))
	}))
	org := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/org/addresses":
			_, _ = w.Write([]byte(`{"items":` + items + `}`))
		case "/v1/org/legal-entities/" + leID:
			_, _ = w.Write([]byte(`{"name":"Passage","country":"DE"}`))
		}
	}))
	t.Cleanup(party.Close)
	t.Cleanup(org.Close)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leID, UserID: "alice"})
	c := partydata.New(party.URL, org.URL, nil)
	items = `[]`
	if _, err := c.SelfBillParties(ctx, leID, vendorID); !errors.Is(err, domain.ErrSelfBillPartyIncomplete) {
		t.Fatalf("a supplier without an address: %v, want ErrSelfBillPartyIncomplete", err)
	}
	items = `[{"countryCode":"","formatted":"x","purposes":[{"purpose":"invoice"}]}]`
	if _, err := c.SelfBillParties(ctx, leID, vendorID); !errors.Is(err, domain.ErrSelfBillPartyIncomplete) {
		t.Fatalf("a supplier address without a country: %v, want ErrSelfBillPartyIncomplete", err)
	}
	// The org service returns purposes as AddressPurpose objects; the invoice
	// address wins over business, and a primary one over a non-primary one.
	items = `[{"countryCode":"FR","formatted":"Home","purposes":[{"purpose":"home","primary":true}]},
		{"countryCode":"AT","formatted":"Office","purposes":[{"purpose":"business","primary":true}]},
		{"countryCode":"NL","formatted":"Old billing","purposes":[{"purpose":"invoice","primary":false}]},
		{"countryCode":"DE","formatted":"Billing","purposes":[{"purpose":"delivery"},{"purpose":"invoice","primary":true}]}]`
	got, err := c.SelfBillParties(ctx, leID, vendorID)
	if err != nil || got.Supplier.Address != "Billing" || got.Supplier.Country != "DE" {
		t.Fatalf("supplier %+v err %v", got.Supplier, err)
	}
	items = `[{"countryCode":"FR","formatted":"Home","purposes":[{"purpose":"home"}]},
		{"countryCode":"AT","formatted":"Office","purposes":[{"purpose":"business","primary":false}]}]`
	if got, err = c.SelfBillParties(ctx, leID, vendorID); err != nil || got.Supplier.Address != "Office" {
		t.Fatalf("without an invoice address the business one is used: %+v err %v", got.Supplier, err)
	}
}
