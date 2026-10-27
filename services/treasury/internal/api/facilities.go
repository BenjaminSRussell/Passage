package api

import (
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

type createFacilityRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	BankName      string `json:"bankName"`
	Currency      string `json:"currency"`
	Limit         string `json:"limit"`
	ExpiryDate    string `json:"expiryDate"`
}

type createInstrumentRequest struct {
	Kind           string  `json:"kind"`
	Reference      string  `json:"reference"`
	Beneficiary    string  `json:"beneficiary"`
	Currency       string  `json:"currency"`
	Amount         string  `json:"amount"`
	IssueDate      string  `json:"issueDate"`
	ExpiryDate     string  `json:"expiryDate"`
	LinkedOrderRef *string `json:"linkedOrderRef"`
	OverrideLimit  bool    `json:"overrideLimit"`
}

// createFacility registers a bank facility with its limit (FR-36.7).
func (a *API) createFacility(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req createFacilityRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !domain.ValidAccountCode(req.Code) {
		fields["code"] = "1 to 20 characters of A-Z, 0-9 or -"
	}
	if n := len(strings.TrimSpace(req.BankName)); n < 1 || n > 120 {
		fields["bankName"] = "1 to 120 characters"
	}
	if !domain.ValidCurrency(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	limit, ok := parseAmount(req.Limit, req.Currency)
	if !ok {
		fields["limit"] = "must be a positive decimal string with at most the currency's minor-unit places"
	}
	if _, err := domain.Day(req.ExpiryDate); err != nil {
		fields["expiryDate"] = "must be a YYYY-MM-DD date"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if outsideScope(w, r, req.LegalEntityID) {
		return
	}
	f, err := a.store.CreateFacility(r.Context(), store.NewFacility{
		LegalEntityID: req.LegalEntityID, Code: req.Code, BankName: strings.TrimSpace(req.BankName),
		Currency: req.Currency, Limit: limit.String(), ExpiryDate: req.ExpiryDate,
	}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, f.Version)
	httpx.JSON(w, http.StatusCreated, f)
}

func (a *API) listFacilities(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "required UUID query parameter"})
		return
	}
	items, err := a.store.ListFacilities(r.Context(), le)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getFacility(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeFacilityNotFound, "facility not found")
	if !ok {
		return
	}
	f, err := a.store.GetFacility(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, f.Version)
	httpx.JSON(w, http.StatusOK, f)
}

// createInstrument books a letter of credit or guarantee against a facility.
// overrideLimit needs the TreasuryFacilityOverride duty; without it a limit
// breach is refused with TRS-0415 (FR-36.7, AC-36.6).
func (a *API) createInstrument(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	facilityID, ok := pathID(w, r, store.CodeFacilityNotFound, "facility not found")
	if !ok {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req createInstrumentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.ValidInstrumentKind(req.Kind) {
		fields["kind"] = "one of lc_import, lc_export, guarantee"
	}
	if n := len(strings.TrimSpace(req.Reference)); n < 1 || n > 64 {
		fields["reference"] = "1 to 64 characters"
	}
	if n := len(strings.TrimSpace(req.Beneficiary)); n < 1 || n > 140 {
		fields["beneficiary"] = "1 to 140 characters"
	}
	if !domain.ValidCurrency(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	amount, ok := parseAmount(req.Amount, req.Currency)
	if !ok {
		fields["amount"] = "must be a positive decimal string with at most the currency's minor-unit places"
	}
	issue, issueErr := domain.Day(req.IssueDate)
	if issueErr != nil {
		fields["issueDate"] = "must be a YYYY-MM-DD date"
	}
	expiry, err := domain.Day(req.ExpiryDate)
	if err != nil {
		fields["expiryDate"] = "must be a YYYY-MM-DD date"
	} else if issueErr == nil && issue.After(expiry) {
		fields["expiryDate"] = "must not precede the issue date"
	}
	var linked *string
	if req.LinkedOrderRef != nil && strings.TrimSpace(*req.LinkedOrderRef) != "" {
		ref := strings.TrimSpace(*req.LinkedOrderRef)
		if len(ref) > 64 {
			fields["linkedOrderRef"] = "at most 64 characters"
		}
		linked = &ref
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if req.OverrideLimit {
		a.limitOverrides.Add(1)
		// Same answer as authz.RequireDuty: the override is a second duty on this route.
		if allowed, err := (authz.DutyPolicy{}).Allow(r.Context(), principal(r), dutyFacilityOverride, facilityID); err != nil || !allowed {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+dutyFacilityOverride+" (overrideLimit)"))
			return
		}
	}
	inst, err := a.store.CreateInstrument(r.Context(), store.NewInstrument{
		FacilityID: facilityID, Kind: req.Kind, Reference: strings.TrimSpace(req.Reference),
		Beneficiary: strings.TrimSpace(req.Beneficiary), Currency: req.Currency, Amount: amount.String(),
		IssueDate: req.IssueDate, ExpiryDate: req.ExpiryDate, LinkedOrderRef: linked,
	}, req.OverrideLimit, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.instrumentsBooked.Add(1)
	setETag(w, inst.Version)
	httpx.JSON(w, http.StatusCreated, inst)
}

// principal returns the caller; validTenant has already rejected requests without one.
func principal(r *http.Request) tenant.Principal {
	p, _ := tenant.FromContext(r.Context())
	return p
}

func (a *API) listInstruments(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeFacilityNotFound, "facility not found")
	if !ok {
		return
	}
	items, err := a.store.ListInstruments(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// closeInstrument releases an open instrument from facility utilization.
func (a *API) closeInstrument(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeInstrumentNotFound, "instrument not found")
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	inst, err := a.store.CloseInstrument(r.Context(), id, version)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, inst.Version)
	httpx.JSON(w, http.StatusOK, inst)
}
