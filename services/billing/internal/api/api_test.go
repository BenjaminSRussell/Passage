package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	leID   = "0190f0a0-0000-7000-8000-000000000001"
	custID = "0190f0a0-0000-7000-8000-000000000002"
	acctID = "0190f0a0-0000-7000-8000-000000000003"
	key    = "post-key-1"
)

// fakeStore keeps invoices in memory and counts writes.
type fakeStore struct {
	invoices      map[string]store.Invoice
	creates       int
	posts         int
	creditCreates int
	creditPosts   int
	postedVersion int
	lastFilter    store.ListFilter
	// Shipment proposal batches by key, their invoice ids and the slip lines invoiced.
	batches       map[string]store.ProposalBatch
	batchInvoices map[string][]string
	slipLines     map[string]bool
	batchCreates  int
}

func newFakeStore() *fakeStore { return &fakeStore{invoices: map[string]store.Invoice{}} }

func (f *fakeStore) Ping(context.Context) error { return nil }

func (f *fakeStore) FindByIdempotencyKey(_ context.Context, k string) (store.Invoice, bool, error) {
	for _, inv := range f.invoices {
		if inv.IdempotencyKey == k {
			return inv, true, nil
		}
	}
	return store.Invoice{}, false, nil
}

func (f *fakeStore) CreateProposal(_ context.Context, inv store.Invoice) (store.Invoice, error) {
	f.creates++
	inv.Version = 1
	f.invoices[inv.ID] = inv
	return inv, nil
}

func (f *fakeStore) GetInvoice(_ context.Context, id string) (store.Invoice, error) {
	inv, ok := f.invoices[id]
	if !ok {
		return store.Invoice{}, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "invoice not found")
	}
	if inv.Type != domain.TypeCreditNote {
		inv.Credited = f.usage(id)
	}
	return inv, nil
}

// usage sums the posted credit notes of an original, as the store does.
func (f *fakeStore) usage(id string) domain.CreditUsage {
	u := domain.CreditUsage{Lines: map[int]domain.Credited{}, Charges: decimal.Zero, Gross: decimal.Zero}
	for _, cn := range f.invoices {
		if cn.OriginalInvoiceID != id || cn.Status != domain.StatusPosted {
			continue
		}
		for _, l := range cn.Lines {
			c := u.Lines[l.OriginalLineNo]
			u.Lines[l.OriginalLineNo] = domain.Credited{Qty: c.Qty.Add(l.Qty), Net: c.Net.Add(l.Net), Tax: c.Tax.Add(l.Tax)}
		}
		u.Charges = u.Charges.Add(cn.Totals.Charges)
		u.Gross = u.Gross.Add(cn.Totals.Gross)
	}
	return u
}

func (f *fakeStore) CreateCreditNote(_ context.Context, cn store.Invoice) (store.Invoice, error) {
	f.creditCreates++
	cn.Version = 1
	f.invoices[cn.ID] = cn
	return cn, nil
}

// PostCreditNote re-checks the remaining creditable amount like the store does
// under the original's lock, so two proposals cannot both post.
func (f *fakeStore) PostCreditNote(_ context.Context, id string, postingDate time.Time, _ int) (store.Invoice, error) {
	cn := f.invoices[id]
	orig := f.invoices[cn.OriginalInvoiceID]
	original := make([]domain.PostedLine, len(orig.Lines))
	for i, l := range orig.Lines {
		original[i] = domain.PostedLine{Line: l.Line, Net: l.Net, Tax: l.Tax}
	}
	credit := make([]domain.CreditLine, len(cn.Lines))
	for i, l := range cn.Lines {
		credit[i] = domain.CreditLine{OriginalLineNo: l.OriginalLineNo, Line: l.Line, Net: l.Net, Tax: l.Tax}
	}
	if err := domain.VerifyCredit(cn.Currency, original, orig.Totals, f.usage(orig.ID), credit, cn.Totals); err != nil {
		var de *domain.Error
		errors.As(err, &de)
		return store.Invoice{}, httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	f.creditPosts++
	cn.Status = domain.StatusPosted
	cn.Number = fmt.Sprintf("CN-%d-%07d", postingDate.Year(), f.creditPosts)
	cn.PostingDate = postingDate
	cn.Version++
	f.invoices[id] = cn
	return cn, nil
}

// ListInvoices filters like the store and pages newest first; invoice ids are
// UUIDv7, so descending id order stands in for (created_at, id).
func (f *fakeStore) ListInvoices(_ context.Context, filter store.ListFilter) (store.InvoicePage, error) {
	f.lastFilter = filter
	var out []store.Invoice
	for _, inv := range f.invoices {
		if (filter.Status == "" || inv.Status == filter.Status) && (filter.Type == "" || inv.Type == filter.Type) &&
			(filter.OriginalInvoiceID == "" || inv.OriginalInvoiceID == filter.OriginalInvoiceID) &&
			(filter.After == "" || inv.ID < filter.After) {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	page := store.InvoicePage{Items: out}
	if len(out) > filter.Limit {
		page.Items = out[:filter.Limit]
		page.NextAfter = page.Items[filter.Limit-1].ID
	}
	return page, nil
}

func (f *fakeStore) PostProposal(_ context.Context, id string, postingDate time.Time, expectedVersion int) (store.Invoice, error) {
	f.posts++
	f.postedVersion = expectedVersion
	inv := f.invoices[id]
	inv.Status = domain.StatusPosted
	inv.Number = fmt.Sprintf("INV-%d-%07d", postingDate.Year(), f.posts)
	inv.PostingDate = postingDate
	inv.Version++
	f.invoices[id] = inv
	return inv, nil
}

// fakeTax quotes the same tax on every line, or fails.
type fakeTax struct {
	perLine string
	err     error
}

func (f *fakeTax) Quote(_ context.Context, _ string, lines []domain.Line) ([]decimal.Decimal, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]decimal.Decimal, len(lines))
	for i := range out {
		out[i] = decimal.RequireFromString(f.perLine)
	}
	return out, nil
}

const allDuties = "billing.invoice.view,billing.invoice.maintain,billing.invoice.post,billing.creditnote.post"

// setup mounts the routes on the platform router, so tenant and duty headers
// go through the same middleware as in production.
func setup(t *testing.T, s *fakeStore, tax TaxEngine) http.Handler {
	t.Helper()
	a := New(s, tax, 2000)
	a.now = func() time.Time { return time.Date(2027, 3, 12, 15, 4, 0, 0, time.UTC) }
	r := httpx.NewRouter()
	a.Register(r)
	return r
}

func do(h http.Handler, method, path, idemKey, body string) *httptest.ResponseRecorder {
	return doAs(h, method, path, idemKey, body, allDuties, nil)
}

func doAs(h http.Handler, method, path, idemKey, body, duties string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
	req.Header.Set(tenant.HeaderUser, "clerk")
	req.Header.Set(tenant.HeaderDuties, duties)
	req.Header.Set(tenant.HeaderLegalEntity, leID)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func proposal(qty, price, account string) string {
	return fmt.Sprintf(`{"legalEntityId":%q,"customerId":%q,"invoiceAccountId":%q,"currency":"EUR","invoiceDate":"2027-03-10","paymentTermDays":30,`+
		`"lines":[{"description":"Consulting","qty":%q,"unitPrice":%q,"revenueAccount":%q,"taxCode":"DE19"}]}`, leID, custID, acctID, qty, price, account)
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func TestCreateProposal_TotalsAndReplay(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	rec := do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100", "4900"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got invoiceDTO
	decode(t, rec, &got)
	if got.Status != "PROPOSAL" || got.Totals.Net != "100.00" || got.Totals.Tax != "19.00" || got.Totals.Gross != "119.00" {
		t.Fatalf("unexpected invoice %+v", got)
	}
	if got.DueDate != "2027-04-09" {
		t.Errorf("due date %s, want 2027-04-09 (30 days)", got.DueDate)
	}

	replay := do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100", "4900"))
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status %d headers %v", replay.Code, replay.Header())
	}
	if s.creates != 1 {
		t.Errorf("creates %d, want 1", s.creates)
	}
	other := do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("2", "100", "4900"))
	if other.Code != http.StatusUnprocessableEntity {
		t.Errorf("reused key with new body: status %d, want 422", other.Code)
	}
}

func TestCreateProposal_Validation(t *testing.T) {
	h := setup(t, newFakeStore(), &fakeTax{perLine: "0"})
	if rec := do(h, http.MethodPost, "/v1/billing/free-text-invoices", "", proposal("1", "1", "4900")); rec.Code != http.StatusBadRequest {
		t.Errorf("missing key: status %d", rec.Code)
	}
	rec := do(h, http.MethodPost, "/v1/billing/free-text-invoices", "k2", proposal("1", "1", ""))
	var p httpx.Problem
	decode(t, rec, &p)
	if rec.Code != http.StatusBadRequest || p.Errors["lines[0].revenueAccount"] == "" {
		t.Errorf("missing account: status %d problem %+v", rec.Code, p)
	}
}

func TestCreateProposal_TaxUnavailableStoresNothing(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, UnavailableTax{})
	rec := do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100", "4900"))
	var p httpx.Problem
	decode(t, rec, &p)
	if rec.Code != http.StatusServiceUnavailable || p.Code != CodeTaxUnavailable {
		t.Fatalf("status %d code %s, want 503 BIL-0001", rec.Code, p.Code)
	}
	if s.creates != 0 {
		t.Errorf("a proposal was stored although tax was unavailable")
	}
}

func TestPost_NumbersOnceAndIsIdempotent(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	var created invoiceDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100", "4900")), &created)

	rec := do(h, http.MethodPost, "/v1/billing/invoices/"+created.ID+":post", "", "")
	var posted invoiceDTO
	decode(t, rec, &posted)
	if rec.Code != http.StatusOK || posted.Status != "POSTED" || posted.Number != "INV-2027-0000001" || posted.PostingDate != "2027-03-12" {
		t.Fatalf("post status %d body %+v", rec.Code, posted)
	}
	again := do(h, http.MethodPost, "/v1/billing/invoices/"+created.ID+":post", "", "")
	var repeat invoiceDTO
	decode(t, again, &repeat)
	if again.Code != http.StatusOK || repeat.Number != posted.Number || s.posts != 1 {
		t.Errorf("repeat post: status %d number %s posts %d", again.Code, repeat.Number, s.posts)
	}
}

func TestPost_RefusesTaxMovedBeyondTolerance(t *testing.T) {
	s := newFakeStore()
	tax := &fakeTax{perLine: "19.00"}
	h := setup(t, s, tax)
	var created invoiceDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100", "4900")), &created)

	tax.perLine = "19.02"
	rec := do(h, http.MethodPost, "/v1/billing/invoices/"+created.ID+":post", "", "")
	var p httpx.Problem
	decode(t, rec, &p)
	if rec.Code != http.StatusConflict || p.Code != CodeTaxMoved || s.posts != 0 {
		t.Fatalf("status %d code %s posts %d, want 409 BIL-0002 and no post", rec.Code, p.Code, s.posts)
	}
}

func TestPost_TaxUnavailableLeavesProposal(t *testing.T) {
	s := newFakeStore()
	tax := &fakeTax{perLine: "19.00"}
	h := setup(t, s, tax)
	var created invoiceDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100", "4900")), &created)

	tax.err = ErrTaxUnavailable
	rec := do(h, http.MethodPost, "/v1/billing/invoices/"+created.ID+":post", "", "")
	if rec.Code != http.StatusServiceUnavailable || s.invoices[created.ID].Status != "PROPOSAL" || s.posts != 0 {
		t.Fatalf("status %d; invoice must stay a proposal", rec.Code)
	}
}

func TestGetInvoice_ETagAndNotFound(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "0"})
	var created invoiceDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "10", "4900")), &created)

	rec := do(h, http.MethodGet, "/v1/billing/invoices/"+created.ID, "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get status %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	if miss := do(h, http.MethodGet, "/v1/billing/invoices/"+leID, "", ""); miss.Code != http.StatusNotFound {
		t.Errorf("unknown id: status %d, want 404", miss.Code)
	}
	if bad := do(h, http.MethodGet, "/v1/billing/invoices/not-a-uuid", "", ""); bad.Code != http.StatusNotFound {
		t.Errorf("malformed id: status %d, want 404", bad.Code)
	}
}

func TestParseMaxLines(t *testing.T) {
	if n, err := ParseMaxLines("", 2000); err != nil || n != 2000 {
		t.Errorf("default: %d %v", n, err)
	}
	for _, bad := range []string{"0", "10001", "many"} {
		if _, err := ParseMaxLines(bad, 2000); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRoutes_EnforceDuties(t *testing.T) {
	h := setup(t, newFakeStore(), &fakeTax{perLine: "0"})
	rec := doAs(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "1", "4900"), "billing.invoice.view", nil)
	var p httpx.Problem
	decode(t, rec, &p)
	if rec.Code != http.StatusForbidden || p.Code != "AUTH-0003" {
		t.Errorf("create without InvoiceMaintain: status %d code %s, want 403 AUTH-0003", rec.Code, p.Code)
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/invoices/"+leID+":post", "", "", "billing.invoice.maintain", nil); rec.Code != http.StatusForbidden {
		t.Errorf("post without InvoicePost: status %d, want 403", rec.Code)
	}
	if rec := doAs(h, http.MethodGet, "/v1/billing/invoices", "", "", "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("list without InvoiceView: status %d, want 403", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/invoices", nil)
	anon := httptest.NewRecorder()
	h.ServeHTTP(anon, req)
	if anon.Code != http.StatusUnauthorized {
		t.Errorf("no tenant: status %d, want 401", anon.Code)
	}
}

func TestCreateProposal_ScopedCallerCannotInvoiceAnotherEntity(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "0"})
	rec := doAs(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "1", "4900"), allDuties,
		map[string]string{tenant.HeaderLegalEntity: "0190f0a0-0000-7000-8000-0000000000ff"})
	if rec.Code != http.StatusForbidden || s.creates != 0 {
		t.Fatalf("status %d creates %d, want 403 and nothing stored", rec.Code, s.creates)
	}
	ok := doAs(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "1", "4900"), allDuties,
		map[string]string{tenant.HeaderLegalEntity: leID})
	if ok.Code != http.StatusCreated {
		t.Fatalf("own entity: status %d %s", ok.Code, ok.Body)
	}
}

func TestCreateProposal_RefusesCallerWithoutLegalEntityScope(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "0"})
	rec := doAs(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "1", "4900"), allDuties,
		map[string]string{tenant.HeaderLegalEntity: ""})
	if rec.Code != http.StatusForbidden || s.creates != 0 {
		t.Fatalf("status %d creates %d, want 403 and nothing stored", rec.Code, s.creates)
	}
}

func TestCreateProposal_FieldErrors(t *testing.T) {
	h := setup(t, newFakeStore(), &fakeTax{perLine: "0"})
	body := fmt.Sprintf(`{"legalEntityId":%q,"customerId":"x","invoiceAccountId":%q,"currency":"eur","invoiceDate":"10.03.2027","paymentTermDays":400,"chargeAmount":"-1",`+
		`"lines":[{"description":" ","qty":"0","unitPrice":"1e3","discountPct":"120","revenueAccount":"4000 rev","taxCode":"de 19"}]}`, leID, acctID)
	rec := do(h, http.MethodPost, "/v1/billing/free-text-invoices", "k-fields", body)
	var p httpx.Problem
	decode(t, rec, &p)
	if rec.Code != http.StatusBadRequest || p.Code != "COM-0001" {
		t.Fatalf("status %d code %s", rec.Code, p.Code)
	}
	for _, f := range []string{"customerId", "currency", "invoiceDate", "paymentTermDays", "chargeAmount", "lines[0].unitPrice", "lines[0].revenueAccount", "lines[0].taxCode"} {
		if p.Errors[f] == "" {
			t.Errorf("no field error for %s in %v", f, p.Errors)
		}
	}
	// Once the decimals parse, the domain rules report against the offending field.
	body = strings.Replace(proposal("0", "1", "4900"), `"Consulting"`, `""`, 1)
	decode(t, do(h, http.MethodPost, "/v1/billing/free-text-invoices", "k-domain", body), &p)
	if p.Errors["lines[0].description"] == "" {
		t.Errorf("empty description not reported: %v", p.Errors)
	}
}

func TestCreateProposal_UnknownTaxCodeIsRefused(t *testing.T) {
	s := newFakeStore()
	tax, err := ParseTaxRates("DE7=7")
	if err != nil {
		t.Fatal(err)
	}
	h := setup(t, s, tax)
	rec := do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100", "4900"))
	var p httpx.Problem
	decode(t, rec, &p)
	if rec.Code != http.StatusUnprocessableEntity || p.Code != CodeUnknownTaxCode || s.creates != 0 {
		t.Fatalf("status %d code %s creates %d, want 422 %s and no proposal", rec.Code, p.Code, s.creates, CodeUnknownTaxCode)
	}
}

// AC-29.9: a DMF-DE free-text line to 4900 of 100.00 EUR at 19% VAT gives 119.00 gross.
func TestRateTableTax_AC29_9(t *testing.T) {
	tax, err := ParseTaxRates("DE19=19, EXEMPT=0")
	if err != nil {
		t.Fatal(err)
	}
	h := setup(t, newFakeStore(), tax)
	var got invoiceDTO
	rec := do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100.00", "4900"))
	decode(t, rec, &got)
	if rec.Code != http.StatusCreated || got.Totals.Net != "100.00" || got.Totals.Tax != "19.00" || got.Totals.Gross != "119.00" {
		t.Fatalf("status %d totals %+v", rec.Code, got.Totals)
	}
	if got.Lines[0].TaxCode != "DE19" || got.Lines[0].Tax != "19.00" || got.Lines[0].RevenueAccount != "4900" {
		t.Errorf("line %+v", got.Lines[0])
	}
}

func TestParseTaxRates(t *testing.T) {
	if e, err := ParseTaxRates(" "); err != nil || e != (UnavailableTax{}) {
		t.Errorf("empty setting: %v %v, want UnavailableTax", e, err)
	}
	for _, bad := range []string{"DE19", "DE19=abc", "DE19=101", "DE19=-1", "de19=19", "DE19=19,DE19=7"} {
		if _, err := ParseTaxRates(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestListInvoices(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	var created invoiceDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100", "4900")), &created)

	rec := do(h, http.MethodGet, "/v1/billing/invoices?status=PROPOSAL&limit=5", "", "")
	var got struct {
		Items []map[string]any `json:"items"`
	}
	decode(t, rec, &got)
	if rec.Code != http.StatusOK || len(got.Items) != 1 || got.Items[0]["id"] != created.ID {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if _, hasLines := got.Items[0]["lines"]; hasLines {
		t.Error("list items carry lines; the list returns headers only")
	}
	if s.lastFilter.Status != "PROPOSAL" || s.lastFilter.Limit != 5 {
		t.Errorf("filter passed to store %+v", s.lastFilter)
	}
	empty := do(h, http.MethodGet, "/v1/billing/invoices?status=SETTLED", "", "")
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Errorf("empty list must be an empty array: %s", empty.Body)
	}
	bad := do(h, http.MethodGet, "/v1/billing/invoices?status=OPEN&limit=0&customerId=x&cursor=bm9wZQ", "", "")
	var p httpx.Problem
	decode(t, bad, &p)
	if bad.Code != http.StatusBadRequest || p.Errors["status"] == "" || p.Errors["limit"] == "" || p.Errors["customerId"] == "" || p.Errors["cursor"] == "" {
		t.Errorf("bad filter: status %d errors %v", bad.Code, p.Errors)
	}
}

// TestListInvoices_CursorPages walks three proposals one per page through
// nextCursor: newest first, each once, and no cursor on the last page.
func TestListInvoices_CursorPages(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	var want []string
	for i := 0; i < 3; i++ {
		var created invoiceDTO
		decode(t, do(h, http.MethodPost, "/v1/billing/free-text-invoices", fmt.Sprintf("page-key-%d", i), proposal("1", "100", "4900")), &created)
		want = append(want, created.ID)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(want)))
	var seen []string
	cursor := ""
	for i := 0; i < 3; i++ {
		path := "/v1/billing/invoices?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var page listDTO
		rec := do(h, http.MethodGet, path, "", "")
		decode(t, rec, &page)
		if rec.Code != http.StatusOK || len(page.Items) != 1 {
			t.Fatalf("page %d: status %d body %s", i, rec.Code, rec.Body)
		}
		seen = append(seen, page.Items[0].ID)
		cursor = page.NextCursor
		if (i < 2) != (cursor != "") {
			t.Fatalf("page %d nextCursor %q: want one on every page but the last", i, cursor)
		}
	}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("paged %v, want %v", seen, want)
	}
}

func TestPost_IfMatchPrecondition(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	var created invoiceDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100", "4900")), &created)
	path := "/v1/billing/invoices/" + created.ID + ":post"

	stale := doAs(h, http.MethodPost, path, "", "", allDuties, map[string]string{"If-Match": `"3"`})
	var p httpx.Problem
	decode(t, stale, &p)
	if stale.Code != http.StatusPreconditionFailed || p.Code != store.CodeVersion || s.posts != 0 {
		t.Fatalf("stale If-Match: status %d code %s posts %d", stale.Code, p.Code, s.posts)
	}
	if bad := doAs(h, http.MethodPost, path, "", "", allDuties, map[string]string{"If-Match": "abc"}); bad.Code != http.StatusBadRequest {
		t.Errorf("malformed If-Match: status %d, want 400", bad.Code)
	}
	ok := doAs(h, http.MethodPost, path, "", "", allDuties, map[string]string{"If-Match": `"1"`})
	if ok.Code != http.StatusOK || s.postedVersion != 1 || ok.Header().Get("ETag") != `"2"` {
		t.Fatalf("matching If-Match: status %d version passed %d etag %q", ok.Code, s.postedVersion, ok.Header().Get("ETag"))
	}
}

func TestMetricsAndReady(t *testing.T) {
	s := newFakeStore()
	tax := &fakeTax{perLine: "19.00"}
	a := New(s, tax, 2000)
	r := httpx.NewRouter()
	a.Register(r)
	req := func(method, path, body string) *httptest.ResponseRecorder {
		rq := httptest.NewRequest(method, path, strings.NewReader(body))
		rq.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
		rq.Header.Set(tenant.HeaderDuties, allDuties)
		rq.Header.Set(tenant.HeaderLegalEntity, leID)
		rq.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, rq)
		return rec
	}
	var created invoiceDTO
	decode(t, req(http.MethodPost, "/v1/billing/free-text-invoices", proposal("1", "100", "4900")), &created)
	tax.err = ErrTaxUnavailable
	req(http.MethodPost, "/v1/billing/invoices/"+created.ID+":post", "")
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"billing_proposals_created_total 1", "billing_invoices_posted_total 0", "billing_posts_refused_total 1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics lack %q:\n%s", want, rec.Body)
		}
	}
	if ready := req(http.MethodGet, "/readyz", ""); ready.Code != http.StatusOK {
		t.Errorf("readyz status %d", ready.Code)
	}
}
