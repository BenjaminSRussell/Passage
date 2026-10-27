package api

import (
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
)

// Slice-2 handlers: mixed costing (FR-39.14), component postings (FR-39.7,
// FR-39.15), sales order estimates (FR-39.16), run comparison (FR-39.10) and
// the standard cost reconciliation (FR-39.11). Like slice 1 they are stateless
// computations; the routes and their duties are in routes() in api.go.

// MaxAlternatives bounds the procurement alternatives of one mixed-cost item.
const MaxAlternatives = 50

// checkCurrency validates an optional ISO 4217 code and returns its posting scale.
func checkCurrency(code string, fields map[string]string) int32 {
	if code == "" {
		return domain.MoneyScale
	}
	if !currencyCode.MatchString(code) {
		fields["currency"] = "must be an ISO 4217 code such as USD"
		return domain.MoneyScale
	}
	return domain.PostingScale(code)
}

type mixedRequest struct {
	Items        []domain.Item      `json:"items"`
	ItemID       string             `json:"itemId"`
	Alternatives []domain.AltRef    `json:"alternatives"`
	Options      domain.RollOptions `json:"options"`
}

func (a *API) mixedCost(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req mixedRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	cat := catalog(req.Items, fields)
	opts := a.withDefaults(req.Options, fields)
	if req.ItemID == "" {
		fields["itemId"] = "is required"
	}
	switch {
	case len(req.Alternatives) == 0:
		fields["alternatives"] = "must contain at least one alternative"
	case len(req.Alternatives) > MaxAlternatives:
		fields["alternatives"] = fmt.Sprintf("must contain at most %d alternatives", MaxAlternatives)
	}
	sources := map[string]bool{}
	for i, alt := range req.Alternatives {
		at := func(key string) string { return fmt.Sprintf("alternatives[%d].%s", i, key) }
		switch {
		case alt.Source == "":
			fields[at("source")] = "is required"
		case sources[alt.Source]:
			fields[at("source")] = "must be unique"
		}
		sources[alt.Source] = true
		if alt.Percent.IsNegative() || alt.Percent.GreaterThan(decimalHundred) {
			fields[at("percent")] = "must be from 0 to 100"
		}
		switch {
		case alt.ItemID == "":
			fields[at("itemId")] = "is required"
		case alt.ItemID == req.ItemID:
			fields[at("itemId")] = "must not be the mixed item itself"
		default:
			if _, ok := cat[alt.ItemID]; cat != nil && !ok {
				fields[at("itemId")] = "must be one of the supplied items"
			}
		}
	}
	if !validation(w, r, fields) {
		return
	}
	mixed, err := domain.RollMixed(cat, req.ItemID, req.Alternatives, opts)
	if err != nil {
		// Options, items and ratio bounds were validated above, so only the
		// ratio total (BR-39.12) is left to fail.
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeMixingRatioTotal, err.Error()))
		return
	}
	a.calculations.Add(1)
	if mixed.Status == domain.StatusError {
		a.rollErrors.Add(1)
	}
	httpx.JSON(w, http.StatusOK, mixed)
}

// postingResponse is the balanced journal preview of a component posting.
type postingResponse struct {
	Currency string               `json:"currency,omitempty"`
	Total    decimal.Decimal      `json:"total"`
	Journal  []domain.JournalLine `json:"journal"`
}

type cogsRequest struct {
	Currency string          `json:"currency"`
	Quantity decimal.Decimal `json:"quantity"`
	UnitCost domain.Split    `json:"unitCost"`
}

type priceDifferenceRequest struct {
	Currency        string          `json:"currency"`
	Quantity        decimal.Decimal `json:"quantity"`
	ReceiptUnitCost decimal.Decimal `json:"receiptUnitCost"`
	Standard        domain.Split    `json:"standard"`
}

// checkSplit requires at least one component, named, with no negative amount.
func checkSplit(s domain.Split, field string, fields map[string]string) {
	if len(s) == 0 {
		fields[field] = "must contain at least one component"
	}
	for c, v := range s {
		if c == "" {
			fields[field] = "component codes must not be empty"
		}
		if v.IsNegative() {
			fields[field+"."+c] = "must not be negative"
		}
	}
}

func (a *API) cogsPosting(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req cogsRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	scale := checkCurrency(req.Currency, fields)
	if !req.Quantity.IsPositive() {
		fields["quantity"] = "must be positive"
	}
	checkSplit(req.UnitCost, "unitCost", fields)
	if !validation(w, r, fields) {
		return
	}
	journal, total := domain.CostOfSalesLines(req.Quantity, req.UnitCost, scale)
	httpx.JSON(w, http.StatusOK, postingResponse{Currency: req.Currency, Total: total, Journal: journal})
}

func (a *API) priceDifferencePosting(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req priceDifferenceRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	scale := checkCurrency(req.Currency, fields)
	if !req.Quantity.IsPositive() {
		fields["quantity"] = "must be positive"
	}
	if req.ReceiptUnitCost.IsNegative() {
		fields["receiptUnitCost"] = "must not be negative"
	}
	checkSplit(req.Standard, "standard", fields)
	if !validation(w, r, fields) {
		return
	}
	journal, total := domain.PriceDifferenceLines(req.Quantity, req.ReceiptUnitCost, req.Standard, scale)
	httpx.JSON(w, http.StatusOK, postingResponse{Currency: req.Currency, Total: total, Journal: journal})
}

type salesRequest struct {
	Currency  string             `json:"currency"`
	Items     []domain.Item      `json:"items"`
	ItemID    string             `json:"itemId"`
	Quantity  decimal.Decimal    `json:"quantity"`
	NetAmount decimal.Decimal    `json:"netAmount"`
	Options   domain.RollOptions `json:"options"`
}

func (a *API) salesOrderEstimate(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req salesRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	scale := checkCurrency(req.Currency, fields)
	cat := catalog(req.Items, fields)
	opts := a.withDefaults(req.Options, fields)
	if req.ItemID == "" {
		fields["itemId"] = "is required"
	} else if _, ok := cat[req.ItemID]; cat != nil && !ok {
		fields["itemId"] = "must be one of the supplied items"
	}
	if !req.Quantity.IsPositive() {
		fields["quantity"] = "must be positive"
	}
	if !req.NetAmount.IsPositive() {
		fields["netAmount"] = "must be positive to compute margin"
	}
	if !validation(w, r, fields) {
		return
	}
	est, err := domain.SalesOrderEstimate(cat, req.ItemID, req.Quantity, req.NetAmount, scale, opts)
	if err != nil {
		// The checks above cover every domain refusal; keep the field error shape.
		validation(w, r, map[string]string{"itemId": err.Error()})
		return
	}
	a.calculations.Add(1)
	if est.Status == domain.StatusError {
		a.rollErrors.Add(1)
	}
	httpx.JSON(w, http.StatusOK, est)
}

// sideRequest is one run of a comparison: its items and roll options.
type sideRequest struct {
	Items   []domain.Item      `json:"items"`
	Options domain.RollOptions `json:"options"`
}

type compareRequest struct {
	Baseline  sideRequest `json:"baseline"`
	Candidate sideRequest `json:"candidate"`
}

func (a *API) compare(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req compareRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	type side struct {
		cat  domain.Catalog
		opts domain.RollOptions
	}
	sides := map[string]side{}
	for name, sr := range map[string]sideRequest{"baseline": req.Baseline, "candidate": req.Candidate} {
		sub := map[string]string{}
		cat := catalog(sr.Items, sub)
		opts := a.withDefaults(sr.Options, sub)
		for k, v := range sub {
			fields[name+"."+k] = v
		}
		sides[name] = side{cat, opts}
	}
	if len(req.Baseline.Items)+len(req.Candidate.Items) > MaxItems {
		fields["candidate.items"] = fmt.Sprintf("baseline and candidate together must contain at most %d items", MaxItems)
	}
	if !validation(w, r, fields) {
		return
	}
	runs := map[string]domain.RollRun{}
	for name, s := range sides {
		run := domain.MassRoll(s.cat, s.opts)
		a.massRolls.Add(1)
		a.itemsRolled.Add(int64(run.ItemCount))
		a.rollErrors.Add(int64(run.ErrorCount))
		runs[name] = run
	}
	httpx.JSON(w, http.StatusOK, domain.Compare(runs["baseline"], runs["candidate"]))
}

type reconcileRequest struct {
	Currency   string                  `json:"currency"`
	OnHand     []domain.StandardOnHand `json:"onHand"`
	GLBalances []domain.SiteBalance    `json:"glBalances"`
}

type reconcileResponse struct {
	Currency    string                      `json:"currency,omitempty"`
	Sites       []domain.SiteReconciliation `json:"sites"`
	Differences int                         `json:"differences"`
}

func (a *API) reconcile(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req reconcileRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	scale := checkCurrency(req.Currency, fields)
	switch {
	case len(req.OnHand) == 0 && len(req.GLBalances) == 0:
		fields["onHand"] = "on-hand lines or GL balances are required"
	case len(req.OnHand) > MaxRevalLines:
		fields["onHand"] = fmt.Sprintf("must contain at most %d lines", MaxRevalLines)
	}
	if len(req.GLBalances) > MaxRevalLines {
		fields["glBalances"] = fmt.Sprintf("must contain at most %d balances", MaxRevalLines)
	}
	for i, l := range req.OnHand {
		at := func(key string) string { return fmt.Sprintf("onHand[%d].%s", i, key) }
		if l.SiteID == "" {
			fields[at("siteId")] = "is required"
		}
		if l.ItemID == "" {
			fields[at("itemId")] = "is required"
		}
		if l.StdUnit.IsNegative() {
			fields[at("stdUnit")] = "must not be negative"
		}
	}
	for i, b := range req.GLBalances {
		if b.SiteID == "" {
			fields[fmt.Sprintf("glBalances[%d].siteId", i)] = "is required"
		}
	}
	if !validation(w, r, fields) {
		return
	}
	sites := domain.ReconcileStandard(req.OnHand, req.GLBalances, scale)
	diffs := 0
	for _, s := range sites {
		if s.Status != domain.ReconReconciled {
			diffs++
		}
	}
	a.reconciliationDifferences.Add(int64(diffs))
	httpx.JSON(w, http.StatusOK, reconcileResponse{Currency: req.Currency, Sites: sites, Differences: diffs})
}
