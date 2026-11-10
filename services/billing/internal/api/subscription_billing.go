package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	// maxDuePeriodsPerSubscription bounds the periods one job run bills for one subscription (a year of monthly periods).
	maxDuePeriodsPerSubscription = 12
	maxDueSubscriptions          = 500
	maxMeterCodeLength           = 50
	maxExternalIDLength          = 100
)

// billPeriod invoices one billing period of a subscription as a SUBSCRIPTION invoice proposal: the prorated plan lines of the
// period plus the usage of the subscription not yet invoiced and dated up to the period's end, grouped by meter and unit price.
// A period is invoiced once; a repeat returns the stored proposal (replayed) and a different invoice date is refused. The usage
// is marked invoiced in the transaction that stores the proposal. Errors are problems ready to be written.
func (a *API) billPeriod(ctx context.Context, sub store.Subscription, periodFrom, invoiceDate time.Time) (store.Invoice, bool, error) {
	hash := hashOf(invoiceDate.Format(dateLayout))
	if existing, found, err := a.subscriptions.FindPeriodInvoice(ctx, sub.ID, periodFrom); err != nil {
		return store.Invoice{}, false, err
	} else if found {
		if existing.Type != domain.TypeSubscription {
			return store.Invoice{}, false, httpx.NewProblem(http.StatusConflict, domain.CodePeriodInvoiced, "this period is linked to an invoice that is not a subscription invoice")
		}
		if existing.RequestHash != hash {
			return store.Invoice{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, "idempotency_key_reuse", "this period was already invoiced with a different invoice date")
		}
		return existing, true, nil
	}
	billed, err := sub.Bill(periodFrom)
	if err != nil {
		return store.Invoice{}, false, problemFrom(err)
	}
	periodTo := sub.PeriodEnd(periodFrom)
	lines := make([]domain.Line, 0, len(billed))
	for _, b := range billed {
		lines = append(lines, domain.Line{No: len(lines) + 1, Description: fmt.Sprintf("%s %s to %s", b.PlanCode, b.From.Format(dateLayout), b.To.Format(dateLayout)),
			Qty: decimal.NewFromInt(1), UnitPrice: b.Amount, DiscountPct: decimal.Zero, RevenueAccount: sub.RevenueAccount, TaxCode: sub.TaxCode})
	}
	usage, err := a.subscriptions.ListUnbilledUsage(ctx, sub.ID, periodTo)
	if err != nil {
		return store.Invoice{}, false, err
	}
	usageIDs := make([]string, 0, len(usage))
	for _, g := range groupUsage(usage) {
		for _, u := range g.records {
			usageIDs = append(usageIDs, u.ID)
		}
		lines = append(lines, domain.Line{No: len(lines) + 1,
			Description: fmt.Sprintf("%s usage %s to %s", g.meter, g.from.Format(dateLayout), g.to.Format(dateLayout)),
			Qty:         g.quantity, UnitPrice: g.unitPrice, DiscountPct: decimal.Zero, RevenueAccount: sub.RevenueAccount, TaxCode: sub.TaxCode})
	}
	taxes, err := a.tax.Quote(ctx, sub.Currency, lines)
	if err != nil {
		return store.Invoice{}, false, taxProblem(err)
	}
	nets, totals, err := domain.Compute(sub.Currency, lines, taxes, decimal.Zero)
	if err != nil {
		return store.Invoice{}, false, problemFrom(err)
	}
	inv := store.Invoice{ID: ids.New(), LegalEntityID: sub.LegalEntityID, CustomerID: sub.CustomerID, InvoiceAccountID: sub.InvoiceAccountID,
		Type: domain.TypeSubscription, Status: domain.StatusProposal, Currency: sub.Currency, InvoiceDate: invoiceDate,
		DueDate: invoiceDate.AddDate(0, 0, sub.PaymentTermDays), PaymentTermDays: sub.PaymentTermDays, Totals: totals,
		IdempotencyKey: subscriptionKeyPrefix + ids.New(), RequestHash: hash}
	for i := range lines {
		inv.Lines = append(inv.Lines, store.Line{Line: lines[i], Net: nets[i], Tax: taxes[i].RoundBank(money.MinorUnits(sub.Currency))})
	}
	return a.subscriptions.BillSubscription(ctx, sub.ID, sub.Version, periodFrom, periodTo, inv, usageIDs)
}

type usageGroup struct {
	meter     string
	unitPrice decimal.Decimal
	quantity  decimal.Decimal
	from, to  time.Time
	records   []store.Usage
}

// groupUsage adds up the usage per meter and unit price, so a thousand readings are one line; groups come in meter order.
func groupUsage(in []store.Usage) []usageGroup {
	var out []usageGroup
	index := map[string]int{}
	for _, u := range in {
		key := u.MeterCode + "|" + u.UnitPrice.String()
		i, ok := index[key]
		if !ok {
			index[key] = len(out)
			out = append(out, usageGroup{meter: u.MeterCode, unitPrice: u.UnitPrice, from: u.UsageDate, to: u.UsageDate})
			i = len(out) - 1
		}
		g := &out[i]
		g.quantity = g.quantity.Add(u.Quantity)
		if u.UsageDate.Before(g.from) {
			g.from = u.UsageDate
		}
		if u.UsageDate.After(g.to) {
			g.to = u.UsageDate
		}
		g.records = append(g.records, u)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].meter != out[j].meter {
			return out[i].meter < out[j].meter
		}
		return out[i].unitPrice.LessThan(out[j].unitPrice)
	})
	return out
}

type usageRequest struct {
	ExternalID string `json:"externalId"`
	MeterCode  string `json:"meterCode"`
	UsageDate  string `json:"usageDate"`
	Quantity   string `json:"quantity"`
	UnitPrice  string `json:"unitPrice"`
}

type usageDTO struct {
	ID             string `json:"id"`
	SubscriptionID string `json:"subscriptionId"`
	ExternalID     string `json:"externalId"`
	MeterCode      string `json:"meterCode"`
	UsageDate      string `json:"usageDate"`
	Quantity       string `json:"quantity"`
	UnitPrice      string `json:"unitPrice"`
	InvoiceID      string `json:"invoiceId,omitempty"`
}

// recordUsage stores one metered quantity against a subscription; it is invoiced with the billing period it falls in (or the
// next period billed after it) as a usage line of the SUBSCRIPTION invoice (FR-29.10).
func (a *API) recordUsage(w http.ResponseWriter, r *http.Request) {
	if !a.subscriptionsReady(w, r) {
		return
	}
	var req usageRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if n := len(req.ExternalID); n < 1 || n > maxExternalIDLength {
		fields["externalId"] = "must be 1 to 100 characters, the meter reading's own id"
	}
	if n := len(req.MeterCode); n < 1 || n > maxMeterCodeLength {
		fields["meterCode"] = "must be 1 to 50 characters"
	}
	usageDate := parseDay(fields, "usageDate", req.UsageDate)
	qty, qerr := decimal.NewFromString(req.Quantity)
	if qerr != nil || !qty.IsPositive() || qty.Exponent() < -6 {
		fields["quantity"] = "must be a decimal greater than 0 with at most 6 fraction digits"
	}
	price, perr := decimal.NewFromString(req.UnitPrice)
	if perr != nil || price.IsNegative() || price.Exponent() < -6 {
		fields["unitPrice"] = "must be a decimal of 0 or more with at most 6 fraction digits"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	sub, ok := a.loadSubscription(w, r)
	if !ok {
		return
	}
	if !tenant.InLegalEntityScope(principalOf(r), sub.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	out, replayed, err := a.subscriptions.RecordUsage(r.Context(), sub.ID, store.Usage{ExternalID: req.ExternalID, MeterCode: req.MeterCode,
		UsageDate: usageDate, Quantity: qty, UnitPrice: price, RequestHash: hashOf(req)})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	httpx.JSON(w, status, usageDTO{ID: out.ID, SubscriptionID: out.SubscriptionID, ExternalID: out.ExternalID, MeterCode: out.MeterCode,
		UsageDate: out.UsageDate.Format(dateLayout), Quantity: out.Quantity.String(), UnitPrice: out.UnitPrice.String(), InvoiceID: out.InvoiceID})
}

// DueResult counts what one due-period run did.
type DueResult struct {
	Considered int      `json:"considered"`
	Billed     int      `json:"billed"`
	Failed     int      `json:"failed"`
	InvoiceIDs []string `json:"invoiceIds"`
}

// BillDueSubscriptions bills every billing period due on asOf for the subscriptions of a legal entity (FR-29.10, the daily
// job): for each subscription whose next period opens on or before asOf it creates the SUBSCRIPTION invoice proposal of that
// period, dated the period's first day, and goes on to the next due period, at most 12 per subscription and run. A period is
// invoiced once, so a repeat of the run bills nothing again; a subscription that cannot be billed (no billable amount, a
// refused tax quote) is counted as failed, logged, and the run goes on with the others. Posting the proposals stays with the
// billing workspace.
func (a *API) BillDueSubscriptions(ctx context.Context, legalEntityID string, asOf time.Time, limit int) (DueResult, error) {
	res := DueResult{InvoiceIDs: []string{}}
	if a.subscriptions == nil {
		return res, httpx.NewProblem(http.StatusServiceUnavailable, "BIL-0109", "subscriptions are not available in this deployment")
	}
	if limit < 1 || limit > maxDueSubscriptions {
		limit = maxDueSubscriptions
	}
	due, err := a.subscriptions.DueSubscriptions(ctx, legalEntityID, asOf, limit)
	if err != nil {
		return res, err
	}
	res.Considered = len(due)
	for _, id := range due {
		for i := 0; i < maxDuePeriodsPerSubscription; i++ {
			sub, err := a.subscriptions.GetSubscription(ctx, id)
			if err != nil {
				res.Failed++
				slog.ErrorContext(ctx, "due subscription not loaded", "subscription", id, "err", err, "correlationId", middleware.GetReqID(ctx))
				break
			}
			from, ok := nextPeriod(sub)
			if !ok || from.After(asOf) || (sub.CancelEffective != nil && from.After(*sub.CancelEffective)) {
				break
			}
			inv, replayed, err := a.billPeriod(ctx, sub, from, from)
			if err != nil {
				res.Failed++
				slog.ErrorContext(ctx, "due subscription period not billed", "subscription", id, "periodFrom", from.Format(dateLayout), "err", err, "correlationId", middleware.GetReqID(ctx))
				break
			}
			if replayed {
				break
			}
			a.subscriptionInvoices.Add(1)
			res.Billed++
			res.InvoiceIDs = append(res.InvoiceIDs, inv.ID)
		}
	}
	return res, nil
}

// nextPeriod is the first day of the period after the one billed last, or of the start month when none was billed.
func nextPeriod(sub store.Subscription) (time.Time, bool) {
	if !sub.BilledThrough.IsZero() {
		return sub.BilledThrough.AddDate(0, 0, 1), true
	}
	return time.Date(sub.Start.Year(), sub.Start.Month(), 1, 0, 0, 0, 0, time.UTC), true
}

type billDueRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	AsOf          string `json:"asOf"`
	Limit         int    `json:"limit"`
}

// billDue is POST /v1/billing/subscriptions:bill-due, the entry point of the due-period job (also run in-process by the
// worker of BILLING_SUBSCRIPTION_JOB_TENANT_IDS).
func (a *API) billDue(w http.ResponseWriter, r *http.Request) {
	if !a.subscriptionsReady(w, r) {
		return
	}
	var req billDueRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	asOf := a.now().UTC()
	asOf = time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	if req.AsOf != "" {
		asOf = parseDay(fields, "asOf", req.AsOf)
	}
	if req.Limit < 0 || req.Limit > maxDueSubscriptions {
		fields["limit"] = "must be 1 to 500"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !tenant.InLegalEntityScope(principalOf(r), req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	res, err := a.BillDueSubscriptions(r.Context(), req.LegalEntityID, asOf, req.Limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// SubscriptionLegalEntities lists the legal entities holding subscriptions, for the in-process job.
func (a *API) SubscriptionLegalEntities(ctx context.Context) ([]string, error) {
	if a.subscriptions == nil {
		return nil, nil
	}
	return a.subscriptions.SubscriptionLegalEntities(ctx)
}
