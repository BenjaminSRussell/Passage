package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/internal/taxrate"
)

// Self-billing intake (#35, 35-5). A customer's INVOIC 389 arrives on the normal EDIFACT route and is mapped like any INVOIC
// with selfBilled set. ReconcileSelfBilled matches its lines to the shipments the 856 or DESADV of this partner registered
// (BR-35.15), opens a dispute for every difference or unmatched line, and marks the message POSTED. The match runs right after
// receipt and again from POST /self-billing:sweep for anything that missed it, together with the unbilled sweep (EDI-0138).

var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

// SelfBillPrincipal is the service principal the tax rate call runs as, in the legal entity of the partner.
const SelfBillPrincipal = "edi-selfbill"

const (
	CodeSelfBillNoProfile = "EDI-0025"
	CodeDisputeResolved   = "EDI-0026"
)

type selfBillConfigBody struct {
	QuantityTolerance string `json:"quantityTolerance"`
	PriceTolerance    string `json:"priceTolerance"`
	WaitDays          int    `json:"waitDays"`
	TaxRatePct        string `json:"taxRatePct"`
	TaxCountry        string `json:"taxCountry"`
	TaxGroup          string `json:"taxGroup"`
	ItemTaxGroup      string `json:"itemTaxGroup"`
}

// SelfBillTax determines the VAT rate of a self-billing customer's deliveries; taxrate.Client implements it. Nil leaves the rate a
// partner setting.
type SelfBillTax interface {
	RatePct(ctx context.Context, q taxrate.Query) (decimal.Decimal, error)
}

func (a *API) putSelfBillConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var body selfBillConfigBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	cfg := store.DefaultSelfBillConfig()
	if body.QuantityTolerance != "" {
		cfg.QuantityTolerance = parseDec(fields, "quantityTolerance", body.QuantityTolerance)
	}
	if body.PriceTolerance != "" {
		cfg.PriceTolerance = parseDec(fields, "priceTolerance", body.PriceTolerance)
	}
	if body.TaxRatePct != "" {
		cfg.TaxRatePct = parseDec(fields, "taxRatePct", body.TaxRatePct)
	}
	if body.WaitDays != 0 {
		cfg.WaitDays = body.WaitDays
	}
	cfg.TaxCountry, cfg.TaxGroup, cfg.ItemTaxGroup = body.TaxCountry, body.TaxGroup, body.ItemTaxGroup
	if cfg.TaxCountry != "" && !countryCode.MatchString(cfg.TaxCountry) {
		fields["taxCountry"] = "must be an ISO 3166 alpha-2 country code"
	}
	if len(cfg.TaxGroup) > 40 || len(cfg.ItemTaxGroup) > 40 {
		fields["taxGroup"] = "taxGroup and itemTaxGroup must be at most 40 characters"
	}
	if cfg.QuantityTolerance.IsNegative() {
		fields["quantityTolerance"] = "must not be negative"
	}
	if cfg.PriceTolerance.IsNegative() {
		fields["priceTolerance"] = "must not be negative"
	}
	if cfg.TaxRatePct.IsNegative() || cfg.TaxRatePct.GreaterThan(decimal.NewFromInt(100)) {
		fields["taxRatePct"] = "must be between 0 and 100"
	}
	if cfg.WaitDays < 1 || cfg.WaitDays > 120 {
		fields["waitDays"] = "must be between 1 and 120"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	if err := a.store.PutSelfBillConfig(r.Context(), id, cfg); errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile; PUT /partners/{id}/trade-profile first"))
		return
	} else if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, cfg)
}

func (a *API) getSelfBillConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	if _, err := a.store.GetPartner(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	} else if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	cfg, _, err := a.store.GetSelfBillConfig(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, cfg)
}

// itemOf picks the item a self-billed line names: our vendor part (VP, the product id the 856 carried), else its first reference.
func itemOf(l domain.InvoiceLine) string {
	for _, r := range l.ItemRefs {
		if r.Qualifier == "VP" {
			return r.Value
		}
	}
	if len(l.ItemRefs) > 0 {
		return l.ItemRefs[0].Value
	}
	return ""
}

// selfBillInput turns the canonical self-billed invoice of a message into the lines to match.
func selfBillInput(messageID string, inv domain.Invoice) store.SelfBillInput {
	in := store.SelfBillInput{MessageID: messageID, Number: inv.Number, Date: inv.Date, Currency: inv.Currency}
	for _, l := range inv.Lines {
		sb := domain.SBLine{Line: l.LineNo, DeliveryNote: l.DeliveryNote, POLine: l.POLine, Item: itemOf(l), Quantity: l.Quantity, UnitPrice: l.UnitPrice}
		if l.Tax != nil {
			sb.Tax = *l.Tax
		} else {
			sb.NoTax = true
		}
		in.Lines = append(in.Lines, sb)
	}
	return in
}

// ReconcileSelfBilled matches the self-billed INVOIC messages not matched yet, at most limit of them. It returns how many it
// matched and the first error; a message without a trade profile stays MAPPED for the next run.
func (a *API) ReconcileSelfBilled(ctx context.Context, limit int) (int, error) {
	pending, err := a.store.PendingSelfBilled(ctx, limit)
	if err != nil {
		return 0, err
	}
	return a.reconcilePending(ctx, pending)
}

// ReconcileSelfBilledMessages matches only the given messages: the receipt of an interchange matches what it brought in and leaves
// every other pending message to the sweep, so one partner's receipt never runs another partner's matching.
func (a *API) ReconcileSelfBilledMessages(ctx context.Context, messageIDs []string) (int, error) {
	pending, err := a.store.PendingSelfBilledIn(ctx, messageIDs)
	if err != nil {
		return 0, err
	}
	return a.reconcilePending(ctx, pending)
}

func (a *API) reconcilePending(ctx context.Context, pending []store.PendingSelfBilled) (int, error) {
	done := 0
	var first error
	for _, p := range pending {
		var inv domain.Invoice
		if err := json.Unmarshal(p.Canonical, &inv); err != nil {
			slog.ErrorContext(ctx, "self-billed message unreadable", "message", p.MessageID, "err", err)
			continue
		}
		in := selfBillInput(p.MessageID, inv)
		rate, err := a.selfBillTaxRate(ctx, p.PartnerID, in)
		if err != nil {
			// The tax service is down or refused: the message stays MAPPED and the next run tries again.
			slog.WarnContext(ctx, "self-billed message waits for the tax rate", "message", p.MessageID, "err", err)
			if first == nil {
				first = err
			}
			continue
		}
		in.TaxRatePct = rate
		_, err = a.store.ReconcileSelfBill(ctx, p.PartnerID, in)
		switch {
		case err == nil:
			done++
		case errors.Is(err, store.ErrSelfBillReconciled):
			// Another run matched this message between the listing and now.
		case errors.Is(err, store.ErrDuplicate):
			// The document number was matched before (the customer sent it twice): the second one is a duplicate, never matched again.
			if perr := a.store.ParkMessage(ctx, p.MessageID, "EDI-0102", "self-billed invoice "+inv.Number+" was already received"); perr != nil && first == nil {
				first = perr
			}
		case errors.Is(err, store.ErrSelfBillNoProfile):
			slog.WarnContext(ctx, "self-billed message waits for a trade profile", "message", p.MessageID, "partner", p.PartnerID)
		default:
			if first == nil {
				first = err
			}
		}
	}
	return done, first
}

// selfBillTaxRate asks the tax service for the VAT rate of the document when the partner names a tax country and the service is
// configured; nil keeps the rate each shipment registered with (the partner's taxRatePct).
func (a *API) selfBillTaxRate(ctx context.Context, partnerID string, in store.SelfBillInput) (*decimal.Decimal, error) {
	if a.SelfBillTax == nil {
		return nil, nil
	}
	cfg, _, err := a.store.GetSelfBillConfig(ctx, partnerID)
	if err != nil || cfg.TaxCountry == "" {
		return nil, err
	}
	if in.Currency == "" {
		slog.WarnContext(ctx, "self-billed document names no currency; the partner's tax rate is used", "message", in.MessageID)
		return nil, nil
	}
	date, err := time.Parse("20060102", in.Date)
	if err != nil {
		return nil, nil // the matcher reports the document; the partner's rate stands in
	}
	profile, err := a.store.GetTradeProfile(ctx, partnerID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil // the match waits for the trade profile (ErrSelfBillNoProfile)
	}
	if err != nil {
		return nil, err
	}
	actor, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("self-billing tax rate: no tenant in context")
	}
	sp, err := tenant.ServicePrincipal(SelfBillPrincipal, actor.TenantID, profile.LegalEntityID)
	if err != nil {
		return nil, err
	}
	rate, err := a.SelfBillTax.RatePct(tenant.With(ctx, sp), taxrate.Query{LegalEntityID: profile.LegalEntityID, Currency: in.Currency, Date: date,
		Country: cfg.TaxCountry, TaxGroup: cfg.TaxGroup, ItemTaxGroup: cfg.ItemTaxGroup})
	if err != nil {
		return nil, err
	}
	return &rate, nil
}

type sweepResult struct {
	Reconciled int                     `json:"reconciled"`
	Unbilled   []store.SelfBillDispute `json:"unbilled"`
}

// selfBillSweep matches pending self-billed messages and opens the unbilled disputes (EDI-0138) of shipments past their wait.
func (a *API) selfBillSweep(w http.ResponseWriter, r *http.Request) {
	done, err := a.ReconcileSelfBilled(r.Context(), 200)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	opened, err := a.store.SweepUnbilled(r.Context(), a.now().UTC())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if opened == nil {
		opened = []store.SelfBillDispute{}
	}
	httpx.JSON(w, http.StatusOK, sweepResult{Reconciled: done, Unbilled: opened})
}

func listLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 {
		return defaultListLimit
	}
	return min(n, maxListLimit)
}

func (a *API) listSelfBillDocuments(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "MATCHED" && status != "DISPUTED" && status != "RESOLVED" {
		httpx.Fail(w, r, validation(map[string]string{"status": "must be MATCHED, DISPUTED or RESOLVED"}))
		return
	}
	docs, err := a.store.ListSelfBillDocuments(r.Context(), status, listLimit(r))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": docs})
}

func (a *API) listDisputes(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "OPEN" && status != "ACCEPTED" && status != "REJECTED" {
		httpx.Fail(w, r, validation(map[string]string{"status": "must be OPEN, ACCEPTED or REJECTED"}))
		return
	}
	partner := r.URL.Query().Get("partnerId")
	if partner != "" && !validID(partner) {
		httpx.Fail(w, r, validation(map[string]string{"partnerId": "must be a UUID"}))
		return
	}
	items, err := a.store.ListDisputes(r.Context(), status, partner, listLimit(r))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type resolveBody struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

func (a *API) resolveDispute(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("dispute"))
		return
	}
	var body resolveBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if body.Decision != "ACCEPTED" && body.Decision != "REJECTED" {
		fields["decision"] = "must be ACCEPTED or REJECTED"
	}
	if len(body.Note) > 500 {
		fields["note"] = "at most 500 characters"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	d, err := a.store.ResolveDispute(r.Context(), id, body.Decision, body.Note, caller.UserID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("dispute"))
		return
	}
	if errors.Is(err, store.ErrDisputeResolved) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeDisputeResolved, "the dispute was already decided"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

// RunSelfBillSweep is the scheduled sweep (35-5): for each tenant with work it matches the pending self-billed documents and opens
// the unbilled disputes, as POST /self-billing:sweep does for the caller's tenant. It returns how many documents it matched and
// disputes it opened; a failing tenant does not stop the others.
func (a *API) RunSelfBillSweep(ctx context.Context) (int, error) {
	tenants, err := a.store.SelfBillDueTenants(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	var failed []error
	for _, tid := range tenants {
		sys := store.SystemContext(ctx, tid)
		done, err := a.ReconcileSelfBilled(sys, 200)
		if err != nil {
			failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
		}
		opened, err := a.store.SweepUnbilled(sys, a.now().UTC())
		if err != nil {
			failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
		}
		total += done + len(opened)
	}
	return total, errors.Join(failed...)
}

// mappedMessages are the ids of the MAPPED messages of the set an interchange brought in.
func mappedMessages(messages []store.Message, set string) []string {
	var out []string
	for _, m := range messages {
		if m.TransactionSet == set && m.Status == "MAPPED" {
			out = append(out, m.ID)
		}
	}
	return out
}
