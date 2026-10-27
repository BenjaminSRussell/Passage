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
	rebateAgreementsPath = "/v1/pricing/rebates/agreements"
	rebateAccrualsPath   = "/v1/pricing/rebates/accruals"
	dutyRebateMaintain   = "pricing.rebate.maintain"
	dutyRebateView       = "pricing.rebate.view"
	// defaultAccrualAccount and defaultExpenseAccount are the FR-26.11 accounts (2150 / 4150).
	defaultAccrualAccount = "2150"
	defaultExpenseAccount = "4150"
	maxInvoiceLineID      = 128
)

var (
	accountCode = regexp.MustCompile(`^[0-9]{4,8}$`)
	periodKey   = regexp.MustCompile(`^[0-9]{4}(-(0[1-9]|1[0-2])|-Q[1-4])?$`)
)

type tierDTO struct {
	From          string `json:"from"`
	RatePct       string `json:"ratePct"`
	AmountPerUnit string `json:"amountPerUnit"`
}

type agreementRequest struct {
	LegalEntityID   string    `json:"legalEntityId"`
	Name            string    `json:"name"`
	CustomerID      string    `json:"customerId"`
	CustomerGroupID string    `json:"customerGroupId"`
	Basis           string    `json:"basis"`
	Period          string    `json:"period"`
	Tiers           []tierDTO `json:"tiers"`
	Currency        string    `json:"currency"`
	ValidFrom       string    `json:"validFrom"`
	ValidTo         string    `json:"validTo"`
	AccrualAccount  string    `json:"accrualAccount"`
	ExpenseAccount  string    `json:"expenseAccount"`
}

type agreementView struct {
	ID              string    `json:"id"`
	LegalEntityID   string    `json:"legalEntityId"`
	Name            string    `json:"name"`
	CustomerID      string    `json:"customerId,omitempty"`
	CustomerGroupID string    `json:"customerGroupId,omitempty"`
	Basis           string    `json:"basis"`
	Period          string    `json:"period"`
	Tiers           []tierDTO `json:"tiers"`
	Currency        string    `json:"currency"`
	ValidFrom       string    `json:"validFrom"`
	ValidTo         string    `json:"validTo"`
	AccrualAccount  string    `json:"accrualAccount"`
	ExpenseAccount  string    `json:"expenseAccount"`
	Status          string    `json:"status"`
}

// accrualRequest is one posted invoice line handed to pricing by the invoice.posted consumer.
type accrualRequest struct {
	AgreementID   string `json:"agreementId"`
	InvoiceLineID string `json:"invoiceLineId"`
	InvoiceDate   string `json:"invoiceDate"`
	Value         string `json:"value"`
	Quantity      string `json:"quantity"`
	Currency      string `json:"currency"`
}

type accrualView struct {
	ID            string `json:"id"`
	AgreementID   string `json:"agreementId"`
	PeriodID      string `json:"periodId"`
	InvoiceLineID string `json:"invoiceLineId"`
	InvoiceDate   string `json:"invoiceDate"`
	BasisValue    string `json:"basisValue"`
	BasisQuantity string `json:"basisQuantity"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
}

func (a *API) createAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req agreementRequest
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
	if req.CustomerID != "" && !ids.Valid(req.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if req.CustomerGroupID != "" && !ids.Valid(req.CustomerGroupID) {
		fields["customerGroupId"] = "must be a UUID"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	from, err1 := time.Parse(dateLayout, req.ValidFrom)
	to, err2 := time.Parse(dateLayout, req.ValidTo)
	if err1 != nil || err2 != nil {
		fields["validity"] = "validFrom and validTo must be dates in YYYY-MM-DD form"
	}
	accrual, expense := orDefault(req.AccrualAccount, defaultAccrualAccount), orDefault(req.ExpenseAccount, defaultExpenseAccount)
	if !accountCode.MatchString(accrual) || !accountCode.MatchString(expense) {
		fields["accounts"] = "accounts must be 4 to 8 digit chart codes"
	}
	tiers := make([]domain.Tier, 0, len(req.Tiers))
	for i, t := range req.Tiers {
		tier, ok := parseTier(t)
		if !ok {
			fields["tiers"] = "tier " + strconv.Itoa(i+1) + " has a value that is not a decimal"
			break
		}
		tiers = append(tiers, tier)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	agreement := domain.RebateAgreement{LegalEntityID: req.LegalEntityID, Name: req.Name, CustomerID: req.CustomerID,
		CustomerGroupID: req.CustomerGroupID, Basis: req.Basis, Period: req.Period, Tiers: tiers, Currency: req.Currency,
		ValidFrom: from, ValidTo: to, AccrualAccount: accrual, ExpenseAccount: expense}
	if e := domain.ValidateAgreement(agreement); e != nil {
		validationFailed(w, r, e.Fields)
		return
	}
	out, err := a.store.CreateAgreement(r.Context(), agreement, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, agreementResponse(out))
}

// accrueInvoiceLine accrues one invoice line. 201 when it posted a new accrual, 200 when the
// invoice line was already accrued (the consumer may redeliver).
func (a *API) accrueInvoiceLine(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req accrualRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.AgreementID) {
		fields["agreementId"] = "must be a UUID"
	}
	if req.InvoiceLineID == "" || len(req.InvoiceLineID) > maxInvoiceLineID {
		fields["invoiceLineId"] = "must be 1 to 128 characters"
	}
	day, err := time.Parse(dateLayout, req.InvoiceDate)
	if err != nil {
		fields["invoiceDate"] = "must be a date in YYYY-MM-DD form"
	}
	value, err1 := decimal.NewFromString(orDefault(req.Value, "0"))
	qty, err2 := decimal.NewFromString(orDefault(req.Quantity, "0"))
	if err1 != nil {
		fields["value"] = "must be a decimal"
	}
	if err2 != nil {
		fields["quantity"] = "must be a decimal"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	acc, created, err := a.store.AccrueInvoiceLine(r.Context(), store.AccrualInput{AgreementID: req.AgreementID,
		InvoiceLineID: req.InvoiceLineID, InvoiceDate: day, Value: value, Quantity: qty, Currency: req.Currency})
	if err != nil {
		slog.Error("rebate accrual failed", "err", err, "agreementId", req.AgreementID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		a.accrualsPosted.Add(1)
		status = http.StatusCreated
	}
	httpx.JSON(w, status, accrualResponse(acc))
}

// listAgreements returns rebate agreements, optionally filtered by legalEntityId (FR-26.11).
func (a *API) listAgreements(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListAgreements(r.Context(), le, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]agreementView, 0, len(rows))
	for _, row := range rows {
		items = append(items, agreementResponse(row))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+id))
		return
	}
	g, err := a.store.GetAgreement(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, agreementResponse(g))
}

func (a *API) listAccruals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.AccrualFilter{AgreementID: q.Get("agreementId"), PeriodKey: q.Get("period")}
	fields := map[string]string{}
	if f.AgreementID != "" && !ids.Valid(f.AgreementID) {
		fields["agreementId"] = "must be a UUID"
	}
	if f.PeriodKey != "" && !periodKey.MatchString(f.PeriodKey) {
		fields["period"] = "must be a period such as 2027-03, 2027-Q1 or 2027"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.ListAccruals(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]accrualView, 0, len(rows))
	for _, row := range rows {
		items = append(items, accrualResponse(row))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// registerRebateRoutes mounts the rebate endpoints (FR-26.11).
func (a *API) registerRebateRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(rebateAgreementsPath, a.createAgreement)
	r.With(authz.RequireDuty(dutyRebateView)).Get(rebateAgreementsPath, a.listAgreements)
	r.With(authz.RequireDuty(dutyRebateView)).Get(rebateAgreementsPath+"/{id}", a.getAgreement)
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(rebateAccrualsPath, a.accrueInvoiceLine)
	r.With(authz.RequireDuty(dutyRebateView)).Get(rebateAccrualsPath, a.listAccruals)
}

func agreementResponse(g domain.RebateAgreement) agreementView {
	tiers := make([]tierDTO, 0, len(g.Tiers))
	for _, t := range g.Tiers {
		tiers = append(tiers, tierDTO{From: t.From.String(), RatePct: t.RatePct.String(), AmountPerUnit: t.AmountPerUnit.String()})
	}
	return agreementView{ID: g.ID, LegalEntityID: g.LegalEntityID, Name: g.Name, CustomerID: g.CustomerID,
		CustomerGroupID: g.CustomerGroupID, Basis: g.Basis, Period: g.Period, Tiers: tiers, Currency: g.Currency,
		ValidFrom: g.ValidFrom.Format(dateLayout), ValidTo: g.ValidTo.Format(dateLayout), AccrualAccount: g.AccrualAccount,
		ExpenseAccount: g.ExpenseAccount, Status: "ACTIVE"}
}

func accrualResponse(a store.Accrual) accrualView {
	return accrualView{ID: a.ID, AgreementID: a.AgreementID, PeriodID: a.PeriodKey, InvoiceLineID: a.InvoiceLineID,
		InvoiceDate: a.InvoiceDate.Format(dateLayout), BasisValue: a.BasisValue.String(), BasisQuantity: a.BasisQty.String(),
		Amount: a.Accrued.String(), Currency: a.Currency, Status: a.Status}
}

func parseTier(t tierDTO) (domain.Tier, bool) {
	from, err1 := decimal.NewFromString(orDefault(t.From, "0"))
	rate, err2 := decimal.NewFromString(orDefault(t.RatePct, "0"))
	per, err3 := decimal.NewFromString(orDefault(t.AmountPerUnit, "0"))
	if err1 != nil || err2 != nil || err3 != nil {
		return domain.Tier{}, false
	}
	return domain.Tier{From: from, RatePct: rate, AmountPerUnit: per}, true
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
