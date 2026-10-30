package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

const (
	// dutyCostTxnPost is the producers' service duty (wave 16 postings, the
	// close, the PPV hook, activation), like costing.ppv.post.
	dutyCostTxnPost = "costing.cost-transaction.post"
	// maxGLBalances bounds one tie-out request.
	maxGLBalances = 10000
	// defaultGroupBy is the report grouping when none is requested.
	defaultGroupBy = "site"
	// Column bounds of cost_transaction: qty numeric(28,6), amount_acct numeric(19,4).
	qtyScale, qtyDigits       = 6, 22
	amountScale, amountDigits = 4, 15
)

// ---- cost transactions (FR-20.4) ----

// costTransactionRequest is one value-affecting event. sourceKey is the
// producer's own key for the event (a voucher line, a settlement row), so a
// repeated feed is a replay. valuationLedgerId empty is the primary valuation.
type costTransactionRequest struct {
	SourceKey              string `json:"sourceKey"`
	LegalEntityID          string `json:"legalEntityId"`
	InventoryTransactionID string `json:"inventoryTransactionId"`
	LedgerEntryID          string `json:"ledgerEntryId"`
	ProductID              string `json:"productId"`
	SiteID                 string `json:"siteId"`
	WarehouseID            string `json:"warehouseId"`
	ValuationType          string `json:"valuationType"`
	ValuationLedgerID      string `json:"valuationLedgerId"`
	Kind                   string `json:"kind"`
	Qty                    string `json:"qty"`
	Amount                 string `json:"amount"`
	Currency               string `json:"currency"`
	FinancialDate          string `json:"financialDate"`
}

type recordCostTransactionsRequest struct {
	Items []costTransactionRequest `json:"items"`
}

type recordCostTransactionsResponse struct {
	Received int `json:"received"`
	Inserted int `json:"inserted"`
}

// boundedDecimal parses a signed decimal that fits numeric(digits+scale, scale)
// without rounding: the store never silently drops a fraction of a cent.
func boundedDecimal(fields map[string]string, field, raw string, scale int32, digits int) decimal.Decimal {
	d := parseDecimal(fields, field, raw, false)
	if _, bad := fields[field]; bad {
		return d
	}
	if !d.Equal(d.Truncate(scale)) || d.Abs().GreaterThanOrEqual(decimal.New(1, int32(digits))) {
		fields[field] = fmt.Sprintf("must have at most %d integer digits and %d decimal places", digits, scale)
	}
	return d
}

func optionalUUID(fields map[string]string, field, v string) string {
	if v != "" && !ids.Valid(v) {
		fields[field] = "must be a UUID"
	}
	return strings.ToLower(v)
}

// validateCostTransaction checks one item and returns its store form.
// Quantities and amounts may be negative (issues, returns, reversals);
// value-only kinds carry quantity 0.
func validateCostTransaction(fields map[string]string, prefix string, c costTransactionRequest) store.CostTransaction {
	field := func(name string) string { return prefix + "." + name }
	if n := len(c.SourceKey); n < 1 || n > 200 {
		fields[field("sourceKey")] = "must be 1 to 200 characters"
	}
	for name, v := range map[string]string{"legalEntityId": c.LegalEntityID, "productId": c.ProductID, "siteId": c.SiteID} {
		if !ids.Valid(v) {
			fields[field(name)] = "must be a UUID"
		}
	}
	out := store.CostTransaction{SourceKey: c.SourceKey, LegalEntityID: strings.ToLower(c.LegalEntityID),
		ProductID: strings.ToLower(c.ProductID), SiteID: strings.ToLower(c.SiteID), ValuationType: c.ValuationType,
		Kind: c.Kind, Currency: c.Currency, FinancialDate: c.FinancialDate}
	out.InventoryTransactionID = optionalUUID(fields, field("inventoryTransactionId"), c.InventoryTransactionID)
	out.LedgerEntryID = optionalUUID(fields, field("ledgerEntryId"), c.LedgerEntryID)
	out.WarehouseID = optionalUUID(fields, field("warehouseId"), c.WarehouseID)
	out.ValuationLedgerID = optionalUUID(fields, field("valuationLedgerId"), c.ValuationLedgerID)
	if len(c.ValuationType) > maxValuationType {
		fields[field("valuationType")] = fmt.Sprintf("must be at most %d characters", maxValuationType)
	}
	if !domain.ValidCostKind(c.Kind) {
		fields[field("kind")] = "must be PHYSICAL, FINANCIAL, INVOICE_DIFF, SETTLEMENT_ADJ, PPV, REVALUATION, MANUAL_ADJ or RECALC"
	}
	if !currencyPattern.MatchString(c.Currency) {
		fields[field("currency")] = "must be an ISO 4217 alpha-3 code"
	}
	qty := boundedDecimal(fields, field("qty"), c.Qty, qtyScale, qtyDigits)
	amount := boundedDecimal(fields, field("amount"), c.Amount, amountScale, amountDigits)
	if _, bad := fields[field("qty")]; !bad && domain.ValidCostKind(c.Kind) && !domain.CarriesQuantity(c.Kind) && !qty.IsZero() {
		fields[field("qty")] = "must be 0 for a value-only kind"
	}
	out.Qty, out.Amount = qty.String(), amount.String()
	parseDate(fields, field("financialDate"), c.FinancialDate)
	return out
}

// recordCostTransactions appends a batch of cost transactions (all or nothing).
// It is the producers' entry point: wave 16 postings, the close and the PPV hook.
func (a *API) recordCostTransactions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req recordCostTransactionsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(req.Items) == 0 || len(req.Items) > store.MaxCostTransactionBatch {
		fields["items"] = fmt.Sprintf("must hold 1 to %d cost transactions", store.MaxCostTransactionBatch)
	}
	rows := make([]store.CostTransaction, 0, len(req.Items))
	seen := map[string]bool{}
	for i, it := range req.Items {
		key := fmt.Sprintf("items[%d]", i)
		rows = append(rows, validateCostTransaction(fields, key, it))
		if seen[it.SourceKey] {
			fields[key+".sourceKey"] = "repeats an earlier item of this batch"
		}
		seen[it.SourceKey] = true
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	for _, c := range rows {
		if !tenant.InLegalEntityScope(p, c.LegalEntityID) {
			httpx.Fail(w, r, outOfScope())
			return
		}
	}
	inserted, err := a.store.RecordCostTransactions(r.Context(), rows)
	if err != nil {
		a.fail(w, r, "record cost transactions", err)
		return
	}
	a.costTxnsRecorded.Add(int64(inserted))
	httpx.JSON(w, http.StatusCreated, recordCostTransactionsResponse{Received: len(rows), Inserted: inserted})
}

func outOfScope() error {
	return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside the caller's scope")
}

// ledgerSelection is the resolved valuation ledger of a read: queryID is ""
// for the primary valuation (where the primary ledger's rows are stored).
type ledgerSelection struct {
	requestedID, queryID, code, currency string
}

// selectLedger resolves an optional valuation ledger id of legalEntityID. No id
// is the primary valuation, in the primary ledger's currency when one exists.
func (a *API) selectLedger(r *http.Request, legalEntityID, ledgerID string) (ledgerSelection, error) {
	if ledgerID == "" {
		ledgers, err := a.store.ListValuationLedgers(r.Context(), legalEntityID)
		if err != nil {
			return ledgerSelection{}, err
		}
		for _, l := range ledgers {
			if l.Primary {
				return ledgerSelection{code: l.Code, currency: l.Currency}, nil
			}
		}
		return ledgerSelection{}, nil
	}
	l, err := a.store.GetValuationLedger(r.Context(), ledgerID)
	if err != nil {
		return ledgerSelection{}, err
	}
	if !strings.EqualFold(l.LegalEntityID, legalEntityID) {
		return ledgerSelection{}, httpx.NewProblem(http.StatusNotFound, store.CodeLedgerNotFound, "the legal entity has no such valuation ledger")
	}
	sel := ledgerSelection{requestedID: l.ID, queryID: l.ID, code: l.Code, currency: l.Currency}
	if l.Primary {
		sel.queryID = ""
	}
	return sel, nil
}

type costTxnListResponse struct {
	Items      []store.CostTransaction `json:"items"`
	NextCursor string                  `json:"nextCursor,omitempty"`
}

// listCostTransactions is the value report's drill-down: the cost transactions
// of one legal entity and valuation ledger, newest financial date first.
func (a *API) listCostTransactions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, fields := pageParams(r, false)
	if date, _, _ := strings.Cut(after, "|"); after != "" {
		parseDate(fields, "cursor", date)
	}
	q := r.URL.Query()
	f := store.CostTxnFilter{LegalEntityID: strings.ToLower(q.Get("legalEntityId")), Kind: q.Get("kind"), AsOf: q.Get("asOf")}
	if !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	f.ProductID = optionalUUID(fields, "productId", q.Get("productId"))
	f.SiteID = optionalUUID(fields, "siteId", q.Get("siteId"))
	ledgerID := optionalUUID(fields, "valuationLedgerId", q.Get("valuationLedgerId"))
	if f.Kind != "" && !domain.ValidCostKind(f.Kind) {
		fields["kind"] = "must be a cost transaction kind"
	}
	if f.AsOf != "" {
		parseDate(fields, "asOf", f.AsOf)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, f.LegalEntityID) {
		httpx.Fail(w, r, outOfScope())
		return
	}
	sel, err := a.selectLedger(r, f.LegalEntityID, ledgerID)
	if err != nil {
		a.fail(w, r, "list cost transactions", err)
		return
	}
	f.ValuationLedgerID = sel.queryID
	page, err := a.store.ListCostTransactions(r.Context(), f, after, limit)
	if err != nil {
		a.fail(w, r, "list cost transactions", err)
		return
	}
	httpx.JSON(w, http.StatusOK, costTxnListResponse{Items: page.Items, NextCursor: cursor(page.Next)})
}

// ---- inventory value report (FR-20.4, FR-20.15, FR-20.16) and tie-out ----

// reportParams is the selector shared by the value report and its tie-out.
type reportParams struct {
	LegalEntityID     string   `json:"legalEntityId"`
	AsOf              string   `json:"asOf"`
	ValuationLedgerID string   `json:"valuationLedgerId"`
	SiteID            string   `json:"siteId"`
	GroupBy           []string `json:"groupBy"`
}

// validate checks the selector, normalises ids and defaults the grouping to
// site. A nil GroupBy is the default; an empty one is a single total row.
func (p *reportParams) validate(fields map[string]string) {
	p.LegalEntityID = strings.ToLower(p.LegalEntityID)
	if !ids.Valid(p.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	parseDate(fields, "asOf", p.AsOf)
	p.ValuationLedgerID = optionalUUID(fields, "valuationLedgerId", p.ValuationLedgerID)
	p.SiteID = optionalUUID(fields, "siteId", p.SiteID)
	if p.GroupBy == nil {
		p.GroupBy = []string{defaultGroupBy}
	}
}

// splitGroupBy turns "site,valuationType" into a list; "" is no grouping.
func splitGroupBy(raw string) []string {
	out := []string{}
	for _, g := range strings.Split(raw, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// loadReport resolves the ledger, sums the stored cost transactions and groups
// them. The report currency is the ledger's; balances in another currency
// (rows recorded before the primary ledger existed) are refused, not mixed.
func (a *API) loadReport(r *http.Request, p reportParams) (domain.ValueReport, ledgerSelection, error) {
	if err := domain.ValidateGroupBy(p.GroupBy); err != nil {
		return domain.ValueReport{}, ledgerSelection{}, err
	}
	if pr, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(pr, p.LegalEntityID) {
		return domain.ValueReport{}, ledgerSelection{}, outOfScope()
	}
	sel, err := a.selectLedger(r, p.LegalEntityID, p.ValuationLedgerID)
	if err != nil {
		return domain.ValueReport{}, ledgerSelection{}, err
	}
	bal, err := a.store.ValueBalances(r.Context(), store.BalanceQuery{LegalEntityID: p.LegalEntityID,
		ValuationLedgerID: sel.queryID, AsOf: p.AsOf, SiteID: p.SiteID})
	if err != nil {
		return domain.ValueReport{}, ledgerSelection{}, err
	}
	rep, err := domain.BuildValueReport(bal, p.GroupBy)
	if err != nil {
		return domain.ValueReport{}, ledgerSelection{}, err
	}
	switch {
	case rep.Currency == "":
		rep.Currency = sel.currency
	case sel.currency != "" && rep.Currency != sel.currency:
		return domain.ValueReport{}, ledgerSelection{}, &domain.Error{Code: domain.CodeReportCurrency,
			Detail: fmt.Sprintf("the valuation ledger values in %s but holds cost transactions in %s", sel.currency, rep.Currency)}
	}
	return rep, sel, nil
}

// formatAmount prints an amount at the currency's minor unit, or in full when
// it carries more precision, so a sub-cent difference is never hidden.
func formatAmount(d decimal.Decimal, scale int32) string {
	if d.Equal(d.Round(scale)) {
		return d.StringFixed(scale)
	}
	return d.String()
}

type amountsDTO struct {
	Qty            string `json:"qty"`
	PhysicalValue  string `json:"physicalValue"`
	FinancialValue string `json:"financialValue"`
	Value          string `json:"value"`
}

func amounts(a domain.Amounts, scale int32) amountsDTO {
	return amountsDTO{Qty: a.Qty.String(), PhysicalValue: formatAmount(a.PhysicalValue, scale),
		FinancialValue: formatAmount(a.FinancialValue, scale), Value: formatAmount(a.Value(), scale)}
}

type reportRowDTO struct {
	Key map[string]string `json:"key"`
	amountsDTO
}

type reportHeader struct {
	LegalEntityID       string   `json:"legalEntityId"`
	AsOf                string   `json:"asOf"`
	ValuationLedgerID   string   `json:"valuationLedgerId,omitempty"`
	ValuationLedgerCode string   `json:"valuationLedgerCode,omitempty"`
	SiteID              string   `json:"siteId,omitempty"`
	Currency            string   `json:"currency"`
	GroupBy             []string `json:"groupBy"`
}

type valueReportResponse struct {
	reportHeader
	Rows  []reportRowDTO `json:"rows"`
	Total amountsDTO     `json:"total"`
}

func header(p reportParams, sel ledgerSelection, rep domain.ValueReport) reportHeader {
	return reportHeader{LegalEntityID: p.LegalEntityID, AsOf: p.AsOf, ValuationLedgerID: sel.requestedID,
		ValuationLedgerCode: sel.code, SiteID: p.SiteID, Currency: rep.Currency, GroupBy: rep.GroupBy}
}

// getValueReport serves the inventory value by dimension at a date: quantity,
// physical (not yet invoiced) and financial value, and their sum, which the
// tie-out compares with the ledger.
func (a *API) getValueReport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	p := reportParams{LegalEntityID: q.Get("legalEntityId"), AsOf: q.Get("asOf"),
		ValuationLedgerID: q.Get("valuationLedgerId"), SiteID: q.Get("siteId")}
	if _, ok := q["groupBy"]; ok {
		p.GroupBy = splitGroupBy(q.Get("groupBy"))
	}
	fields := map[string]string{}
	p.validate(fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rep, sel, err := a.loadReport(r, p)
	if err != nil {
		a.fail(w, r, "value report", err)
		return
	}
	scale := money.MinorUnits(rep.Currency)
	out := valueReportResponse{reportHeader: header(p, sel, rep), Rows: []reportRowDTO{}, Total: amounts(rep.Total, scale)}
	for _, row := range rep.Rows {
		out.Rows = append(out.Rows, reportRowDTO{Key: row.Key, amountsDTO: amounts(row.Amounts, scale)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

type glBalanceRequest struct {
	Dimensions map[string]string `json:"dimensions"`
	Balance    string            `json:"balance"`
}

type tieOutRequest struct {
	reportParams
	GLBalances []glBalanceRequest `json:"glBalances"`
}

type tieRowDTO struct {
	Key       map[string]string `json:"key"`
	Inventory string            `json:"inventory"`
	Ledger    string            `json:"ledger"`
	Diff      string            `json:"diff"`
}

type tieOutResponse struct {
	reportHeader
	Rows        []tieRowDTO `json:"rows"`
	Differences int         `json:"differences"`
	TotalDiff   string      `json:"totalDiff"`
	Ties        bool        `json:"ties"`
}

// parseTieOut validates a tie-out request and returns its ledger balances.
func parseTieOut(fields map[string]string, req *tieOutRequest) []domain.GLBalance {
	req.validate(fields)
	if len(req.GLBalances) == 0 || len(req.GLBalances) > maxGLBalances {
		fields["glBalances"] = fmt.Sprintf("must hold 1 to %d balances", maxGLBalances)
	}
	gl := make([]domain.GLBalance, 0, len(req.GLBalances))
	for i, g := range req.GLBalances {
		key := fmt.Sprintf("glBalances[%d]", i)
		amt := boundedDecimal(fields, key+".balance", g.Balance, amountScale, amountDigits)
		dims := make(map[string]string, len(g.Dimensions))
		for k, v := range g.Dimensions {
			if k != "valuationType" {
				v = strings.ToLower(v) // ids compare case-insensitively; valuation types are codes
			}
			dims[k] = v
		}
		gl = append(gl, domain.GLBalance{Dims: dims, Balance: amt})
	}
	return gl
}

// tieOut loads the value report and compares it with the ledger balances.
func (a *API) tieOut(r *http.Request, p reportParams, gl []domain.GLBalance) (tieOutResponse, error) {
	rep, sel, err := a.loadReport(r, p)
	if err != nil {
		return tieOutResponse{}, err
	}
	res, err := domain.TieOutReport(rep, gl)
	if err != nil {
		return tieOutResponse{}, err
	}
	scale := money.MinorUnits(rep.Currency)
	out := tieOutResponse{reportHeader: header(p, sel, rep), Rows: []tieRowDTO{}, Differences: res.Differences,
		TotalDiff: formatAmount(res.TotalDiff, scale), Ties: res.Ties()}
	for _, row := range res.Rows {
		out.Rows = append(out.Rows, tieRowDTO{Key: row.Key, Inventory: formatAmount(row.Inventory, scale),
			Ledger: formatAmount(row.Ledger, scale), Diff: formatAmount(row.Diff, scale)})
	}
	return out, nil
}

// tieOutValueReport compares the value report with the ledger balances of the
// inventory accounts, grouped the same way (FR-20.4). It only reads: the
// differences go back to the caller. POST /v1/costing/reconciliations runs the
// same tie-out, stores it and emits reconciliation.diff per differing group.
func (a *API) tieOutValueReport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req tieOutRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	gl := parseTieOut(fields, &req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.tieOut(r, req.reportParams, gl)
	if err != nil {
		a.fail(w, r, "value report tie-out", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
