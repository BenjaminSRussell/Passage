package api

import (
	"log/slog"
	"net/http"
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

// dutyContractAccrue is the service-only duty of the two accrual routes, which stand in for
// the invoice.posted consumer: each accrual is a Dr expense / Cr payable GL posting, so no
// human role may feed invoice lines (handoff: seeded service_only, granted to the caller
// that delivers posted invoice lines).
const dutyContractAccrue = "pricing.contract.accrue"

const (
	brokerPath        = "/v1/pricing/broker-contracts"
	royaltyPath       = "/v1/pricing/royalty-agreements"
	contractClaimPath = "/v1/pricing/contract-claims"
)

type brokerRequest struct {
	LegalEntityID   string `json:"legalEntityId"`
	Name            string `json:"name"`
	BrokerVendorID  string `json:"brokerVendorId"`
	CustomerID      string `json:"customerId"`
	CustomerGroupID string `json:"customerGroupId"`
	ProductID       string `json:"productId"`
	ProductGroupID  string `json:"productGroupId"`
	RateKind        string `json:"rateKind"`
	Rate            string `json:"rate"`
	Currency        string `json:"currency"`
	ValidFrom       string `json:"validFrom"`
	ValidTo         string `json:"validTo"`
	PayableAccount  string `json:"payableAccount"`
	ExpenseAccount  string `json:"expenseAccount"`
}

type brokerView struct {
	ID              string `json:"id"`
	LegalEntityID   string `json:"legalEntityId"`
	Name            string `json:"name"`
	BrokerVendorID  string `json:"brokerVendorId"`
	CustomerID      string `json:"customerId,omitempty"`
	CustomerGroupID string `json:"customerGroupId,omitempty"`
	ProductID       string `json:"productId,omitempty"`
	ProductGroupID  string `json:"productGroupId,omitempty"`
	RateKind        string `json:"rateKind"`
	Rate            string `json:"rate"`
	Currency        string `json:"currency"`
	ValidFrom       string `json:"validFrom"`
	ValidTo         string `json:"validTo"`
	PayableAccount  string `json:"payableAccount"`
	ExpenseAccount  string `json:"expenseAccount"`
	Status          string `json:"status"`
	Version         int    `json:"version"`
}

type royaltyRequest struct {
	LegalEntityID    string    `json:"legalEntityId"`
	Name             string    `json:"name"`
	LicensorVendorID string    `json:"licensorVendorId"`
	ProductID        string    `json:"productId"`
	ProductGroupID   string    `json:"productGroupId"`
	Basis            string    `json:"basis"`
	Period           string    `json:"period"`
	Tiers            []tierDTO `json:"tiers"`
	MinimumRoyalty   string    `json:"minimumRoyalty"`
	Currency         string    `json:"currency"`
	ValidFrom        string    `json:"validFrom"`
	ValidTo          string    `json:"validTo"`
	PayableAccount   string    `json:"payableAccount"`
	ExpenseAccount   string    `json:"expenseAccount"`
}

type royaltyView struct {
	ID               string    `json:"id"`
	LegalEntityID    string    `json:"legalEntityId"`
	Name             string    `json:"name"`
	LicensorVendorID string    `json:"licensorVendorId"`
	ProductID        string    `json:"productId,omitempty"`
	ProductGroupID   string    `json:"productGroupId,omitempty"`
	Basis            string    `json:"basis"`
	Period           string    `json:"period"`
	Tiers            []tierDTO `json:"tiers"`
	MinimumRoyalty   string    `json:"minimumRoyalty"`
	Currency         string    `json:"currency"`
	ValidFrom        string    `json:"validFrom"`
	ValidTo          string    `json:"validTo"`
	PayableAccount   string    `json:"payableAccount"`
	ExpenseAccount   string    `json:"expenseAccount"`
	Status           string    `json:"status"`
	Version          int       `json:"version"`
}

// contractAccrualRequest is one posted invoice line (or credit note line) handed to a contract
// by the invoice.posted consumer. A credit note line names the invoice line it reverses and
// carries positive netAmount and quantity.
type contractAccrualRequest struct {
	InvoiceLineID         string `json:"invoiceLineId"`
	InvoiceDate           string `json:"invoiceDate"`
	CustomerID            string `json:"customerId"`
	CustomerGroupID       string `json:"customerGroupId"`
	ProductID             string `json:"productId"`
	ProductGroupID        string `json:"productGroupId"`
	Currency              string `json:"currency"`
	NetAmount             string `json:"netAmount"`
	Quantity              string `json:"quantity"`
	ReversesInvoiceLineID string `json:"reversesInvoiceLineId"`
}

type contractAccrualView struct {
	ID              string `json:"id"`
	ContractKind    string `json:"contractKind"`
	ContractID      string `json:"contractId"`
	PeriodID        string `json:"periodId"`
	LineKey         string `json:"lineKey"`
	Kind            string `json:"kind"`
	InvoiceDate     string `json:"invoiceDate"`
	BasisValue      string `json:"basisValue"`
	BasisQuantity   string `json:"basisQuantity"`
	Amount          string `json:"amount"`
	Currency        string `json:"currency"`
	ReversesLineKey string `json:"reversesLineKey,omitempty"`
}

type royaltyCloseRequest struct {
	Period string `json:"period"`
}

type royaltyCloseView struct {
	AgreementID    string `json:"agreementId"`
	PeriodID       string `json:"periodId"`
	Accrued        string `json:"accrued"`
	MinimumRoyalty string `json:"minimumRoyalty"`
	Shortfall      string `json:"shortfall"`
	Currency       string `json:"currency"`
	AlreadyClosed  bool   `json:"alreadyClosed"`
	// Notice is PRC-0017 when a shortfall posted at close, informational only.
	Notice  string               `json:"notice,omitempty"`
	Accrual *contractAccrualView `json:"accrual,omitempty"`
}

type contractClaimRequest struct {
	ContractKind string `json:"contractKind"`
	ContractID   string `json:"contractId"`
	Period       string `json:"period"`
}

type contractClaimView struct {
	ID            string `json:"id"`
	Number        string `json:"number"`
	LegalEntityID string `json:"legalEntityId"`
	ContractKind  string `json:"contractKind"`
	ContractID    string `json:"contractId"`
	VendorID      string `json:"vendorId"`
	PeriodID      string `json:"periodId"`
	AccruedTotal  string `json:"accruedTotal"`
	PriorClaimed  string `json:"priorClaimed"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	Version       int    `json:"version"`
	CreatedBy     string `json:"createdBy,omitempty"`
	ApprovedBy    string `json:"approvedBy,omitempty"`
	CreatedAt     string `json:"createdAt"`
	ApprovedAt    string `json:"approvedAt,omitempty"`
}

type contractClaimPreviewView struct {
	ContractKind string `json:"contractKind"`
	ContractID   string `json:"contractId"`
	VendorID     string `json:"vendorId"`
	PeriodID     string `json:"periodId"`
	Accrued      string `json:"accrued"`
	Shortfall    string `json:"shortfall"`
	PriorClaimed string `json:"priorClaimed"`
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	Claims       int    `json:"claims"`
	Closed       bool   `json:"closed"`
}

// registerBrokerRoyaltyRoutes mounts the broker contract, royalty agreement and claim surface
// (FR-26.16, FR-26.17). Reads take RebateView; maintaining contracts, closing periods and
// creating claims RebateMaintain; approving a claim VendorRebateClaimApprove; feeding posted
// invoice lines the service-only pricing.contract.accrue.
func (a *API) registerBrokerRoyaltyRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(brokerPath, a.createBroker)
	r.With(authz.RequireDuty(dutyRebateView)).Get(brokerPath, a.listBrokers)
	r.With(authz.RequireDuty(dutyRebateView)).Get(brokerPath+"/{id}", a.getBroker)
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(brokerPath+"/{id}:close", a.closeBroker)
	r.With(authz.RequireDuty(dutyContractAccrue)).Post(brokerPath+"/{id}/accruals", a.accrueBroker)
	r.With(authz.RequireDuty(dutyRebateView)).Get(brokerPath+"/{id}/accruals", a.listBrokerAccruals)
	r.With(authz.RequireDuty(dutyRebateView)).Get(brokerPath+"/{id}/claim-preview", a.previewBrokerClaim)

	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(royaltyPath, a.createRoyalty)
	r.With(authz.RequireDuty(dutyRebateView)).Get(royaltyPath, a.listRoyalties)
	r.With(authz.RequireDuty(dutyRebateView)).Get(royaltyPath+"/{id}", a.getRoyalty)
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(royaltyPath+"/{id}:close", a.closeRoyalty)
	r.With(authz.RequireDuty(dutyContractAccrue)).Post(royaltyPath+"/{id}/accruals", a.accrueRoyalty)
	r.With(authz.RequireDuty(dutyRebateView)).Get(royaltyPath+"/{id}/accruals", a.listRoyaltyAccruals)
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(royaltyPath+"/{id}:close-period", a.closeRoyaltyPeriod)
	r.With(authz.RequireDuty(dutyRebateView)).Get(royaltyPath+"/{id}/claim-preview", a.previewRoyaltyClaim)

	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(contractClaimPath, a.createContractClaim)
	r.With(authz.RequireDuty(dutyRebateView)).Get(contractClaimPath, a.listContractClaims)
	r.With(authz.RequireDuty(dutyRebateView)).Get(contractClaimPath+"/{id}", a.getContractClaim)
	r.With(authz.RequireDuty(dutyClaimApprove)).Post(contractClaimPath+"/{id}:approve", a.approveContractClaim)
}

func brokerResponse(c domain.BrokerContract) brokerView {
	return brokerView{ID: c.ID, LegalEntityID: c.LegalEntityID, Name: c.Name, BrokerVendorID: c.BrokerVendorID, CustomerID: c.CustomerID,
		CustomerGroupID: c.CustomerGroupID, ProductID: c.ProductID, ProductGroupID: c.ProductGroupID, RateKind: c.RateKind, Rate: c.Rate.String(),
		Currency: c.Currency, ValidFrom: c.ValidFrom.Format(dateLayout), ValidTo: c.ValidTo.Format(dateLayout), PayableAccount: c.PayableAccount,
		ExpenseAccount: c.ExpenseAccount, Status: c.Status, Version: c.Version}
}

func royaltyResponse(g domain.RoyaltyAgreement) royaltyView {
	tiers := make([]tierDTO, 0, len(g.Tiers))
	for _, t := range g.Tiers {
		tiers = append(tiers, tierDTO{From: t.From.String(), RatePct: t.RatePct.String(), AmountPerUnit: t.AmountPerUnit.String()})
	}
	return royaltyView{ID: g.ID, LegalEntityID: g.LegalEntityID, Name: g.Name, LicensorVendorID: g.LicensorVendorID, ProductID: g.ProductID,
		ProductGroupID: g.ProductGroupID, Basis: g.Basis, Period: g.Period, Tiers: tiers, MinimumRoyalty: g.MinimumRoyalty.String(), Currency: g.Currency,
		ValidFrom: g.ValidFrom.Format(dateLayout), ValidTo: g.ValidTo.Format(dateLayout), PayableAccount: g.PayableAccount,
		ExpenseAccount: g.ExpenseAccount, Status: g.Status, Version: g.Version}
}

func contractAccrualResponse(a store.ContractAccrual) contractAccrualView {
	return contractAccrualView{ID: a.ID, ContractKind: a.ContractKind, ContractID: a.ContractID, PeriodID: a.PeriodKey, LineKey: a.LineKey, Kind: a.Kind,
		InvoiceDate: a.InvoiceDate.Format(dateLayout), BasisValue: a.BasisValue.String(), BasisQuantity: a.BasisQty.String(), Amount: a.Amount.String(),
		Currency: a.Currency, ReversesLineKey: a.ReversesLineKey}
}

func contractClaimResponse(c store.ContractClaim) contractClaimView {
	v := contractClaimView{ID: c.ID, Number: c.Number, LegalEntityID: c.LegalEntityID, ContractKind: c.ContractKind, ContractID: c.ContractID,
		VendorID: c.VendorID, PeriodID: c.PeriodKey, AccruedTotal: c.AccruedTotal.String(), PriorClaimed: c.PriorClaimed.String(), Amount: c.Amount.String(),
		Currency: c.Currency, Status: c.Status, Version: c.Version, CreatedBy: c.CreatedBy, ApprovedBy: c.ApprovedBy,
		CreatedAt: c.CreatedAt.UTC().Format(timestampLayout)}
	if c.ApprovedAt != nil {
		v.ApprovedAt = c.ApprovedAt.UTC().Format(timestampLayout)
	}
	return v
}

func validOptionalUUID(v, field string, fields map[string]string) {
	if v != "" && !ids.Valid(v) {
		fields[field] = "must be a UUID"
	}
}

// dates parses the validity of a create request into fields.
func requestDates(from, to string, fields map[string]string) (time.Time, time.Time) {
	f, err1 := time.Parse(dateLayout, from)
	t, err2 := time.Parse(dateLayout, to)
	if err1 != nil || err2 != nil {
		fields["validity"] = "validFrom and validTo must be dates in YYYY-MM-DD form"
	}
	return f, t
}

func (a *API) createBroker(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req brokerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.BrokerVendorID) {
		fields["brokerVendorId"] = "must be a UUID"
	}
	validOptionalUUID(req.CustomerID, "customerId", fields)
	validOptionalUUID(req.CustomerGroupID, "customerGroupId", fields)
	rate, err := decimal.NewFromString(req.Rate)
	if err != nil {
		fields["rate"] = "must be a decimal"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	from, to := requestDates(req.ValidFrom, req.ValidTo, fields)
	c := domain.BrokerContract{LegalEntityID: req.LegalEntityID, Name: req.Name, BrokerVendorID: req.BrokerVendorID, CustomerID: req.CustomerID,
		CustomerGroupID: req.CustomerGroupID, ProductID: req.ProductID, ProductGroupID: req.ProductGroupID, RateKind: req.RateKind, Rate: rate,
		Currency: req.Currency, ValidFrom: from, ValidTo: to, PayableAccount: orDefault(req.PayableAccount, domain.BrokerPayableAccount),
		ExpenseAccount: orDefault(req.ExpenseAccount, domain.BrokerExpenseAccount)}
	for k, v := range domain.ValidateBrokerContract(c) {
		if _, set := fields[k]; !set {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !legalEntityInScope(w, r, c.LegalEntityID) {
		return
	}
	out, err := a.store.CreateBrokerContract(r.Context(), c, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, brokerResponse(out))
}

func contractFilter(w http.ResponseWriter, r *http.Request, vendorParam string) (store.ContractFilter, bool) {
	q := r.URL.Query()
	f := store.ContractFilter{LegalEntityID: q.Get("legalEntityId"), VendorID: q.Get(vendorParam), Status: q.Get("status"), Limit: store.MaxListLimit}
	fields := map[string]string{}
	validOptionalUUID(f.LegalEntityID, "legalEntityId", fields)
	validOptionalUUID(f.VendorID, vendorParam, fields)
	if f.Status != "" && f.Status != domain.AgreementActive && f.Status != domain.AgreementClosed {
		fields["status"] = "must be ACTIVE or CLOSED"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return f, false
	}
	return f, true
}

func (a *API) listBrokers(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := contractFilter(w, r, "brokerVendorId")
	if !ok {
		return
	}
	rows, err := a.store.ListBrokerContracts(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]brokerView, 0, len(rows))
	for _, c := range rows {
		items = append(items, brokerResponse(c))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getBroker(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeContractNotFound, "broker contract")
	if !ok {
		return
	}
	c, err := a.store.GetBrokerContract(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, brokerResponse(c))
}

func (a *API) closeBroker(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeContractNotFound, "broker contract")
	if !ok {
		return
	}
	version, ok := decodeVersion(w, r)
	if !ok {
		return
	}
	c, err := a.store.CloseBrokerContract(r.Context(), id, version, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, brokerResponse(c))
}

// decodeVersion reads the body version of a lifecycle command.
func decodeVersion(w http.ResponseWriter, r *http.Request) (int, bool) {
	var req versionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return 0, false
	}
	if req.Version < 1 {
		validationFailed(w, r, map[string]string{"version": "is required and must be at least 1"})
		return 0, false
	}
	return req.Version, true
}

func (a *API) createRoyalty(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req royaltyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.LicensorVendorID) {
		fields["licensorVendorId"] = "must be a UUID"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	minimum, err := decimal.NewFromString(orDefault(req.MinimumRoyalty, "0"))
	if err != nil {
		fields["minimumRoyalty"] = "must be a decimal"
	}
	from, to := requestDates(req.ValidFrom, req.ValidTo, fields)
	tiers := make([]domain.Tier, 0, len(req.Tiers))
	for i, t := range req.Tiers {
		tier, ok := parseTier(t)
		if !ok {
			fields["tiers"] = "tier " + strconv.Itoa(i+1) + " has a value that is not a decimal"
			break
		}
		tiers = append(tiers, tier)
	}
	g := domain.RoyaltyAgreement{LegalEntityID: req.LegalEntityID, Name: req.Name, LicensorVendorID: req.LicensorVendorID, ProductID: req.ProductID,
		ProductGroupID: req.ProductGroupID, Basis: req.Basis, Period: req.Period, Tiers: tiers, MinimumRoyalty: minimum, Currency: req.Currency,
		ValidFrom: from, ValidTo: to, PayableAccount: orDefault(req.PayableAccount, domain.RoyaltyPayableAccount),
		ExpenseAccount: orDefault(req.ExpenseAccount, domain.RoyaltyExpenseAccount)}
	for k, v := range domain.ValidateRoyaltyAgreement(g) {
		if _, set := fields[k]; !set {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !legalEntityInScope(w, r, g.LegalEntityID) {
		return
	}
	out, err := a.store.CreateRoyaltyAgreement(r.Context(), g, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, royaltyResponse(out))
}

func (a *API) listRoyalties(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := contractFilter(w, r, "licensorVendorId")
	if !ok {
		return
	}
	rows, err := a.store.ListRoyaltyAgreements(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]royaltyView, 0, len(rows))
	for _, g := range rows {
		items = append(items, royaltyResponse(g))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getRoyalty(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeContractNotFound, "royalty agreement")
	if !ok {
		return
	}
	g, err := a.store.GetRoyaltyAgreement(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, royaltyResponse(g))
}

func (a *API) closeRoyalty(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeContractNotFound, "royalty agreement")
	if !ok {
		return
	}
	version, ok := decodeVersion(w, r)
	if !ok {
		return
	}
	g, err := a.store.CloseRoyaltyAgreement(r.Context(), id, version, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, royaltyResponse(g))
}

func (a *API) accrueBroker(w http.ResponseWriter, r *http.Request) {
	a.accrueContract(w, r, domain.ContractBroker, "broker contract")
}

func (a *API) accrueRoyalty(w http.ResponseWriter, r *http.Request) {
	a.accrueContract(w, r, domain.ContractRoyalty, "royalty agreement")
}

// accrueContract accrues one posted invoice line or credit note line against a contract: the
// stand-in for the invoice.posted consumer. 201 when it posted a new accrual, 200 when the
// line was already accrued (the consumer may redeliver).
func (a *API) accrueContract(w http.ResponseWriter, r *http.Request, kind, what string) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeContractNotFound, what)
	if !ok {
		return
	}
	var req contractAccrualRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.InvoiceLineID == "" || len(req.InvoiceLineID) > maxInvoiceLineID {
		fields["invoiceLineId"] = "must be 1 to 128 characters"
	}
	if len(req.ReversesInvoiceLineID) > maxInvoiceLineID {
		fields["reversesInvoiceLineId"] = "must be at most 128 characters"
	}
	day, err := time.Parse(dateLayout, req.InvoiceDate)
	if err != nil {
		fields["invoiceDate"] = "must be a date in YYYY-MM-DD form"
	}
	net, err1 := decimal.NewFromString(orDefault(req.NetAmount, "0"))
	qty, err2 := decimal.NewFromString(orDefault(req.Quantity, "0"))
	if err1 != nil {
		fields["netAmount"] = "must be a decimal"
	}
	if err2 != nil {
		fields["quantity"] = "must be a decimal"
	}
	if net.IsNegative() || qty.IsNegative() {
		fields["netAmount"] = "netAmount and quantity are positive; a credit note line names reversesInvoiceLineId"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	validOptionalUUID(req.CustomerID, "customerId", fields)
	validOptionalUUID(req.CustomerGroupID, "customerGroupId", fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	acc, created, err := a.store.AccrueContractLine(r.Context(), store.ContractAccrualInput{Kind: kind, ContractID: id, Line: domain.ContractLine{
		InvoiceLineID: req.InvoiceLineID, InvoiceDate: day, CustomerID: req.CustomerID, CustomerGroupID: req.CustomerGroupID, ProductID: req.ProductID,
		ProductGroupID: req.ProductGroupID, Currency: req.Currency, NetAmount: net, Quantity: qty, ReversesInvoiceLineID: req.ReversesInvoiceLineID}})
	if err != nil {
		slog.Error("contract accrual failed", "err", err, "contractId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		a.contractAccruals.Add(1)
		status = http.StatusCreated
	}
	httpx.JSON(w, status, contractAccrualResponse(acc))
}

func (a *API) listBrokerAccruals(w http.ResponseWriter, r *http.Request) {
	a.listContractAccruals(w, r, domain.ContractBroker, "broker contract")
}

func (a *API) listRoyaltyAccruals(w http.ResponseWriter, r *http.Request) {
	a.listContractAccruals(w, r, domain.ContractRoyalty, "royalty agreement")
}

func (a *API) listContractAccruals(w http.ResponseWriter, r *http.Request, kind, what string) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeContractNotFound, what)
	if !ok {
		return
	}
	period := r.URL.Query().Get("period")
	if period != "" && !periodKey.MatchString(period) {
		validationFailed(w, r, map[string]string{"period": claimPeriodDetail})
		return
	}
	rows, err := a.store.ListContractAccruals(r.Context(), store.ContractAccrualFilter{Kind: kind, ContractID: id, PeriodKey: period, Limit: store.MaxListLimit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]contractAccrualView, 0, len(rows))
	for _, x := range rows {
		items = append(items, contractAccrualResponse(x))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// closeRoyaltyPeriod closes one period of a royalty agreement and posts the minimum royalty
// shortfall, if any (FR-26.17). Closing a closed period answers 200 with alreadyClosed.
func (a *API) closeRoyaltyPeriod(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeContractNotFound, "royalty agreement")
	if !ok {
		return
	}
	var req royaltyCloseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !periodKey.MatchString(req.Period) {
		validationFailed(w, r, map[string]string{"period": claimPeriodDetail})
		return
	}
	res, err := a.store.CloseRoyaltyPeriod(r.Context(), id, req.Period)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	v := royaltyCloseView{AgreementID: res.AgreementID, PeriodID: res.PeriodKey, Accrued: res.Accrued.String(), MinimumRoyalty: res.Minimum.String(),
		Shortfall: res.Shortfall.String(), Currency: res.Currency, AlreadyClosed: res.AlreadyClosed}
	if res.Shortfall.IsPositive() {
		v.Notice = domain.CodeMinimumRoyaltyShortfall + ": minimum royalty shortfall " + res.Shortfall.String() + " " + res.Currency + " posted at period close"
	}
	if res.Accrual != nil {
		acc := contractAccrualResponse(*res.Accrual)
		v.Accrual = &acc
	}
	httpx.JSON(w, http.StatusOK, v)
}

func (a *API) previewBrokerClaim(w http.ResponseWriter, r *http.Request) {
	a.previewContractClaim(w, r, domain.ContractBroker, "broker contract")
}

func (a *API) previewRoyaltyClaim(w http.ResponseWriter, r *http.Request) {
	a.previewContractClaim(w, r, domain.ContractRoyalty, "royalty agreement")
}

func (a *API) previewContractClaim(w http.ResponseWriter, r *http.Request, kind, what string) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeContractNotFound, what)
	if !ok {
		return
	}
	period := r.URL.Query().Get("period")
	if !periodKey.MatchString(period) {
		validationFailed(w, r, map[string]string{"period": claimPeriodDetail})
		return
	}
	p, err := a.store.PreviewContractClaim(r.Context(), kind, id, period)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, contractClaimPreviewView{ContractKind: p.ContractKind, ContractID: p.ContractID, VendorID: p.VendorID, PeriodID: p.PeriodKey,
		Accrued: p.Accrued.String(), Shortfall: p.Shortfall.String(), PriorClaimed: p.PriorClaimed.String(), Amount: p.Amount.String(), Currency: p.Currency,
		Claims: p.Claims, Closed: p.Closed})
}

func validContractKind(kind string) bool {
	return kind == domain.ContractBroker || kind == domain.ContractRoyalty
}

func (a *API) createContractClaim(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req contractClaimRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !validContractKind(req.ContractKind) {
		fields["contractKind"] = "must be BROKER or ROYALTY"
	}
	if !ids.Valid(req.ContractID) {
		fields["contractId"] = "must be a UUID"
	}
	if !periodKey.MatchString(req.Period) {
		fields["period"] = claimPeriodDetail
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	c, err := a.store.CreateContractClaim(r.Context(), req.ContractKind, req.ContractID, req.Period, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("contract claim failed", "err", err, "contractId", req.ContractID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, contractClaimResponse(c))
}

// listContractClaims is the Broker and royalty claims inquiry (§5.26.14).
func (a *API) listContractClaims(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.ContractClaimFilter{Kind: q.Get("contractKind"), ContractID: q.Get("contractId"), VendorID: q.Get("vendorId"), PeriodKey: q.Get("period"),
		Status: q.Get("status"), Limit: store.MaxListLimit}
	fields := map[string]string{}
	if f.Kind != "" && !validContractKind(f.Kind) {
		fields["contractKind"] = "must be BROKER or ROYALTY"
	}
	validOptionalUUID(f.ContractID, "contractId", fields)
	validOptionalUUID(f.VendorID, "vendorId", fields)
	if f.PeriodKey != "" && !periodKey.MatchString(f.PeriodKey) {
		fields["period"] = claimPeriodDetail
	}
	if f.Status != "" && f.Status != domain.ClaimOpen && f.Status != domain.ClaimApproved {
		fields["status"] = "must be OPEN or APPROVED"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.ListContractClaims(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]contractClaimView, 0, len(rows))
	for _, c := range rows {
		items = append(items, contractClaimResponse(c))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getContractClaim(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, store.CodeClaimNotFound, "broker or royalty claim")
	if !ok {
		return
	}
	c, err := a.store.GetContractClaim(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, contractClaimResponse(c))
}

// approveContractClaim approves an OPEN claim at the body version for w19 AP (duty
// VendorRebateClaimApprove; the claim's creator cannot approve it).
func (a *API) approveContractClaim(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, store.CodeClaimNotFound, "broker or royalty claim")
	if !ok {
		return
	}
	version, ok := decodeVersion(w, r)
	if !ok {
		return
	}
	c, err := a.store.ApproveContractClaim(r.Context(), id, version, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, contractClaimResponse(c))
}
