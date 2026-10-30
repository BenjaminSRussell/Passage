package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

const (
	// dutyClaim is the PRD duty of supplier warranty claims (8.45.5, 8.45.6).
	dutyClaim       = "SvcClaimMaintain"
	maxClaimSources = 50
	maxClaimRef     = 100
	maxFailureCode  = 40
)

var claimStatuses = []string{domain.ClaimDraft, domain.ClaimSubmitted, domain.ClaimAccepted, domain.ClaimPartial,
	domain.ClaimRejected, domain.ClaimSettled, domain.ClaimClosed}

func (a *API) registerClaims(r chi.Router) {
	read := authz.Require(anyDuty{dutyClaim, dutyInquire}, "SvcClaimMaintain or SvcInquire", "")
	r.With(authz.RequireDuty(dutyClaim)).Post("/v1/service/supplier-claims", a.createClaim)
	r.With(read).Get("/v1/service/supplier-claims", a.listClaims)
	r.With(read).Get("/v1/service/supplier-claims/{id}", a.getClaim)
	r.With(authz.RequireDuty(dutyClaim)).Post("/v1/service/supplier-claims/{id}:submit", a.submitClaim)
	r.With(authz.RequireDuty(dutyClaim)).Post("/v1/service/supplier-claims/{id}/decision", a.decideClaim)
	r.With(authz.RequireDuty(dutyClaim)).Post("/v1/service/supplier-claims/{id}:settle", a.settleClaim)
	r.With(authz.RequireDuty(dutyClaim)).Post("/v1/service/supplier-claims/{id}:close", a.closeClaim)
}

type claimLineRequest struct {
	ItemID      string          `json:"itemId"`
	Serial      string          `json:"serial"`
	FailureCode string          `json:"failureCode"`
	PartAmount  decimal.Decimal `json:"partAmount"`
	LaborHours  decimal.Decimal `json:"laborHours"`
}

type claimRequest struct {
	VendorPartyID  string             `json:"vendorPartyId"`
	SourceKind     string             `json:"sourceKind"`
	SourceIDs      []string           `json:"sourceIds"`
	LaborRate      decimal.Decimal    `json:"laborRate"`
	FailureCode    string             `json:"failureCode"`
	WindowDays     *int               `json:"windowDays"`
	ReturnRequired bool               `json:"returnRequired"`
	LegalEntityID  string             `json:"legalEntityId"`
	Currency       string             `json:"currency"`
	CompletedOn    string             `json:"completedOn"`
	Lines          []claimLineRequest `json:"lines"`
}

// createClaim drafts a supplier warranty claim (FR-45.15). SERVICE_WO claims are
// built from the WARRANTY lines of posted work orders; EAM_WO claims carry their
// lines, legal entity, currency and completion date.
func (a *API) createClaim(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req claimRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	uuidField(fields, "vendorPartyId", req.VendorPartyID)
	if n := len(req.SourceIDs); n < 1 || n > maxClaimSources {
		fields["sourceIds"] = fmt.Sprintf("must hold 1 to %d work order ids", maxClaimSources)
	}
	seen := map[string]bool{}
	for _, id := range req.SourceIDs {
		uuidField(fields, "sourceIds", id)
		if seen[id] {
			fields["sourceIds"] = "must not repeat a work order"
		}
		seen[id] = true
	}
	if req.LaborRate.IsNegative() {
		fields["laborRate"] = "must not be negative; 0 when the vendor does not cover labor"
	}
	if len(req.FailureCode) > maxFailureCode {
		fields["failureCode"] = fmt.Sprintf("must be at most %d characters", maxFailureCode)
	}
	window := domain.DefaultClaimWindowDays
	if req.WindowDays != nil {
		window = *req.WindowDays
	}
	if window < 1 || window > 365 {
		fields["windowDays"] = "must be 1 to 365"
	}
	var lines []domain.ClaimLine
	switch req.SourceKind {
	case domain.ClaimFromServiceWO:
		if req.LegalEntityID != "" || req.Currency != "" || req.CompletedOn != "" || len(req.Lines) > 0 {
			fields["sourceKind"] = "a SERVICE_WO claim takes its legal entity, currency, completion date and lines from the work orders"
		}
	case domain.ClaimFromEAMWO:
		uuidField(fields, "legalEntityId", req.LegalEntityID)
		if !currency.MatchString(req.Currency) {
			fields["currency"] = "must be an ISO 4217 alpha-3 code"
		}
		if _, ok := parseDate(req.CompletedOn); !ok {
			fields["completedOn"] = "must be the work order completion date as YYYY-MM-DD"
		}
		if len(req.Lines) != len(req.SourceIDs) {
			fields["lines"] = "an EAM_WO claim has one line per source work order"
		}
		for i, l := range req.Lines {
			key := fmt.Sprintf("lines[%d]", i)
			uuidField(fields, key+".itemId", l.ItemID)
			if l.PartAmount.IsNegative() || l.LaborHours.IsNegative() {
				fields[key] = "part amount and labor hours must not be negative"
			}
			if len([]rune(l.Serial)) > maxSerial || len(l.FailureCode) > maxFailureCode {
				fields[key] = "serial is at most 100 and failure code at most 40 characters"
			}
			code := l.FailureCode
			if code == "" {
				code = req.FailureCode
			}
			src := ""
			if i < len(req.SourceIDs) {
				src = req.SourceIDs[i]
			}
			lines = append(lines, domain.ClaimLine{SourceID: src, ItemID: l.ItemID, Serial: l.Serial, FailureCode: code,
				PartAmount: l.PartAmount, LaborHours: l.LaborHours})
		}
	default:
		fields["sourceKind"] = "must be SERVICE_WO or EAM_WO"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	c, replayed, err := a.store.CreateClaim(r.Context(), store.ClaimInput{
		VendorPartyID: req.VendorPartyID, SourceKind: req.SourceKind, SourceIDs: req.SourceIDs, LaborRate: req.LaborRate,
		FailureCode: req.FailureCode, WindowDays: window, ReturnRequired: req.ReturnRequired, LegalEntityID: req.LegalEntityID,
		Currency: req.Currency, CompletedOn: req.CompletedOn, Lines: lines, At: a.now().UTC(),
	}, key, hashOf(req))
	if err != nil {
		a.fail(w, r, "create supplier claim", err)
		return
	}
	setETag(w, c.Version)
	a.respond(w, replayed, http.StatusCreated, c)
}

func (a *API) listClaims(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.ClaimFilter{VendorPartyID: q.Get("vendorPartyId"), Status: q.Get("status")}
	fields := map[string]string{}
	if f.VendorPartyID != "" {
		uuidField(fields, "vendorPartyId", f.VendorPartyID)
	}
	if f.Status != "" && !oneOf(f.Status, claimStatuses...) {
		fields["status"] = "must be one of " + strings.Join(claimStatuses, ", ")
	}
	f.Cursor, f.Limit = pageParams(fields, q.Get("cursor"), q.Get("limit"))
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, err := a.store.ListClaims(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list supplier claims", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getClaim(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeClaimUnknown, "supplier claim not found")
	if !ok {
		return
	}
	c, err := a.store.GetClaim(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get supplier claim", err)
		return
	}
	setETag(w, c.Version)
	httpx.JSON(w, http.StatusOK, c)
}

type claimSubmitRequest struct {
	VendorReference string `json:"vendorReference"`
	ReturnOrderID   string `json:"returnOrderId"`
}

type claimDecisionRequest struct {
	Decision        string            `json:"decision"`
	Reason          string            `json:"reason"`
	VendorReference string            `json:"vendorReference"`
	AcceptedAmounts []decimal.Decimal `json:"acceptedAmounts"`
}

type claimSettleRequest struct {
	CreditNoteID    string          `json:"creditNoteId"`
	VendorReference string          `json:"vendorReference"`
	Amount          decimal.Decimal `json:"amount"`
	Currency        string          `json:"currency"`
}

// claimMove reads the path id, If-Match and Idempotency-Key of a claim command,
// decodes and validates its body and runs it; the answer carries the new ETag.
func (a *API) claimMove(w http.ResponseWriter, r *http.Request, op string, body any, validate func(fields map[string]string, m *store.ClaimMove),
	run func(ctx context.Context, m store.ClaimMove, key, hash string) (store.SupplierClaim, bool, error)) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeClaimUnknown, "supplier claim not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	if body != nil {
		if err := httpx.Decode(r, body); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	m := store.ClaimMove{ID: id, Version: version, At: a.now().UTC()}
	fields := map[string]string{}
	validate(fields, &m)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	c, replayed, err := run(r.Context(), m, key, requestHash(op, id, body))
	if err != nil {
		a.fail(w, r, op+" supplier claim", err)
		return
	}
	setETag(w, c.Version)
	a.respond(w, replayed, http.StatusOK, c)
}

func refField(fields map[string]string, name, v string, required bool) {
	if len(v) > maxClaimRef || (required && strings.TrimSpace(v) == "") || strings.TrimSpace(v) != v {
		fields[name] = fmt.Sprintf("must be %s%d characters without surrounding spaces", map[bool]string{true: "1 to ", false: "at most "}[required], maxClaimRef)
	}
}

// submitClaim sends a DRAFT claim to the vendor today (BR-45.12: SVC-SWC-001 after
// the window, SVC-SWC-002 without a required failed-part return).
func (a *API) submitClaim(w http.ResponseWriter, r *http.Request) {
	var req claimSubmitRequest
	a.claimMove(w, r, "submit", &req, func(fields map[string]string, m *store.ClaimMove) {
		refField(fields, "vendorReference", req.VendorReference, false)
		refField(fields, "returnOrderId", req.ReturnOrderID, false)
		m.VendorReference, m.ReturnOrderID = req.VendorReference, req.ReturnOrderID
	}, a.store.SubmitClaim)
}

// decideClaim records the vendor decision (PRD 8.45.5 POST .../{id}/decision).
func (a *API) decideClaim(w http.ResponseWriter, r *http.Request) {
	var req claimDecisionRequest
	a.claimMove(w, r, "decide", &req, func(fields map[string]string, m *store.ClaimMove) {
		if !oneOf(req.Decision, domain.DecisionAccepted, domain.DecisionPartial, domain.DecisionRejected) {
			fields["decision"] = "must be ACCEPTED, PARTIALLY_ACCEPTED or REJECTED"
		}
		reasonField(fields, req.Reason, req.Decision == domain.DecisionRejected)
		refField(fields, "vendorReference", req.VendorReference, false)
		for i, amt := range req.AcceptedAmounts {
			if amt.IsNegative() {
				fields[fmt.Sprintf("acceptedAmounts[%d]", i)] = "must not be negative"
			}
		}
		m.Decision, m.Reason, m.VendorReference, m.Accepted = req.Decision, strings.TrimSpace(req.Reason), req.VendorReference, req.AcceptedAmounts
	}, a.store.DecideClaim)
}

// settleClaim matches the vendor credit note (BR-45.12, AC-45.13).
func (a *API) settleClaim(w http.ResponseWriter, r *http.Request) {
	var req claimSettleRequest
	a.claimMove(w, r, "settle", &req, func(fields map[string]string, m *store.ClaimMove) {
		refField(fields, "creditNoteId", req.CreditNoteID, true)
		refField(fields, "vendorReference", req.VendorReference, true)
		if !req.Amount.IsPositive() {
			fields["amount"] = "must be a positive decimal"
		}
		if !currency.MatchString(req.Currency) {
			fields["currency"] = "must be an ISO 4217 alpha-3 code"
		}
		m.CreditNoteID, m.VendorReference, m.CreditAmount, m.CreditCurrency = req.CreditNoteID, req.VendorReference, req.Amount, req.Currency
	}, a.store.SettleClaim)
}

// closeClaim closes a REJECTED or SETTLED claim.
func (a *API) closeClaim(w http.ResponseWriter, r *http.Request) {
	a.claimMove(w, r, "close", nil, func(map[string]string, *store.ClaimMove) {}, a.store.CloseClaim)
}
