package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// Self-billing intake (#35, 35-5). A customer's INVOIC 389 arrives on the normal EDIFACT route and is mapped like any INVOIC
// with selfBilled set. ReconcileSelfBilled matches its lines to the shipments the 856 or DESADV of this partner registered
// (BR-35.15), opens a dispute for every difference or unmatched line, and marks the message POSTED. The match runs right after
// receipt and again from POST /self-billing:sweep for anything that missed it, together with the unbilled sweep (EDI-0138).

const (
	CodeSelfBillNoProfile = "EDI-0025"
	CodeDisputeResolved   = "EDI-0026"
)

type selfBillConfigBody struct {
	QuantityTolerance string `json:"quantityTolerance"`
	PriceTolerance    string `json:"priceTolerance"`
	WaitDays          int    `json:"waitDays"`
	TaxRatePct        string `json:"taxRatePct"`
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
	in := store.SelfBillInput{MessageID: messageID, Number: inv.Number, Date: inv.Date}
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
	done := 0
	var first error
	for _, p := range pending {
		var inv domain.Invoice
		if err := json.Unmarshal(p.Canonical, &inv); err != nil {
			slog.ErrorContext(ctx, "self-billed message unreadable", "message", p.MessageID, "err", err)
			continue
		}
		_, err := a.store.ReconcileSelfBill(ctx, p.PartnerID, selfBillInput(p.MessageID, inv))
		switch {
		case err == nil:
			done++
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
