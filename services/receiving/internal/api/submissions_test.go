package api_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const receiptA = "55555555-5555-4555-8555-555555555555"

// fakeSubmissions records what the API handed the store, so the tests can say whose vendor and what lines reached it.
type fakeSubmissions struct {
	store.SubmissionStore
	created  []store.Submission
	listed   []store.SubmissionFilter
	decided  []string
	withdraw []string
}

func (f *fakeSubmissions) CreateSubmission(_ context.Context, in store.Submission) (store.Submission, error) {
	f.created = append(f.created, in)
	in.Status, in.CreatedAt = domain.SubmissionSubmitted, time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	return in, nil
}

func (f *fakeSubmissions) GetSubmission(_ context.Context, id string) (store.Submission, error) {
	for _, s := range f.created {
		if s.ID == id {
			s.Status = domain.SubmissionSubmitted
			return s, nil
		}
	}
	return store.Submission{}, store.ErrSubmissionNotFound
}

func (f *fakeSubmissions) ListSubmissions(_ context.Context, fl store.SubmissionFilter) ([]store.Submission, error) {
	f.listed = append(f.listed, fl)
	return nil, nil
}

func (f *fakeSubmissions) InvoiceableLines(_ context.Context, vendorID, _ string, _ int) ([]store.InvoiceableLine, error) {
	f.listed = append(f.listed, store.SubmissionFilter{VendorID: vendorID})
	return nil, nil
}

func (f *fakeSubmissions) WithdrawSubmission(_ context.Context, id, vendorID string) (store.Submission, error) {
	f.withdraw = append(f.withdraw, vendorID)
	return store.Submission{ID: id, VendorID: vendorID, Status: domain.SubmissionWithdrawn}, nil
}

func (f *fakeSubmissions) DecideSubmission(_ context.Context, id, by, status, note, invoiceID string) (store.Submission, error) {
	f.decided = append(f.decided, by+"|"+status+"|"+note+"|"+invoiceID)
	return store.Submission{ID: id, Status: status}, nil
}

type submissionRig struct {
	h      http.Handler
	f      *fakeSubmissions
	signer *tenant.ServiceSigner
	keys   int
}

func newSubmissionRig(t *testing.T) *submissionRig {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner(tenant.VendorPortalIssuer, priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("receiving", map[string]ed25519.PublicKey{tenant.VendorPortalIssuer: pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	f := &fakeSubmissions{}
	a := api.New(memstore.New(), api.Ports{Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{}}).WithSubmissions(f)
	r := httpx.NewRouter()
	a.Register(r)
	return &submissionRig{h: r, f: f, signer: signer}
}

func (s *submissionRig) vendor(t *testing.T, vendor, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := s.signer.Mint("receiving", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntityID, UserID: "vendor-user", VendorID: vendor, Duties: map[string]struct{}{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	s.keys++
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Service-Token", token)
	req.Header.Set("Idempotency-Key", "k-"+string(rune('a'+s.keys)))
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func submissionJSON(extra string) string {
	return `{"legalEntityId":"` + legalEntityID + `","invoiceNumber":"INV-1","invoiceDate":"2026-03-05","currency":"USD","note":"March","lines":[{"receiptId":"` + receiptA + `","receiptLineNo":1,"qty":"2","unitPrice":"2.5"}]` + extra + `}`
}

func TestInvoiceSubmission_TheVendorIsTheTokensAndTheAmountsAreComputed(t *testing.T) {
	s := newSubmissionRig(t)
	rec := s.vendor(t, vendorID, http.MethodPost, "/v1/receiving/vendor/invoice-submissions", submissionJSON(""))
	if rec.Code != http.StatusCreated || len(s.f.created) != 1 {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body)
	}
	got := s.f.created[0]
	if got.VendorID != vendorID || got.Total.String() != "5" || got.Lines[0].Amount.String() != "5" || got.CreatedBy != "vendor-user" {
		t.Errorf("the store was handed %+v", got)
	}
	if strings.Contains(rec.Body.String(), `"vendorId"`) {
		t.Errorf("a vendor's answer carries the vendor id: %s", rec.Body)
	}
	// The vendor in the body is not a field: it is refused, never used.
	if rec := s.vendor(t, vendorID, http.MethodPost, "/v1/receiving/vendor/invoice-submissions", submissionJSON(`,"vendorId":"`+otherVendorID+`"`)); rec.Code != http.StatusBadRequest {
		t.Errorf("a vendorId in the body: %d %s", rec.Code, rec.Body)
	}
	// A listing never takes the vendor from the query.
	s.vendor(t, vendorID, http.MethodGet, "/v1/receiving/vendor/invoice-submissions?vendorId="+otherVendorID, "")
	if len(s.f.listed) != 1 || s.f.listed[0].VendorID != vendorID {
		t.Errorf("the list was made for %+v", s.f.listed)
	}
	// Another vendor reads it as missing, and cannot withdraw it into its own name.
	if rec := s.vendor(t, otherVendorID, http.MethodGet, "/v1/receiving/vendor/invoice-submissions/"+got.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("another vendor reads it: %d %s", rec.Code, rec.Body)
	}
}

func TestInvoiceSubmission_BadRequestsNeverReachTheStore(t *testing.T) {
	s := newSubmissionRig(t)
	path := "/v1/receiving/vendor/invoice-submissions"
	one := `{"receiptId":"` + receiptA + `","receiptLineNo":1,"qty":"1","unitPrice":"1"}`
	for name, body := range map[string]string{
		"no lines":            `{"legalEntityId":"` + legalEntityID + `","invoiceNumber":"I","invoiceDate":"2026-03-05","currency":"USD","lines":[]}`,
		"no number":           `{"legalEntityId":"` + legalEntityID + `","invoiceNumber":" ","invoiceDate":"2026-03-05","currency":"USD","lines":[` + one + `]}`,
		"bad date":            `{"legalEntityId":"` + legalEntityID + `","invoiceNumber":"I","invoiceDate":"5 March","currency":"USD","lines":[` + one + `]}`,
		"lower-case money":    `{"legalEntityId":"` + legalEntityID + `","invoiceNumber":"I","invoiceDate":"2026-03-05","currency":"usd","lines":[` + one + `]}`,
		"zero quantity":       `{"legalEntityId":"` + legalEntityID + `","invoiceNumber":"I","invoiceDate":"2026-03-05","currency":"USD","lines":[{"receiptId":"` + receiptA + `","receiptLineNo":1,"qty":"0","unitPrice":"1"}]}`,
		"negative price":      `{"legalEntityId":"` + legalEntityID + `","invoiceNumber":"I","invoiceDate":"2026-03-05","currency":"USD","lines":[{"receiptId":"` + receiptA + `","receiptLineNo":1,"qty":"1","unitPrice":"-1"}]}`,
		"the same line twice": `{"legalEntityId":"` + legalEntityID + `","invoiceNumber":"I","invoiceDate":"2026-03-05","currency":"USD","lines":[` + one + `,` + one + `]}`,
		"no receipt id":       `{"legalEntityId":"` + legalEntityID + `","invoiceNumber":"I","invoiceDate":"2026-03-05","currency":"USD","lines":[{"receiptLineNo":1,"qty":"1","unitPrice":"1"}]}`,
		"no legal entity":     `{"invoiceNumber":"I","invoiceDate":"2026-03-05","currency":"USD","lines":[` + one + `]}`,
	} {
		if rec := s.vendor(t, vendorID, http.MethodPost, path, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	// A legal entity the token is not in.
	other := strings.Replace(submissionJSON(""), legalEntityID, "00000000-0000-0000-0000-0000000000ee", 1)
	if rec := s.vendor(t, vendorID, http.MethodPost, path, other); rec.Code != http.StatusForbidden {
		t.Errorf("another legal entity: %d %s, want 403", rec.Code, rec.Body)
	}
	if len(s.f.created) != 0 {
		t.Fatalf("%d refused submissions reached the store", len(s.f.created))
	}
}

func TestInvoiceSubmission_BuyerRoutesNeedTheBuyersDutiesAndAPNeedsAReason(t *testing.T) {
	s := newSubmissionRig(t)
	id := "77777777-7777-4777-8777-777777777777"
	// A portal token holds only vendor-portal duties: it opens no buyer route.
	if rec := s.vendor(t, vendorID, http.MethodGet, "/v1/receiving/invoice-submissions", ""); rec.Code != http.StatusForbidden {
		t.Errorf("a portal token on the buyer list: %d %s", rec.Code, rec.Body)
	}
	buyer := func(method, path, body, duties string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Legal-Entity", legalEntityID)
		req.Header.Set("X-Passage-User", "ap-clerk")
		req.Header.Set("X-Passage-Duties", duties)
		req.Header.Set("Idempotency-Key", "kk-"+method+path)
		rec := httptest.NewRecorder()
		s.h.ServeHTTP(rec, req)
		return rec
	}
	if rec := buyer(http.MethodPost, "/v1/receiving/invoice-submissions/"+id+":reject", `{"reason":"x"}`, "receiving.ap.inquire"); rec.Code != http.StatusForbidden {
		t.Errorf("reject with the inquiry duty: %d %s", rec.Code, rec.Body)
	}
	if rec := buyer(http.MethodPost, "/v1/receiving/invoice-submissions/"+id+":reject", `{"reason":" "}`, "receiving.vendor-invoice.post"); rec.Code != http.StatusBadRequest {
		t.Errorf("reject without a reason: %d %s", rec.Code, rec.Body)
	}
	if rec := buyer(http.MethodPost, "/v1/receiving/invoice-submissions/"+id+":resolve", `{"invoiceId":"nope"}`, "receiving.vendor-invoice.post"); rec.Code != http.StatusBadRequest {
		t.Errorf("resolve without an invoice: %d %s", rec.Code, rec.Body)
	}
	if rec := buyer(http.MethodPost, "/v1/receiving/invoice-submissions/"+id+":reject", `{"reason":"price differs"}`, "receiving.vendor-invoice.post"); rec.Code != http.StatusOK || len(s.f.decided) != 1 || s.f.decided[0] != "ap-clerk|REJECTED|price differs|" {
		t.Errorf("reject: %d %s decided=%v", rec.Code, rec.Body, s.f.decided)
	}
	if rec := buyer(http.MethodGet, "/v1/receiving/invoice-submissions?status=BOGUS", "", "receiving.ap.inquire"); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad status filter: %d %s", rec.Code, rec.Body)
	}
}
