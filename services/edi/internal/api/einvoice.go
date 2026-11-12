package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// The e-invoice store and mandate routes (#35, 35-2). A posted billing invoice is built for a mandate (Peppol UBL 2.1 here; IT SDI
// and MX CFDI are hooks), stored with its status, handed to the network through the AccessPoint, and the network's answer recorded.
// The access point is a seam: a live Peppol access point, the SDI or a PAC is out of scope and injected by the deployment; without
// one the submit route answers 503.

const (
	CodePartyProfile      = "EDI-0027"
	CodeUnknownMandate    = "EDI-0028"
	CodeAccessPointOff    = "EDI-0029"
	CodeEInvoiceState     = "EDI-0030"
	CodeEInvoiceNotValid  = "EDI-0031"
	roleSeller, roleBuyer = "SELLER", "BUYER"
)

// submitCallTimeout bounds one call to the access point; it is far below store.SubmitClaimTTL.
const submitCallTimeout = 2 * time.Minute

// AccessPoint hands one document to the network of a mandate and returns the network's reference for it.
type AccessPoint interface {
	Submit(ctx context.Context, mandate, number, document string) (reference string, err error)
}

// einvoicePartyBody is a seller or buyer profile: the party as the dry-run builder takes it, plus the buyer's reference (BT-10),
// which Peppol requires when the invoice has no customer PO.
type einvoicePartyBody struct {
	LegalEntityID  string      `json:"legalEntityId"`
	Party          ublPartyDTO `json:"party"`
	BuyerReference string      `json:"buyerReference,omitempty"`
	// IBAN and PaymentMeansCode (30 credit transfer, 58 SEPA) are the seller's payment details (PayeeFinancialAccount).
	IBAN             string `json:"iban,omitempty"`
	PaymentMeansCode string `json:"paymentMeansCode,omitempty"`
}

func partyFields(p ublPartyDTO) map[string]string {
	fields := map[string]string{}
	if strings.TrimSpace(p.Name) == "" {
		fields["party.name"] = "is required"
	}
	if len(p.CountryCode) != 2 || strings.ToUpper(p.CountryCode) != p.CountryCode {
		fields["party.countryCode"] = "must be an ISO 3166-1 alpha-2 code"
	}
	if p.EndpointScheme == "" || p.EndpointID == "" {
		fields["party.endpointId"] = "the endpoint id and its scheme are required"
	}
	return fields
}

func (a *API) einvoiceRole(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	role, subject := strings.ToUpper(chi.URLParam(r, "role")), chi.URLParam(r, "subjectId")
	if (role != roleSeller && role != roleBuyer) || !validID(subject) {
		httpx.Fail(w, r, notFound("party profile"))
		return "", "", false
	}
	return role, subject, true
}

// putEInvoiceParty stores the seller profile of a legal entity (subject = the legal entity) or the buyer profile of one of its
// customers (subject = the customer).
func (a *API) putEInvoiceParty(w http.ResponseWriter, r *http.Request) {
	role, subject, ok := a.einvoiceRole(w, r)
	if !ok {
		return
	}
	var body einvoicePartyBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := partyFields(body.Party)
	if !validID(body.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	} else if caller, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(caller, body.LegalEntityID) {
		fields["legalEntityId"] = "must be a legal entity of the caller"
	}
	if role == roleSeller && validID(body.LegalEntityID) && subject != body.LegalEntityID {
		fields["legalEntityId"] = "the seller profile of a legal entity is stored under that legal entity's id"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	raw, err := json.Marshal(body)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.store.PutEInvoiceParty(r.Context(), body.LegalEntityID, role, subject, raw); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) getEInvoiceParty(w http.ResponseWriter, r *http.Request) {
	role, subject, ok := a.einvoiceRole(w, r)
	if !ok {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !validID(le) {
		httpx.Fail(w, r, validation(map[string]string{"legalEntityId": "must be a UUID"}))
		return
	}
	raw, err := a.store.GetEInvoiceParty(r.Context(), le, role, subject)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("party profile"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

type einvoiceRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	InvoiceID     string `json:"invoiceId"`
	Mandate       string `json:"mandate"`
}

// createEInvoice builds the e-invoice of a posted billing invoice for a mandate and stores it. The answer is 201 with the stored
// e-invoice, VALID with its document or INVALID with the blocking issues; a repeat returns the stored one (200), and a repeat of an
// INVALID one builds again, so fixing a profile and asking again repairs it.
func (a *API) createEInvoice(w http.ResponseWriter, r *http.Request) {
	var req einvoiceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Mandate = strings.ToUpper(strings.TrimSpace(req.Mandate))
	fields := map[string]string{}
	if !validID(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	} else if caller, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(caller, req.LegalEntityID) {
		fields["legalEntityId"] = "must be a legal entity of the caller"
	}
	if !validID(req.InvoiceID) {
		fields["invoiceId"] = "must be a UUID"
	}
	mandate, known := a.mandates()[req.Mandate]
	if !known {
		fields["mandate"] = "must be PEPPOL, IT_SDI, MX_CFDI or FACTURX"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	saved, created, err := a.BuildEInvoice(r.Context(), mandate, req.LegalEntityID, req.InvoiceID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, saved)
}

// BuildEInvoice reads the posted billing invoice, maps it, builds it with the mandate and stores it (VALID with its document or
// INVALID with the issues). The route and the automatic e-invoicing on invoice.posted share it. Errors are problems.
func (a *API) BuildEInvoice(ctx context.Context, mandate domain.Mandate, legalEntityID, invoiceID string) (store.EInvoice, bool, error) {
	if a.Sources == nil {
		return store.EInvoice{}, false, httpx.NewProblem(http.StatusServiceUnavailable, CodeSourceDown, "source services are not configured")
	}
	inv, err := a.Sources.Invoice(ctx, legalEntityID, invoiceID)
	if err != nil {
		return store.EInvoice{}, false, sourceProblem(err)
	}
	if inv.LegalEntityID != legalEntityID {
		return store.EInvoice{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeSourceScope, "the invoice belongs to another legal entity")
	}
	sellerRaw, err := a.store.GetEInvoiceParty(ctx, legalEntityID, roleSeller, legalEntityID)
	var buyerRaw json.RawMessage
	if err == nil {
		buyerRaw, err = a.store.GetEInvoiceParty(ctx, legalEntityID, roleBuyer, inv.CustomerID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return store.EInvoice{}, false, httpx.NewProblem(http.StatusConflict, CodePartyProfile,
			"the seller profile of the legal entity and the buyer profile of the customer are needed first (PUT /einvoice/parties/{role}/{subjectId})")
	}
	if err != nil {
		return store.EInvoice{}, false, err
	}
	return a.buildAndSaveEInvoice(ctx, legalEntityID, mandate, inv, sellerRaw, buyerRaw)
}

func (a *API) buildAndSaveEInvoice(ctx context.Context, legalEntityID string, mandate domain.Mandate, inv domain.SourceInvoice, sellerRaw, buyerRaw json.RawMessage) (store.EInvoice, bool, error) {
	var seller, buyer einvoicePartyBody
	if json.Unmarshal(sellerRaw, &seller) != nil || json.Unmarshal(buyerRaw, &buyer) != nil {
		return store.EInvoice{}, false, errors.New("stored party profile is unreadable")
	}
	orderRef := ""
	if orderID, err := domain.InvoiceOrderID(inv); err == nil {
		if o, err := a.Sources.Order(ctx, legalEntityID, orderID); err == nil {
			orderRef = o.ExternalRef
		} else if !errors.Is(err, sources.ErrNotFound) {
			return store.EInvoice{}, false, sourceProblem(err)
		}
	}
	doc, issues := domain.EInvoiceFromBilling(inv, orderRef, buyer.BuyerReference, seller.IBAN, seller.PaymentMeansCode, toUBLParty(seller.Party), toUBLParty(buyer.Party))
	xmlOut := ""
	if len(issues) == 0 {
		xmlOut, issues = mandate.Build(doc)
	}
	e := store.EInvoice{LegalEntityID: legalEntityID, InvoiceID: inv.ID, Number: inv.Number, Mandate: mandate.Code(), Format: mandate.Format(),
		DocumentType: "INVOICE", Status: "VALID", XML: xmlOut, Issues: issues}
	if inv.Type == "CREDIT_NOTE" {
		e.DocumentType = "CREDIT_NOTE"
	}
	if len(issues) > 0 {
		e.Status, e.XML = "INVALID", ""
	} else {
		e.Issues = []domain.Issue{}
	}
	return a.store.SaveEInvoice(ctx, e)
}

func (a *API) mandates() domain.Mandates {
	if a.Mandates == nil {
		a.Mandates = domain.DefaultMandates()
	}
	return a.Mandates
}

func (a *API) listEInvoices(w http.ResponseWriter, r *http.Request) {
	status, mandate := r.URL.Query().Get("status"), strings.ToUpper(r.URL.Query().Get("mandate"))
	fields := map[string]string{}
	switch status {
	case "", "VALID", "INVALID", "SUBMITTING", "SUBMITTED", "ACCEPTED", "REJECTED":
	default:
		fields["status"] = "must be VALID, INVALID, SUBMITTING, SUBMITTED, ACCEPTED or REJECTED"
	}
	if _, ok := a.mandates()[mandate]; mandate != "" && !ok {
		fields["mandate"] = "must be PEPPOL, IT_SDI, MX_CFDI or FACTURX"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	items, err := a.store.ListEInvoices(r.Context(), status, mandate, listLimit(r))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getEInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("e-invoice"))
		return
	}
	e, err := a.store.GetEInvoice(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("e-invoice"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

func eInvoiceProblem(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return notFound("e-invoice")
	case errors.Is(err, store.ErrEInvoiceState):
		return httpx.NewProblem(http.StatusConflict, CodeEInvoiceState, "the e-invoice is not in a status this action applies to")
	}
	return err
}

// submitEInvoice hands a VALID (or previously REJECTED) e-invoice to the network through the access point.
func (a *API) submitEInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("e-invoice"))
		return
	}
	out, err := a.SubmitEInvoice(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// SubmitEInvoice claims the e-invoice (VALID or REJECTED to SUBMITTING, atomically, so a concurrent submit gets 409 EDI-0030 and
// never reaches the access point), hands it over, and records SUBMITTED with the network's reference; when the access point does
// not take it the claim is released and the e-invoice returns to the status it had. Errors are problems.
func (a *API) SubmitEInvoice(ctx context.Context, id string) (store.EInvoice, error) {
	if a.AccessPoint == nil {
		return store.EInvoice{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeAccessPointOff, "no access point is configured for e-invoice submission")
	}
	e, err := a.store.GetEInvoice(ctx, id)
	if err != nil {
		return store.EInvoice{}, eInvoiceProblem(err)
	}
	switch e.Status {
	case "VALID", "REJECTED", "SUBMITTING": // the claim decides on SUBMITTING: only a stale one can be taken over
	case "INVALID":
		return store.EInvoice{}, httpx.NewProblem(http.StatusConflict, CodeEInvoiceNotValid, "the e-invoice has blocking issues; fix them and build it again before submitting")
	default:
		return store.EInvoice{}, httpx.NewProblem(http.StatusConflict, CodeEInvoiceState, "the e-invoice is already "+strings.ToLower(e.Status))
	}
	var claim store.SubmitClaim
	if e, claim, err = a.store.ClaimEInvoiceSubmit(ctx, id); err != nil {
		return store.EInvoice{}, eInvoiceProblem(err)
	}
	// The call to the access point is bounded well under the claim's life, so a claim that outlives it belongs to a caller that died.
	callCtx, cancel := context.WithTimeout(ctx, submitCallTimeout)
	ref, err := a.AccessPoint.Submit(callCtx, e.Mandate, e.Number, e.XML)
	cancel()
	if err != nil {
		if rerr := a.store.ReleaseEInvoiceClaim(ctx, id, claim); rerr != nil {
			slog.ErrorContext(ctx, "e-invoice claim not released", "einvoice", id, "err", rerr)
		}
		return store.EInvoice{}, httpx.NewProblem(http.StatusBadGateway, CodeAccessPointOff, "the access point did not take the document: "+err.Error())
	}
	out, err := a.store.MarkEInvoiceSubmitted(ctx, id, claim, ref)
	if err != nil {
		if errors.Is(err, store.ErrEInvoiceState) {
			// The claim was taken over while the access point was answering: the document may have been handed over twice; the access point deduplicates by number.
			slog.WarnContext(ctx, "e-invoice claim lost before the access point's answer was recorded", "einvoice", id, "reference", ref)
		}
		return store.EInvoice{}, eInvoiceProblem(err)
	}
	return out, nil
}

type einvoiceResponseBody struct {
	Outcome   string `json:"outcome"`
	Reference string `json:"reference"`
	Reason    string `json:"reason"`
}

// recordEInvoiceResponse records the network's answer to a submitted e-invoice. The answer comes from whoever receives the
// network's status messages (the access point integration or an operator); it moves SUBMITTED to ACCEPTED or REJECTED.
func (a *API) recordEInvoiceResponse(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("e-invoice"))
		return
	}
	var body einvoiceResponseBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if body.Outcome != "ACCEPTED" && body.Outcome != "REJECTED" {
		fields["outcome"] = "must be ACCEPTED or REJECTED"
	}
	if body.Outcome == "REJECTED" && strings.TrimSpace(body.Reason) == "" {
		fields["reason"] = "is required when the network rejected the document"
	}
	if len(body.Reason) > 1000 || len(body.Reference) > 200 {
		fields["reason"] = "at most 1000 characters (reference 200)"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	out, err := a.store.RecordEInvoiceResponse(r.Context(), id, body.Outcome == "ACCEPTED", body.Reference, body.Reason)
	if err != nil {
		httpx.Fail(w, r, eInvoiceProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
