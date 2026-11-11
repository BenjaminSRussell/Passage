package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

// Prospect registration and vendor-admin invitations (#511 remainders). The registration route is the one place a supplier portal
// token carries no vendor; every other portal route here takes its vendor from the verified token like the rest of vendor-portal.
const (
	dutyPortalRegister     = "party.vendor-portal.register"
	dutyPortalInviteAccept = "party.vendor-portal.invite-accept"
	// dutyPortalAdmin lists, creates and revokes the vendor's invitations. Every signed-in supplier user's token carries
	// party.vendor-portal.write; only the vendor administrator's (the supplier-admin role) carries this one, and the portal
	// signs it only for the invitation routes.
	dutyPortalAdmin = "party.vendor-portal.admin"
	// registerBodyLimit is the largest registration the page may send.
	registerBodyLimit = 16 << 10
)

// WithPortal enables prospect registration and invitations.
func (a *API) WithPortal(s store.PortalStore) *API {
	a.portal = s
	return a
}

func (a *API) registerPortal(r chi.Router) {
	admin := authz.RequireDuty(dutyPortalAdmin)
	accept, register := authz.RequireDuty(dutyPortalInviteAccept), authz.RequireDuty(dutyPortalRegister)
	r.With(register).Post("/v1/party/vendor-portal/prospects", a.registerProspect)
	r.With(admin).Get("/v1/party/vendor-portal/invites", a.listInvites)
	r.With(admin).Post("/v1/party/vendor-portal/invites", a.createInvite)
	r.With(admin).Post("/v1/party/vendor-portal/invites/{id}:revoke", a.revokeInvite)
	r.With(accept).Post("/v1/party/vendor-portal/invites/{id}:verify", a.invite(false))
	r.With(accept).Post("/v1/party/vendor-portal/invites/{id}:accept", a.invite(true))

	buyerRead, buyerWrite, decide := authz.RequireDuty(dutyRead), authz.RequireDuty(dutyWrite), authz.RequireDuty(dutyVendorRequestDecide)
	r.With(buyerRead).Get("/v1/party/vendor-prospects", a.listProspects)
	r.With(buyerRead).Get("/v1/party/vendor-prospects/{id}", a.getProspect)
	r.With(decide).Post("/v1/party/vendor-prospects/{id}:reject", a.rejectProspect)
	if a.vendors != nil {
		r.With(buyerWrite).Post("/v1/party/vendor-prospects/{id}:accept", a.acceptProspect)
	}
}

// registrationCaller is the portal acting for no vendor: a supplier-portal token whose only duty is the registration.
func registrationCaller(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || p.Service != tenant.VendorPortalIssuer || p.VendorID != "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeSupplierScope, "only the supplier portal registers prospects"))
		return false
	}
	return true
}

func (a *API) registerProspect(w http.ResponseWriter, r *http.Request) {
	if !registrationCaller(w, r) {
		return
	}
	var in domain.NewProspect
	if err := httpx.DecodeLimit(r, &in, registerBodyLimit); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in = in.Clean()
	if errs := in.Validate(); len(errs) > 0 {
		invalid(w, r, errs)
		return
	}
	if _, err := a.portal.RegisterProspect(r.Context(), in); err != nil {
		failParty(w, r, err)
		return
	}
	// The same answer whether or not the address was already waiting.
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "RECEIVED"})
}

func (a *API) listProspects(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != domain.ProspectNew && status != domain.ProspectAccepted && status != domain.ProspectRejected {
		invalid(w, r, map[string]string{"status": "must be NEW, ACCEPTED or REJECTED"})
		return
	}
	items, err := a.portal.ListProspects(r.Context(), status, pageLimit(r))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getProspect(w http.ResponseWriter, r *http.Request) {
	p, err := a.portal.GetProspect(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (a *API) rejectProspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note string `json:"note"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if n := len([]rune(req.Note)); n < 1 || n > 1000 {
		invalid(w, r, map[string]string{"note": "is required, at most 1000 characters"})
		return
	}
	p, err := a.portal.RejectProspect(r.Context(), chi.URLParam(r, "id"), req.Note)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

type acceptProspectBody struct {
	domain.NewVendor
	QuestionnaireID string `json:"questionnaireId"`
}

// acceptProspect turns a reviewed registration into master data and the existing onboarding: the party, a vendor role (blocked
// until the onboarding is approved by someone other than the buyer who accepted), and the questionnaire request. The registration
// is claimed first (ClaimProspect), and every step is idempotent on the registration id, so a failure part-way is finished by
// repeating the call.
func (a *API) acceptProspect(w http.ResponseWriter, r *http.Request) {
	var req acceptProspectBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.NewVendor = req.NewVendor.Clean()
	errs := req.NewVendor.Validate()
	if req.QuestionnaireID == "" {
		errs["questionnaireId"] = "is required"
	}
	if len(errs) > 0 {
		invalid(w, r, errs)
		return
	}
	prospect, err := a.portal.GetProspect(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	key := "prospect-" + prospect.ID
	fields := domain.PartyFields{LegalName: &prospect.CompanyName}
	if prospect.TaxID != "" {
		fields.RegistrationNumber = &prospect.TaxID
	}
	if errs := domain.ValidateParty(domain.TypeOrganization, fields, true); len(errs) > 0 {
		invalid(w, r, errs)
		return
	}
	// Claim the registration before creating anything: a second buyer accepting at the same time is refused here, and a repeat
	// of an accept that stopped part-way resumes it.
	if _, err := a.portal.ClaimProspect(r.Context(), prospect.ID); err != nil {
		failParty(w, r, err)
		return
	}
	party, _, err := a.parties.CreateParty(r.Context(), store.NewParty{PartyType: domain.TypeOrganization, Fields: fields, Key: key, Hash: domain.RequestHash(fields)},
		func(ctx context.Context) (string, error) { return a.sequence.Next(ctx, partySequence, key) })
	if err != nil {
		failParty(w, r, err)
		return
	}
	vendor, _, err := a.vendors.CreateVendorRole(r.Context(), party.ID, store.NewVendorRole{In: req.NewVendor, Key: key, Hash: domain.RequestHash(req.NewVendor)})
	if err != nil {
		failParty(w, r, err)
		return
	}
	// A repeated accept finds the onboarding already open; AcceptProspect then links that one.
	onboarding, err := a.suppliers.InviteOnboarding(r.Context(), vendor.ID, req.QuestionnaireID)
	var problem *httpx.Problem
	if err != nil && !(errors.As(err, &problem) && problem.Code == store.CodeSupplierState) {
		failParty(w, r, err)
		return
	}
	out, err := a.portal.AcceptProspect(r.Context(), prospect.ID, party.ID, vendor.ID, onboarding.ID)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listInvites(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	items, err := a.portal.ListInvites(r.Context(), vendor, pageLimit(r))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) createInvite(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	var req struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Email, req.DisplayName = strings.ToLower(strings.TrimSpace(req.Email)), strings.TrimSpace(req.DisplayName)
	errs := map[string]string{}
	if !domain.ValidEmail(req.Email) {
		errs["email"] = "must be an email address"
	}
	if len([]rune(req.DisplayName)) > 140 {
		errs["displayName"] = "must be at most 140 characters"
	}
	if len(errs) > 0 {
		invalid(w, r, errs)
		return
	}
	inv, token, err := a.portal.CreateInvite(r.Context(), vendor, req.Email, req.DisplayName)
	if err != nil {
		failParty(w, r, err)
		return
	}
	// The token is in this answer only; nothing stores it.
	httpx.JSON(w, http.StatusCreated, map[string]any{"invite": inv, "token": token})
}

func (a *API) revokeInvite(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	inv, err := a.portal.RevokeInvite(r.Context(), vendor, chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, inv)
}

// invite verifies (consume false) or accepts an invitation. The vendor is the verified token's; the token in the body must name
// that vendor and the invitation in the path, and its secret must match.
func (a *API) invite(consume bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vendor, ok := portalVendor(w, r)
		if !ok {
			return
		}
		var req struct {
			Token string `json:"token"`
		}
		if err := httpx.DecodeLimit(r, &req, 2<<10); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if t, ok := domain.ParseInviteToken(req.Token); !ok || t.InviteID != chi.URLParam(r, "id") {
			failParty(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeInviteInvalid, "the invitation is not valid, has expired or was already used"))
			return
		}
		var d store.InviteDetails
		var err error
		if consume {
			d, err = a.portal.AcceptInvite(r.Context(), vendor, req.Token)
		} else {
			d, err = a.portal.VerifyInvite(r.Context(), vendor, req.Token)
		}
		if err != nil {
			failParty(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, d)
	}
}
