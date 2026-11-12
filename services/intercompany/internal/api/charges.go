package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/posting"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/taxclient"
)

type postChargeRequest struct {
	chargeRequest
	SourceID    string         `json:"sourceId"`
	Version     int            `json:"version"`
	PostingDate string         `json:"postingDate"`
	Entities    []entityTarget `json:"entities"`
}

type chargeRecordResponse struct {
	ID                  string `json:"id"`
	SourceID            string `json:"sourceId"`
	Version             int    `json:"version"`
	LendingEntity       string `json:"lendingEntity"`
	BorrowingEntity     string `json:"borrowingEntity"`
	Category            string `json:"category"`
	ResourceRole        string `json:"resourceRole,omitempty"`
	TransactionDate     string `json:"transactionDate"`
	Quantity            string `json:"quantity"`
	UnitCost            string `json:"unitCost"`
	Currency            string `json:"currency"`
	Status              string `json:"status"`
	TransferPriceMethod string `json:"transferPriceMethod,omitempty"`
	Cost                string `json:"cost,omitempty"`
	Price               string `json:"price,omitempty"`
	Margin              string `json:"margin,omitempty"`
	JournalID           string `json:"journalId,omitempty"`
	FailureCode         string `json:"failureCode,omitempty"`
	SettlementMode      string `json:"settlementMode,omitempty"`
	// Tax is the cross-border tax the charge carries (0 when none applies); TaxRecorded says the tax service has it.
	Tax         string    `json:"tax,omitempty"`
	TaxCode     string    `json:"taxCode,omitempty"`
	TaxRecorded bool      `json:"taxRecorded,omitempty"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

type chargeListResponse struct {
	Items      []chargeRecordResponse `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

func toChargeResponse(c domain.ChargeRecord) chargeRecordResponse {
	places := money.MinorUnits(c.Currency)
	out := chargeRecordResponse{ID: c.ID, SourceID: c.SourceID, Version: c.Version, LendingEntity: c.Lender, BorrowingEntity: c.Borrower, Category: c.Category,
		ResourceRole: c.Role, TransactionDate: c.TransactionDate.Format(dateLayout), Quantity: c.Quantity.String(), UnitCost: c.UnitCost.String(), Currency: c.Currency,
		Status: string(c.Status), TransferPriceMethod: c.Method, JournalID: c.JournalID, FailureCode: c.FailureCode, CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt.UTC()}
	if c.Status == domain.ChargePosted || c.Status == domain.ChargeCompensated || c.Status == domain.ChargeInvoiceRequested {
		out.Cost, out.Price, out.Margin = c.Cost.StringFixed(places), c.Price.StringFixed(places), c.Margin.StringFixed(places)
		out.SettlementMode = c.SettlementMode
		if c.TaxAmount.IsPositive() {
			out.Tax, out.TaxCode, out.TaxRecorded = c.TaxAmount.StringFixed(places), c.TaxCode, c.TaxRecorded
		}
	}
	return out
}

// postCharge prices an IC charge from the transfer prices sent with it, or the stored ones when it sent none, and settles it in the
// tenant's settlement mode (FR-40.13): Journal posts the legs to both entities, Invoice hands the charge to the pairing's invoice
// documents and posts nothing here. A charge between two countries carries the tax the tax service works out when the tenant has
// cross-border tax on. The charge is stored per sourceId and version: the same pair sent again replays the stored result, and a
// source with no applicable transfer price stays on suspense (202, IC-0821, BR-40.12) until the next version reprices it.
func (a *API) postCharge(w http.ResponseWriter, r *http.Request) {
	if !a.postingReady(w, r) {
		return
	}
	var req postChargeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := parseCharge(req.chargeRequest)
	f := fieldParser(fields)
	if f == nil {
		f = fieldParser{}
	}
	if len(req.TransferPrices) == 0 {
		delete(f, "transferPrices") // stored prices apply, and a source without any price goes to suspense
	}
	source := strings.TrimSpace(req.SourceID)
	if source == "" || len(source) > 128 {
		f["sourceId"] = "is required, at most 128 characters"
	}
	if req.Version < 1 {
		f["version"] = "must be 1 or higher"
	}
	date := f.date("postingDate", req.PostingDate)
	var sent map[string]domain.Target
	if len(req.Entities) > 0 || a.registry == nil {
		sent = parseTargets(req.Entities, f)
	}
	if !validation(w, r, f) {
		return
	}
	ctx := r.Context()
	targets, err := a.targetsFor(ctx, sent, in.lender, in.borrower)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if !targetsInScope(w, r, targets) {
		return
	}
	hash, err := store.HashOf("charge", req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rec, existing, err := a.charges.BeginCharge(ctx, domain.ChargeRecord{SourceID: source, Version: req.Version, Lender: in.lender, Borrower: in.borrower,
		Category: in.category, Role: in.role, TransactionDate: in.on, Quantity: in.quantity, UnitCost: in.unitCost, Currency: req.Currency}, hash)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if existing && rec.Status != domain.ChargePending {
		w.Header().Set("Idempotent-Replayed", "true")
		rec = a.retryTaxRecord(ctx, rec)
		a.respondCharge(w, rec)
		return
	}

	settings := domain.DefaultSettings
	if a.registry != nil {
		if settings, err = a.registry.GetSettings(ctx); err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		if len(in.prices) == 0 {
			if in.prices, err = a.registry.PricesFor(ctx, in.lender, in.borrower); err != nil {
				httpx.Fail(w, r, problemFrom(err))
				return
			}
		}
	}
	if in.matrix, err = a.matrixFor(ctx, in.matrix); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}

	tp, err := domain.ResolveTransferPrice(in.prices, in.lender, in.borrower, in.category, in.role, in.on)
	var de *domain.Error
	if errors.As(err, &de) && de.Code == domain.CodeTransferPrice {
		done, ferr := a.charges.FinishCharge(ctx, rec.ID, domain.ChargeSuspense, "", domain.ChargePricing{}, "", de.Code, domain.ChargeExtra{})
		if ferr != nil {
			httpx.Fail(w, r, problemFrom(ferr))
			return
		}
		a.chargesSuspended.Add(1)
		a.respondCharge(w, done)
		return
	}
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if tp.Currency != req.Currency {
		httpx.Fail(w, r, problemFrom(&domain.Error{Code: domain.CodeInvalid, Detail: "the transfer price in force is in " + tp.Currency + " and the charge in " + req.Currency}))
		return
	}
	pricing, err := domain.PriceWithTransfer(tp, in.quantity, in.unitCost)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	tax, taxRes, err := a.chargeTax(ctx, settings, in, pricing, rec.ID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	extra := domain.ChargeExtra{SettlementMode: settings.ChargeSettlementMode, TaxAmount: tax.Amount, TaxCode: tax.Code}

	if settings.ChargeSettlementMode == domain.SettlementInvoice {
		done, err := a.charges.FinishCharge(ctx, rec.ID, domain.ChargeInvoiceRequested, tp.Method, pricing, "", "", extra)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		a.chargesInvoiced.Add(1)
		a.respondCharge(w, done)
		return
	}

	j, err := domain.ChargeJournalWithTax(in.matrix, ids.New(), in.lender, in.borrower, req.CostAccount, req.RecoveryAccount, req.MarginAccount, pricing, tax)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	jrec, _, perr := a.poster.Post(ctx, posting.Input{Source: domain.SourceCharge, SourceRef: rec.ID, Key: "charge:" + rec.ID, Hash: hash, Journal: j, Targets: targets, PostingDate: date})
	status := domain.ChargePosted
	switch {
	case perr == nil && jrec.Status == domain.PostingCompensated:
		status = domain.ChargeCompensated
	case perr == nil:
	case errors.As(perr, &de) && de.Code == domain.CodeSecondSideFailed:
		status = domain.ChargeCompensated
	default:
		httpx.Fail(w, r, postProblem(jrec, perr))
		return
	}
	failure := ""
	if status == domain.ChargeCompensated {
		failure = domain.CodeSecondSideFailed
	}
	done, err := a.charges.FinishCharge(ctx, rec.ID, status, tp.Method, pricing, jrec.ID, failure, extra)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if status == domain.ChargeCompensated {
		httpx.Fail(w, r, postProblem(jrec, &domain.Error{Code: domain.CodeSecondSideFailed, Detail: jrec.FailureDetail + "; the entries already posted were reversed"}))
		return
	}
	a.chargesPosted.Add(1)
	if tax.Amount.IsPositive() {
		done = a.recordTax(ctx, done, taxRes)
	}
	a.respondCharge(w, done)
}

// chargeTax works out the cross-border tax of a charge: none unless the tenant has it on and the two entities are in different
// countries. The tax service being unreachable, or a tax account missing, stops the charge before anything is posted; the charge
// stays pending and the same request resumes it.
func (a *API) chargeTax(ctx context.Context, settings domain.Settings, in chargeInput, pricing domain.ChargePricing, chargeID string) (domain.ChargeTax, taxclient.Result, error) {
	if !settings.CrossBorderTax {
		return domain.ChargeTax{}, taxclient.Result{}, nil
	}
	reg, err := a.registryEntities(ctx)
	if err != nil {
		return domain.ChargeTax{}, taxclient.Result{}, err
	}
	lender, okL := reg[in.lender]
	borrower, okB := reg[in.borrower]
	if !okL || !okB {
		return domain.ChargeTax{}, taxclient.Result{}, &domain.Error{Code: domain.CodeEntityUnknown, Detail: "cross-border tax needs both entities, with their country, in the registry"}
	}
	if !domain.CrossBorder(lender, borrower) {
		return domain.ChargeTax{}, taxclient.Result{}, nil
	}
	if a.tax == nil {
		return domain.ChargeTax{}, taxclient.Result{}, &domain.Error{Code: domain.CodeTaxUnavailable, Detail: "the tax service is not configured, so the tax of a cross-border charge cannot be worked out; nothing was posted"}
	}
	res, err := a.tax.Calculate(ctx, taxclient.Input{LegalEntityID: lender.LegalEntityID, Currency: pricing.Price.Currency, Date: in.on, Country: borrower.Country,
		TaxGroup: borrower.TaxGroup, Amount: pricing.Price.Value, Ref: chargeID})
	if err != nil {
		return domain.ChargeTax{}, taxclient.Result{}, &domain.Error{Code: domain.CodeTaxUnavailable, Detail: "the tax service could not price the cross-border charge; nothing was posted, send the same request again to continue"}
	}
	amount := money.Amount{Value: res.Amount, Currency: pricing.Price.Currency}.Round().Value
	return domain.ChargeTax{Amount: amount, Code: res.Code, LenderAccount: lender.OutputTaxAccount, BorrowerAccount: borrower.InputTaxAccount}, taxclient.Result{Amount: amount, Code: res.Code}, nil
}

// recordTax gives the tax service the transaction of a posted cross-border charge. A failure is logged and left for the next replay
// of the request: the charge is posted either way.
func (a *API) recordTax(ctx context.Context, c domain.ChargeRecord, res taxclient.Result) domain.ChargeRecord {
	if a.tax == nil || c.TaxRecorded || !c.TaxAmount.IsPositive() {
		return c
	}
	reg, err := a.registryEntities(ctx)
	if err != nil || reg[c.Lender].LegalEntityID == "" {
		slog.WarnContext(ctx, "intercompany cannot record the tax of a posted charge", "charge", c.ID, "err", err)
		return c
	}
	if res.Code == "" {
		res = taxclient.Result{Amount: c.TaxAmount, Code: c.TaxCode}
	}
	if err := a.tax.Record(ctx, reg[c.Lender].LegalEntityID, c.ID, c.TransactionDate, res); err != nil {
		slog.WarnContext(ctx, "intercompany could not record the tax of a posted charge; a repeat of the request tries again", "charge", c.ID, "err", err)
		return c
	}
	if err := a.charges.MarkTaxRecorded(ctx, c.ID); err != nil {
		slog.WarnContext(ctx, "intercompany could not note the recorded tax", "charge", c.ID, "err", err)
		return c
	}
	c.TaxRecorded = true
	return c
}

// retryTaxRecord records the tax of a posted charge whose first attempt failed.
func (a *API) retryTaxRecord(ctx context.Context, c domain.ChargeRecord) domain.ChargeRecord {
	if c.Status != domain.ChargePosted || c.TaxRecorded || !c.TaxAmount.IsPositive() {
		return c
	}
	return a.recordTax(ctx, c, taxclient.Result{})
}

func (a *API) respondCharge(w http.ResponseWriter, c domain.ChargeRecord) {
	w.Header().Set("Location", "/v1/interco/charges/"+c.ID)
	switch c.Status {
	case domain.ChargeSuspense:
		httpx.JSON(w, http.StatusAccepted, toChargeResponse(c))
	default:
		httpx.JSON(w, http.StatusCreated, toChargeResponse(c))
	}
}

func (a *API) listCharges(w http.ResponseWriter, r *http.Request) {
	if !a.postingReady(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	filter := store.ChargeFilter{Status: q.Get("status"), SourceID: strings.TrimSpace(q.Get("sourceId"))}
	switch filter.Status {
	case "", "pending", "posted", "compensated", "suspense", "invoice_requested":
	default:
		fields["status"] = "must be pending, posted, compensated, suspense or invoice_requested"
	}
	after, limit := pageParams(w, r, fields)
	if !validation(w, r, fields) {
		return
	}
	page, err := a.charges.ListCharges(r.Context(), filter, after, limit)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := chargeListResponse{Items: make([]chargeRecordResponse, 0, len(page.Items)), NextCursor: page.Next}
	for _, c := range page.Items {
		out.Items = append(out.Items, toChargeResponse(c))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getCharge(w http.ResponseWriter, r *http.Request) {
	if !a.postingReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeChargeNotFound, "no intercompany charge with id "+id))
		return
	}
	c, err := a.charges.GetCharge(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toChargeResponse(c))
}
