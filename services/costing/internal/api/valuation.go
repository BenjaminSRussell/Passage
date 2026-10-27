package api

import (
	"fmt"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"

	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

const (
	// maxLedgers bounds the valuation ledgers of one legal entity (FR-20.15).
	maxLedgers = 8
	// maxBucketBounds bounds a custom aging bucket set (FR-20.17).
	maxBucketBounds = 12
)

// ---- parallel valuation ledgers (FR-20.15) ----

type ledgerSpecDTO struct {
	ID               string  `json:"id"`
	Method           string  `json:"method"`
	Currency         string  `json:"currency"`
	Rate             string  `json:"rate"`
	Primary          bool    `json:"primary"`
	StandardUnitCost *string `json:"standardUnitCost"`
	Fallback         string  `json:"fallback"`
}

type ledgersPreviewRequest struct {
	Ledgers      []ledgerSpecDTO  `json:"ledgers"`
	Transactions []transactionDTO `json:"transactions"`
}

type ledgerResultDTO struct {
	ID             string            `json:"id"`
	Currency       string            `json:"currency"`
	Primary        bool              `json:"primary"`
	IssueCosts     map[string]string `json:"issueCosts"`
	RemainingQty   string            `json:"remainingQty"`
	RemainingValue string            `json:"remainingValue"`
	Settlements    int               `json:"settlements"`
	Exceptions     int               `json:"exceptions"`
}

type ledgersPreviewResponse struct {
	Ledgers []ledgerResultDTO `json:"ledgers"`
}

// previewValuationLedgers settles the same transactions under each valuation
// ledger. Nothing is posted: the response is the per-ledger preview (BR-20.13).
func (a *API) previewValuationLedgers(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req ledgersPreviewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(req.Ledgers) == 0 || len(req.Ledgers) > maxLedgers {
		fields["ledgers"] = fmt.Sprintf("must hold 1 to %d valuation ledgers", maxLedgers)
	}
	if len(req.Transactions) == 0 || len(req.Transactions) > maxTxns {
		fields["transactions"] = fmt.Sprintf("must hold 1 to %d transactions", maxTxns)
	}
	specs := make([]domain.LedgerSpec, 0, len(req.Ledgers))
	for i, l := range req.Ledgers {
		key := fmt.Sprintf("ledgers[%d]", i)
		if !currencyPattern.MatchString(l.Currency) {
			fields[key+".currency"] = "must be an ISO 4217 alpha-3 code"
		}
		if !domain.Method(l.Method).Valid() {
			fields[key+".method"] = "must be a known costing method"
		}
		if !fallbacks[l.Fallback] {
			fields[key+".fallback"] = "must be RUNNING_AVG, STANDARD or LAST_RECEIPT"
		}
		rate := parseDecimal(fields, key+".rate", l.Rate, false)
		if _, bad := fields[key+".rate"]; !bad && !rate.IsPositive() {
			fields[key+".rate"] = "must be greater than zero"
		}
		spec := domain.LedgerSpec{ID: l.ID, Method: domain.Method(l.Method), Currency: l.Currency,
			Scale: money.MinorUnits(l.Currency), Rate: rate, Primary: l.Primary, Fallback: domain.Fallback(l.Fallback)}
		if l.StandardUnitCost != nil {
			spec.Standard = parseDecimal(fields, key+".standardUnitCost", *l.StandardUnitCost, true)
		}
		specs = append(specs, spec)
	}
	txns := parseTransactions(fields, req.Transactions)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	results, err := domain.SettleLedgers(txns, specs)
	if err != nil {
		a.fail(w, r, "settle valuation ledgers", err)
		return
	}
	a.previews.Add(1)
	out := ledgersPreviewResponse{Ledgers: make([]ledgerResultDTO, 0, len(results))}
	for _, lr := range results {
		scale := money.MinorUnits(lr.Currency)
		dto := ledgerResultDTO{ID: lr.ID, Currency: lr.Currency, Primary: lr.Primary, IssueCosts: map[string]string{},
			RemainingQty: lr.Result.RemainingQty.String(), RemainingValue: lr.Result.RemainingValue.StringFixed(scale),
			Settlements: len(lr.Result.Settlements), Exceptions: len(lr.Result.Exceptions)}
		for id, c := range lr.Result.IssueCosts {
			dto.IssueCosts[id] = c.StringFixed(scale)
		}
		out.Ledgers = append(out.Ledgers, dto)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- inventory aging (FR-20.17) ----

type agingPreviewRequest struct {
	Method               string           `json:"method"`
	Currency             string           `json:"currency"`
	Fallback             string           `json:"fallback"`
	StandardUnitCost     *string          `json:"standardUnitCost"`
	DefaultValuationType string           `json:"defaultValuationType,omitempty"`
	AsOf                 string           `json:"asOf"`
	BucketBounds         []int            `json:"bucketBounds"`
	Transactions         []transactionDTO `json:"transactions"`
}

type agingBucketDTO struct {
	Label string `json:"label"`
	Qty   string `json:"qty"`
	Value string `json:"value"`
}

// agingTypeDTO is the aging of one valuation type (FR-20.16, FR-20.17).
type agingTypeDTO struct {
	ValuationType  string           `json:"valuationType"`
	Buckets        []agingBucketDTO `json:"buckets"`
	TotalQty       string           `json:"totalQty"`
	TotalValue     string           `json:"totalValue"`
	RemainingValue string           `json:"remainingValue"`
}

type agingPreviewResponse struct {
	Method         string           `json:"method"`
	Currency       string           `json:"currency"`
	AsOf           string           `json:"asOf"`
	Buckets        []agingBucketDTO `json:"buckets"`
	TotalQty       string           `json:"totalQty"`
	TotalValue     string           `json:"totalValue"`
	RemainingValue string           `json:"remainingValue"`
	// TiesToRemaining is true when the bucket total equals the settled remaining value.
	TiesToRemaining bool `json:"tiesToRemaining"`
	// ValuationTypes is set when stock is split by valuation type.
	ValuationTypes []agingTypeDTO `json:"valuationTypes,omitempty"`
}

func agingBuckets(rep domain.AgingReport, scale int32) []agingBucketDTO {
	out := make([]agingBucketDTO, 0, len(rep.Buckets))
	for _, b := range rep.Buckets {
		out = append(out, agingBucketDTO{Label: b.Label, Qty: b.Qty.String(), Value: b.Value.StringFixed(scale)})
	}
	return out
}

// previewAging buckets the remaining cost of a product as of a date, per
// valuation type when the product is split. Bucket bounds default to
// 30/90/180/365 days. Transactions dated after asOf are refused.
func (a *API) previewAging(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req agingPreviewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !currencyPattern.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !domain.Method(req.Method).Valid() {
		fields["method"] = "must be a known costing method"
	}
	if !fallbacks[req.Fallback] {
		fields["fallback"] = "must be RUNNING_AVG, STANDARD or LAST_RECEIPT"
	}
	if len(req.DefaultValuationType) > maxValuationType {
		fields["defaultValuationType"] = fmt.Sprintf("must be at most %d characters", maxValuationType)
	}
	buckets := domain.DefaultBuckets()
	if len(req.BucketBounds) > maxBucketBounds {
		fields["bucketBounds"] = fmt.Sprintf("must hold at most %d bounds", maxBucketBounds)
	} else if len(req.BucketBounds) > 0 {
		b, err := domain.BucketsFromBounds(req.BucketBounds)
		if err != nil {
			fields["bucketBounds"] = "must be non-negative whole days in strictly ascending order"
		}
		buckets = b
	}
	asOf := parseDate(fields, "asOf", req.AsOf)
	if len(req.Transactions) == 0 || len(req.Transactions) > maxTxns {
		fields["transactions"] = fmt.Sprintf("must hold 1 to %d transactions", maxTxns)
	}
	opt := domain.Options{Method: domain.Method(req.Method), Scale: money.MinorUnits(req.Currency), Fallback: domain.Fallback(req.Fallback)}
	if req.StandardUnitCost != nil {
		opt.Standard = parseDecimal(fields, "standardUnitCost", *req.StandardUnitCost, true)
	}
	txns := parseTransactions(fields, req.Transactions)
	for i, t := range txns {
		if !asOf.IsZero() && t.FinancialDate.After(asOf) {
			fields[fmt.Sprintf("transactions[%d].financialDate", i)] = "is after asOf"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	total, perType, remaining, err := domain.AgingByValuationType(txns, opt, req.DefaultValuationType, asOf, buckets)
	if err != nil {
		a.fail(w, r, "aging", err)
		return
	}
	a.previews.Add(1)
	scale := opt.Scale
	out := agingPreviewResponse{Method: req.Method, Currency: req.Currency, AsOf: req.AsOf,
		Buckets: agingBuckets(total, scale), TotalQty: total.TotalQty.String(),
		TotalValue: total.TotalValue.StringFixed(scale), RemainingValue: remaining.StringFixed(scale),
		TiesToRemaining: total.TotalValue.Equal(remaining)}
	for _, ta := range perType {
		out.ValuationTypes = append(out.ValuationTypes, agingTypeDTO{ValuationType: ta.ValuationType,
			Buckets: agingBuckets(ta.Report, scale), TotalQty: ta.Report.TotalQty.String(),
			TotalValue: ta.Report.TotalValue.StringFixed(scale), RemainingValue: ta.RemainingValue.StringFixed(scale)})
	}
	httpx.JSON(w, http.StatusOK, out)
}
