package identity

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	dutyMaintainCustomerContacts = "identity.customer-contact.maintain"
	dutyCustomerPortalRead       = "identity.customer-portal.read"

	codeContactNotFound   = "IDN-CONTACT-NOT-FOUND"
	codeContactExists     = "IDN-CONTACT-EXISTS"
	codeContactKeycloak   = "IDN-CONTACT-KEYCLOAK"
	codeContactNoKeycloak = "IDN-CONTACT-KEYCLOAK-OFF"
	codeContactDisabled   = "IDN-CONTACT-DISABLED"
	codeContactScope      = "IDN-CONTACT-SCOPE"

	maxContactLegalEntities = 20
	maxContactName          = 100
)

// CustomerContactsAPI lets an administrator invite customer contacts to the customer portal and map each to a customer and its
// legal entities (#30). The account lives in the tenant's Keycloak realm; the portal BFF reads the mapping from the sign-in claims
// tenant_id, legal_entity_id and customer_id. Every change is written to Keycloak and to identity_customer_contacts, audited, and
// ends the contact's sessions so the next sign-in carries the new claims. Identity does not read customers (party owns them): the
// customer id is taken as given, so the screen that calls this picks it from the customer lookup.
type CustomerContactsAPI struct {
	pool       *pgxpool.Pool
	keycloak   ContactProvisioner
	invitation Invitation
	now        func() time.Time
}

// NewCustomerContactsAPI builds the API. keycloak may be nil (Keycloak administration is not configured): the directory can then
// be read but nothing is created or changed, answering 503 IDN-CONTACT-KEYCLOAK-OFF.
func NewCustomerContactsAPI(pool *pgxpool.Pool, keycloak ContactProvisioner, invitation Invitation) *CustomerContactsAPI {
	return &CustomerContactsAPI{pool: pool, keycloak: keycloak, invitation: invitation, now: time.Now}
}

func (a *CustomerContactsAPI) Register(r chi.Router) {
	guard := requireAnyDuty(dutyMaintainCustomerContacts)
	r.With(guard).Get("/v1/identity/customer-contacts", a.list)
	r.With(guard).Post("/v1/identity/customer-contacts", a.invite)
	r.With(guard).Patch("/v1/identity/customer-contacts/{id}", a.remap)
	// chi cannot match a literal ':' after a path parameter; action splits {ref} into id and verb.
	r.With(guard).Post("/v1/identity/customer-contacts/{ref}", a.action)
	r.With(requireAnyDuty(dutyCustomerPortalRead)).Get("/v1/identity/customer-portal/legal-entity", a.legalEntity)
}

type customerContact struct {
	ContactID            string     `json:"contactId"`
	Email                string     `json:"email"`
	DisplayName          string     `json:"displayName"`
	CustomerID           string     `json:"customerId"`
	LegalEntityIDs       []string   `json:"legalEntityIds"`
	Status               string     `json:"status"`
	InvitedBy            string     `json:"invitedBy"`
	InvitedAt            time.Time  `json:"invitedAt"`
	LastInvitationSentAt *time.Time `json:"lastInvitationSentAt,omitempty"`
	UpdatedBy            string     `json:"updatedBy"`
	UpdatedAt            time.Time  `json:"updatedAt"`
	// InvitationSent is false when the contact was created but Keycloak could not send the email; resend it with :resend.
	InvitationSent bool `json:"invitationSent"`
}

const contactColumns = `contact_id::text, email, display_name, customer_id::text, legal_entity_ids::text[], status, invited_by, invited_at,
  last_invitation_sent_at, updated_by, updated_at`

func scanContact(row pgx.CollectableRow) (customerContact, error) {
	var c customerContact
	err := row.Scan(&c.ContactID, &c.Email, &c.DisplayName, &c.CustomerID, &c.LegalEntityIDs, &c.Status, &c.InvitedBy, &c.InvitedAt, &c.LastInvitationSentAt, &c.UpdatedBy, &c.UpdatedAt)
	c.InvitationSent = c.LastInvitationSentAt != nil
	return c, err
}

func (a *CustomerContactsAPI) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	limit, after := pageParams(r, fields)
	customerID, status := q.Get("customerId"), q.Get("status")
	if customerID != "" && !validUUID(customerID) {
		fields["customerId"] = "must be a UUID"
	}
	if status != "" && status != "enabled" && status != "disabled" {
		fields["status"] = "must be enabled or disabled"
	}
	if len(fields) > 0 {
		invalidFields(w, r, "invalid query", fields)
		return
	}
	var items []customerContact
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+contactColumns+` from identity_customer_contacts
where tenant_id = $1 and ($2::text = '' or customer_id = $2::uuid) and ($3::text = '' or status = $3) and ($4::text is null or contact_id > $4::uuid)
order by contact_id limit $5`, p.TenantID, strings.ToLower(customerID), status, after, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, scanContact)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, paged(items, limit, func(c customerContact) string { return c.ContactID }))
}

type inviteRequest struct {
	Email          string   `json:"email"`
	GivenName      string   `json:"givenName"`
	FamilyName     string   `json:"familyName"`
	CustomerID     string   `json:"customerId"`
	LegalEntityIDs []string `json:"legalEntityIds"`
}

// mappingFields validates a customer and legal-entity set, and returns the legal entities lower-cased and without repeats.
func mappingFields(fields map[string]string, customerID string, legalEntityIDs []string) []string {
	if !validUUID(customerID) {
		fields["customerId"] = "must be a UUID"
	}
	var out []string
	seen := map[string]bool{}
	for _, le := range legalEntityIDs {
		id, ok := canonicalUUID(le)
		if !ok {
			fields["legalEntityIds"] = "must be a list of legal entity UUIDs"
			return nil
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 || len(out) > maxContactLegalEntities {
		fields["legalEntityIds"] = "must name 1 to 20 legal entities"
	}
	return out
}

// checkMapping refuses legal entities that do not exist in the tenant or that are outside the caller's own scope.
func (a *CustomerContactsAPI) checkMapping(w http.ResponseWriter, r *http.Request, p tenant.Principal, legalEntityIDs []string) bool {
	for _, le := range legalEntityIDs {
		if !tenant.InLegalEntityScope(p, le) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeContactScope, "a legal entity of the mapping is outside the caller's scope"))
			return false
		}
	}
	var known int
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `select count(*) from identity_legal_entities where tenant_id = $1 and legal_entity_id = any($2::uuid[]) and status = 'active'`,
			p.TenantID, legalEntityIDs).Scan(&known)
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return false
	}
	if known != len(legalEntityIDs) {
		invalidFields(w, r, "unknown legal entity", map[string]string{"legalEntityIds": "every legal entity must exist and be active in this tenant"})
		return false
	}
	return true
}

func (a *CustomerContactsAPI) realm(ctx context.Context, tenantID string) (string, error) {
	var realm string
	err := dbx.WithTenantTx(ctx, a.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select realm_name from identity_tenants where tenant_id = $1`, tenantID).Scan(&realm)
	})
	return realm, err
}

func (a *CustomerContactsAPI) keycloakFailed(w http.ResponseWriter, r *http.Request, op string, err error) {
	slog.ErrorContext(r.Context(), "customer contact "+op+" failed in keycloak", "err", err)
	httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, codeContactKeycloak, "Keycloak could not "+op+"; nothing was changed in the portal, retry"))
}

func (a *CustomerContactsAPI) requireKeycloak(w http.ResponseWriter, r *http.Request) bool {
	if a.keycloak == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeContactNoKeycloak, "Keycloak administration is not configured for identity"))
		return false
	}
	return true
}

func (a *CustomerContactsAPI) invite(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	var req inviteRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > maxContactEmailBytes {
		fields["email"] = "must be an email address"
	}
	req.GivenName, req.FamilyName = strings.TrimSpace(req.GivenName), strings.TrimSpace(req.FamilyName)
	if utf8.RuneCountInString(req.GivenName) > maxContactName {
		fields["givenName"] = "must be at most 100 characters"
	}
	if utf8.RuneCountInString(req.FamilyName) > maxContactName {
		fields["familyName"] = "must be at most 100 characters"
	}
	legalEntities := mappingFields(fields, req.CustomerID, req.LegalEntityIDs)
	if len(fields) > 0 {
		invalidFields(w, r, "invalid customer contact", fields)
		return
	}
	if !a.requireKeycloak(w, r) || !a.checkMapping(w, r, p, legalEntities) {
		return
	}
	customerID, _ := canonicalUUID(req.CustomerID)
	realm, err := a.realm(r.Context(), p.TenantID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	userID, err := a.keycloak.CreateContact(r.Context(), realm, ContactSpec{TenantID: p.TenantID, Email: email, GivenName: req.GivenName, FamilyName: req.FamilyName, CustomerID: customerID, LegalEntityIDs: legalEntities})
	if err != nil {
		a.keycloakFailed(w, r, "create the account", err)
		return
	}
	name := strings.TrimSpace(req.GivenName + " " + req.FamilyName)
	var created customerContact
	err = dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `insert into identity_customer_contacts (tenant_id, contact_id, keycloak_user_id, email, display_name, customer_id, legal_entity_ids, invited_by, updated_by)
values ($1, $2, $3, $4, $5, $6, $7, $8, $8) returning `+contactColumns, p.TenantID, ids.New(), userID, email, name, customerID, legalEntities, p.UserID)
		if err != nil {
			return err
		}
		if created, err = pgx.CollectExactlyOneRow(rows, scanContact); err != nil {
			return err
		}
		return record(r.Context(), tx, p.TenantID, p.UserID, "customercontact", created.ContactID, "invited", contactEvent(created))
	})
	if pgCode(err) == pgUnique {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeContactExists, "a customer contact with this email address already exists"))
		return
	}
	if err != nil {
		failWrite(w, r, err)
		return
	}
	created = a.sendInvitation(r.Context(), p, realm, userID, created)
	w.Header().Set("Location", "/v1/identity/customer-contacts/"+created.ContactID)
	httpx.JSON(w, http.StatusCreated, created)
}

// sendInvitation emails the link and stamps the contact. A failure is logged and leaves invitationSent false, never failing the
// invite: the account exists and :resend sends the email again.
func (a *CustomerContactsAPI) sendInvitation(ctx context.Context, p tenant.Principal, realm, userID string, c customerContact) customerContact {
	if err := a.keycloak.SendInvitation(ctx, realm, userID, a.invitation); err != nil {
		slog.ErrorContext(ctx, "customer contact invitation email failed", "err", err, "contactId", c.ContactID)
		return c
	}
	return a.stampInvitation(ctx, p, c)
}

// contactEvent is what the audit trail and the event carry of a contact: ids and status, never the address or name.
func contactEvent(c customerContact) map[string]any {
	return map[string]any{"contactId": c.ContactID, "customerId": c.CustomerID, "legalEntityIds": c.LegalEntityIDs, "status": c.Status}
}

type contactState struct {
	customerContact
	keycloakUserID string
}

func loadContact(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (contactState, error) {
	suffix := ""
	if lock {
		suffix = " for update"
	}
	var s contactState
	row := tx.QueryRow(ctx, `select `+contactColumns+`, keycloak_user_id::text from identity_customer_contacts where tenant_id = $1 and contact_id = $2`+suffix, tenantID, id)
	err := row.Scan(&s.ContactID, &s.Email, &s.DisplayName, &s.CustomerID, &s.LegalEntityIDs, &s.Status, &s.InvitedBy, &s.InvitedAt, &s.LastInvitationSentAt, &s.UpdatedBy, &s.UpdatedAt, &s.keycloakUserID)
	s.InvitationSent = s.LastInvitationSentAt != nil
	return s, err
}

func (a *CustomerContactsAPI) contact(w http.ResponseWriter, r *http.Request, tenantID, id string) (contactState, bool) {
	if !validUUID(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeContactNotFound, "customer contact not found"))
		return contactState{}, false
	}
	var s contactState
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		var err error
		s, err = loadContact(r.Context(), tx, tenantID, id, false)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeContactNotFound, "customer contact not found"))
		return s, false
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return s, false
	}
	return s, true
}

type remapRequest struct {
	CustomerID     string   `json:"customerId"`
	LegalEntityIDs []string `json:"legalEntityIds"`
}

// remap changes the customer and legal entities of a contact. Keycloak is changed first and its sessions end; the registry row then
// follows, so a failure leaves the registry showing the old mapping and a retry converges.
func (a *CustomerContactsAPI) remap(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	var req remapRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	legalEntities := mappingFields(fields, req.CustomerID, req.LegalEntityIDs)
	if len(fields) > 0 {
		invalidFields(w, r, "invalid customer contact mapping", fields)
		return
	}
	if !a.requireKeycloak(w, r) {
		return
	}
	current, ok := a.contact(w, r, p.TenantID, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if current.Status == "disabled" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeContactDisabled, "the contact is disabled"))
		return
	}
	// The caller must hold scope over the new entities and over the ones being taken away, so a contact of an entity outside the
	// caller's scope can neither be moved out of it nor into it.
	if !a.checkMapping(w, r, p, legalEntities) {
		return
	}
	for _, le := range current.LegalEntityIDs {
		if !tenant.InLegalEntityScope(p, le) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeContactScope, "the contact belongs to a legal entity outside the caller's scope"))
			return
		}
	}
	customerID, _ := canonicalUUID(req.CustomerID)
	realm, err := a.realm(r.Context(), p.TenantID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.keycloak.MapContact(r.Context(), realm, p.TenantID, current.keycloakUserID, customerID, legalEntities); err != nil {
		a.keycloakFailed(w, r, "change the mapping", err)
		return
	}
	var updated customerContact
	err = dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `update identity_customer_contacts set customer_id = $3, legal_entity_ids = $4, updated_by = $5, updated_at = $6
where tenant_id = $1 and contact_id = $2 returning `+contactColumns, p.TenantID, current.ContactID, customerID, legalEntities, p.UserID, a.now())
		if err != nil {
			return err
		}
		if updated, err = pgx.CollectExactlyOneRow(rows, scanContact); err != nil {
			return err
		}
		return record(r.Context(), tx, p.TenantID, p.UserID, "customercontact", updated.ContactID, "remapped", contactEvent(updated))
	})
	if err != nil {
		failWrite(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, updated)
}

// action serves POST /customer-contacts/{id}:resend and {id}:disable.
func (a *CustomerContactsAPI) action(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	id, verb, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	if !found || (verb != "resend" && verb != "disable") {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeContactNotFound, "no such action"))
		return
	}
	if !a.requireKeycloak(w, r) {
		return
	}
	current, ok := a.contact(w, r, p.TenantID, id)
	if !ok {
		return
	}
	for _, le := range current.LegalEntityIDs {
		if !tenant.InLegalEntityScope(p, le) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeContactScope, "the contact belongs to a legal entity outside the caller's scope"))
			return
		}
	}
	realm, err := a.realm(r.Context(), p.TenantID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if verb == "resend" {
		if current.Status == "disabled" {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeContactDisabled, "the contact is disabled"))
			return
		}
		if err := a.keycloak.SendInvitation(r.Context(), realm, current.keycloakUserID, a.invitation); err != nil {
			a.keycloakFailed(w, r, "send the invitation", err)
			return
		}
		httpx.JSON(w, http.StatusOK, a.stampInvitation(r.Context(), p, current.customerContact))
		return
	}
	if current.Status != "disabled" {
		if err := a.keycloak.DisableContact(r.Context(), realm, p.TenantID, current.keycloakUserID); err != nil {
			a.keycloakFailed(w, r, "disable the account", err)
			return
		}
	}
	var updated customerContact
	err = dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `update identity_customer_contacts set status = 'disabled', updated_by = $3, updated_at = $4
where tenant_id = $1 and contact_id = $2 returning `+contactColumns, p.TenantID, current.ContactID, p.UserID, a.now())
		if err != nil {
			return err
		}
		if updated, err = pgx.CollectExactlyOneRow(rows, scanContact); err != nil {
			return err
		}
		if current.Status == "disabled" {
			return nil
		}
		return record(r.Context(), tx, p.TenantID, p.UserID, "customercontact", updated.ContactID, "disabled", contactEvent(updated))
	})
	if err != nil {
		failWrite(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, updated)
}

// stampInvitation records that the email was sent (Keycloak already accepted it) and returns the contact.
func (a *CustomerContactsAPI) stampInvitation(ctx context.Context, p tenant.Principal, c customerContact) customerContact {
	err := dbx.WithTenantTx(ctx, a.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `update identity_customer_contacts set last_invitation_sent_at = $3 where tenant_id = $1 and contact_id = $2 returning last_invitation_sent_at`,
			p.TenantID, c.ContactID, a.now()).Scan(&c.LastInvitationSentAt)
	})
	if err != nil {
		slog.ErrorContext(ctx, "customer contact invitation stamp failed", "err", err, "contactId", c.ContactID)
		return c
	}
	c.InvitationSent = true
	return c
}

type portalLegalEntity struct {
	LegalEntityID string `json:"legalEntityId"`
	DisplayName   string `json:"displayName"`
}

// legalEntity answers the name of the one legal entity of the customer portal token, for the portal's company selector. It is the
// token's own entity and nothing else: no id is taken from the request, and a caller that is not the customer portal acting for a
// customer is refused.
func (a *CustomerContactsAPI) legalEntity(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if _, isPortal := tenant.PortalCustomer(r.Context()); !isPortal || !validUUID(p.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "this account is not a customer account"))
		return
	}
	var out portalLegalEntity
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `select legal_entity_id::text, display_name from identity_legal_entities where tenant_id = $1 and legal_entity_id = $2 and status = 'active'`,
			p.TenantID, p.LegalEntityID).Scan(&out.LegalEntityID, &out.DisplayName)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeLegalEntityNotFound, "legal entity not found"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
