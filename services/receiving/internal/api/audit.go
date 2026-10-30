package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const dutyInvoiceAuditRelease = "VendorInvoiceAuditRelease"

type auditPolicyRequest struct {
	LegalEntityID string        `json:"legalEntityId"`
	SampleRatePct *fieldDecimal `json:"sampleRatePct"`
}

type auditPolicyBody struct {
	LegalEntityID string `json:"legalEntityId"`
	SampleRatePct string `json:"sampleRatePct"`
}

type ersVendorRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Enabled       *bool  `json:"enabled"`
}

type ersVendorBody struct {
	LegalEntityID string `json:"legalEntityId"`
	VendorID      string `json:"vendorId"`
	Enabled       bool   `json:"enabled"`
}

type releaseRequest struct {
	LegalEntityID string `json:"legalEntityId"`
}

func principalWithUser(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return p, false
	}
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "the request needs an authenticated user so the change can be audited"))
		return p, false
	}
	return p, true
}

func (a *API) setAuditPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req auditPolicyRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	rate, present := decimalField(fields, "sampleRatePct", req.SampleRatePct, true)
	if present && !domain.ValidSampleRate(rate) {
		fields["sampleRatePct"] = "must be from 0 to 100 with at most two decimals"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.store.SetAuditRate(r.Context(), req.LegalEntityID, rate, p.UserID); err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, auditPolicyBody{LegalEntityID: req.LegalEntityID, SampleRatePct: rate.StringFixed(2)})
}

func (a *API) setErsVendor(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req ersVendorRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	vendorID := chi.URLParam(r, "vendorId")
	fields := map[string]string{}
	checkUUID(fields, "vendorId", vendorID)
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	if req.Enabled == nil {
		fields["enabled"] = "is required"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.store.SetErsVendor(r.Context(), req.LegalEntityID, vendorID, *req.Enabled, p.UserID); err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ersVendorBody{LegalEntityID: req.LegalEntityID, VendorID: vendorID, Enabled: *req.Enabled})
}

func (a *API) releaseAuditBlock(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req releaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	fields := map[string]string{}
	checkUUID(fields, "id", id)
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	ctx := r.Context()
	released, err := a.store.ReleaseInvoice(ctx, id, req.LegalEntityID, p.UserID, func(inv store.Invoice) (store.InvoiceOutcome, error) {
		lines := make([]domain.HeldLine, len(inv.Lines))
		subjects := []string{inv.ID}
		for i, l := range inv.Lines {
			lines[i] = domain.HeldLine{Qty: l.Qty, UnitPrice: l.UnitPrice, StandardCost: l.StandardCost, Clearing: l.GrirCleared}
			subjects = append(subjects, l.ReceiptID)
		}
		entry, err := domain.PostHeldInvoice(ctx, domain.VendorInvoice{
			ID: inv.ID, LegalEntityID: inv.LegalEntityID, LedgerID: inv.LedgerID, FiscalPeriodID: inv.FiscalPeriodID,
			PostingDate: inv.PostingDate, InvoiceDate: inv.InvoiceDate, Currency: inv.Currency,
		}, lines, a.ports.Periods, a.ledgerFor(subjects...), a.ports.Profile)
		if err != nil {
			return store.InvoiceOutcome{}, err
		}
		total := entry.Lines[len(entry.Lines)-1].Amount.Value.Neg()
		evt, err := invoicePostedEvent(p.TenantID, inv, entry.LedgerEntryID, total, entry.GrirCleared)
		return store.InvoiceOutcome{LedgerEntryID: entry.LedgerEntryID, Total: total, Event: evt}, err
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.pendingPostings.finish(invoiceSubjects(released)...)
	a.auditReleased.Add(1)
	a.invoiced.Add(1)
	httpx.JSON(w, http.StatusOK, toInvoiceBody(released))
}
