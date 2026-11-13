package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

const (
	requesterA = "00000000-0000-0000-0000-0000000000a9"
	requesterB = "00000000-0000-0000-0000-0000000000b9"
)

type fakeRequisitions struct {
	calls []sources.RequisitionRequest
	keys  []string
	users []string
	err   error
}

func (f *fakeRequisitions) CreateRequisition(ctx context.Context, _ string, key string, body sources.RequisitionRequest) (sources.RequisitionRef, error) {
	if f.err != nil {
		return sources.RequisitionRef{}, f.err
	}
	p, _ := tenant.FromContext(ctx)
	f.calls, f.keys, f.users = append(f.calls, body), append(f.keys, key), append(f.users, p.UserID)
	return sources.RequisitionRef{ID: "00000000-0000-0000-0000-00000000f001", Status: "DRAFT"}, nil
}

func asUser(user string) map[string]string {
	return map[string]string{tenant.HeaderLegalEntity: srcLE, tenant.HeaderUser: user}
}

// supplierServer plays the supplier's setup endpoint: it records the setup request and answers with the sample StartPage response.
type supplierServer struct {
	*httptest.Server
	request string
}

func newSupplier(t *testing.T, status int) *supplierServer {
	t.Helper()
	s := &supplierServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.request = string(raw)
		if status != http.StatusOK {
			http.Error(w, "down", status)
			return
		}
		_, _ = io.WriteString(w, strings.Replace(sample(t, "punchout-setup-response.xml"), "{{START}}", "https://supplier.example/catalog?session=1", 1))
	}))
	t.Cleanup(s.Close)
	return s
}

func punchoutSetup(t *testing.T, supplierURL string) (*API, http.Handler, http.Handler, string, *fakeRequisitions) {
	t.Helper()
	a, h, _, partner, _ := autoSetup(t, newFake())
	a.AS2PublicURL, a.AllowInsecureAS2 = "https://as2.test", true
	reqs := &fakeRequisitions{}
	a.Requisitions = reqs
	cfg := `{"setupUrl":"` + supplierURL + `","buyerIdentity":"BUYER-PASSAGE","supplierIdentity":"SUPPLIER-1","currencies":["USD"],"uomMap":{"CA":"CS"},"tolerancePct":"5",
"referencePrices":{"SEAL-100":"25.00","OIL-5L":"43.50"}}`
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/punchout", cfg, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("supplier: %d %s", rec.Code, rec.Body.String())
	}
	return a, h, a.AS2Routes(), partner, reqs
}

func openSession(t *testing.T, h http.Handler, partner, format, user string) (store.PunchoutSession, *httptest.ResponseRecorder) {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/edi/v1/punchout/sessions", `{"partnerId":"`+partner+`","format":"`+format+`"}`, "edi.requisition.create", asUser(user))
	var s store.PunchoutSession
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	return s, rec
}

// returnURL pulls the session secret out of the setup request the supplier received, as the supplier's page would.
func returnURLOf(t *testing.T, setupRequest string) (path, cookie string) {
	t.Helper()
	start := strings.Index(setupRequest, "<BrowserFormPost><URL>")
	end := strings.Index(setupRequest, "</URL></BrowserFormPost>")
	if start < 0 || end < 0 {
		t.Fatalf("no return URL in the setup request: %s", setupRequest)
	}
	u, err := url.Parse(strings.ReplaceAll(setupRequest[start+len("<BrowserFormPost><URL>"):end], "&amp;", "&"))
	if err != nil {
		t.Fatal(err)
	}
	cs := strings.Index(setupRequest, "<BuyerCookie>")
	ce := strings.Index(setupRequest, "</BuyerCookie>")
	return u.RequestURI(), setupRequest[cs+len("<BuyerCookie>") : ce]
}

func TestPunchout_CXMLSessionReturnAndRequisition(t *testing.T) {
	sup := newSupplier(t, http.StatusOK)
	_, h, pub, partner, reqs := punchoutSetup(t, sup.URL)

	sess, rec := openSession(t, h, partner, "CXML", requesterA)
	if rec.Code != http.StatusCreated || sess.Status != "OPEN" || sess.StartURL != "https://supplier.example/catalog?session=1" || sess.RequesterID != requesterA {
		t.Fatalf("open: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "buyerCookie") {
		t.Fatalf("credentials leaked: %s", rec.Body.String())
	}
	for _, want := range []string{`operation="create"`, "<Identity>BUYER-PASSAGE</Identity>", "<Identity>SUPPLIER-1</Identity>", "https://as2.test/punchout/v1/return/" + sess.ID} {
		if !strings.Contains(sup.request, want) {
			t.Errorf("setup request lacks %q: %s", want, sup.request)
		}
	}
	path, cookie := returnURLOf(t, sup.request)

	// Another requester cannot see the session; the requester can.
	if rec := do(t, h, http.MethodGet, "/edi/v1/punchout/sessions/"+sess.ID, "", "edi.requisition.create", asUser(requesterB)); rec.Code != http.StatusNotFound {
		t.Fatalf("another requester: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/punchout/sessions/"+sess.ID, "", "edi.requisition.create", asUser(requesterA)); rec.Code != http.StatusOK {
		t.Fatalf("own session: %d", rec.Code)
	}

	// The supplier's page posts the cart to the return URL. A wrong secret, then a wrong BuyerCookie, are refused.
	post := func(p, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(body))
		req.Header.Set("Content-Type", "text/xml")
		out := httptest.NewRecorder()
		pub.ServeHTTP(out, req)
		return out
	}
	cart := strings.Replace(sample(t, "punchout-order-message.xml"), "{{COOKIE}}", cookie, 1)
	if got := post(strings.Split(path, "?")[0]+"?t=nope", cart); got.Code != http.StatusNotFound {
		t.Fatalf("wrong secret: %d", got.Code)
	}
	if got := post(path, strings.Replace(cart, cookie, "forged", 1)); got.Code != http.StatusBadRequest {
		t.Fatalf("wrong cookie: %d %s", got.Code, got.Body.String())
	}
	if got := post(path, cart); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "2 line(s)") {
		t.Fatalf("return: %d %s", got.Code, got.Body.String())
	}
	if got := post(path, cart); got.Code != http.StatusConflict {
		t.Fatalf("a cart is returned once: %d", got.Code)
	}

	// Two lines, both from the catalog (AC-35.10): the CA unit maps to CS, the price stays at the catalog price.
	rec = do(t, h, http.MethodGet, "/edi/v1/punchout/sessions/"+sess.ID, "", "edi.requisition.create", asUser(requesterA))
	var got store.PunchoutSession
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Status != "RETURNED" || !strings.Contains(string(got.Cart), `"catalog":true`) || !strings.Contains(string(got.Cart), `"unitCode":"CS"`) {
		t.Fatalf("returned session: %s", rec.Body.String())
	}

	// Only the requester turns the cart into a requisition, once.
	create := "/edi/v1/punchout/sessions/" + sess.ID + ":create-requisition"
	if rec := do(t, h, http.MethodPost, create, "", "edi.requisition.create", asUser(requesterB)); rec.Code != http.StatusNotFound {
		t.Fatalf("another requester: %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, create, "", "edi.requisition.create", asUser(requesterA))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "00000000-0000-0000-0000-00000000f001") {
		t.Fatalf("requisition: %d %s", rec.Code, rec.Body.String())
	}
	if len(reqs.calls) != 1 || reqs.keys[0] != "punchout:"+sess.ID || reqs.users[0] != requesterA {
		t.Fatalf("purchasing call: %+v %v %v", reqs.calls, reqs.keys, reqs.users)
	}
	body := reqs.calls[0]
	if body.LegalEntityID != srcLE || body.Currency != "USD" || len(body.Lines) != 2 || body.Lines[0].Description != "Pump seal kit" || body.Lines[0].Category != "31161500" ||
		body.Lines[0].Qty != "2" || body.Lines[0].EstimatedUnitPrice != "25" || body.Lines[1].Unit != "CS" || body.Lines[1].Category != "15121500" ||
		body.Lines[0].CatalogSource != "PUNCHOUT" || body.Lines[0].SupplierPartID == "" {
		t.Fatalf("requisition body: %+v", body)
	}
	if rec := do(t, h, http.MethodPost, create, "", "edi.requisition.create", asUser(requesterA)); rec.Code != http.StatusOK || len(reqs.calls) != 1 {
		t.Fatalf("repeat: %d, calls %d", rec.Code, len(reqs.calls))
	}
}

func TestPunchout_OCIHookURLPost(t *testing.T) {
	sup := newSupplier(t, http.StatusOK)
	_, h, pub, partner, _ := punchoutSetup(t, sup.URL)
	sess, rec := openSession(t, h, partner, "OCI", requesterA)
	if rec.Code != http.StatusCreated {
		t.Fatalf("open: %d %s", rec.Code, rec.Body.String())
	}
	u, _ := url.Parse(sess.StartURL)
	hook := u.Query().Get("HOOK_URL")
	if !strings.HasPrefix(hook, "https://as2.test/punchout/v1/return/"+sess.ID+"?t=") || sup.request != "" {
		t.Fatalf("OCI needs no setup request and a HOOK_URL: %s (request %q)", sess.StartURL, sup.request)
	}
	hu, _ := url.Parse(hook)
	form := url.Values{"NEW_ITEM-VENDORMAT[1]": {"SEAL-100"}, "NEW_ITEM-DESCRIPTION[1]": {"Pump seal kit"}, "NEW_ITEM-QUANTITY[1]": {"2"}, "NEW_ITEM-UNIT[1]": {"EA"},
		"NEW_ITEM-PRICE[1]": {"25.00"}, "NEW_ITEM-CURRENCY[1]": {"USD"}, "NEW_ITEM-VENDORMAT[2]": {"OIL-5L"}, "NEW_ITEM-DESCRIPTION[2]": {"Gear oil"},
		"NEW_ITEM-QUANTITY[2]": {"1"}, "NEW_ITEM-UNIT[2]": {"EA"}, "NEW_ITEM-PRICE[2]": {"43.50"}, "NEW_ITEM-CURRENCY[2]": {"USD"}}
	req := httptest.NewRequest(http.MethodPost, hu.RequestURI(), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	out := httptest.NewRecorder()
	pub.ServeHTTP(out, req)
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), "2 line(s)") {
		t.Fatalf("OCI return: %d %s", out.Code, out.Body.String())
	}
}

func TestPunchout_ExpiredAndRejectedCartsAndSetupFailure(t *testing.T) {
	sup := newSupplier(t, http.StatusOK)
	a, h, pub, partner, reqs := punchoutSetup(t, sup.URL)

	// A cart whose stated total differs from its lines is rejected (EDI-0134): the session is REJECTED and no requisition can come of it.
	sess, _ := openSession(t, h, partner, "CXML", requesterA)
	path, cookie := returnURLOf(t, sup.request)
	cart := strings.Replace(strings.Replace(sample(t, "punchout-order-message.xml"), "{{COOKIE}}", cookie, 1), `>93.50<`, `>99.99<`, 1)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(cart))
	out := httptest.NewRecorder()
	pub.ServeHTTP(out, req)
	if out.Code != http.StatusUnprocessableEntity || !strings.Contains(out.Body.String(), "EDI-0134") {
		t.Fatalf("rejected cart: %d %s", out.Code, out.Body.String())
	}
	rec := do(t, h, http.MethodPost, "/edi/v1/punchout/sessions/"+sess.ID+":create-requisition", "", "edi.requisition.create", asUser(requesterA))
	if rec.Code != http.StatusConflict || len(reqs.calls) != 0 {
		t.Fatalf("no requisition from a rejected cart: %d", rec.Code)
	}

	// An expired session takes no cart.
	sess2, _ := openSession(t, h, partner, "CXML", requesterA)
	path2, cookie2 := returnURLOf(t, sup.request)
	a.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	req = httptest.NewRequest(http.MethodPost, path2, strings.NewReader(strings.Replace(sample(t, "punchout-order-message.xml"), "{{COOKIE}}", cookie2, 1)))
	out = httptest.NewRecorder()
	pub.ServeHTTP(out, req)
	if out.Code != http.StatusGone {
		t.Fatalf("expired: %d %s", out.Code, out.Body.String())
	}
	a.now = time.Now
	if rec := do(t, h, http.MethodPost, "/edi/v1/punchout/sessions/"+sess2.ID+":cancel", "", "edi.requisition.create", asUser(requesterA)); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}

	// A supplier that is down: 502 EDI-0033 and no session stored.
	down := newSupplier(t, http.StatusServiceUnavailable)
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/punchout", `{"setupUrl":"`+down.URL+`","buyerIdentity":"B","supplierIdentity":"S","currencies":["USD"]}`, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	before := do(t, h, http.MethodGet, "/edi/v1/punchout/sessions", "", "edi.requisition.create", asUser(requesterA)).Body.String()
	if _, rec := openSession(t, h, partner, "CXML", requesterA); rec.Code != http.StatusBadGateway || problemCode(t, rec) != CodePunchoutSetup {
		t.Fatalf("supplier down: %d %s", rec.Code, rec.Body.String())
	}
	if after := do(t, h, http.MethodGet, "/edi/v1/punchout/sessions", "", "edi.requisition.create", asUser(requesterA)).Body.String(); after != before {
		t.Fatal("a failed setup must store no session")
	}
}

func TestPunchout_RefusedRequisitionKeepsTheCartForAnotherTry(t *testing.T) {
	sup := newSupplier(t, http.StatusOK)
	_, h, pub, partner, reqs := punchoutSetup(t, sup.URL)
	sess, _ := openSession(t, h, partner, "CXML", requesterA)
	path, cookie := returnURLOf(t, sup.request)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.Replace(sample(t, "punchout-order-message.xml"), "{{COOKIE}}", cookie, 1)))
	out := httptest.NewRecorder()
	pub.ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("return: %d %s", out.Code, out.Body.String())
	}
	create := "/edi/v1/punchout/sessions/" + sess.ID + ":create-requisition"
	// Purchasing refusing the lines is a 422 with its reason; the session stays RETURNED.
	reqs.err = &sources.RequisitionRefusedError{Status: 422, Code: "PRC-REQ-001", Detail: "unit CS is unknown"}
	rec := do(t, h, http.MethodPost, create, "", "edi.requisition.create", asUser(requesterA))
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeRequisitionRefuse || !strings.Contains(rec.Body.String(), "unit CS is unknown") {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body.String())
	}
	// An outage is a 502, not a refusal.
	reqs.err = errors.New("purchasing down")
	if rec := do(t, h, http.MethodPost, create, "", "edi.requisition.create", asUser(requesterA)); rec.Code != http.StatusBadGateway {
		t.Fatalf("outage: %d", rec.Code)
	}
	reqs.err = nil
	if rec := do(t, h, http.MethodPost, create, "", "edi.requisition.create", asUser(requesterA)); rec.Code != http.StatusCreated {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPunchout_SupplierConfigValidationAndPublicURL(t *testing.T) {
	a, h, _, partner, _ := autoSetup(t, newFake())
	bad := `{"setupUrl":"ftp://x","buyerIdentity":"","supplierIdentity":"S","currencies":[],"tolerancePct":"50"}`
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/punchout", bad, "edi.admin", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("validation: %d %s", rec.Code, rec.Body.String())
	}
	good := `{"setupUrl":"https://supplier.example/setup","buyerIdentity":"B","supplierIdentity":"S","currencies":["USD"]}`
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/punchout", good, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("good: %d %s", rec.Code, rec.Body.String())
	}
	// Without the public URL the cart could not come back.
	if _, rec := openSession(t, h, partner, "CXML", requesterA); rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != CodePunchoutNoURL {
		t.Fatalf("no public url: %d %s", rec.Code, rec.Body.String())
	}
	_ = a
}

// #35 (35-3): the requester sees the active punchout suppliers by name, not the partner list.
func TestPunchout_SuppliersListForRequesters(t *testing.T) {
	_, h, _, partner, _ := punchoutSetup(t, "https://supplier.example/setup")
	rec := do(t, h, http.MethodGet, "/edi/v1/punchout/suppliers", "", "edi.requisition.create", asUser(requesterA))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), partner) || !strings.Contains(rec.Body.String(), `"name"`) {
		t.Fatalf("suppliers: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/punchout/suppliers", "", "edi.operate", asUser(requesterA)); rec.Code != http.StatusForbidden {
		t.Fatalf("duty: %d", rec.Code)
	}
}
