package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	currencyBase     = "/v1/org/currencies/{code}"
	maxRoundingScale = 6
)

type putCurrencyRoundingRequest struct {
	Unit          *string `json:"unit"`
	Method        string  `json:"method"`
	LegalEntityID *string `json:"legalEntityId"`
}

type roundResult struct {
	Currency      string  `json:"currency"`
	Purpose       string  `json:"purpose"`
	LegalEntityID *string `json:"legalEntityId"`
	Amount        string  `json:"amount"`
	Rounded       string  `json:"rounded"`
	Unit          string  `json:"unit"`
	Method        string  `json:"method"`
	Source        string  `json:"source"`
}

func (a *API) registerCurrencyRounding(r chi.Router) {
	r.With(authz.RequireDuty(dutyReference)).Put(currencyBase+"/rounding-rules/{purpose}", a.putCurrencyRounding)
	r.With(authz.RequireDuty(dutyRead)).Get(currencyBase+"/rounding-rules", a.listCurrencyRoundings)
	r.With(authz.RequireDuty(dutyRead)).Get(currencyBase+"/round", a.roundCurrency)
}

func decimalInBounds(d decimal.Decimal) bool {
	return d.Exponent() <= maxQuantityExp && d.Exponent() >= minQuantityExp && !d.Abs().GreaterThan(maxQuantity)
}

func normalized(d decimal.Decimal) decimal.Decimal { return decimal.RequireFromString(d.String()) }

func parseUnit(raw *string) (decimal.Decimal, string) {
	if raw == nil {
		return decimal.Zero, "is required"
	}
	unit, err := decimal.NewFromString(*raw)
	if err != nil || !unit.IsPositive() || !decimalInBounds(unit) {
		return decimal.Zero, "must be a positive decimal"
	}
	if normalized(unit).Exponent() < -maxRoundingScale {
		return decimal.Zero, "must have at most 6 decimal places"
	}
	return unit, ""
}

func parseRule(purpose string, req putCurrencyRoundingRequest) (store.CurrencyRounding, map[string]string) {
	fields := map[string]string{}
	in := store.CurrencyRounding{Purpose: purpose, Method: req.Method, LegalEntityID: req.LegalEntityID}
	if _, err := domain.ParsePurpose(purpose); err != nil {
		fields["purpose"] = "must be general, price, purchase, sales, tax or cash"
	}
	if _, err := domain.ParseMethod(req.Method); err != nil {
		fields["method"] = "must be half_even, half_up, down or up"
	}
	unit, unitErr := parseUnit(req.Unit)
	if unitErr != "" {
		fields["unit"] = unitErr
	}
	in.Unit = unit
	if req.LegalEntityID != nil && !canonicalUUID(*req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	return in, fields
}

func (a *API) putCurrencyRounding(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || rejectScopedCaller(w, r, "change currency rounding rules") {
		return
	}
	var req putCurrencyRoundingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := parseRule(chi.URLParam(r, "purpose"), req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	in.CurrencyCode = chi.URLParam(r, "code")
	rule, changed, err := a.store.PutCurrencyRounding(r.Context(), in)
	if err != nil {
		slog.Error("put currency rounding failed", "err", err, "currency", in.CurrencyCode, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if changed {
		a.roundingChanged.Add(1)
	}
	httpx.JSON(w, http.StatusOK, rule)
}

func (a *API) listCurrencyRoundings(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	rules, err := a.store.ListCurrencyRoundings(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		slog.Error("list currency rounding rules failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rules})
}

func (a *API) roundCurrency(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	amountText := q.Get("amount")
	amount, err := decimal.NewFromString(amountText)
	if err != nil || !decimalInBounds(amount) {
		badQuery(w, r, "amount", "is required and must be a finite decimal within range")
		return
	}
	purpose := q.Get("purpose")
	if purpose == "" {
		purpose = string(domain.PurposeGeneral)
	}
	if _, err := domain.ParsePurpose(purpose); err != nil {
		badQuery(w, r, "purpose", "must be general, price, purchase, sales, tax or cash")
		return
	}
	p, _ := tenant.FromContext(r.Context())
	legalEntityID := p.LegalEntityID
	if q.Has("legalEntityId") {
		asked := q.Get("legalEntityId")
		if !canonicalUUID(asked) {
			badQuery(w, r, "legalEntityId", "must be a UUID")
			return
		}
		if p.LegalEntityID != "" && !strings.EqualFold(asked, p.LegalEntityID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a legal-entity-scoped caller cannot round for another legal entity"))
			return
		}
		legalEntityID = asked
	}
	code := chi.URLParam(r, "code")
	rule, err := a.store.ResolveCurrencyRounding(r.Context(), code, purpose, legalEntityID)
	if err != nil {
		slog.Error("resolve currency rounding failed", "err", err, "currency", code, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	unit := normalized(rule.Unit)
	rounded := domain.Round(amount, unit, rule.Method)
	result := roundResult{Currency: code, Purpose: purpose, Amount: amountText, Rounded: rounded.StringFixed(max(0, -unit.Exponent())), Unit: unit.String(), Method: string(rule.Method), Source: rule.Source}
	if legalEntityID != "" {
		result.LegalEntityID = &legalEntityID
	}
	httpx.JSON(w, http.StatusOK, result)
}
