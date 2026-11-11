package identity_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/identity"
)

const (
	dutyContacts = "identity.customer-contact.maintain"
	custOne      = "50000000-0000-4000-8000-000000000001"
	custTwo      = "50000000-0000-4000-8000-000000000002"
)

// fakeContacts records what the contact API asks Keycloak for.
type fakeContacts struct {
	created    []identity.ContactSpec
	realms     []string
	invites    []string
	mapped     []string
	disabled   []string
	createErr  error
	inviteErr  error
	mapErr     error
	disableErr error
	next       int
}

func (f *fakeContacts) CreateContact(_ context.Context, realm string, spec identity.ContactSpec) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	f.next++
	f.created = append(f.created, spec)
	f.realms = append(f.realms, realm)
	return "60000000-0000-4000-8000-00000000000" + string(rune('0'+f.next)), nil
}

func (f *fakeContacts) SendInvitation(_ context.Context, _, userID string, _ identity.Invitation) error {
	if f.inviteErr != nil {
		return f.inviteErr
	}
	f.invites = append(f.invites, userID)
	return nil
}

func (f *fakeContacts) MapContact(_ context.Context, _, _, userID, customerID string, les []string) error {
	if f.mapErr != nil {
		return f.mapErr
	}
	f.mapped = append(f.mapped, userID+"|"+customerID+"|"+strings.Join(les, ","))
	return nil
}

func (f *fakeContacts) DisableContact(_ context.Context, _, _, userID string) error {
	if f.disableErr != nil {
		return f.disableErr
	}
	f.disabled = append(f.disabled, userID)
	return nil
}

type contactRow struct {
	ContactID      string   `json:"contactId"`
	Email          string   `json:"email"`
	DisplayName    string   `json:"displayName"`
	CustomerID     string   `json:"customerId"`
	LegalEntityIDs []string `json:"legalEntityIds"`
	Status         string   `json:"status"`
	InvitationSent bool     `json:"invitationSent"`
}

func contactFixture(t *testing.T, prov identity.ContactProvisioner) *adminFixture {
	t.Helper()
	f := newAdminFixture(t)
	r := chi.NewRouter()
	identity.NewCustomerContactsAPI(f.app, prov, identity.Invitation{ClientID: "passage-portal-customer", RedirectURI: "https://customer.example/"}).Register(r)
	f.router = r
	return f
}

func contactAdmin(f *adminFixture, tenantID string, legalEntity string) caller {
	c := caller{tenantID: tenantID, user: "admin-1", legalEntity: legalEntity, duties: []string{dutyContacts}}
	if legalEntity == "" {
		c.duties = append(c.duties, dutyAllScope)
	}
	return c
}

func TestCustomerContactInviteCreatesTheKeycloakAccountAndTheRegistryRow(t *testing.T) {
	prov := &fakeContacts{}
	f := contactFixture(t, prov)
	admin := contactAdmin(f, f.tenantA, "")
	rec := f.do(admin, http.MethodPost, "/v1/identity/customer-contacts", map[string]any{"email": " Pat.Lee@Example.com ", "givenName": "Pat", "familyName": "Lee", "customerId": strings.ToUpper(custOne), "legalEntityIds": []string{legalA, legalB, legalA}})
	created := decode[contactRow](t, rec, http.StatusCreated)
	if created.Email != "pat.lee@example.com" || created.DisplayName != "Pat Lee" || created.CustomerID != custOne || created.Status != "enabled" || !created.InvitationSent ||
		!slices.Equal(created.LegalEntityIDs, []string{legalA, legalB}) || rec.Header().Get("Location") != "/v1/identity/customer-contacts/"+created.ContactID {
		t.Fatalf("created = %+v", created)
	}
	if len(prov.created) != 1 || prov.created[0].TenantID != f.tenantA || prov.created[0].CustomerID != custOne || prov.created[0].Email != "pat.lee@example.com" ||
		!slices.Equal(prov.created[0].LegalEntityIDs, []string{legalA, legalB}) || prov.realms[0] != "realm-"+f.tenantA || len(prov.invites) != 1 {
		t.Fatalf("keycloak was asked %+v realms %v invites %v", prov.created, prov.realms, prov.invites)
	}
	audit, eventTypes := f.auditAndEvents(t, f.tenantA)
	if !slices.Contains(audit, "customercontact.invited|admin-1") || !slices.Contains(eventTypes, "dev.passage.identity.customercontact.invited.v1") {
		t.Fatalf("audit %v events %v", audit, eventTypes)
	}
	var leaked int
	if err := asTenant(t, f.app, f.tenantA, "*", func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select (select count(*) from outbox where payload::text ilike '%pat.lee%') + (select count(*) from identity_audit where detail::text ilike '%pat.lee%')`).Scan(&leaked)
	}); err != nil || leaked != 0 {
		t.Fatalf("the contact's address is in the audit trail or an event: %d %v", leaked, err)
	}

	// The same address, in any case, is one contact.
	dup := f.do(admin, http.MethodPost, "/v1/identity/customer-contacts", map[string]any{"email": "PAT.LEE@example.com", "customerId": custTwo, "legalEntityIds": []string{legalA}})
	wantProblem(t, dup, http.StatusConflict, "IDN-CONTACT-EXISTS")

	list := decode[pageOf[contactRow]](t, f.do(admin, http.MethodGet, "/v1/identity/customer-contacts?customerId="+custOne, nil), http.StatusOK)
	if len(list.Items) != 1 || list.Items[0].ContactID != created.ContactID {
		t.Fatalf("list = %+v", list)
	}
	if other := decode[pageOf[contactRow]](t, f.do(admin, http.MethodGet, "/v1/identity/customer-contacts?customerId="+custTwo, nil), http.StatusOK); len(other.Items) != 0 {
		t.Fatalf("customer two lists %+v", other)
	}
	// Another tenant's administrator sees nothing and cannot touch the contact.
	foreign := contactAdmin(f, f.tenantB, "")
	if l := decode[pageOf[contactRow]](t, f.do(foreign, http.MethodGet, "/v1/identity/customer-contacts", nil), http.StatusOK); len(l.Items) != 0 {
		t.Fatalf("tenant B sees %+v", l)
	}
	wantProblem(t, f.do(foreign, http.MethodPatch, "/v1/identity/customer-contacts/"+created.ContactID, map[string]any{"customerId": custTwo, "legalEntityIds": []string{legalA}}), http.StatusNotFound, "IDN-CONTACT-NOT-FOUND")
	wantProblem(t, f.do(foreign, http.MethodPost, "/v1/identity/customer-contacts/"+created.ContactID+":disable", nil), http.StatusNotFound, "IDN-CONTACT-NOT-FOUND")
}

func TestCustomerContactInviteRefusals(t *testing.T) {
	prov := &fakeContacts{}
	f := contactFixture(t, prov)
	admin := contactAdmin(f, f.tenantA, "")
	good := map[string]any{"email": "a@example.com", "customerId": custOne, "legalEntityIds": []string{legalA}}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for key, val := range good {
			m[key] = val
		}
		m[k] = v
		return m
	}
	for name, body := range map[string]map[string]any{
		"bad email":            with("email", "not-an-address"),
		"display-name email":   with("email", "Pat <a@example.com>"),
		"bad customer":         with("customerId", "abc"),
		"no legal entity":      with("legalEntityIds", []string{}),
		"bad legal entity":     with("legalEntityIds", []string{"x"}),
		"unknown legal entity": with("legalEntityIds", []string{legalNone}),
	} {
		wantProblem(t, f.do(admin, http.MethodPost, "/v1/identity/customer-contacts", body), http.StatusBadRequest, "")
		if len(prov.created) != 0 {
			t.Fatalf("%s reached Keycloak", name)
		}
	}
	// A caller scoped to one legal entity cannot map a contact into another.
	scoped := contactAdmin(f, f.tenantA, legalA)
	wantProblem(t, f.do(scoped, http.MethodPost, "/v1/identity/customer-contacts", with("legalEntityIds", []string{legalA, legalB})), http.StatusForbidden, "IDN-CONTACT-SCOPE")
	if len(prov.created) != 0 {
		t.Fatal("an out-of-scope mapping reached Keycloak")
	}
	if rec := f.do(scoped, http.MethodPost, "/v1/identity/customer-contacts", good); rec.Code != http.StatusCreated {
		t.Fatalf("in scope: %d %s", rec.Code, rec.Body)
	}
	// Without the duty nothing is served.
	for _, rec := range []*httptest.ResponseRecorder{
		f.do(caller{tenantID: f.tenantA, user: "u", duties: []string{dutyUsers, dutyAllScope}}, http.MethodGet, "/v1/identity/customer-contacts", nil),
		f.do(caller{tenantID: f.tenantA, user: "u", duties: []string{dutyUsers, dutyAllScope}}, http.MethodPost, "/v1/identity/customer-contacts", good),
	} {
		wantProblem(t, rec, http.StatusForbidden, "AUTH-0003")
	}
	// A Keycloak failure creates no row and tells the administrator to retry.
	prov.createErr = errors.New("keycloak is down")
	wantProblem(t, f.do(admin, http.MethodPost, "/v1/identity/customer-contacts", with("email", "b@example.com")), http.StatusBadGateway, "IDN-CONTACT-KEYCLOAK")
	if l := decode[pageOf[contactRow]](t, f.do(admin, http.MethodGet, "/v1/identity/customer-contacts", nil), http.StatusOK); len(l.Items) != 1 {
		t.Fatalf("a failed invite left %+v", l.Items)
	}
}

func TestCustomerContactWithoutKeycloakCanBeListedButNotChanged(t *testing.T) {
	f := contactFixture(t, nil)
	admin := contactAdmin(f, f.tenantA, "")
	wantProblem(t, f.do(admin, http.MethodPost, "/v1/identity/customer-contacts", map[string]any{"email": "a@example.com", "customerId": custOne, "legalEntityIds": []string{legalA}}), http.StatusServiceUnavailable, "IDN-CONTACT-KEYCLOAK-OFF")
	if rec := f.do(admin, http.MethodGet, "/v1/identity/customer-contacts", nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
}

func TestCustomerContactInvitationEmailFailureIsRetriedWithResend(t *testing.T) {
	prov := &fakeContacts{inviteErr: errors.New("smtp is not configured")}
	f := contactFixture(t, prov)
	admin := contactAdmin(f, f.tenantA, "")
	created := decode[contactRow](t, f.do(admin, http.MethodPost, "/v1/identity/customer-contacts", map[string]any{"email": "a@example.com", "customerId": custOne, "legalEntityIds": []string{legalA}}), http.StatusCreated)
	if created.InvitationSent {
		t.Fatal("the invitation was reported sent")
	}
	wantProblem(t, f.do(admin, http.MethodPost, "/v1/identity/customer-contacts/"+created.ContactID+":resend", nil), http.StatusBadGateway, "IDN-CONTACT-KEYCLOAK")
	prov.inviteErr = nil
	if sent := decode[contactRow](t, f.do(admin, http.MethodPost, "/v1/identity/customer-contacts/"+created.ContactID+":resend", nil), http.StatusOK); !sent.InvitationSent || len(prov.invites) != 1 {
		t.Fatalf("resend = %+v invites %v", sent, prov.invites)
	}
	wantProblem(t, f.do(admin, http.MethodPost, "/v1/identity/customer-contacts/"+created.ContactID+":wipe", nil), http.StatusNotFound, "IDN-CONTACT-NOT-FOUND")
	wantProblem(t, f.do(admin, http.MethodPost, "/v1/identity/customer-contacts/not-an-id:resend", nil), http.StatusNotFound, "IDN-CONTACT-NOT-FOUND")
}

func TestCustomerContactRemapAndDisable(t *testing.T) {
	prov := &fakeContacts{}
	f := contactFixture(t, prov)
	admin := contactAdmin(f, f.tenantA, "")
	created := decode[contactRow](t, f.do(admin, http.MethodPost, "/v1/identity/customer-contacts", map[string]any{"email": "a@example.com", "customerId": custOne, "legalEntityIds": []string{legalA}}), http.StatusCreated)
	path := "/v1/identity/customer-contacts/" + created.ContactID

	moved := decode[contactRow](t, f.do(admin, http.MethodPatch, path, map[string]any{"customerId": custTwo, "legalEntityIds": []string{legalB}}), http.StatusOK)
	if moved.CustomerID != custTwo || !slices.Equal(moved.LegalEntityIDs, []string{legalB}) || len(prov.mapped) != 1 || !strings.Contains(prov.mapped[0], "|"+custTwo+"|"+legalB) {
		t.Fatalf("moved = %+v keycloak %v", moved, prov.mapped)
	}
	// Keycloak failing leaves the registry on the old mapping.
	prov.mapErr = errors.New("down")
	wantProblem(t, f.do(admin, http.MethodPatch, path, map[string]any{"customerId": custOne, "legalEntityIds": []string{legalA}}), http.StatusBadGateway, "IDN-CONTACT-KEYCLOAK")
	if l := decode[pageOf[contactRow]](t, f.do(admin, http.MethodGet, "/v1/identity/customer-contacts", nil), http.StatusOK); l.Items[0].CustomerID != custTwo {
		t.Fatalf("the registry moved without Keycloak: %+v", l.Items[0])
	}
	prov.mapErr = nil
	// A caller scoped to legal entity A cannot move a contact that sits in B out of it.
	wantProblem(t, f.do(contactAdmin(f, f.tenantA, legalA), http.MethodPatch, path, map[string]any{"customerId": custOne, "legalEntityIds": []string{legalA}}), http.StatusForbidden, "IDN-CONTACT-SCOPE")
	wantProblem(t, f.do(admin, http.MethodPatch, path, map[string]any{"customerId": "x", "legalEntityIds": []string{legalA}}), http.StatusBadRequest, "")

	off := decode[contactRow](t, f.do(admin, http.MethodPost, path+":disable", nil), http.StatusOK)
	if off.Status != "disabled" || len(prov.disabled) != 1 {
		t.Fatalf("disabled = %+v keycloak %v", off, prov.disabled)
	}
	// Idempotent, and a disabled contact can be neither moved nor invited again.
	if again := decode[contactRow](t, f.do(admin, http.MethodPost, path+":disable", nil), http.StatusOK); again.Status != "disabled" || len(prov.disabled) != 1 {
		t.Fatalf("second disable = %+v keycloak %v", again, prov.disabled)
	}
	wantProblem(t, f.do(admin, http.MethodPatch, path, map[string]any{"customerId": custOne, "legalEntityIds": []string{legalA}}), http.StatusConflict, "IDN-CONTACT-DISABLED")
	wantProblem(t, f.do(admin, http.MethodPost, path+":resend", nil), http.StatusConflict, "IDN-CONTACT-DISABLED")
	if l := decode[pageOf[contactRow]](t, f.do(admin, http.MethodGet, "/v1/identity/customer-contacts?status=disabled", nil), http.StatusOK); len(l.Items) != 1 {
		t.Fatalf("disabled list = %+v", l)
	}
	if l := decode[pageOf[contactRow]](t, f.do(admin, http.MethodGet, "/v1/identity/customer-contacts?status=enabled", nil), http.StatusOK); len(l.Items) != 0 {
		t.Fatalf("enabled list = %+v", l)
	}
}

func TestCustomerPortalLegalEntityNameIsTheTokensOwn(t *testing.T) {
	f := contactFixture(t, nil)
	portal := func(le string, duties ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/identity/customer-portal/legal-entity?legalEntityId="+legalB, nil)
		held := map[string]struct{}{}
		for _, d := range duties {
			held[d] = struct{}{}
		}
		req = req.WithContext(tenant.With(req.Context(), tenant.Principal{TenantID: f.tenantA, LegalEntityID: le, UserID: "contact", Service: tenant.CustomerPortalIssuer, CustomerID: custOne, Duties: held}))
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec
	}
	got := decode[struct {
		LegalEntityID string `json:"legalEntityId"`
		DisplayName   string `json:"displayName"`
	}](t, portal(legalA, "identity.customer-portal.read"), http.StatusOK)
	if got.LegalEntityID != legalA || got.DisplayName != "Legal entity" {
		t.Fatalf("answer = %+v, want the token's entity (a query parameter is ignored)", got)
	}
	wantProblem(t, portal(legalNone, "identity.customer-portal.read"), http.StatusNotFound, "IDN-LEGAL-ENTITY-NOT-FOUND")
	wantProblem(t, portal(legalA), http.StatusForbidden, "AUTH-0003")
	// A staff user holding the duty is not the customer portal acting for a customer.
	staff := f.do(caller{tenantID: f.tenantA, legalEntity: legalA, user: "u", duties: []string{"identity.customer-portal.read"}}, http.MethodGet, "/v1/identity/customer-portal/legal-entity", nil)
	wantProblem(t, staff, http.StatusForbidden, "AUTH-0003")
	// Tenant B has no such entity under tenant A's id.
	other := httptest.NewRequest(http.MethodGet, "/v1/identity/customer-portal/legal-entity", nil)
	other = other.WithContext(tenant.With(other.Context(), tenant.Principal{TenantID: f.tenantB, LegalEntityID: legalB, UserID: "contact", Service: tenant.CustomerPortalIssuer, CustomerID: custOne, Duties: map[string]struct{}{"identity.customer-portal.read": {}}}))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, other)
	wantProblem(t, rec, http.StatusNotFound, "IDN-LEGAL-ENTITY-NOT-FOUND")
}
