package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
	"github.com/BenjaminSRussell/passage/services/party/migrations"
)

// buyer calls the real routes as a buyer user of one legal entity with the named duties.
func buyer(h http.Handler, user, duties, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Legal-Entity", portalLE)
	req.Header.Set("X-Passage-User", user)
	req.Header.Set("X-Passage-Duties", duties)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAcceptProspect_CreatesThePartyABlockedVendorAndTheOnboardingFromTheRegistration(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)}), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pg := store.New(app)
	r := httpx.NewRouter()
	seq := &fakeSequencer{}
	api.New(&stubChecker{}, stubPinger{}, pg, seq).WithVendors(pg).WithSuppliers(pg).WithPortal(pg).Register(r)

	asBuyer := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "buyer", Duties: testkit.ScopeDuties(portalLE)})
	q, err := pg.CreateQuestionnaire(asBuyer, "Basics", []domain.Question{{ID: "iso", Label: "ISO 9001?", Type: domain.QuestionYesNo, Required: true}})
	if err != nil {
		t.Fatal(err)
	}
	registrar := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "portal-prospect", Service: tenant.VendorPortalIssuer, Duties: map[string]struct{}{tenant.DutyPortalRegister: {}}})
	if _, err := pg.RegisterProspect(registrar, domain.NewProspect{CompanyName: "Birch Valve Co", ContactName: "Sam", Email: "sam@birch.example", Country: "US", TaxID: "12-3456789"}); err != nil {
		t.Fatal(err)
	}
	list := buyer(r, "buyer", "party.read", http.MethodGet, "/v1/party/vendor-prospects?status=NEW", "")
	var page struct {
		Items []store.Prospect `json:"items"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || len(page.Items) != 1 {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	id := page.Items[0].ID
	body := `{"accountNumber":"V-100","vendorGroupId":"` + ids.New() + `","currencyCode":"USD","questionnaireId":"` + q.ID + `"}`

	// Reading is not accepting, and accepting is not rejecting.
	for name, tc := range map[string]struct{ duties, path string }{
		"read duty accepts":   {"party.read", "/v1/party/vendor-prospects/" + id + ":accept"},
		"decide duty accepts": {"party.vendor-request.decide", "/v1/party/vendor-prospects/" + id + ":accept"},
		"write duty rejects":  {"party.write", "/v1/party/vendor-prospects/" + id + ":reject"},
	} {
		if rec := buyer(r, "buyer", tc.duties, http.MethodPost, tc.path, body); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", name, rec.Code, rec.Body)
		}
	}
	if rec := buyer(r, "buyer", "party.write", http.MethodPost, "/v1/party/vendor-prospects/"+id+":accept", `{"accountNumber":"V-100"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an incomplete accept: %d %s", rec.Code, rec.Body)
	}

	rec := buyer(r, "buyer", "party.write", http.MethodPost, "/v1/party/vendor-prospects/"+id+":accept", body)
	var accepted store.Prospect
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &accepted) != nil || accepted.Status != domain.ProspectAccepted || accepted.PartyID == "" || accepted.VendorID == "" || accepted.RequestID == "" {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	if len(seq.calls) != 1 || seq.calls[0] != "PARTY/prospect-"+id {
		t.Errorf("numbering calls %v", seq.calls)
	}
	party, _, err := pg.GetParty(asBuyer, accepted.PartyID)
	if err != nil || party.Name != "Birch Valve Co" || *party.RegistrationNumber != "12-3456789" || party.PartyType != domain.TypeOrganization {
		t.Fatalf("party %+v %v", party, err)
	}
	vendor, err := pg.GetVendor(asBuyer, accepted.VendorID)
	if err != nil || vendor.Blocked != "all" || vendor.PartyID != party.ID || vendor.AccountNumber != "V-100" {
		t.Fatalf("vendor %+v %v", vendor, err)
	}
	onboarding, err := pg.GetRequest(asBuyer, "", accepted.RequestID)
	if err != nil || onboarding.Kind != domain.RequestOnboarding || onboarding.Status != domain.RequestInvited || onboarding.VendorID != vendor.ID {
		t.Fatalf("onboarding %+v %v", onboarding, err)
	}
	// A second accept, or a reject after the decision, finds it decided.
	if again := buyer(r, "buyer", "party.write", http.MethodPost, "/v1/party/vendor-prospects/"+id+":accept", body); again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), "PTY-PROSPECT-STATE") {
		t.Errorf("accept twice: %d %s", again.Code, again.Body)
	}
	if late := buyer(r, "ap", "party.vendor-request.decide", http.MethodPost, "/v1/party/vendor-prospects/"+id+":reject", `{"note":"too late"}`); late.Code != http.StatusConflict {
		t.Errorf("reject after accept: %d %s", late.Code, late.Body)
	}
}

func TestRejectProspect_NeedsANoteAndCreatesNothing(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)}), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pg := store.New(app)
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, pg, &fakeSequencer{}).WithVendors(pg).WithSuppliers(pg).WithPortal(pg).Register(r)
	registrar := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "portal-prospect", Service: tenant.VendorPortalIssuer, Duties: map[string]struct{}{tenant.DutyPortalRegister: {}}})
	if _, err := pg.RegisterProspect(registrar, domain.NewProspect{CompanyName: "Spam Ltd", ContactName: "S", Email: "s@spam.example", Country: "US"}); err != nil {
		t.Fatal(err)
	}
	asBuyer := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "buyer", Duties: testkit.ScopeDuties(portalLE)})
	list, _ := pg.ListProspects(asBuyer, "", 10)
	path := "/v1/party/vendor-prospects/" + list[0].ID + ":reject"
	if rec := buyer(r, "ap", "party.vendor-request.decide", http.MethodPost, path, `{"note":" "}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a reject without a note: %d %s", rec.Code, rec.Body)
	}
	if rec := buyer(r, "ap", "party.vendor-request.decide", http.MethodPost, path, `{"note":"not a supplier we use"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"REJECTED"`) {
		t.Errorf("reject: %d %s", rec.Code, rec.Body)
	}
}

func TestAcceptProspect_ARepeatAfterAStopFinishesWithTheSameOnboardingAndASecondBuyerIsRefused(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)}), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pg := store.New(app)
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, pg, &fakeSequencer{}).WithVendors(pg).WithSuppliers(pg).WithPortal(pg).Register(r)
	asBuyer := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "buyer", Duties: testkit.ScopeDuties(portalLE)})
	q, err := pg.CreateQuestionnaire(asBuyer, "Basics", []domain.Question{{ID: "iso", Label: "ISO 9001?", Type: domain.QuestionYesNo, Required: true}})
	if err != nil {
		t.Fatal(err)
	}
	registrar := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "portal-prospect", Service: tenant.VendorPortalIssuer, Duties: map[string]struct{}{tenant.DutyPortalRegister: {}}})
	if _, err := pg.RegisterProspect(registrar, domain.NewProspect{CompanyName: "Birch Valve Co", ContactName: "Sam", Email: "sam@birch.example", Country: "US"}); err != nil {
		t.Fatal(err)
	}
	list, _ := pg.ListProspects(asBuyer, "", 10)
	path := "/v1/party/vendor-prospects/" + list[0].ID + ":accept"
	body := `{"accountNumber":"V-100","vendorGroupId":"` + ids.New() + `","currencyCode":"USD","questionnaireId":"` + q.ID + `"}`

	// A wrong questionnaire stops the accept after the party and the vendor role exist: the registration stays claimed.
	if rec := buyer(r, "buyer", "party.write", http.MethodPost, path, strings.Replace(body, q.ID, ids.New(), 1)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("accept with a wrong questionnaire: %d %s", rec.Code, rec.Body)
	}
	if rec := buyer(r, "buyer2", "party.write", http.MethodPost, path, body); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "PTY-PROSPECT-STATE") {
		t.Errorf("a second buyer while the first is accepting: %d %s", rec.Code, rec.Body)
	}
	first := buyer(r, "buyer", "party.write", http.MethodPost, path, body)
	var accepted store.Prospect
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &accepted) != nil || accepted.RequestID == "" {
		t.Fatalf("the buyer's repeat: %d %s", first.Code, first.Body)
	}

	// Stopping after the onboarding was opened but before the registration was linked: the repeat links the same onboarding.
	if _, err := admin.Exec(context.Background(), `update vendor_prospect set party_id = null, vendor_id = null, request_id = null where id = $1`, accepted.ID); err != nil {
		t.Fatal(err)
	}
	again := buyer(r, "buyer", "party.write", http.MethodPost, path, body)
	var resumed store.Prospect
	if again.Code != http.StatusOK || json.Unmarshal(again.Body.Bytes(), &resumed) != nil {
		t.Fatalf("repeat after the onboarding opened: %d %s", again.Code, again.Body)
	}
	if resumed.RequestID != accepted.RequestID || resumed.VendorID != accepted.VendorID || resumed.PartyID != accepted.PartyID {
		t.Errorf("repeat linked %+v, want %+v", resumed, accepted)
	}
}

// Approving a PROFILE_CHANGE rewrites the vendor's tenant-wide party, so through the real route it needs the party edit duty
// (party.write, the duty of PATCH /v1/party/parties/{id}) besides the decide duty; without it the answer is 403
// PTY-SUP-PARTY-WRITE and nothing changes.
func TestApproveProfileChange_ThroughTheRouteNeedsPartyWrite(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)}), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pg := store.New(app)
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, pg, &fakeSequencer{}).WithVendors(pg).WithSuppliers(pg).WithPortal(pg).Register(r)

	asBuyer := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "buyer", Duties: testkit.ScopeDuties(portalLE)})
	q, err := pg.CreateQuestionnaire(asBuyer, "Basics", []domain.Question{{ID: "iso", Label: "ISO 9001?", Type: domain.QuestionYesNo}})
	if err != nil {
		t.Fatal(err)
	}
	registrar := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "portal-prospect", Service: tenant.VendorPortalIssuer, Duties: map[string]struct{}{tenant.DutyPortalRegister: {}}})
	if _, err := pg.RegisterProspect(registrar, domain.NewProspect{CompanyName: "Birch Valve Co", ContactName: "Sam", Email: "sam@birch.example", Country: "US"}); err != nil {
		t.Fatal(err)
	}
	prospects, err := pg.ListProspects(asBuyer, domain.ProspectNew, 10)
	if err != nil || len(prospects) != 1 {
		t.Fatalf("prospects %v %v", prospects, err)
	}
	body := `{"accountNumber":"V-200","vendorGroupId":"` + ids.New() + `","currencyCode":"USD","questionnaireId":"` + q.ID + `"}`
	var accepted store.Prospect
	if rec := buyer(r, "buyer", "party.write", http.MethodPost, "/v1/party/vendor-prospects/"+prospects[0].ID+":accept", body); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &accepted) != nil {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	req, err := pg.CreateRequest(asBuyer, accepted.VendorID, domain.RequestProfileChange, "Rename", "New legal name.", map[string]string{"legalName": "Birch Valve Company Ltd", "registrationNumber": "RC-77"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/party/vendor-requests/" + req.ID + ":approve"

	refused := buyer(r, "ap", "party.vendor-request.decide", http.MethodPost, path, `{"note":"ok"}`)
	if refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), store.CodeSupplierPartyWrite) {
		t.Fatalf("approve with the decide duty only: %d %s, want 403 %s", refused.Code, refused.Body, store.CodeSupplierPartyWrite)
	}
	if got, err := pg.GetRequest(asBuyer, "", req.ID); err != nil || got.Status != domain.RequestSubmitted || got.AppliedAt != nil {
		t.Errorf("request after a refused approval: %+v %v", got, err)
	}
	if p, _, err := pg.GetParty(asBuyer, accepted.PartyID); err != nil || p.Name != "Birch Valve Co" || p.Version != 1 {
		t.Errorf("party after a refused approval: %+v %v", p, err)
	}
	// party.write alone cannot decide a vendor request either.
	if rec := buyer(r, "ap", "party.write", http.MethodPost, path, `{"note":"ok"}`); rec.Code != http.StatusForbidden {
		t.Errorf("approve with party.write only: %d %s, want 403", rec.Code, rec.Body)
	}

	approved := buyer(r, "ap", "party.vendor-request.decide,party.write", http.MethodPost, path, `{"note":"ok"}`)
	if approved.Code != http.StatusOK || !strings.Contains(approved.Body.String(), domain.RequestApproved) {
		t.Fatalf("approve with both duties: %d %s", approved.Code, approved.Body)
	}
	p, _, err := pg.GetParty(asBuyer, accepted.PartyID)
	if err != nil || p.LegalName == nil || *p.LegalName != "Birch Valve Company Ltd" || p.RegistrationNumber == nil || *p.RegistrationNumber != "RC-77" || p.Version != 2 {
		t.Fatalf("party after approval: %+v %v", p, err)
	}
}
