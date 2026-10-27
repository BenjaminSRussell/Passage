package api

import (
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	salesGroupsPath     = "/v1/pricing/commission/sales-groups"
	commissionRulesPath = "/v1/pricing/commission/rules"
	commissionPostPath  = "/v1/pricing/commission/postings"
	commissionTxnsPath  = "/v1/pricing/commission/transactions"
	// Duties CommissionMaintain and CommissionView (PRD §5.26.17).
	dutyCommissionMaintain = "pricing.commission.maintain"
	dutyCommissionView     = "pricing.commission.view"
)

var monthKey = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

type repShareDTO struct {
	RepID    string `json:"repId"`
	SharePct string `json:"sharePct"`
}

type salesGroupRequest struct {
	LegalEntityID string        `json:"legalEntityId"`
	Name          string        `json:"name"`
	Reps          []repShareDTO `json:"reps"`
}

type salesGroupView struct {
	ID            string        `json:"id"`
	LegalEntityID string        `json:"legalEntityId"`
	Name          string        `json:"name"`
	Reps          []repShareDTO `json:"reps"`
}

type commissionRuleRequest struct {
	SalesGroupID  string `json:"salesGroupId"`
	CustomerGroup string `json:"customerGroup"`
	ItemGroup     string `json:"itemGroup"`
	Basis         string `json:"basis"`
	RatePct       string `json:"ratePct"`
}

type commissionRuleView struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	SalesGroupID  string `json:"salesGroupId"`
	CustomerGroup string `json:"customerGroup"`
	ItemGroup     string `json:"itemGroup"`
	Basis         string `json:"basis"`
	RatePct       string `json:"ratePct"`
}

// commissionPostingRequest is one posted invoice line handed to pricing by the invoice.posted
// consumer (w29/w30); until that event exists the endpoint stands in for it.
type commissionPostingRequest struct {
	LegalEntityID         string  `json:"legalEntityId"`
	SalesGroupID          string  `json:"salesGroupId"`
	InvoiceLineID         string  `json:"invoiceLineId"`
	ReversesInvoiceLineID string  `json:"reversesInvoiceLineId"`
	InvoiceDate           string  `json:"invoiceDate"`
	CustomerID            string  `json:"customerId"`
	CustomerGroup         string  `json:"customerGroup"`
	ItemGroup             string  `json:"itemGroup"`
	Currency              string  `json:"currency"`
	LineNet               string  `json:"lineNet"`
	CostSnapshot          *string `json:"costSnapshot"`
}

type commissionTxnView struct {
	ID                    string `json:"id"`
	LegalEntityID         string `json:"legalEntityId"`
	InvoiceLineID         string `json:"invoiceLineId"`
	ReversesInvoiceLineID string `json:"reversesInvoiceLineId,omitempty"`
	InvoiceDate           string `json:"invoiceDate"`
	PeriodID              string `json:"periodId"`
	CustomerID            string `json:"customerId,omitempty"`
	CustomerGroup         string `json:"customerGroup"`
	ItemGroup             string `json:"itemGroup"`
	SalesGroupID          string `json:"salesGroupId"`
	RepID                 string `json:"repId"`
	SharePct              string `json:"sharePct"`
	Basis                 string `json:"basis"`
	LineNet               string `json:"lineNet"`
	CostSnapshot          string `json:"costSnapshot,omitempty"`
	BasisAmount           string `json:"basisAmount"`
	RatePct               string `json:"ratePct"`
	Amount                string `json:"amount"`
	Currency              string `json:"currency"`
	DebitAccount          string `json:"debitAccount"`
	CreditAccount         string `json:"creditAccount"`
	Status                string `json:"status"`
}

func (a *API) createSalesGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req salesGroupRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.Name == "" || len(req.Name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	shares := make([]domain.Share, 0, len(req.Reps))
	for i, rs := range req.Reps {
		pct, err := decimal.NewFromString(rs.SharePct)
		if err != nil {
			fields["reps["+strconv.Itoa(i)+"].sharePct"] = "must be a decimal"
		}
		shares = append(shares, domain.Share{RepID: rs.RepID, SharePct: pct})
	}
	if len(fields) == 0 {
		for k, v := range domain.ValidateShares(shares) {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	g, err := a.store.CreateSalesGroup(r.Context(), store.SalesGroup{LegalEntityID: req.LegalEntityID, Name: req.Name, Reps: shares}, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, salesGroupResponse(g))
}

func (a *API) listSalesGroups(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListSalesGroups(r.Context(), le, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]salesGroupView, 0, len(rows))
	for _, g := range rows {
		items = append(items, salesGroupResponse(g))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getSalesGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeSalesGroupNotFound, "no sales group "+id))
		return
	}
	g, err := a.store.GetSalesGroup(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, salesGroupResponse(g))
}

func (a *API) createCommissionRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req commissionRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.SalesGroupID) {
		fields["salesGroupId"] = "must be a UUID"
	}
	rate, err := decimal.NewFromString(req.RatePct)
	if err != nil {
		fields["ratePct"] = "must be a decimal"
	}
	rule := domain.CommissionRule{SalesGroupID: req.SalesGroupID, CustomerGroup: req.CustomerGroup, ItemGroup: req.ItemGroup,
		Basis: req.Basis, RatePct: rate}
	for k, v := range domain.ValidateCommissionRule(rule) {
		if _, set := fields[k]; !set {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	row, err := a.store.CreateCommissionRule(r.Context(), rule, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, commissionRuleResponse(row))
}

func (a *API) listCommissionRules(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	sg := r.URL.Query().Get("salesGroupId")
	if sg != "" && !ids.Valid(sg) {
		validationFailed(w, r, map[string]string{"salesGroupId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListCommissionRules(r.Context(), sg, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]commissionRuleView, 0, len(rows))
	for _, row := range rows {
		items = append(items, commissionRuleResponse(row))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// postCommission computes the commission of one posted invoice line (FR-26.18). 201 when the
// line posted, 200 when it was already posted (the invoice feed may redeliver).
func (a *API) postCommission(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req commissionPostingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.SalesGroupID) {
		fields["salesGroupId"] = "must be a UUID"
	}
	if len(req.CustomerID) > maxInvoiceLineID {
		fields["customerId"] = "must be at most 128 characters"
	}
	day, err := time.Parse(dateLayout, req.InvoiceDate)
	if err != nil {
		fields["invoiceDate"] = "must be a date in YYYY-MM-DD form"
	}
	net, err := decimal.NewFromString(req.LineNet)
	if err != nil {
		fields["lineNet"] = "must be a decimal"
	}
	line := domain.CommissionLine{InvoiceLineID: req.InvoiceLineID, ReversesInvoiceLineID: req.ReversesInvoiceLineID, InvoiceDate: day,
		CustomerID: req.CustomerID, CustomerGroup: req.CustomerGroup, ItemGroup: req.ItemGroup, Currency: req.Currency, LineNet: net}
	if req.CostSnapshot != nil {
		cost, err := decimal.NewFromString(*req.CostSnapshot)
		if err != nil {
			fields["costSnapshot"] = "must be a decimal"
		}
		line.CostSnapshot, line.HasCost = cost, true
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	for k, v := range domain.ValidateCommissionLine(line) {
		if _, set := fields[k]; !set {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	txns, created, err := a.store.PostCommission(r.Context(), store.CommissionPosting{LegalEntityID: req.LegalEntityID,
		SalesGroupID: req.SalesGroupID, Line: line})
	if err != nil {
		slog.Error("commission posting failed", "err", err, "salesGroupId", req.SalesGroupID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		a.commissionsPosted.Add(1)
		status = http.StatusCreated
	}
	items := make([]commissionTxnView, 0, len(txns))
	for _, t := range txns {
		items = append(items, commissionTxnResponse(t))
	}
	httpx.JSON(w, status, map[string]any{"items": items})
}

// listCommissionTransactions is the commission inquiry (§5.26.14): by rep, sales group,
// period, customer and invoice line.
func (a *API) listCommissionTransactions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.CommissionFilter{LegalEntityID: q.Get("legalEntityId"), SalesGroupID: q.Get("salesGroupId"), RepID: q.Get("repId"),
		CustomerID: q.Get("customerId"), InvoiceLineID: q.Get("invoiceLineId"), PeriodKey: q.Get("period"), Limit: store.MaxListLimit}
	fields := map[string]string{}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if f.SalesGroupID != "" && !ids.Valid(f.SalesGroupID) {
		fields["salesGroupId"] = "must be a UUID"
	}
	if f.PeriodKey != "" && !monthKey.MatchString(f.PeriodKey) {
		fields["period"] = "must be a month such as 2027-03"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.ListCommissionTransactions(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]commissionTxnView, 0, len(rows))
	for _, t := range rows {
		items = append(items, commissionTxnResponse(t))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// registerCommissionRoutes mounts the commission endpoints (FR-26.12, FR-26.18).
func (a *API) registerCommissionRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyCommissionMaintain)).Post(salesGroupsPath, a.createSalesGroup)
	r.With(authz.RequireDuty(dutyCommissionView)).Get(salesGroupsPath, a.listSalesGroups)
	r.With(authz.RequireDuty(dutyCommissionView)).Get(salesGroupsPath+"/{id}", a.getSalesGroup)
	r.With(authz.RequireDuty(dutyCommissionMaintain)).Post(commissionRulesPath, a.createCommissionRule)
	r.With(authz.RequireDuty(dutyCommissionView)).Get(commissionRulesPath, a.listCommissionRules)
	r.With(authz.RequireDuty(dutyCommissionMaintain)).Post(commissionPostPath, a.postCommission)
	r.With(authz.RequireDuty(dutyCommissionView)).Get(commissionTxnsPath, a.listCommissionTransactions)
}

func salesGroupResponse(g store.SalesGroup) salesGroupView {
	reps := make([]repShareDTO, 0, len(g.Reps))
	for _, s := range g.Reps {
		reps = append(reps, repShareDTO{RepID: s.RepID, SharePct: s.SharePct.String()})
	}
	return salesGroupView{ID: g.ID, LegalEntityID: g.LegalEntityID, Name: g.Name, Reps: reps}
}

func commissionRuleResponse(row store.CommissionRuleRow) commissionRuleView {
	return commissionRuleView{ID: row.ID, LegalEntityID: row.LegalEntityID, SalesGroupID: row.Rule.SalesGroupID,
		CustomerGroup: row.Rule.CustomerGroup, ItemGroup: row.Rule.ItemGroup, Basis: row.Rule.Basis, RatePct: row.Rule.RatePct.String()}
}

func commissionTxnResponse(t store.CommissionTxn) commissionTxnView {
	v := commissionTxnView{ID: t.ID, LegalEntityID: t.LegalEntityID, InvoiceLineID: t.InvoiceLineID, ReversesInvoiceLineID: t.ReversesInvoiceLineID,
		InvoiceDate: t.InvoiceDate.Format(dateLayout), PeriodID: t.PeriodKey, CustomerID: t.CustomerID, CustomerGroup: t.CustomerGroup,
		ItemGroup: t.ItemGroup, SalesGroupID: t.SalesGroupID, RepID: t.RepID, SharePct: t.SharePct.String(), Basis: t.Basis,
		LineNet: t.LineNet.String(), BasisAmount: t.BasisAmount.String(), RatePct: t.RatePct.String(), Amount: t.Amount.String(),
		Currency: t.Currency, DebitAccount: t.DebitAccount, CreditAccount: t.CreditAccount, Status: t.Status}
	if t.CostSnapshot != nil {
		v.CostSnapshot = t.CostSnapshot.String()
	}
	return v
}
