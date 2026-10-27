package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

const maxCapexVariants = 10

var (
	indexCode        = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,31}$`)
	countryCode      = regexp.MustCompile(`^[A-Z]{2}$`)
	yearKey          = regexp.MustCompile(`^[12][0-9]{3}$`)
	validInsurance   = map[string]bool{"Property": true, "MachineryBreakdown": true, "Transit": true, "Fleet": true}
	validBasis       = map[string]bool{"Replacement": true, "NBV": true, "Agreed": true}
	validCapexStatus = map[string]bool{"Draft": true, "Submitted": true, "InApproval": true, "Approved": true, "Released": true,
		"Closed": true, "Rejected": true, "Postponed": true}
)

type indexValueRequest struct {
	Year  int    `json:"year"`
	Value string `json:"value"`
}

type indexSeriesRequest struct {
	Country   string              `json:"country"`
	ClassCode string              `json:"classCode"`
	Values    []indexValueRequest `json:"values"`
}

type insurancePolicyRequest struct {
	InsurerPartyID  string  `json:"insurerPartyId"`
	PolicyNumber    string  `json:"policyNumber"`
	InsuranceType   string  `json:"insuranceType"`
	CoverageFrom    string  `json:"coverageFrom"`
	CoverageTo      string  `json:"coverageTo"`
	Premium         *string `json:"premium"`
	Currency        string  `json:"currency"`
	Basis           string  `json:"basis"`
	BaseValue       string  `json:"baseValue"`
	SumInsured      string  `json:"sumInsured"`
	IndexSeriesCode *string `json:"indexSeriesCode"`
	IndexBaseYear   *int    `json:"indexBaseYear"`
	ValuationYear   *int    `json:"valuationYear"`
}

type capexVariantRequest struct {
	Description       string            `json:"description"`
	AmountsByYear     map[string]string `json:"amountsByYear"`
	InServiceDate     string            `json:"inServiceDate"`
	ProfileAssumption string            `json:"profileAssumption"`
}

type capexRequestBody struct {
	LegalEntityID string                `json:"legalEntityId"`
	Number        string                `json:"number"`
	Title         string                `json:"title"`
	RequesterID   string                `json:"requesterId"`
	Currency      string                `json:"currency"`
	Variants      []capexVariantRequest `json:"variants"`
}

type submitCapexBody struct {
	VariantID string `json:"variantId"`
}

func validYear(y int) bool { return y >= 1900 && y <= 2999 }

// amountField reads a decimal that is non-negative (positive when strict) and
// fits the currency minor unit; the message is "" when it is valid.
func amountField(raw, ccy string, strict bool) (decimal.Decimal, string) {
	v, err := decimal.NewFromString(raw)
	switch {
	case err != nil || v.IsNegative() || (strict && v.IsZero()):
		if strict {
			return decimal.Zero, "must be a positive decimal"
		}
		return decimal.Zero, "must be a non-negative decimal"
	case currency.MatchString(ccy) && !v.Equal(v.Round(money.MinorUnits(ccy))):
		return decimal.Zero, "has more decimals than the currency minor unit"
	}
	return v, ""
}

// putIndexSeries creates or replaces an insured-value index series and upserts
// its yearly values (FR-37.15, insurance.indexSeries). PUT is idempotent by
// itself, so no Idempotency-Key is needed.
func (a *API) putIndexSeries(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	var req indexSeriesRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !indexCode.MatchString(code) {
		fields["code"] = "must be upper-case letters, digits, hyphen or underscore (max 32)"
	}
	if !countryCode.MatchString(req.Country) {
		fields["country"] = "must be a two-letter ISO 3166 code"
	}
	if !classCode.MatchString(req.ClassCode) {
		fields["classCode"] = "must be upper-case letters, digits, hyphen or underscore"
	}
	if len(req.Values) == 0 || len(req.Values) > 200 {
		fields["values"] = "must contain 1 to 200 years"
	}
	seen := map[int]bool{}
	values := make([]store.IndexPoint, 0, len(req.Values))
	for _, v := range req.Values {
		d, err := decimal.NewFromString(v.Value)
		switch {
		case !validYear(v.Year):
			fields["values"] = "every year must be between 1900 and 2999"
		case seen[v.Year]:
			fields["values"] = fmt.Sprintf("year %d is listed twice", v.Year)
		case err != nil || !d.IsPositive() || d.Exponent() < -4 || d.GreaterThanOrEqual(decimal.NewFromInt(100000)):
			fields["values"] = "every value must be a positive decimal below 100000 with at most 4 places"
		}
		seen[v.Year] = true
		values = append(values, store.IndexPoint{Year: v.Year, Value: d})
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	series, err := a.store.UpsertIndexSeries(r.Context(), store.IndexSeries{Code: code, Country: req.Country, ClassCode: req.ClassCode, Values: values})
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, series)
}

func (a *API) listIndexSeries(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListIndexSeries(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// addInsurancePolicy records a policy on an asset (FR-37.15). With an index
// series the indexed replacement value is computed for the valuation year
// (default: the coverage start year); a missing year falls back to the latest
// loaded year (AST-0531) and cover below 90% of the indexed value is flagged
// under-insured (AST-0532). Both come back in warnings.
func (a *API) addInsurancePolicy(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req insurancePolicyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.InsurerPartyID) {
		fields["insurerPartyId"] = "must be a party UUID"
	}
	if n := strings.TrimSpace(req.PolicyNumber); n == "" || len(n) > 60 {
		fields["policyNumber"] = "must be 1 to 60 characters"
	}
	if !validInsurance[req.InsuranceType] {
		fields["insuranceType"] = "must be Property, MachineryBreakdown, Transit or Fleet"
	}
	from, errFrom := store.ParseDate(req.CoverageFrom)
	to, errTo := store.ParseDate(req.CoverageTo)
	if errFrom != nil {
		fields["coverageFrom"] = "must be a YYYY-MM-DD date"
	}
	if errTo != nil {
		fields["coverageTo"] = "must be a YYYY-MM-DD date"
	}
	if errFrom == nil && errTo == nil && !to.After(from.Time) {
		fields["coverageTo"] = "must be after coverageFrom"
	}
	if !currency.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if !validBasis[req.Basis] {
		fields["basis"] = "must be Replacement, NBV or Agreed"
	}
	premium := decimal.Zero
	if req.Premium != nil {
		var msg string
		if premium, msg = amountField(*req.Premium, req.Currency, false); msg != "" {
			fields["premium"] = msg
		}
	}
	base, msg := amountField(req.BaseValue, req.Currency, false)
	if msg != "" {
		fields["baseValue"] = msg
	}
	sum, msg := amountField(req.SumInsured, req.Currency, false)
	if msg != "" {
		fields["sumInsured"] = msg
	}
	var valuation *int
	if req.IndexSeriesCode != nil {
		if !indexCode.MatchString(*req.IndexSeriesCode) {
			fields["indexSeriesCode"] = "must be an index series code"
		}
		if req.IndexBaseYear == nil || !validYear(*req.IndexBaseYear) {
			fields["indexBaseYear"] = "is required with an index series and must be a year"
		}
		year := from.Year()
		if req.ValuationYear != nil {
			year = *req.ValuationYear
		}
		if !validYear(year) {
			fields["valuationYear"] = "must be a year"
		}
		valuation = &year
	} else if req.IndexBaseYear != nil || req.ValuationYear != nil {
		fields["indexSeriesCode"] = "is required when indexBaseYear or valuationYear is given"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	id, ok := assetID(w, r)
	if !ok {
		return
	}
	policy, err := a.store.AddInsurancePolicy(r.Context(), id, store.InsurancePolicy{InsurerPartyID: req.InsurerPartyID,
		PolicyNumber: strings.TrimSpace(req.PolicyNumber), InsuranceType: req.InsuranceType, CoverageFrom: from, CoverageTo: to, Premium: premium,
		Currency: req.Currency, Basis: req.Basis, BaseValue: base, SumInsured: sum, IndexSeriesCode: req.IndexSeriesCode,
		IndexBaseYear: req.IndexBaseYear, ValuationYear: valuation}, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, policy)
}

func (a *API) listInsurancePolicies(w http.ResponseWriter, r *http.Request) {
	id, ok := assetID(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListInsurancePolicies(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// createCapexRequest records an appropriation request in Draft with one to ten
// variants, each with positive amounts per year (FR-37.17).
func (a *API) createCapexRequest(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req capexRequestBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if n := strings.TrimSpace(req.Number); n == "" || len(n) > 40 {
		fields["number"] = "must be 1 to 40 characters"
	}
	if n := strings.TrimSpace(req.Title); n == "" || len(n) > 200 {
		fields["title"] = "must be 1 to 200 characters"
	}
	if n := strings.TrimSpace(req.RequesterID); n == "" || len(n) > 200 {
		fields["requesterId"] = "must be 1 to 200 characters"
	}
	if !currency.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if len(req.Variants) == 0 || len(req.Variants) > maxCapexVariants {
		fields["variants"] = fmt.Sprintf("must contain 1 to %d variants", maxCapexVariants)
	}
	variants := make([]store.CapexVariant, 0, len(req.Variants))
	for i, v := range req.Variants {
		prefix := fmt.Sprintf("variants[%d].", i)
		if n := strings.TrimSpace(v.Description); n == "" || len(n) > 200 {
			fields[prefix+"description"] = "must be 1 to 200 characters"
		}
		if n := strings.TrimSpace(v.ProfileAssumption); n == "" || len(n) > 60 {
			fields[prefix+"profileAssumption"] = "must be 1 to 60 characters"
		}
		inService, err := store.ParseDate(v.InServiceDate)
		if err != nil {
			fields[prefix+"inServiceDate"] = "must be a YYYY-MM-DD date"
		}
		if len(v.AmountsByYear) == 0 || len(v.AmountsByYear) > 30 {
			fields[prefix+"amountsByYear"] = "must contain 1 to 30 years"
		}
		amounts := map[string]decimal.Decimal{}
		for year, raw := range v.AmountsByYear {
			if !yearKey.MatchString(year) {
				fields[prefix+"amountsByYear"] = "keys must be four-digit years"
				continue
			}
			d, msg := amountField(raw, req.Currency, true)
			if msg != "" {
				fields[prefix+"amountsByYear"] = "every amount " + msg
				continue
			}
			amounts[year] = d
		}
		variants = append(variants, store.CapexVariant{Position: i + 1, Description: strings.TrimSpace(v.Description),
			AmountsByYear: amounts, InServiceDate: inService, ProfileAssumption: strings.TrimSpace(v.ProfileAssumption)})
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	created, err := a.store.CreateCapexRequest(r.Context(), store.CapexRequest{LegalEntityID: req.LegalEntityID,
		Number: strings.TrimSpace(req.Number), Title: strings.TrimSpace(req.Title), RequesterID: strings.TrimSpace(req.RequesterID),
		Currency: req.Currency, Variants: variants}, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	a.capex.Add(1)
	setETag(w, created.Version)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listCapexRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	limit, after := pageLimit(r, fields)
	status := q.Get("status")
	if status != "" && !validCapexStatus[status] {
		fields["status"] = "is not a known capex request status"
	}
	legalEntity := q.Get("legalEntityId")
	if legalEntity != "" && !ids.Valid(legalEntity) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := a.store.ListCapexRequests(r.Context(), store.CapexFilter{LegalEntityID: legalEntity, Status: status, After: after, Limit: limit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func capexID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeCapexNotFound, "no capex request with id "+id))
		return "", false
	}
	return id, true
}

func (a *API) getCapexRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := capexID(w, r)
	if !ok {
		return
	}
	req, err := a.store.GetCapexRequest(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, req.Version)
	httpx.JSON(w, http.StatusOK, req)
}

// submitCapexRequest submits a Draft request with the variant to approve at the
// version the caller read (If-Match). The variant's total routes it to an
// approval level (BR-37.12, AC-37.11) and it waits InApproval for the wave-4
// workflow; a request in any other status is AST-0016.
func (a *API) submitCapexRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := capexID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req submitCapexBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.VariantID) {
		writeValidation(w, r, map[string]string{"variantId": "must be a variant UUID"})
		return
	}
	submitted, err := a.store.SubmitCapexRequest(r.Context(), id, store.SubmitInput{VariantID: req.VariantID, Version: version})
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	setETag(w, submitted.Version)
	httpx.JSON(w, http.StatusOK, submitted)
}
