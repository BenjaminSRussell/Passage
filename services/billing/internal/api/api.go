// Package api serves the billing HTTP surface: free-text invoice proposals
// with optional installment plans, invoice proposals from posted packing slips
// (one per order or consolidated), their posting (tax, gap-free number,
// invoice.posted), credit notes against posted invoices, invoice reads and the
// invoice audit trace.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/revrec"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	idempotencyKey = "Idempotency-Key"

	subscriptionKeyPrefix = "subscription-"
	idempotencyKeyDetail  = `Idempotency-Key header is required (1 to 255 characters, not starting with "subscription-")`
	validationDetail      = "request failed validation"
	dateLayout            = "2006-01-02"
	maxLinesCeiling       = 10000
	readyTimeout          = 500 * time.Millisecond
	defaultListLimit      = 100
	maxListLimit          = 200
	maxAccountLength      = 20
	maxReasonLength       = 200
	maxInstallments       = 24
	maxOffsetDays         = 365
	// Duties (§5.29.17): InvoiceView, InvoiceMaintain, InvoicePost, CreditNotePost.
	dutyView       = "billing.invoice.view"
	dutyMaintain   = "billing.invoice.maintain"
	dutyPost       = "billing.invoice.post"
	dutyCreditPost = "billing.creditnote.post"
	dutyReserve    = "billing.credit-reservation.write"
	// CodeTaxUnavailable is BIL-0001: the tax engine did not answer, so nothing posts.
	CodeTaxUnavailable = "BIL-0001"
	// CodeTaxMoved is BIL-0002: the tax re-quote at post moved beyond tolerance.
	CodeTaxMoved = "BIL-0002"
	// CodeUnknownTaxCode is BIL-0102: a line's tax code is not known to the tax engine.
	CodeUnknownTaxCode = "BIL-0102"
)

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	decimalText  = regexp.MustCompile(`^[0-9]{1,14}(\.[0-9]{1,6})?$`)
	accountText  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)
	statuses     = map[string]bool{domain.StatusProposal: true, domain.StatusPosted: true, domain.StatusPartiallySettled: true, domain.StatusSettled: true, domain.StatusReversed: true}
	types        = map[string]bool{domain.TypeShipment: true, domain.TypeConsolidated: true, domain.TypeFreeText: true, domain.TypeSubscription: true, domain.TypeMilestone: true, domain.TypeCreditNote: true}
)

// Store is the persistence the handlers need; store.Postgres implements it.
type Store interface {
	Ping(ctx context.Context) error
	FindByIdempotencyKey(ctx context.Context, key string) (store.Invoice, bool, error)
	CreateProposal(ctx context.Context, inv store.Invoice) (store.Invoice, error)
	CreateCreditNote(ctx context.Context, cn store.Invoice) (store.Invoice, error)
	FindProposalBatch(ctx context.Context, key string) (store.ProposalBatch, []store.Invoice, bool, error)
	CreateShipmentProposals(ctx context.Context, b store.ProposalBatch, invs []store.Invoice) ([]store.Invoice, error)
	GetInvoice(ctx context.Context, id string) (store.Invoice, error)
	ListInvoices(ctx context.Context, f store.ListFilter) (store.InvoicePage, error)
	PostProposal(ctx context.Context, id string, postingDate time.Time, expectedVersion int) (store.Invoice, error)
	PostCreditNote(ctx context.Context, id string, postingDate time.Time, expectedVersion int) (store.Invoice, error)
	ReserveCredit(ctx context.Context, res store.CreditReservation, req []domain.CreditQty, charges *decimal.Decimal) (store.CreditReservation, bool, error)
	CommitCredit(ctx context.Context, id string) (store.CreditReservation, error)
	ReleaseCredit(ctx context.Context, id string) (store.CreditReservation, bool, error)
}

// API holds the dependencies of the handlers and their counters.
type API struct {
	store         Store
	subscriptions SubscriptionStore
	tax           TaxEngine
	maxLines      int
	now           func() time.Time
	ledger        ledgerclient.Ledger
	glBooks       GLBooks
	glBatch       int
	profiles      map[string]store.GLProfile
	allocations   revrec.Reader

	proposalsCreated   atomic.Int64
	invoicesPosted     atomic.Int64
	postsRefused       atomic.Int64
	creditNotesCreated atomic.Int64
	creditNotesPosted  atomic.Int64
	reservationsMade   atomic.Int64
	reservationsFreed  atomic.Int64
	shipmentProposals  atomic.Int64

	subscriptionsCreated atomic.Int64
	subscriptionInvoices atomic.Int64
	glPostings           atomic.Int64
}

// New returns the handlers. maxLines bounds a proposal (BILLING_MAX_LINES).
func New(s Store, tax TaxEngine, maxLines int) *API {
	return &API{store: s, tax: tax, maxLines: maxLines, now: time.Now, glBatch: defaultGLBatch}
}

// Register mounts the billing routes on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/billing/free-text-invoices", a.createProposal)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/billing/proposals", a.createShipmentProposals)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/billing/invoices", a.listInvoices)
	r.With(authz.RequireAnyDuty(dutyView, "billing.invoice.read")).Get("/v1/billing/invoices/{id}", a.getInvoice)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/billing/invoices/{id}/trace", a.invoiceTrace)
	r.With(authz.RequireDuty(dutyPost)).Post("/v1/billing/invoices/{id}:post", a.postInvoice)
	r.With(authz.RequireDuty(dutyPost)).Post("/v1/billing/gl-postings", a.postGL)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/billing/credit-notes", a.createCreditNote)
	r.With(authz.RequireDuty(dutyCreditPost)).Post("/v1/billing/credit-notes/{id}:post", a.postCreditNote)
	r.With(authz.RequireDuty(dutyReserve)).Post("/v1/billing/invoices/{id}/credit-reservations", a.createCreditReservation)
	r.With(authz.RequireDuty(dutyReserve)).Post("/v1/billing/credit-reservations/{id}:commit", a.commitCreditReservation)
	r.With(authz.RequireDuty(dutyReserve)).Post("/v1/billing/credit-reservations/{id}:release", a.releaseCreditReservation)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/billing/subscriptions", a.createSubscription)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/billing/subscriptions/{id}", a.getSubscription)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/billing/subscriptions/{id}:change-plan", a.changePlan)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/billing/subscriptions/{id}:cancel", a.cancelSubscription)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/billing/subscriptions/{id}:bill", a.billSubscription)
}

// Metrics serves the counters in the Prometheus text format on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP billing_proposals_created_total Invoice proposals created; idempotent replays are not counted.\n# TYPE billing_proposals_created_total counter\nbilling_proposals_created_total %d\n", a.proposalsCreated.Load())
		fmt.Fprintf(w, "# HELP billing_invoices_posted_total Invoices posted with a number.\n# TYPE billing_invoices_posted_total counter\nbilling_invoices_posted_total %d\n", a.invoicesPosted.Load())
		fmt.Fprintf(w, "# HELP billing_posts_refused_total Posts refused because tax was unavailable, unknown or moved.\n# TYPE billing_posts_refused_total counter\nbilling_posts_refused_total %d\n", a.postsRefused.Load())
		fmt.Fprintf(w, "# HELP billing_credit_notes_created_total Credit note proposals created; idempotent replays are not counted.\n# TYPE billing_credit_notes_created_total counter\nbilling_credit_notes_created_total %d\n", a.creditNotesCreated.Load())
		fmt.Fprintf(w, "# HELP billing_credit_notes_posted_total Credit notes posted with a number.\n# TYPE billing_credit_notes_posted_total counter\nbilling_credit_notes_posted_total %d\n", a.creditNotesPosted.Load())
		fmt.Fprintf(w, "# HELP billing_credit_reservations_created_total Credit reservations taken from invoices; replays and refusals are not counted.\n# TYPE billing_credit_reservations_created_total counter\nbilling_credit_reservations_created_total %d\n", a.reservationsMade.Load())
		fmt.Fprintf(w, "# HELP billing_credit_reservations_released_total Credit reservations released.\n# TYPE billing_credit_reservations_released_total counter\nbilling_credit_reservations_released_total %d\n", a.reservationsFreed.Load())
		fmt.Fprintf(w, "# HELP billing_shipment_proposals_created_total Shipment and consolidated invoice proposals created from packing slips; replays are not counted.\n# TYPE billing_shipment_proposals_created_total counter\nbilling_shipment_proposals_created_total %d\n", a.shipmentProposals.Load())
		fmt.Fprintf(w, "# HELP billing_subscriptions_created_total Subscriptions created; idempotent replays are not counted.\n# TYPE billing_subscriptions_created_total counter\nbilling_subscriptions_created_total %d\n", a.subscriptionsCreated.Load())
		fmt.Fprintf(w, "# HELP billing_subscription_invoices_created_total Prorated subscription invoice proposals created; replays are not counted.\n# TYPE billing_subscription_invoices_created_total counter\nbilling_subscription_invoices_created_total %d\n", a.subscriptionInvoices.Load())
		fmt.Fprintf(w, "# HELP billing_gl_postings_total Invoice and credit note journals booked in the general ledger.\n# TYPE billing_gl_postings_total counter\nbilling_gl_postings_total %d\n", a.glPostings.Load())
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type lineDTO struct {
	Description    string `json:"description"`
	Qty            string `json:"qty"`
	UnitPrice      string `json:"unitPrice"`
	DiscountPct    string `json:"discountPct,omitempty"`
	RevenueAccount string `json:"revenueAccount"`
	TaxCode        string `json:"taxCode"`
}

// installmentTermDTO is one installment of the payment terms: a percentage of
// the gross due offsetDays after the invoice date (FR-29.18). Until the wave-7
// terms client is wired, the plan is sent with the proposal.
type installmentTermDTO struct {
	Percent    string `json:"percent"`
	OffsetDays int    `json:"offsetDays"`
}

type proposalRequest struct {
	LegalEntityID    string               `json:"legalEntityId"`
	CustomerID       string               `json:"customerId"`
	InvoiceAccountID string               `json:"invoiceAccountId"`
	Currency         string               `json:"currency"`
	InvoiceDate      string               `json:"invoiceDate"`
	PaymentTermDays  int                  `json:"paymentTermDays"`
	ChargeAmount     string               `json:"chargeAmount,omitempty"`
	InstallmentTerms []installmentTermDTO `json:"installmentTerms,omitempty"`
	Lines            []lineDTO            `json:"lines"`
}

type totalsDTO struct {
	Net     string `json:"net"`
	Charges string `json:"charges"`
	Tax     string `json:"tax"`
	Gross   string `json:"gross"`
}

type lineOutDTO struct {
	LineNo         int    `json:"lineNo"`
	Description    string `json:"description"`
	Qty            string `json:"qty"`
	UnitPrice      string `json:"unitPrice"`
	DiscountPct    string `json:"discountPct"`
	Net            string `json:"net"`
	Tax            string `json:"tax"`
	RevenueAccount string `json:"revenueAccount"`
	TaxCode        string `json:"taxCode"`
	OriginalLineNo int    `json:"originalLineNo,omitempty"`
	CreditedQty    string `json:"creditedQty,omitempty"`
	CreditedTax    string `json:"creditedTax,omitempty"`
	ReservedQty    string `json:"reservedQty,omitempty"`
	// Source references, set on shipment and consolidated invoice lines (FR-29.1).
	PackingSlipID     string `json:"packingSlipId,omitempty"`
	PackingSlipLineID string `json:"packingSlipLineId,omitempty"`
	OrderID           string `json:"orderId,omitempty"`
	OrderLineID       string `json:"orderLineId,omitempty"`
	ProductID         string `json:"productId,omitempty"`
	PriceExplainRef   string `json:"priceExplainRef,omitempty"`
}

type installmentOutDTO struct {
	No         int    `json:"no"`
	Percent    string `json:"percent"`
	OffsetDays int    `json:"offsetDays"`
	Amount     string `json:"amount"`
	DueDate    string `json:"dueDate"`
}

// creditedDTO is what posted credit notes took from an invoice and what is left.
type creditedDTO struct {
	Gross     string `json:"gross"`
	Reserved  string `json:"reserved"`
	Remaining string `json:"remaining"`
}

type summaryDTO struct {
	ID               string    `json:"id"`
	LegalEntityID    string    `json:"legalEntityId"`
	CustomerID       string    `json:"customerId"`
	InvoiceAccountID string    `json:"invoiceAccountId"`
	Type             string    `json:"type"`
	Status           string    `json:"status"`
	Number           string    `json:"number,omitempty"`
	Currency         string    `json:"currency"`
	InvoiceDate      string    `json:"invoiceDate"`
	DueDate          string    `json:"dueDate"`
	PostingDate      string    `json:"postingDate,omitempty"`
	PaymentTermDays  int       `json:"paymentTermDays"`
	Totals           totalsDTO `json:"totals"`
	// OriginalInvoiceID and OriginalNumber are set on a credit note.
	OriginalInvoiceID string `json:"originalInvoiceId,omitempty"`
	OriginalNumber    string `json:"originalNumber,omitempty"`
	Version           int    `json:"version"`
}

type invoiceDTO struct {
	summaryDTO
	Reason       string              `json:"reason,omitempty"`
	Lines        []lineOutDTO        `json:"lines"`
	Installments []installmentOutDTO `json:"installments"`
	Credited     *creditedDTO        `json:"credited,omitempty"`
}

type listDTO struct {
	Items      []summaryDTO `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

func toSummary(inv store.Invoice) summaryDTO {
	exp := money.MinorUnits(inv.Currency)
	out := summaryDTO{ID: inv.ID, LegalEntityID: inv.LegalEntityID, CustomerID: inv.CustomerID, InvoiceAccountID: inv.InvoiceAccountID,
		Type: inv.Type, Status: inv.Status, Number: inv.Number, Currency: inv.Currency, InvoiceDate: inv.InvoiceDate.Format(dateLayout),
		DueDate: inv.DueDate.Format(dateLayout), PaymentTermDays: inv.PaymentTermDays, Version: inv.Version,
		OriginalInvoiceID: inv.OriginalInvoiceID, OriginalNumber: inv.OriginalNumber,
		Totals: totalsDTO{Net: fixed(inv.Totals.Net, exp), Charges: fixed(inv.Totals.Charges, exp), Tax: fixed(inv.Totals.Tax, exp), Gross: fixed(inv.Totals.Gross, exp)}}
	if !inv.PostingDate.IsZero() {
		out.PostingDate = inv.PostingDate.Format(dateLayout)
	}
	return out
}

func toDTO(inv store.Invoice) invoiceDTO {
	exp := money.MinorUnits(inv.Currency)
	out := invoiceDTO{summaryDTO: toSummary(inv), Reason: inv.Reason, Lines: []lineOutDTO{}, Installments: []installmentOutDTO{}}
	creditable := inv.Type != domain.TypeCreditNote && domain.CanBeCredited(inv.Status, inv.Type) == nil
	for _, l := range inv.Lines {
		line := lineOutDTO{LineNo: l.No, Description: l.Description, Qty: l.Qty.String(), UnitPrice: l.UnitPrice.String(),
			DiscountPct: l.DiscountPct.String(), Net: fixed(l.Net, exp), Tax: fixed(l.Tax, exp), RevenueAccount: l.RevenueAccount,
			TaxCode: l.TaxCode, OriginalLineNo: l.OriginalLineNo, PackingSlipID: l.Source.PackingSlipID,
			PackingSlipLineID: l.Source.PackingSlipLineID, OrderID: l.Source.OrderID, OrderLineID: l.Source.OrderLineID,
			ProductID: l.Source.ProductID, PriceExplainRef: l.Source.PriceExplainRef}
		if creditable {
			line.CreditedQty = decimal.Zero.String()
			if c, ok := inv.Credited.Lines[l.No]; ok {
				line.CreditedQty = c.Qty.Sub(c.ReservedQty).String()
				line.CreditedTax = fixed(c.Tax.Sub(c.ReservedTax), exp)
				line.ReservedQty = c.ReservedQty.String()
			}
		}
		out.Lines = append(out.Lines, line)
	}
	for _, in := range inv.Installments {
		out.Installments = append(out.Installments, installmentOutDTO{No: in.No, Percent: in.Term.Percent.String(), OffsetDays: in.Term.OffsetDays,
			Amount: fixed(in.Amount, exp), DueDate: in.DueDate.Format(dateLayout)})
	}
	if creditable {
		used := inv.Credited
		out.Credited = &creditedDTO{Gross: fixed(used.Gross.Sub(used.Reserved), exp), Reserved: fixed(used.Reserved, exp), Remaining: fixed(inv.Totals.Gross.Sub(used.Gross), exp)}
	}
	return out
}

func fixed(v decimal.Decimal, exp int32) string { return v.StringFixed(exp) }

func invalidIdempotencyKey(key string) bool {
	return key == "" || len(key) > 255 || strings.HasPrefix(key, subscriptionKeyPrefix)
}

func (a *API) createProposal(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get(idempotencyKey)
	if invalidIdempotencyKey(key) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", idempotencyKeyDetail))
		return
	}
	var req proposalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	inv, lines, charges, fields := a.validate(req)
	terms := toTerms(req.InstallmentTerms, fields)
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if existing, found, err := a.store.FindByIdempotencyKey(r.Context(), key); err != nil {
		httpx.Fail(w, r, err)
		return
	} else if found {
		if existing.RequestHash != hash {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "idempotency_key_reuse", "Idempotency-Key was already used with a different request body"))
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("ETag", etag(existing.Version))
		httpx.JSON(w, http.StatusOK, toDTO(existing))
		return
	}
	taxes, err := a.tax.Quote(r.Context(), inv.Currency, lines)
	if err != nil {
		httpx.Fail(w, r, taxProblem(err))
		return
	}
	nets, totals, err := domain.Compute(inv.Currency, lines, taxes, charges)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	for i := range lines {
		inv.Lines = append(inv.Lines, store.Line{Line: lines[i], Net: nets[i], Tax: taxes[i].RoundBank(money.MinorUnits(inv.Currency))})
	}
	inv.Totals = totals
	if len(terms) > 0 {
		split, err := domain.SplitInstallments(inv.Currency, totals.Gross, inv.InvoiceDate, terms)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		for i, in := range split {
			inv.Installments = append(inv.Installments, store.Installment{Installment: in, Term: terms[i]})
		}
		// The whole gross is due by the last installment.
		inv.DueDate = split[len(split)-1].DueDate
	}
	inv.IdempotencyKey = key
	inv.RequestHash = hash
	created, err := a.store.CreateProposal(r.Context(), inv)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.proposalsCreated.Add(1)
	w.Header().Set("ETag", etag(created.Version))
	httpx.JSON(w, http.StatusCreated, toDTO(created))
}

// validate checks the request and returns the invoice shell, domain lines and
// header charges. Field problems are returned in fields.
func (a *API) validate(req proposalRequest) (store.Invoice, []domain.Line, decimal.Decimal, map[string]string) {
	fields := map[string]string{}
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "customerId": req.CustomerID, "invoiceAccountId": req.InvoiceAccountID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	invoiceDate, err := time.Parse(dateLayout, req.InvoiceDate)
	if err != nil {
		fields["invoiceDate"] = "must be a YYYY-MM-DD date"
	}
	if req.PaymentTermDays < 0 || req.PaymentTermDays > 365 {
		fields["paymentTermDays"] = "must be 0 to 365"
	}
	charges := decimal.Zero
	if req.ChargeAmount != "" {
		if !decimalText.MatchString(req.ChargeAmount) {
			fields["chargeAmount"] = "must be a non-negative decimal with at most 6 fraction digits"
		} else {
			charges = decimal.RequireFromString(req.ChargeAmount)
		}
	}
	if n := len(req.Lines); n == 0 || n > a.maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", a.maxLines)
	}
	lines := make([]domain.Line, 0, len(req.Lines))
	for i, l := range req.Lines {
		lines = append(lines, toLine(i, l, fields))
	}
	inv := store.Invoice{ID: ids.New(), LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID, InvoiceAccountID: req.InvoiceAccountID,
		Type: domain.TypeFreeText, Status: domain.StatusProposal, Currency: req.Currency, InvoiceDate: invoiceDate,
		DueDate: invoiceDate.AddDate(0, 0, req.PaymentTermDays), PaymentTermDays: req.PaymentTermDays}
	return inv, lines, charges, fields
}

// toTerms converts the installment plan, recording each problem under its
// field name. The percentages' total is checked by SplitInstallments (BIL-0011).
func toTerms(in []installmentTermDTO, fields map[string]string) []domain.InstallmentTerm {
	if len(in) > maxInstallments {
		fields["installmentTerms"] = fmt.Sprintf("must hold at most %d installments", maxInstallments)
		return nil
	}
	out := make([]domain.InstallmentTerm, 0, len(in))
	for i, t := range in {
		field := func(name string) string { return fmt.Sprintf("installmentTerms[%d].%s", i, name) }
		term := domain.InstallmentTerm{OffsetDays: t.OffsetDays}
		if !decimalText.MatchString(t.Percent) || !decimal.RequireFromString(t.Percent).IsPositive() || decimal.RequireFromString(t.Percent).GreaterThan(decimal.NewFromInt(100)) {
			fields[field("percent")] = "must be a percentage above 0 and at most 100"
		} else {
			term.Percent = decimal.RequireFromString(t.Percent)
		}
		if t.OffsetDays < 0 || t.OffsetDays > maxOffsetDays {
			fields[field("offsetDays")] = fmt.Sprintf("must be 0 to %d days", maxOffsetDays)
		}
		if i > 0 && t.OffsetDays < in[i-1].OffsetDays {
			fields[field("offsetDays")] = "must not fall before the previous installment"
		}
		out = append(out, term)
	}
	return out
}

// toLine converts line i, recording each problem under its field name.
func toLine(i int, l lineDTO, fields map[string]string) domain.Line {
	return toLineAt(fmt.Sprintf("lines[%d]", i), i+1, l, fields)
}

// toLineAt converts one line numbered no whose request fields sit under prefix.
func toLineAt(prefix string, no int, l lineDTO, fields map[string]string) domain.Line {
	field := func(name string) string { return prefix + "." + name }
	out := domain.Line{No: no, Description: strings.TrimSpace(l.Description), RevenueAccount: l.RevenueAccount, TaxCode: l.TaxCode, DiscountPct: decimal.Zero}
	parse := func(name, raw string, dst *decimal.Decimal) bool {
		if !decimalText.MatchString(raw) {
			fields[field(name)] = "must be a non-negative decimal with at most 6 fraction digits"
			return false
		}
		*dst = decimal.RequireFromString(raw)
		return true
	}
	ok := parse("qty", l.Qty, &out.Qty)
	ok = parse("unitPrice", l.UnitPrice, &out.UnitPrice) && ok
	if l.DiscountPct != "" {
		ok = parse("discountPct", l.DiscountPct, &out.DiscountPct) && ok
	}
	switch {
	case l.RevenueAccount == "":
		fields[field("revenueAccount")] = "is required (BIL-0004)"
		ok = false
	case len(l.RevenueAccount) > maxAccountLength || !accountText.MatchString(l.RevenueAccount):
		fields[field("revenueAccount")] = "must be a ledger account of up to 20 letters, digits, dots, dashes or underscores"
		ok = false
	}
	if !taxCodeText.MatchString(l.TaxCode) {
		fields[field("taxCode")] = "must be a tax code of up to 20 capital letters, digits, dashes or underscores"
		ok = false
	}
	if !ok {
		return out
	}
	if err := out.Validate(); err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			fields[field(lineField(de.Detail))] = de.Detail
		}
	}
	return out
}

// lineField names the request field a domain line rule refers to.
func lineField(detail string) string {
	switch {
	case strings.Contains(detail, "description"):
		return "description"
	case strings.Contains(detail, "quantity"):
		return "qty"
	case strings.Contains(detail, "discount"):
		return "discountPct"
	case strings.Contains(detail, "price"):
		return "unitPrice"
	}
	return "line"
}

// listInvoices lists invoice headers newest first (§5.29.14 invoice register),
// filtered by status, type, customer and original invoice. A page holds at most
// `limit` items; nextCursor, when present, is passed back as `cursor`.
func (a *API) listInvoices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ListFilter{Status: q.Get("status"), Type: q.Get("type"), CustomerID: q.Get("customerId"),
		OriginalInvoiceID: q.Get("originalInvoiceId"), Limit: defaultListLimit}
	fields := map[string]string{}
	if f.Status != "" && !statuses[f.Status] {
		fields["status"] = "must be PROPOSAL, POSTED, PARTIALLY_SETTLED, SETTLED or REVERSED"
	}
	if f.Type != "" && !types[f.Type] {
		fields["type"] = "must be SHIPMENT, CONSOLIDATED, FREE_TEXT, SUBSCRIPTION, MILESTONE or CREDIT_NOTE"
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if f.OriginalInvoiceID != "" && !ids.Valid(f.OriginalInvoiceID) {
		fields["originalInvoiceId"] = "must be a UUID"
	}
	if raw := q.Get("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || !ids.Valid(string(b)) {
			fields["cursor"] = "must be a nextCursor returned by a previous page"
		}
		f.After = strings.ToLower(string(b))
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxListLimit {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxListLimit)
		}
		f.Limit = n
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	page, err := a.store.ListInvoices(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := listDTO{Items: make([]summaryDTO, 0, len(page.Items))}
	for _, inv := range page.Items {
		out.Items = append(out.Items, toSummary(inv))
	}
	if page.NextAfter != "" {
		out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.NextAfter))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "invoice not found"))
		return
	}
	inv, err := a.store.GetInvoice(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(inv.Version))
	httpx.JSON(w, http.StatusOK, toDTO(inv))
}

// postInvoice re-quotes tax, refuses a move beyond tolerance (BIL-0002) and
// posts. Posting an already posted invoice returns it unchanged, so a retried
// post is idempotent (the PRD's key = invoice id). An If-Match header, when
// sent, must carry the version the caller reviewed (412 BIL-0103 otherwise).
func (a *API) postInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "invoice not found"))
		return
	}
	expected, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", `If-Match must be the invoice version as an entity tag, for example "1"`))
		return
	}
	inv, err := a.store.GetInvoice(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if inv.Type == domain.TypeCreditNote {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeNotProposal, "a credit note posts through /v1/billing/credit-notes/{id}:post"))
		return
	}
	if inv.Status == domain.StatusPosted {
		w.Header().Set("ETag", etag(inv.Version))
		httpx.JSON(w, http.StatusOK, toDTO(inv))
		return
	}
	if inv.Status != domain.StatusProposal {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeNotProposal, "invoice is not a proposal and cannot be posted"))
		return
	}
	if expected != 0 && expected != inv.Version {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersion, "the invoice changed since it was read; reload and review it"))
		return
	}
	lines := make([]domain.Line, len(inv.Lines))
	for i, l := range inv.Lines {
		lines[i] = l.Line
	}
	taxes, err := a.tax.Quote(r.Context(), inv.Currency, lines)
	if err != nil {
		a.postsRefused.Add(1)
		httpx.Fail(w, r, taxProblem(err))
		return
	}
	requoted := decimal.Zero
	for _, t := range taxes {
		requoted = requoted.Add(t.RoundBank(money.MinorUnits(inv.Currency)))
	}
	if domain.TaxMoved(inv.Totals.Tax, requoted) {
		a.postsRefused.Add(1)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeTaxMoved,
			fmt.Sprintf("Tax changed by %s since proposal; review required", requoted.Sub(inv.Totals.Tax).String())))
		return
	}
	posted, err := a.store.PostProposal(r.Context(), id, a.now().UTC().Truncate(24*time.Hour), inv.Version)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.invoicesPosted.Add(1)
	w.Header().Set("ETag", etag(posted.Version))
	httpx.JSON(w, http.StatusOK, toDTO(posted))
}

// parseIfMatch reads `"3"`, `W/"3"` or `3`; an absent header returns 0 (no precondition).
func parseIfMatch(raw string) (int, bool) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "W/")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(strings.Trim(raw, `"`))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func etag(version int) string { return strconv.Quote(strconv.Itoa(version)) }

func taxProblem(err error) error {
	var unknown *UnknownTaxCodeError
	switch {
	case errors.Is(err, ErrTaxUnavailable):
		return httpx.NewProblem(http.StatusServiceUnavailable, CodeTaxUnavailable, "Tax engine unavailable; invoice not posted")
	case errors.As(err, &unknown):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnknownTaxCode, unknown.Error())
	}
	return err
}

func problemFrom(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	status := http.StatusUnprocessableEntity
	switch de.Code {
	case "COM-0001":
		status = http.StatusBadRequest
	case CodeTaxUnavailable:
		status = http.StatusServiceUnavailable
	}
	return httpx.NewProblem(status, de.Code, de.Detail)
}

func writeFields(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// ParseMaxLines validates the BILLING_MAX_LINES setting (1 to 10000).
func ParseMaxLines(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxLinesCeiling {
		return 0, fmt.Errorf("BILLING_MAX_LINES must be an integer from 1 to %d, got %q", maxLinesCeiling, raw)
	}
	return n, nil
}
