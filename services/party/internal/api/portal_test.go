package api_test

import (
	"context"
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

type fakePortal struct {
	store.PortalStore
	prospects []domain.NewProspect
	vendors   []string
	tokens    []string
}

func (f *fakePortal) RegisterProspect(_ context.Context, in domain.NewProspect) (bool, error) {
	f.prospects = append(f.prospects, in)
	return true, nil
}

func (f *fakePortal) CreateInvite(_ context.Context, vendorID, email, _ string) (store.Invite, string, error) {
	f.vendors = append(f.vendors, vendorID)
	return store.Invite{ID: knownID, VendorID: vendorID, Email: email, Role: domain.InviteRole, Status: domain.InvitePending}, "inv1.secret", nil
}

func (f *fakePortal) VerifyInvite(_ context.Context, vendorID, token string) (store.InviteDetails, error) {
	f.vendors, f.tokens = append(f.vendors, vendorID), append(f.tokens, token)
	return store.InviteDetails{Invite: store.Invite{ID: knownID, VendorID: vendorID, Email: "c@acme.example"}}, nil
}

func (f *fakePortal) AcceptInvite(ctx context.Context, vendorID, token string) (store.InviteDetails, error) {
	return f.VerifyInvite(ctx, vendorID, token)
}

type portalHarness struct {
	h      http.Handler
	f      *fakePortal
	signer *tenant.ServiceSigner
}

func newPortalHarness(t *testing.T) *portalHarness {
	t.Helper()
	s := newSupplierHarness(t)
	f := &fakePortal{}
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithSuppliers(s.f).WithPortal(f).Register(r)
	return &portalHarness{h: r, f: f, signer: s.signer}
}

func (p *portalHarness) call(t *testing.T, vendor string, only []string, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := p.signer.MintDuties("party", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "portal-prospect", VendorID: vendor, Duties: map[string]struct{}{}}, "", only)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Service-Token", token)
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec
}

const prospectBody = `{"companyName":" Birch Valve Co ","contactName":"Sam Birch","email":"Sam@Birch.example","country":"us","taxId":"12-3456789"}`

func TestProspectRegistration_OnlyTheVendorlessPortalTokenMayRegister(t *testing.T) {
	p := newPortalHarness(t)
	path := "/v1/party/vendor-portal/prospects"
	rec := p.call(t, "", []string{tenant.DutyPortalRegister}, http.MethodPost, path, prospectBody)
	if rec.Code != http.StatusAccepted || len(p.f.prospects) != 1 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	if got := p.f.prospects[0]; got.CompanyName != "Birch Valve Co" || got.Email != "sam@birch.example" || got.Country != "US" {
		t.Errorf("the registration was not cleaned: %+v", got)
	}
	// A signed-in vendor user's token, even holding the register duty, is not the registration caller.
	if rec := p.call(t, portalVendorA, []string{tenant.DutyPortalRegister}, http.MethodPost, path, prospectBody); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "PTY-SUP-SCOPE") {
		t.Errorf("vendor token: %d %s", rec.Code, rec.Body)
	}
	// A buyer holding every party duty cannot register on a prospect's behalf.
	if rec := do(p.h, http.MethodPost, path, prospectBody, "party.write,party.read,party.vendor-portal.register", true); rec.Code != http.StatusForbidden {
		t.Errorf("buyer: %d %s", rec.Code, rec.Body)
	}
	if len(p.f.prospects) != 1 {
		t.Fatalf("a refused call reached the store: %d registrations", len(p.f.prospects))
	}
	// The registration token opens nothing else.
	for _, path := range []string{"/v1/party/vendor-portal/requests", "/v1/party/vendor-portal/invites"} {
		if rec := p.call(t, "", []string{tenant.DutyPortalRegister}, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s with a registration token: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestProspectRegistration_IsValidatedAndBounded(t *testing.T) {
	p := newPortalHarness(t)
	path := "/v1/party/vendor-portal/prospects"
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"no company":     {`{"companyName":"","contactName":"S","email":"s@b.example","country":"US"}`, http.StatusUnprocessableEntity},
		"bad email":      {`{"companyName":"B","contactName":"S","email":"not-an-email","country":"US"}`, http.StatusUnprocessableEntity},
		"display name":   {`{"companyName":"B","contactName":"S","email":"Sam <s@b.example>","country":"US"}`, http.StatusUnprocessableEntity},
		"bad country":    {`{"companyName":"B","contactName":"S","email":"s@b.example","country":"USA"}`, http.StatusUnprocessableEntity},
		"long message":   {`{"companyName":"B","contactName":"S","email":"s@b.example","country":"US","message":"` + strings.Repeat("x", 2001) + `"}`, http.StatusUnprocessableEntity},
		"unknown field":  {`{"companyName":"B","contactName":"S","email":"s@b.example","country":"US","vendorId":"` + portalVendorB + `"}`, http.StatusBadRequest},
		"oversized body": {`{"companyName":"B","contactName":"S","email":"s@b.example","country":"US","message":"` + strings.Repeat("x", 20<<10) + `"}`, http.StatusRequestEntityTooLarge},
		"not json":       {`company=B`, http.StatusBadRequest},
	} {
		if rec := p.call(t, "", []string{tenant.DutyPortalRegister}, http.MethodPost, path, tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body, tc.want)
		}
	}
	if len(p.f.prospects) != 0 {
		t.Fatalf("%d refused registrations reached the store", len(p.f.prospects))
	}
}

func TestInviteRoutes_TheVendorIsTheTokensAndTheTokenMustNameTheInvitation(t *testing.T) {
	p := newPortalHarness(t)
	rec := p.call(t, portalVendorA, []string{"party.vendor-portal.write"}, http.MethodPost, "/v1/party/vendor-portal/invites", `{"email":"New@Acme.example","displayName":"Casey"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"token":"inv1.secret"`) || p.f.vendors[0] != portalVendorA {
		t.Fatalf("create: %d %s vendors=%v", rec.Code, rec.Body, p.f.vendors)
	}
	if rec := p.call(t, portalVendorA, []string{"party.vendor-portal.write"}, http.MethodPost, "/v1/party/vendor-portal/invites", `{"email":"nope"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad email: %d %s", rec.Code, rec.Body)
	}
	// The default portal token never carries the accept duty; the accept routes need a token minted for them.
	path := "/v1/party/vendor-portal/invites/" + knownID
	good := `{"token":"inv1.` + testkit.TenantID(1) + `.` + portalLE + `.` + portalVendorA + `.` + knownID + `.` + strings.Repeat("A", 43) + `"}`
	user := []string{"party.vendor-portal.read", "party.vendor-portal.write"}
	if rec := p.call(t, portalVendorA, user, http.MethodPost, path+":accept", good); rec.Code != http.StatusForbidden {
		t.Errorf("a normal user token accepting: %d %s", rec.Code, rec.Body)
	}
	only := []string{"party.vendor-portal.invite-accept"}
	if rec := p.call(t, portalVendorA, only, http.MethodPost, path+":verify", good); rec.Code != http.StatusOK || len(p.f.tokens) != 1 {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body)
	}
	wrongInvite := strings.Replace(good, knownID, "0b1c2d3e-0000-4000-8000-0000000000cc", 1)
	for name, body := range map[string]string{"another invitation's token": wrongInvite, "garbled": `{"token":"x"}`} {
		if rec := p.call(t, portalVendorA, only, http.MethodPost, path+":accept", body); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "PTY-INVITE-INVALID") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(p.f.tokens) != 1 {
		t.Errorf("a refused token reached the store: %v", p.f.tokens)
	}
}

func TestProfileChangeRequests_AreValidatedWhereTheyAreMade(t *testing.T) {
	s := newSupplierHarness(t)
	path := "/v1/party/vendor-portal/requests"
	for name, body := range map[string]string{
		"profile change without changes": `{"kind":"PROFILE_CHANGE","subject":"S","details":"d"}`,
		"a field a vendor cannot change": `{"kind":"PROFILE_CHANGE","subject":"S","details":"d","changes":{"status":"inactive"}}`,
		"empty legal name":               `{"kind":"PROFILE_CHANGE","subject":"S","details":"d","changes":{"legalName":" "}}`,
		"other with changes":             `{"kind":"OTHER","subject":"S","details":"d","changes":{"legalName":"X"}}`,
	} {
		if rec := s.portal(t, portalVendorA, http.MethodPost, path, body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s, want 422", name, rec.Code, rec.Body)
		}
	}
	if rec := s.portal(t, portalVendorA, http.MethodPost, path, `{"kind":"PROFILE_CHANGE","subject":"New name","details":"We rebranded","changes":{"legalName":"Acme Industrial","tradeName":"Acme"}}`); rec.Code != http.StatusCreated {
		t.Errorf("valid change: %d %s", rec.Code, rec.Body)
	}
}
