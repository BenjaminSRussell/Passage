package api_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	importDuty    = "party.import.write"
	importerActor = "service:migration-loader"
	importPartyID = "00000000-0000-0000-0000-0000000000c9"
	groupID       = "00000000-0000-0000-0000-0000000000d1"
	termID        = "00000000-0000-0000-0000-0000000000d2"
	profileID     = "00000000-0000-0000-0000-0000000000d3"
)

type fakeCustomers struct {
	partyID string
	in      store.NewCustomerRole
	scopes  []string
	replay  bool
	err     error
}

func (f *fakeCustomers) GetCustomer(_ context.Context, id string) (domain.Customer, error) {
	if id != knownID {
		return domain.Customer{}, httpx.NewProblem(http.StatusNotFound, store.CodeCustomerNotFound, "customer not found")
	}
	return domain.Customer{ID: knownID, PartyID: importPartyID, AccountNumber: "C-1", CurrencyCode: "EUR", Version: 3}, f.err
}

func (f *fakeCustomers) PortalAccounts(context.Context, string) ([]store.PortalAccount, error) {
	return nil, nil
}

func (f *fakeCustomers) CreateCustomer(ctx context.Context, partyID string, in store.NewCustomerRole) (domain.Customer, bool, error) {
	caller, _ := tenant.FromContext(ctx)
	f.scopes = tenant.LegalEntityScopeList(caller)
	f.partyID, f.in = partyID, in
	if f.err != nil {
		return domain.Customer{}, false, f.err
	}
	c := in.In
	le := ""
	if len(f.scopes) > 0 {
		le = f.scopes[0]
	}
	return domain.Customer{ID: knownID, PartyID: partyID, LegalEntityID: le, AccountNumber: c.AccountNumber, CustomerGroupID: c.CustomerGroupID,
		PaymentTermID: c.PaymentTermID, PostingProfileID: c.PostingProfileID, CurrencyCode: c.CurrencyCode, CreditLimit: c.CreditLimit,
		CreditCurrency: c.CreditCurrency, Blocked: c.Blocked, Version: 1}, !f.replay, nil
}

const customerBody = `{"accountNumber":" C-1 ","customerGroupId":"` + groupID + `","paymentTermId":"` + termID + `","postingProfileId":"` + profileID + `","currencyCode":"eur","creditLimit":"1500.5"}`

func customerRouter(f *fakeCustomers, parties *fakeParties, vendors *fakeVendors) *chi.Mux {
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, parties, &fakeSequencer{}).WithVendors(vendors).WithCustomers(f).Register(r)
	return r
}

func TestCustomerRoutes_CreateWithHumanDuty(t *testing.T) {
	f, parties := &fakeCustomers{}, &fakeParties{}
	r := customerRouter(f, parties, &fakeVendors{})
	path := partiesPath + "/" + knownID + "/customer"
	rec := do(r, http.MethodPost, path, customerBody, "party.write", true)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"accountNumber":"C-1"`) || !strings.Contains(rec.Body.String(), `"creditCurrency":"EUR"`) {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	c := f.in.In
	if f.partyID != knownID || c.CurrencyCode != "EUR" || c.Blocked != "none" || *c.CreditLimit != "1500.5" || f.in.Import {
		t.Fatalf("store input = %+v for %s", c, f.partyID)
	}
	if rec := do(r, http.MethodPost, path, customerBody, "party.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("party.read only: %d, want 403", rec.Code)
	}
}

func TestCustomerRoutes_ValidationReplayAndConflict(t *testing.T) {
	f := &fakeCustomers{}
	r := customerRouter(f, &fakeParties{}, &fakeVendors{})
	path := partiesPath + "/" + knownID + "/customer"
	for name, body := range map[string]string{
		"missing fields": `{}`,
		"bad limit":      `{"accountNumber":"C-1","customerGroupId":"` + groupID + `","paymentTermId":"` + termID + `","postingProfileId":"` + profileID + `","currencyCode":"EUR","creditLimit":"1e3"}`,
		"bad blocked":    `{"accountNumber":"C-1","customerGroupId":"` + groupID + `","paymentTermId":"` + termID + `","postingProfileId":"` + profileID + `","currencyCode":"EUR","blocked":"payment"}`,
		"bad term":       `{"accountNumber":"C-1","customerGroupId":"` + groupID + `","paymentTermId":"x","postingProfileId":"` + profileID + `","currencyCode":"EUR"}`,
	} {
		if rec := do(r, http.MethodPost, path, body, "party.write", true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeCustomerInvalid) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPost, path, `{"nope":1}`, "party.write", true); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(customerBody))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "party.write")
	req.Header.Set("Idempotency-Key", "k-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || f.in.Key != "k-1" || f.in.Hash == "" {
		t.Fatalf("keyed create: %d key=%q hash=%q", rec.Code, f.in.Key, f.in.Hash)
	}
	f.err = httpx.NewProblem(http.StatusConflict, store.CodeCustomerExists, "exists")
	if rec := do(r, http.MethodPost, path, customerBody, "party.write", true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeCustomerExists) {
		t.Errorf("exists: %d %s", rec.Code, rec.Body)
	}
	f.err = store.ErrNotFound
	if rec := do(r, http.MethodPost, path, customerBody, "party.write", true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown party: %d", rec.Code)
	}
}

func TestCreateParty_CallerSuppliedIDNeedsImportDuty(t *testing.T) {
	parties := &fakeParties{}
	r := customerRouter(&fakeCustomers{}, parties, &fakeVendors{})
	body := `{"id":"` + importPartyID + `","partyType":"organization","legalName":"Acme"}`
	if rec := do(r, http.MethodPost, partiesPath, body, "party.write", true); rec.Code != http.StatusForbidden || parties.created.ID != "" {
		t.Fatalf("human supplying id: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPost, partiesPath, strings.Replace(body, importPartyID, strings.ToUpper(importPartyID), 1), importDuty, true); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("non-canonical id: %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, partiesPath, body, importDuty, true); rec.Code != http.StatusCreated || parties.created.ID != importPartyID {
		t.Fatalf("importer supplying id: %d %s id=%q", rec.Code, rec.Body, parties.created.ID)
	}
	if rec := do(r, http.MethodPost, partiesPath, `{"partyType":"organization","legalName":"Acme"}`, "party.write", true); rec.Code != http.StatusCreated || parties.created.ID != "" {
		t.Fatalf("human without id: %d", rec.Code)
	}
}

// migrationRig drives the real routes with a real service verifier and a
// signer named migration, the way the migration loader calls party.
type migrationRig struct {
	handler   http.Handler
	signer    *tenant.ServiceSigner
	parties   *fakeParties
	vendors   *fakeVendors
	customers *fakeCustomers
}

func newMigrationRig(t *testing.T) *migrationRig {
	t.Helper()
	// The grant is seeded on main (platform/go/tenant); a missing grant must
	// fail here, never skip, or the loader contract goes untested.
	if !slicesContains(tenant.ServiceGrantTable()["migration"]["party"], importDuty) {
		t.Fatal("service grant migration -> party.import.write is missing from platform/go/tenant")
	}
	pub, key, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("migration", key)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("party", map[string]ed25519.PublicKey{"migration": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	rig := &migrationRig{signer: signer, parties: &fakeParties{}, vendors: &fakeVendors{}, customers: &fakeCustomers{}}
	rig.handler = customerRouter(rig.customers, rig.parties, rig.vendors)
	return rig
}

func slicesContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func (m *migrationRig) call(t *testing.T, legalEntity, method, path, body string, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := m.signer.Mint("party", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntity, UserID: importerActor}, legalEntity)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(tenant.HeaderServiceToken, token)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	m.handler.ServeHTTP(rec, req)
	return rec
}

func TestMigrationLoader_CreatesPartyVendorAndCustomerInItsOwnLegalEntity(t *testing.T) {
	m := newMigrationRig(t)
	le := testkit.TenantID(7)
	other := testkit.TenantID(8)
	body := `{"id":"` + importPartyID + `","partyType":"organization","legalName":"Acme"}`
	if rec := m.call(t, le, http.MethodPost, partiesPath, body, nil); rec.Code != http.StatusCreated || m.parties.created.ID != importPartyID {
		t.Fatalf("party: %d %s", rec.Code, rec.Body)
	}
	if rec := m.call(t, le, http.MethodPatch, partiesPath+"/"+knownID, `{"version":1,"tradeName":"Acme Trading"}`, nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	vendor := `{"accountNumber":"V-1","vendorGroupId":"` + vendorGroup + `","currencyCode":"USD"}`
	// A header naming another legal entity must not move the role: the token's wins.
	hdr := map[string]string{"X-Passage-Legal-Entity": other, "Idempotency-Key": "row-17"}
	if rec := m.call(t, le, http.MethodPost, partiesPath+"/"+knownID+"/vendor", vendor, hdr); rec.Code != http.StatusCreated || m.vendors.partyID != knownID || m.vendors.key != "row-17" || m.vendors.hash == "" {
		t.Fatalf("vendor: %d %s key=%q", rec.Code, rec.Body, m.vendors.key)
	}
	if rec := m.call(t, le, http.MethodPost, partiesPath+"/"+knownID+"/customer", customerBody, hdr); rec.Code != http.StatusCreated || !m.customers.in.Import || m.customers.in.Key != "row-17" {
		t.Fatalf("customer: %d %s import=%v", rec.Code, rec.Body, m.customers.in.Import)
	}
	if len(m.customers.scopes) != 1 || m.customers.scopes[0] != le {
		t.Fatalf("customer role scoped to %v, want only %s", m.customers.scopes, le)
	}
	if rec := m.call(t, other, http.MethodPost, partiesPath+"/"+knownID+"/customer", customerBody, nil); rec.Code != http.StatusCreated || m.customers.scopes[0] != other {
		t.Fatalf("a token for another legal entity gets its own scope: %d %v", rec.Code, m.customers.scopes)
	}
}

func TestMigrationLoader_CannotReachOtherRoutes(t *testing.T) {
	m := newMigrationRig(t)
	le := testkit.TenantID(7)
	for name, c := range map[string]struct{ method, path, body string }{
		"list vendors":   {http.MethodGet, partiesPath + "/" + knownID + "/vendors", ""},
		"vendor groups":  {http.MethodGet, "/v1/party/vendor-groups", ""},
		"bank list":      {http.MethodGet, "/v1/party/vendors/" + knownID + "/bank-accounts", ""},
		"bank accounts":  {http.MethodPost, "/v1/party/vendors/" + knownID + "/bank-accounts", `{}`},
		"reveal":         {http.MethodGet, "/v1/party/vendors/" + knownID + "/bank-accounts:reveal", ""},
		"tax validation": {http.MethodPost, validatePath, `{"type":"EIN","number":"12-3456789"}`},
	} {
		if rec := m.call(t, le, c.method, c.path, c.body, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, rec.Code)
		}
	}
}

// party.import.read opens the three detail reads the loader reconciles against, and the date of birth stays masked
// because the loader holds no pii.read.
func TestMigrationLoader_ReadsBackPartyCustomerAndVendor(t *testing.T) {
	m := newMigrationRig(t)
	le := testkit.TenantID(7)
	party := m.call(t, le, http.MethodGet, partiesPath+"/"+knownID, "", nil)
	if party.Code != http.StatusOK || !strings.Contains(party.Body.String(), `"name":"Ada L"`) || strings.Contains(party.Body.String(), "1990-02-03") || party.Header().Get("ETag") != `"4"` {
		t.Fatalf("party: %d %s etag=%s", party.Code, party.Body, party.Header().Get("ETag"))
	}
	customer := m.call(t, le, http.MethodGet, "/v1/party/customers/"+knownID, "", nil)
	if customer.Code != http.StatusOK || !strings.Contains(customer.Body.String(), `"accountNumber":"C-1"`) || customer.Header().Get("ETag") != `"3"` {
		t.Fatalf("customer: %d %s", customer.Code, customer.Body)
	}
	if rec := m.call(t, le, http.MethodGet, "/v1/party/customers/"+importPartyID, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown customer: %d %s", rec.Code, rec.Body)
	}
	if rec := m.call(t, le, http.MethodGet, "/v1/party/vendors/"+knownID, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("vendor: %d %s", rec.Code, rec.Body)
	}
	// A person needs party.read for the customer read; party.write alone does not open it.
	if rec := do(m.handler, http.MethodGet, "/v1/party/customers/"+knownID, "", "party.write", true); rec.Code != http.StatusForbidden {
		t.Fatalf("party.write customer read: %d", rec.Code)
	}
	if rec := do(m.handler, http.MethodGet, "/v1/party/customers/"+knownID, "", "party.read", true); rec.Code != http.StatusOK {
		t.Fatalf("party.read customer read: %d", rec.Code)
	}
}

// The loader's create or PATCH answers with a receipt, never the record: a write
// answers with a receipt, never the names or personal data it could not GET.
func TestMigrationLoader_WritesGetAReceiptNotTheRecord(t *testing.T) {
	m := newMigrationRig(t)
	le := testkit.TenantID(7)
	person := `{"id":"` + importPartyID + `","partyType":"person","givenName":"Ada","familyName":"Lovelace","dob":"1815-12-10"}`
	rec := m.call(t, le, http.MethodPost, partiesPath, person, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"create": rec,
		"patch":  m.call(t, le, http.MethodPatch, partiesPath+"/"+knownID, `{"version":1}`, nil),
	} {
		body := rec.Body.String()
		for _, leak := range []string{"Ada", "Lovelace", "1815", `"name"`, `"dob"`, `"searchName"`} {
			if strings.Contains(body, leak) {
				t.Errorf("%s response leaks %s: %s", name, leak, body)
			}
		}
		if !strings.Contains(body, `"id":`) || !strings.Contains(body, `"version":`) {
			t.Errorf("%s receipt lacks id or version: %s", name, body)
		}
	}
	// A person with party.write still gets the record back, as before.
	if rec := do(m.handler, http.MethodPost, partiesPath, `{"partyType":"organization","legalName":"Acme"}`, "party.write", true); !strings.Contains(rec.Body.String(), `"legalName":"Acme"`) {
		t.Fatalf("human create lost the record: %s", rec.Body)
	}
}

func TestVendorRoutes_IdempotencyKeyReachesTheStore(t *testing.T) {
	f := &fakeVendors{}
	r := vendorRouter(f)
	req := httptest.NewRequest(http.MethodPost, partiesPath+"/"+knownID+"/vendor", strings.NewReader(`{"accountNumber":"V-1","vendorGroupId":"`+vendorGroup+`","currencyCode":"usd"}`))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "party.write")
	req.Header.Set("Idempotency-Key", "v-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || f.key != "v-1" || f.hash == "" || f.created.CurrencyCode != "USD" {
		t.Fatalf("keyed vendor create: %d key=%q hash=%q", rec.Code, f.key, f.hash)
	}
	f.err = httpx.NewProblem(http.StatusConflict, store.CodeIdempotencyReuse, "reused")
	if rec := do(r, http.MethodPost, partiesPath+"/"+knownID+"/vendor", `{"accountNumber":"V-1","vendorGroupId":"`+vendorGroup+`","currencyCode":"USD"}`, "party.write", true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeIdempotencyReuse) {
		t.Fatalf("key reuse: %d %s", rec.Code, rec.Body)
	}
}
