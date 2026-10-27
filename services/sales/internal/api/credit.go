package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const maxDunningLevel = 9

// creditProfileRequest is the PUT body for /sales/credit-profiles/{customerId}.
// Exposure is supplied with the profile until the receivables projection
// exists (waves 29/30).
type creditProfileRequest struct {
	Limit             decimal.Decimal `json:"limit"`
	Currency          string          `json:"currency"`
	LimitType         string          `json:"limitType"`
	TolerancePct      decimal.Decimal `json:"tolerancePct"`
	ToleranceAmount   decimal.Decimal `json:"toleranceAmount"`
	OldestItemDaysMax int             `json:"oldestItemDaysMax"`
	MaxDunningLevel   int             `json:"maxDunningLevel"`
	ReviewDate        *string         `json:"reviewDate"`
	CustomerSince     *string         `json:"customerSince"`
	MandatoryCheck    bool            `json:"mandatoryCheck"`
	Exposure          domain.Exposure `json:"exposure"`
}

// buildCreditProfile validates a profile body. Field errors are keyed by JSON
// path, as for orders.
func buildCreditProfile(legalEntityID, customerID string, req creditProfileRequest) (store.CreditProfile, map[string]string) {
	f := map[string]string{}
	if !ids.Valid(customerID) {
		f["customerId"] = "must be a UUID"
	}
	if req.Limit.IsNegative() {
		f["limit"] = "must not be negative"
	}
	if !currencyCode.MatchString(req.Currency) {
		f["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !limitTypes[req.LimitType] {
		f["limitType"] = "must be NONE, BALANCE, BALANCE_PACKING_SLIP or BALANCE_ALL"
	}
	if req.TolerancePct.IsNegative() || req.TolerancePct.GreaterThan(decimal.NewFromInt(100)) {
		f["tolerancePct"] = "must be between 0 and 100"
	}
	if req.ToleranceAmount.IsNegative() {
		f["toleranceAmount"] = "must not be negative"
	}
	if req.OldestItemDaysMax < 0 {
		f["oldestItemDaysMax"] = "must not be negative"
	}
	if req.MaxDunningLevel < 0 || req.MaxDunningLevel > maxDunningLevel {
		f["maxDunningLevel"] = fmt.Sprintf("must be from 0 to %d", maxDunningLevel)
	}
	if !nonNegative(req.Exposure) {
		f["exposure"] = "amounts and counts must not be negative"
	}
	profile := domain.CreditProfile{Limit: req.Limit, Currency: req.Currency, LimitType: req.LimitType,
		TolerancePct: req.TolerancePct, ToleranceAmount: req.ToleranceAmount, OldestItemDaysMax: req.OldestItemDaysMax,
		MaxDunningLevel: req.MaxDunningLevel, MandatoryCheck: req.MandatoryCheck}
	var err error
	if profile.ReviewDate, err = optionalDate(req.ReviewDate); err != nil {
		f["reviewDate"] = "must be a date such as 2027-03-04"
	}
	if profile.CustomerSince, err = optionalDate(req.CustomerSince); err != nil {
		f["customerSince"] = "must be a date such as 2027-03-04"
	}
	return store.CreditProfile{LegalEntityID: legalEntityID, CustomerID: customerID, CreditProfile: profile, Exposure: req.Exposure}, f
}

var limitTypes = map[string]bool{domain.LimitNone: true, domain.LimitBalance: true,
	domain.LimitBalancePackingSlip: true, domain.LimitBalanceAll: true}

func nonNegative(e domain.Exposure) bool {
	amounts := []decimal.Decimal{e.OpenAR, e.Unapplied, e.DeliveredNotInvoiced, e.OpenOrders, e.OverdueAR}
	for _, a := range amounts {
		if a.IsNegative() {
			return false
		}
	}
	return e.OldestItemDays >= 0 && e.DunningLevel >= 0
}

func optionalDate(raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	t, err := time.Parse(dayLayout, *raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// creditProfileView is the wire form of a profile: dates as YYYY-MM-DD and
// decimals as strings, matching api/openapi.yaml.
type creditProfileView struct {
	LegalEntityID     string          `json:"legalEntityId"`
	CustomerID        string          `json:"customerId"`
	Limit             decimal.Decimal `json:"limit"`
	Currency          string          `json:"currency"`
	LimitType         string          `json:"limitType"`
	TolerancePct      decimal.Decimal `json:"tolerancePct"`
	ToleranceAmount   decimal.Decimal `json:"toleranceAmount"`
	OldestItemDaysMax int             `json:"oldestItemDaysMax"`
	MaxDunningLevel   int             `json:"maxDunningLevel"`
	ReviewDate        *string         `json:"reviewDate,omitempty"`
	CustomerSince     *string         `json:"customerSince,omitempty"`
	MandatoryCheck    bool            `json:"mandatoryCheck"`
	Exposure          domain.Exposure `json:"exposure"`
	Version           int             `json:"version"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

func dayPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(dayLayout)
	return &s
}

func profileView(c store.CreditProfile) creditProfileView {
	return creditProfileView{LegalEntityID: c.LegalEntityID, CustomerID: c.CustomerID, Limit: c.Limit, Currency: c.Currency,
		LimitType: c.LimitType, TolerancePct: c.TolerancePct, ToleranceAmount: c.ToleranceAmount, OldestItemDaysMax: c.OldestItemDaysMax,
		MaxDunningLevel: c.MaxDunningLevel, ReviewDate: dayPtr(c.ReviewDate), CustomerSince: dayPtr(c.CustomerSince),
		MandatoryCheck: c.MandatoryCheck, Exposure: c.Exposure, Version: c.Version, UpdatedAt: c.UpdatedAt}
}

// creditLegalEntity resolves the legal entity a profile belongs to: the
// legalEntityId query parameter, or the caller's legal entity. A caller scoped
// to one legal entity may not name another.
func creditLegalEntity(w http.ResponseWriter, r *http.Request, required bool) (string, bool) {
	p, _ := tenant.FromContext(r.Context())
	le := r.URL.Query().Get("legalEntityId")
	if le == "" {
		le = p.LegalEntityID
	}
	if le == "" && !required {
		return "", true
	}
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "a legal entity UUID is required"})
		return "", false
	}
	if p.LegalEntityID != "" && le != p.LegalEntityID {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside the caller's scope"))
		return "", false
	}
	return le, true
}

// listCreditProfiles lists profiles of one legal entity, or of every legal
// entity in the caller's scope.
func (a *API) listCreditProfiles(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	limit, ok := pageLimit(r.URL.Query().Get("limit"))
	if !ok {
		validationFailed(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", maxPageSize)})
		return
	}
	profiles, err := a.store.ListCreditProfiles(r.Context(), legalEntity, limit)
	if err != nil {
		a.fail(w, r, "list credit profiles", err)
		return
	}
	items := make([]creditProfileView, 0, len(profiles))
	for _, c := range profiles {
		items = append(items, profileView(c))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getCreditProfile returns the customer's profile for one legal entity.
func (a *API) getCreditProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	customerID := chi.URLParam(r, "customerId")
	if !ids.Valid(customerID) {
		validationFailed(w, r, map[string]string{"customerId": "must be a UUID"})
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, true)
	if !ok {
		return
	}
	profile, err := a.store.GetCreditProfile(r.Context(), legalEntity, customerID)
	if err != nil {
		a.fail(w, r, "get credit profile", err)
		return
	}
	setETag(w, profile.Version)
	httpx.JSON(w, http.StatusOK, profileView(profile))
}

// putCreditProfile creates or replaces the profile. If-Match carries the
// current ETag, or "0" to create.
func (a *API) putCreditProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	customerID := chi.URLParam(r, "customerId")
	legalEntity, ok := creditLegalEntity(w, r, true)
	if !ok {
		return
	}
	var req creditProfileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := buildCreditProfile(legalEntity, customerID, req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	saved, err := a.store.PutCreditProfile(r.Context(), in, version)
	if err != nil {
		a.fail(w, r, "put credit profile", err)
		return
	}
	setETag(w, saved.Version)
	httpx.JSON(w, http.StatusOK, profileView(saved))
}
