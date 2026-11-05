package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// dutySigningLimitApprove decides signing limit requests and recertifies limits (FR-17.17, PRD
// 3.17.17). Raising a request needs procurement.requisition.create; reading is open to procurement.purchase-order.inquire.
const dutySigningLimitApprove = "procurement.signing-limit.approve"

// maxSigningNoteLen bounds a request reason and a decision note (the column checks are the same).
const maxSigningNoteLen = 1000

type signingRequestBody struct {
	LegalEntityID string `json:"legalEntityId"`
	WorkerID      string `json:"workerId"`
	DocumentType  string `json:"documentType"`
	Currency      string `json:"currency"`
	SpendingLimit string `json:"spendingLimit"`
	ApprovalLimit string `json:"approvalLimit"`
	Reason        string `json:"reason,omitempty"`
}

func (a *API) createSigningRequest(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if _, err := caller(r.Context()); err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body signingRequestBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	for _, f := range []struct{ name, v string }{{"legalEntityId", body.LegalEntityID}, {"workerId", body.WorkerID}} {
		if err := uuidField(f.name, f.v, true); err != nil {
			fail(w, r, err)
			return
		}
	}
	if !domain.ValidSigningDocType(body.DocumentType) {
		fail(w, r, invalid("documentType", "must be REQUISITION, PURCHASE_ORDER, INVOICE or EXPENSE"))
		return
	}
	currency, err := currencyField(body.Currency)
	if err != nil {
		fail(w, r, err)
		return
	}
	spending, err := limitField("spendingLimit", body.SpendingLimit)
	if err != nil {
		fail(w, r, err)
		return
	}
	approval, err := limitField("approvalLimit", body.ApprovalLimit)
	if err != nil {
		fail(w, r, err)
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if utf8.RuneCountInString(reason) > maxSigningNoteLen {
		fail(w, r, invalid("reason", "must be at most 1000 characters"))
		return
	}
	if err := inScope(p, body.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateSigningLimitRequest(r.Context(), domain.SigningLimitRequest{LegalEntityID: body.LegalEntityID, WorkerID: body.WorkerID,
		DocumentType: body.DocumentType, Currency: currency, SpendingLimit: spending, ApprovalLimit: approval, Reason: reason},
		store.Idem{Key: key, Hash: hashBody("signing-request.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

// limitField parses a non-negative limit amount.
func limitField(field, raw string) (decimal.Decimal, error) {
	d, err := decimalField(field, raw, "")
	if err != nil {
		return decimal.Zero, err
	}
	if raw == "" || d.IsNegative() || d.Exponent() < -6 {
		return decimal.Zero, invalid(field, "must be a decimal amount of zero or more with at most 6 decimals")
	}
	return d, nil
}

func (a *API) getSigningRequest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetSigningLimitRequest(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listSigningRequests(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.SigningRequestFilter{LegalEntityID: q.Get("legalEntityId"), WorkerID: q.Get("workerId"), Status: q.Get("status")}
	for _, u := range []struct{ name, v string }{{"legalEntityId", f.LegalEntityID}, {"workerId", f.WorkerID}} {
		if err := uuidField(u.name, u.v, false); err != nil {
			fail(w, r, err)
			return
		}
	}
	if f.Status != "" && f.Status != domain.SigningPending && f.Status != domain.SigningApproved && f.Status != domain.SigningRejected {
		fail(w, r, invalid("status", "must be PENDING, APPROVED or REJECTED"))
		return
	}
	page, err := a.store.ListSigningLimitRequests(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// decisionBody is the optional body of :approve and :reject.
type decisionBody struct {
	Note string `json:"note,omitempty"`
}

// readDecision reads the optional body of a decision; an empty body is no note.
func readDecision(r *http.Request) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var body decisionBody
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return "", invalid("body", "must be a JSON object with an optional note")
	}
	note := strings.TrimSpace(body.Note)
	if utf8.RuneCountInString(note) > maxSigningNoteLen {
		return "", invalid("note", "must be at most 1000 characters")
	}
	return note, nil
}

// signingRequestAction handles POST /signing-limit-requests/{id}:approve|:reject.
func (a *API) signingRequestAction(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || (action != store.SigningApprove && action != store.SigningReject) || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown signing limit request action"))
		return
	}
	if !p.HasDuty(dutySigningLimitApprove) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeScope, "missing duty "+dutySigningLimitApprove))
		return
	}
	if _, err := caller(r.Context()); err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	note, err := readDecision(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	cur, err := a.store.GetSigningLimitRequest(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, cur.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.DecideSigningLimitRequest(r.Context(), id, version, action, note, a.today(),
		store.Idem{Key: key, Hash: hashBody("signing-request."+action, map[string]any{"id": id, "version": version, "note": note})})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getSigningLimit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetSigningLimit(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listSigningLimits(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.SigningLimitFilter{LegalEntityID: q.Get("legalEntityId"), WorkerID: q.Get("workerId"), DocumentType: q.Get("documentType"),
		Expired: q.Get("expired") == "true", Today: a.today()}
	for _, u := range []struct{ name, v string }{{"legalEntityId", f.LegalEntityID}, {"workerId", f.WorkerID}} {
		if err := uuidField(u.name, u.v, false); err != nil {
			fail(w, r, err)
			return
		}
	}
	if f.DocumentType != "" && !domain.ValidSigningDocType(f.DocumentType) {
		fail(w, r, invalid("documentType", "must be REQUISITION, PURCHASE_ORDER, INVOICE or EXPENSE"))
		return
	}
	page, err := a.store.ListSigningLimits(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// signingEvaluation is the answer of GET /signing-limits/evaluation.
type signingEvaluation struct {
	Limit              *domain.SigningLimit `json:"limit,omitempty"`
	Expired            bool                 `json:"expired"`
	RequesterEscalates bool                 `json:"requesterEscalates"`
	ApproverAllowed    bool                 `json:"approverAllowed"`
	Code               string               `json:"code,omitempty"`
	Detail             string               `json:"detail,omitempty"`
}

// evaluateSigningLimit handles GET /signing-limits/evaluation: what the worker's limit does to
// a document of the given amount, as requester (an extra approval level) and as approver.
func (a *API) evaluateSigningLimit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for _, f := range []struct{ name, v string }{{"legalEntityId", q.Get("legalEntityId")}, {"workerId", q.Get("workerId")}} {
		if err := uuidField(f.name, f.v, true); err != nil {
			fail(w, r, err)
			return
		}
	}
	docType := q.Get("documentType")
	if !domain.ValidSigningDocType(docType) {
		fail(w, r, invalid("documentType", "must be REQUISITION, PURCHASE_ORDER, INVOICE or EXPENSE"))
		return
	}
	currency, err := currencyField(q.Get("currency"))
	if err != nil {
		fail(w, r, err)
		return
	}
	amount, err := limitField("amount", q.Get("amount"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if p, err := principal(r.Context()); err != nil {
		fail(w, r, err)
		return
	} else if err := inScope(p, q.Get("legalEntityId")); err != nil {
		fail(w, r, err)
		return
	}
	limits, err := a.store.SigningLimitsFor(r.Context(), q.Get("legalEntityId"), q.Get("workerId"), docType)
	if err != nil {
		fail(w, r, err)
		return
	}
	today := a.today()
	out := signingEvaluation{ApproverAllowed: true}
	for i := range limits {
		if limits[i].Currency == currency {
			out.Limit, out.Expired = &limits[i], limits[i].Expired(today)
		}
	}
	out.RequesterEscalates, _ = domain.RequesterEscalates(limits, amount, currency, today)
	if err := domain.CheckApprover(limits, amount, currency, today); err != nil {
		de := err.(*domain.Error)
		out.ApproverAllowed, out.Code, out.Detail = false, de.Code, de.Detail
	}
	httpx.JSON(w, http.StatusOK, out)
}

// signingLimitAction handles POST /signing-limits/{id}:recertify.
func (a *API) signingLimitAction(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || action != "recertify" || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown signing limit action"))
		return
	}
	if !p.HasDuty(dutySigningLimitApprove) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeScope, "missing duty "+dutySigningLimitApprove))
		return
	}
	if _, err := caller(r.Context()); err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	cur, err := a.store.GetSigningLimit(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, cur.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.RecertifySigningLimit(r.Context(), id, version, a.today(),
		store.Idem{Key: key, Hash: hashBody("signing-limit.recertify", map[string]any{"id": id, "version": version})})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// approverCheck applies the approval limit of userID to a document. A caller that is not
// a named user has no signing limit, so it cannot approve an amount above zero.
func (a *API) approverCheck(r *http.Request, legalEntityID, userID, docType string, amount decimal.Decimal, currency string) error {
	if !ids.Valid(userID) {
		return domain.CheckApprover(nil, amount, currency, a.today())
	}
	limits, err := a.store.SigningLimitsFor(r.Context(), legalEntityID, userID, docType)
	if err != nil {
		return err
	}
	return domain.CheckApprover(limits, amount, currency, a.today())
}

// registerSigningRoutes mounts the FR-17.17 routes under /v1/procurement.
func (a *API) registerSigningRoutes(r chi.Router) {
	read := r.With(authz.Require(anyDuty{dutySigningLimitApprove, dutyPurchaseOrderInquire},
		dutySigningLimitApprove+" or "+dutyPurchaseOrderInquire, ""))
	r.With(authz.RequireDuty(dutyRequisitionCreate)).Post("/signing-limit-requests", a.createSigningRequest)
	read.Get("/signing-limit-requests", a.listSigningRequests)
	read.Get("/signing-limit-requests/{id}", a.getSigningRequest)
	// ":approve" and ":reject" arrive as one path segment and check their own duty.
	r.Post("/signing-limit-requests/{id}", a.signingRequestAction)
	read.Get("/signing-limits", a.listSigningLimits)
	read.Get("/signing-limits/evaluation", a.evaluateSigningLimit)
	read.Get("/signing-limits/{id}", a.getSigningLimit)
	r.Post("/signing-limits/{id}", a.signingLimitAction)
}
