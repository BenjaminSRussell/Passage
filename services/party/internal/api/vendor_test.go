package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const vendorGroup = "00000000-0000-0000-0000-0000000000c1"

type fakeVendors struct {
	created domain.NewVendor
	partyID string
	key     string
	hash    string
	banked  domain.NewBankAccount
	reveal  bool
	err     error
}

func (f *fakeVendors) CreateVendorRole(_ context.Context, partyID string, role store.NewVendorRole) (domain.Vendor, bool, error) {
	in := role.In
	f.partyID, f.created, f.key, f.hash = partyID, in, role.Key, role.Hash
	return domain.Vendor{ID: knownID, PartyID: partyID, VendorGroupID: in.VendorGroupID, AccountNumber: in.AccountNumber, CurrencyCode: in.CurrencyCode, Blocked: "none", Version: 1}, f.err == nil, f.err
}

func (f *fakeVendors) GetVendor(_ context.Context, vendorID string) (domain.Vendor, error) {
	return domain.Vendor{ID: vendorID, VendorGroupID: vendorGroup}, f.err
}

func (f *fakeVendors) ListPartyVendors(_ context.Context, partyID string) ([]domain.Vendor, error) {
	return []domain.Vendor{{ID: knownID, PartyID: partyID, VendorGroupID: vendorGroup}}, f.err
}

func (f *fakeVendors) AddBankAccount(_ context.Context, vendorID string, in domain.NewBankAccount) (domain.BankAccount, error) {
	f.banked = in
	return domain.BankAccount{ID: knownID, VendorID: vendorID, AccountHolder: in.AccountHolder, AccountLast4: in.Last4()}, f.err
}

func (f *fakeVendors) ListBankAccounts(_ context.Context, vendorID string, reveal bool) ([]domain.BankAccount, error) {
	f.reveal = reveal
	b := domain.BankAccount{ID: knownID, VendorID: vendorID, AccountLast4: "3000"}
	if reveal {
		b.AccountNumber = "DE89370400440532013000"
	}
	return []domain.BankAccount{b}, f.err
}

func vendorRouter(f *fakeVendors) http.Handler {
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithVendors(f).Register(r)
	return r
}

func TestVendorRoutes_CreateAndRead(t *testing.T) {
	f := &fakeVendors{}
	r := vendorRouter(f)
	body := `{"accountNumber":" V-1 ","vendorGroupId":"` + strings.ToUpper(vendorGroup) + `","currencyCode":"usd"}`
	rec := do(r, http.MethodPost, "/v1/party/parties/"+knownID+"/vendor", body, "party.write", true)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"vendorGroupId":"`+vendorGroup+`"`) || f.created.CurrencyCode != "USD" || f.created.AccountNumber != "V-1" || f.partyID != knownID {
		t.Fatalf("create: %d %s %+v", rec.Code, rec.Body, f.created)
	}
	rec = do(r, http.MethodGet, "/v1/party/vendors/"+knownID, "", "party.read", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"vendorGroupId":"`+vendorGroup+`"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	rec = do(r, http.MethodGet, "/v1/party/parties/"+knownID+"/vendors", "", "party.read", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"vendors":[{`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
}

func TestVendorRoutes_RefusalsAndValidation(t *testing.T) {
	f := &fakeVendors{}
	r := vendorRouter(f)
	valid := `{"accountNumber":"V-1","vendorGroupId":"` + vendorGroup + `","currencyCode":"USD"}`
	for _, tc := range []struct {
		name, method, path, body, duties string
		tenant                           bool
		want                             int
		wantBody                         string
	}{
		{"create needs party.write", http.MethodPost, "/v1/party/parties/" + knownID + "/vendor", valid, "party.read", true, http.StatusForbidden, ""},
		{"create without tenant", http.MethodPost, "/v1/party/parties/" + knownID + "/vendor", valid, "", false, http.StatusUnauthorized, ""},
		{"group must be a uuid", http.MethodPost, "/v1/party/parties/" + knownID + "/vendor", `{"accountNumber":"V-1","vendorGroupId":"x","currencyCode":"USD"}`, "party.write", true, http.StatusUnprocessableEntity, `"vendorGroupId"`},
		{"currency must be iso", http.MethodPost, "/v1/party/parties/" + knownID + "/vendor", `{"accountNumber":"V-1","vendorGroupId":"` + vendorGroup + `","currencyCode":"US"}`, "party.write", true, http.StatusUnprocessableEntity, `"currencyCode"`},
		{"unknown field", http.MethodPost, "/v1/party/parties/" + knownID + "/vendor", `{"vendorGroup":"x"}`, "party.write", true, http.StatusBadRequest, ""},
		{"get needs party.read", http.MethodGet, "/v1/party/vendors/" + knownID, "", "party.write", true, http.StatusForbidden, ""},
		{"bank write needs its own duty", http.MethodPost, "/v1/party/vendors/" + knownID + "/bank-accounts", bankBody, "party.write", true, http.StatusForbidden, ""},
		{"bank account number too short", http.MethodPost, "/v1/party/vendors/" + knownID + "/bank-accounts", `{"accountHolder":"A","bankCountry":"DE","currencyCode":"EUR","accountNumber":"12"}`, "party.vendor_bank.write", true, http.StatusUnprocessableEntity, `"accountNumber"`},
		{"bank country must be iso", http.MethodPost, "/v1/party/vendors/" + knownID + "/bank-accounts", `{"accountHolder":"A","bankCountry":"DEU","currencyCode":"EUR","accountNumber":"DE89370400440532013000"}`, "party.vendor_bank.write", true, http.StatusUnprocessableEntity, `"bankCountry"`},
		{"reveal needs its own duty", http.MethodGet, "/v1/party/vendors/" + knownID + "/bank-accounts:reveal", "", "party.read", true, http.StatusForbidden, ""},
		{"reveal is not granted by bank write", http.MethodGet, "/v1/party/vendors/" + knownID + "/bank-accounts:reveal", "", "party.vendor_bank.write", true, http.StatusForbidden, ""},
	} {
		rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.tenant)
		if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	if f.created != (domain.NewVendor{}) || f.banked != (domain.NewBankAccount{}) || f.reveal {
		t.Fatalf("a refused request reached the store: %+v %+v reveal=%t", f.created, f.banked, f.reveal)
	}
}

const bankBody = `{"accountHolder":"Acme GmbH","bankCountry":"de","currencyCode":"eur","accountNumber":"de89 3704 0044 0532 0130 00","routingNumber":""}`

func TestVendorRoutes_BankAccountsMaskedUnlessRevealDuty(t *testing.T) {
	f := &fakeVendors{}
	r := vendorRouter(f)
	rec := do(r, http.MethodPost, "/v1/party/vendors/"+knownID+"/bank-accounts", bankBody, "party.vendor_bank.write", true)
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "DE89") || !strings.Contains(rec.Body.String(), `"accountLast4":"3000"`) || f.banked.AccountNumber != "DE89370400440532013000" || f.banked.BankCountry != "DE" {
		t.Fatalf("add: %d %s %+v", rec.Code, rec.Body, f.banked)
	}
	rec = do(r, http.MethodGet, "/v1/party/vendors/"+knownID+"/bank-accounts", "", "party.read", true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "accountNumber") || f.reveal {
		t.Fatalf("masked: %d %s reveal=%t", rec.Code, rec.Body, f.reveal)
	}
	rec = do(r, http.MethodGet, "/v1/party/vendors/"+knownID+"/bank-accounts:reveal", "", "party.vendor_bank.reveal", true)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("reveal Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	if rec = do(r, http.MethodGet, "/v1/party/vendors/"+knownID+"/bank-accounts:reveal", "", "party.read", true); rec.Code != http.StatusForbidden || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("refused reveal: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	rec = do(r, http.MethodGet, "/v1/party/vendors/"+knownID+"/bank-accounts:reveal", "", "party.vendor_bank.reveal", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"accountNumber":"DE89370400440532013000"`) || !f.reveal {
		t.Fatalf("reveal: %d %s reveal=%t", rec.Code, rec.Body, f.reveal)
	}
}

func TestVendorRoutes_StoreProblemsPassThrough(t *testing.T) {
	f := &fakeVendors{err: httpx.NewProblem(http.StatusConflict, store.CodeVendorExists, "exists")}
	valid := `{"accountNumber":"V-1","vendorGroupId":"` + vendorGroup + `","currencyCode":"USD"}`
	rec := do(vendorRouter(f), http.MethodPost, "/v1/party/parties/"+knownID+"/vendor", valid, "party.write", true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeVendorExists) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	f.err = store.ErrNotFound
	rec = do(vendorRouter(f), http.MethodPost, "/v1/party/parties/"+knownID+"/vendor", valid, "party.write", true)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "PTY-PARTY-NOT-FOUND") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
