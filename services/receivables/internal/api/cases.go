package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const maxCaseReason = 500

// caseRoutes mounts the minimal collection cases (FR-30.18). Reads need ARView;
// opening, assigning and closing need CollectionsMaintain (§5.30.17).
func (a *API) caseRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyCollections)).Post("/cases", a.createCase)
	r.With(authz.RequireDuty(dutyView)).Get("/cases", a.listCases)
	r.With(authz.RequireDuty(dutyView)).Get("/cases/{ref}", a.getCase)
	// :assign and :close share one route; caseAction dispatches and checks the duty.
	r.Post("/cases/{ref}", a.caseAction)
}

type createCaseRequest struct {
	LegalEntityID  string           `json:"legalEntityId"`
	CustomerID     string           `json:"customerId"`
	Category       string           `json:"category"`
	Owner          string           `json:"owner"`
	Priority       string           `json:"priority"`
	Reason         string           `json:"reason"`
	Currency       string           `json:"currency"`
	OpenItemID     *string          `json:"openItemId"`
	AttachedAmount *decimal.Decimal `json:"attachedAmount"`
}

// createCase handles POST /cases.
func (a *API) createCase(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requiredKey(w, r)
	if !ok {
		return
	}
	var req createCaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Owner, req.Reason = strings.TrimSpace(req.Owner), strings.TrimSpace(req.Reason)
	if req.Priority == "" {
		req.Priority = domain.CasePriorityNormal
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if !domain.ValidCaseCategory(req.Category) {
		fields["category"] = "must be COLLECTION, DISPUTE or DEDUCTION"
	}
	if !domain.ValidCasePriority(req.Priority) {
		fields["priority"] = "must be LOW, NORMAL or HIGH"
	}
	if n := len(req.Owner); n < 1 || n > maxOwner {
		fields["owner"] = fmt.Sprintf("must be 1 to %d characters", maxOwner)
	}
	if n := len(req.Reason); n < 1 || n > maxCaseReason {
		fields["reason"] = fmt.Sprintf("must be 1 to %d characters", maxCaseReason)
	}
	if req.OpenItemID != nil && !ids.Valid(*req.OpenItemID) {
		fields["openItemId"] = "must be a UUID"
	}
	if (req.OpenItemID == nil || req.Currency != "") && !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code; it may be left out only when an open item is linked"
	}
	if req.AttachedAmount != nil && req.AttachedAmount.IsNegative() {
		fields["attachedAmount"] = "must not be negative"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.CreateCase(r.Context(), store.Case{LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID,
		Category: req.Category, Owner: req.Owner, Priority: req.Priority, Reason: req.Reason, Currency: req.Currency,
		OpenItemID: req.OpenItemID, AttachedAmount: req.AttachedAmount}, key)
	if err != nil {
		a.fail("create case", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

// listCases handles GET /cases?status=&category=&customer=, open first and nearest SLA first.
func (a *API) listCases(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.CaseFilter{Status: q.Get("status"), Category: q.Get("category"), CustomerID: q.Get("customer")}
	if f.Status != "" && f.Status != domain.CaseOpen && f.Status != domain.CaseClosed {
		badQuery(w, r, "status", "must be OPEN or CLOSED")
		return
	}
	if f.Category != "" && !domain.ValidCaseCategory(f.Category) {
		badQuery(w, r, "category", "must be COLLECTION, DISPUTE or DEDUCTION")
		return
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		badQuery(w, r, "customer", "must be a UUID")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListCases(r.Context(), f, limit)
	if err != nil {
		a.fail("list cases", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getCase handles GET /cases/{id}.
func (a *API) getCase(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "ref")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeCaseNotFound, "collection case not found"))
		return
	}
	out, err := a.store.GetCase(r.Context(), id)
	if err != nil {
		a.fail("get case", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

type caseActionRequest struct {
	Owner string  `json:"owner"`
	Note  *string `json:"note"`
}

// caseAction handles POST /cases/{id}:assign and POST /cases/{id}:close. Both need If-Match.
func (a *API) caseAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	if !found || !ids.Valid(id) || (action != "assign" && action != "close") {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeCaseNotFound, "unknown case or action"))
		return
	}
	if !require(w, r, dutyCollections) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req caseActionRequest
	if err := decodeOptional(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var out store.Case
	if action == "assign" {
		owner := strings.TrimSpace(req.Owner)
		if n := len(owner); n < 1 || n > maxOwner {
			writeFields(w, r, map[string]string{"owner": fmt.Sprintf("must be 1 to %d characters", maxOwner)})
			return
		}
		out, err = a.store.AssignCase(r.Context(), id, version, owner)
	} else {
		if req.Note != nil && len(*req.Note) > maxNote {
			writeFields(w, r, map[string]string{"note": fmt.Sprintf("must be at most %d characters", maxNote)})
			return
		}
		out, err = a.store.CloseCase(r.Context(), id, version, req.Note)
	}
	if err != nil {
		a.fail(action+" case", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// decodeOptional decodes a JSON body when there is one; an empty body leaves v as is.
func decodeOptional(r *http.Request, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return httpx.NewProblem(http.StatusBadRequest, "COM-0001", "unreadable body: "+err.Error())
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return httpx.NewProblem(http.StatusBadRequest, "COM-0001", "malformed JSON body: "+err.Error())
	}
	return nil
}
