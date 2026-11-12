package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	portalVendorA = "0b1c2d3e-0000-4000-8000-0000000000aa"
	portalVendorB = "0b1c2d3e-0000-4000-8000-0000000000bb"
	portalLE      = "00000000-0000-0000-0000-0000000000a1"
)

// fakeSuppliers records which vendor each call was made for.
type fakeSuppliers struct {
	store.SupplierStore
	vendors  []string
	uploaded domain.Document
	decided  bool
	note     string
}

func (f *fakeSuppliers) ListRequests(_ context.Context, vendorID, _ string, _ int) ([]store.VendorRequest, error) {
	f.vendors = append(f.vendors, vendorID)
	return []store.VendorRequest{}, nil
}

func (f *fakeSuppliers) AddDocument(_ context.Context, vendorID, _ string, d domain.Document) (store.DocumentMeta, error) {
	f.vendors, f.uploaded = append(f.vendors, vendorID), d
	return store.DocumentMeta{ID: knownID, SizeBytes: len(d.Content)}, nil
}

func (f *fakeSuppliers) DecideRequest(_ context.Context, id string, approve bool, note string) (store.VendorRequest, error) {
	f.decided, f.note = approve, note
	return store.VendorRequest{ID: id}, nil
}

func (f *fakeSuppliers) CreateRequest(_ context.Context, vendorID, kind, _, _ string, changes map[string]string) (store.VendorRequest, error) {
	f.vendors = append(f.vendors, vendorID)
	return store.VendorRequest{ID: knownID, Kind: kind, Status: domain.RequestSubmitted, Changes: changes}, nil
}

func (f *fakeSuppliers) CreateQuestionnaire(_ context.Context, name string, qs []domain.Question) (store.Questionnaire, error) {
	return store.Questionnaire{ID: knownID, Name: name, Version: 1, Questions: qs}, nil
}

type supplierHarness struct {
	h      http.Handler
	f      *fakeSuppliers
	signer *tenant.ServiceSigner
}

func newSupplierHarness(t *testing.T) *supplierHarness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner(tenant.VendorPortalIssuer, priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("party", map[string]ed25519.PublicKey{tenant.VendorPortalIssuer: pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	f := &fakeSuppliers{}
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithSuppliers(f).Register(r)
	return &supplierHarness{h: r, f: f, signer: signer}
}

func (s *supplierHarness) portal(t *testing.T, vendor, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := s.signer.Mint("party", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "vendor-user", VendorID: vendor, Duties: map[string]struct{}{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Service-Token", token)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func TestSupplierRoutes_TheVendorComesFromTheTokenNotTheRequest(t *testing.T) {
	s := newSupplierHarness(t)
	rec := s.portal(t, portalVendorA, http.MethodGet, "/v1/party/vendor-portal/requests?vendorId="+portalVendorB, "")
	if rec.Code != http.StatusOK || len(s.f.vendors) != 1 || s.f.vendors[0] != portalVendorA {
		t.Fatalf("list: %d %s, store saw %v, want only %s", rec.Code, rec.Body, s.f.vendors, portalVendorA)
	}
}

func TestSupplierRoutes_ABuyerHoldingAPortalDutyIsNotASupplier(t *testing.T) {
	s := newSupplierHarness(t)
	for _, tc := range []struct{ method, path, duty string }{
		{http.MethodGet, "/v1/party/vendor-portal/requests", "party.vendor-portal.read"},
		{http.MethodGet, "/v1/party/vendor-portal/documents", "party.vendor-portal.read"},
		{http.MethodPost, "/v1/party/vendor-portal/distributions/" + knownID + ":acknowledge", "party.vendor-portal.write"},
	} {
		rec := do(s.h, tc.method, tc.path, "", tc.duty, true)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "PTY-SUP-SCOPE") {
			t.Errorf("%s %s: %d %s, want 403 PTY-SUP-SCOPE", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
	if len(s.f.vendors) != 0 {
		t.Fatalf("a refused call reached the store: %v", s.f.vendors)
	}
}

func upload(title, name, mediaType string, content []byte) string {
	return `{"title":"` + title + `","fileName":"` + name + `","contentType":"` + mediaType + `","contentBase64":"` + base64.StdEncoding.EncodeToString(content) + `"}`
}

func TestSupplierRoutes_UploadsAreCheckedBeforeTheyAreStored(t *testing.T) {
	s := newSupplierHarness(t)
	path := "/v1/party/vendor-portal/documents"
	ok := s.portal(t, portalVendorA, http.MethodPost, path, upload("Certificate", `..\\..\\evil/cert.pdf`, "application/pdf", []byte("%PDF-1.4")))
	if ok.Code != http.StatusCreated || s.f.uploaded.FileName != "cert.pdf" || len(s.f.uploaded.SHA256) != 64 {
		t.Fatalf("valid upload: %d %s %+v", ok.Code, ok.Body, s.f.uploaded)
	}
	s.f.vendors = nil
	for name, body := range map[string]string{
		"executable":    upload("x", "a.exe", "application/x-msdownload", []byte("MZ")),
		"html":          upload("x", "a.html", "text/html", []byte("<script>")),
		"empty":         upload("x", "a.pdf", "application/pdf", nil),
		"too large":     upload("x", "a.pdf", "application/pdf", make([]byte, domain.MaxDocumentBytes+1)),
		"no title":      upload("", "a.pdf", "application/pdf", []byte("x")),
		"bad base64":    `{"title":"x","fileName":"a.pdf","contentType":"application/pdf","contentBase64":"!!!"}`,
		"unknown field": `{"title":"x","fileName":"a.pdf","contentType":"application/pdf","contentBase64":"eA==","vendorId":"` + portalVendorB + `"}`,
	} {
		rec := s.portal(t, portalVendorA, http.MethodPost, path, body)
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want a refusal", name, rec.Code, rec.Body)
		}
	}
	if len(s.f.vendors) != 0 {
		t.Fatalf("a refused upload reached the store: %v", s.f.vendors)
	}
}

func TestSupplierRoutes_BuyerDutiesAreSeparate(t *testing.T) {
	s := newSupplierHarness(t)
	decide := "/v1/party/vendor-requests/" + knownID
	for _, tc := range []struct {
		name, path, duty string
		want             int
	}{
		{"party.write cannot approve", decide + ":approve", "party.write", http.StatusForbidden},
		{"party.approve cannot approve a vendor request", decide + ":approve", "party.approve", http.StatusForbidden},
		{"the decide duty approves", decide + ":approve", "party.vendor-request.decide", http.StatusOK},
		{"rejection needs a note", decide + ":reject", "party.vendor-request.decide", http.StatusUnprocessableEntity},
	} {
		if rec := do(s.h, http.MethodPost, tc.path, "", tc.duty, true); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
	if rec := do(s.h, http.MethodPost, decide+":reject", `{"note":"no certificate"}`, "party.vendor-request.decide", true); rec.Code != http.StatusOK || s.f.note != "no certificate" || s.f.decided {
		t.Errorf("reject with a note: %d %s decided=%v note=%q", rec.Code, rec.Body, s.f.decided, s.f.note)
	}
	// A portal token cannot use buyer routes either: it holds only portal duties.
	if rec := s.portal(t, portalVendorA, http.MethodGet, "/v1/party/vendor-requests", ""); rec.Code != http.StatusForbidden {
		t.Errorf("portal token on a buyer route: %d %s, want 403", rec.Code, rec.Body)
	}
}

func TestSupplierRoutes_QuestionnairesAreValidated(t *testing.T) {
	s := newSupplierHarness(t)
	for name, body := range map[string]string{
		"no questions":      `{"name":"Q","questions":[]}`,
		"duplicate id":      `{"name":"Q","questions":[{"id":"a","label":"A","type":"TEXT"},{"id":"a","label":"B","type":"TEXT"}]}`,
		"choice no options": `{"name":"Q","questions":[{"id":"a","label":"A","type":"CHOICE"}]}`,
		"bad type":          `{"name":"Q","questions":[{"id":"a","label":"A","type":"FILE"}]}`,
		"no name":           `{"name":"","questions":[{"id":"a","label":"A","type":"TEXT"}]}`,
	} {
		if rec := do(s.h, http.MethodPost, "/v1/party/vendor-questionnaires", body, "party.write", true); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s, want 422", name, rec.Code, rec.Body)
		}
	}
	good := `{"name":"Q","questions":[{"id":"iso","label":"ISO?","type":"YESNO","required":true},{"id":"c","label":"Region","type":"CHOICE","options":["EU","US"]}]}`
	if rec := do(s.h, http.MethodPost, "/v1/party/vendor-questionnaires", good, "party.write", true); rec.Code != http.StatusCreated {
		t.Errorf("valid questionnaire: %d %s", rec.Code, rec.Body)
	}
}
