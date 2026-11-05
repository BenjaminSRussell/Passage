package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// changeOrderLineRequest is one impact line of POST /v1/projects/change-orders.
type changeOrderLineRequest struct {
	WBSNodeID         *string `json:"wbsNodeId,omitempty"`
	CostImpact        string  `json:"costImpact"`
	RevenueImpact     string  `json:"revenueImpact"`
	EffortHoursDelta  string  `json:"effortHoursDelta,omitempty"`
	ScheduleDaysDelta int     `json:"scheduleDaysDelta"`
}

// changeOrderRequest is the wire shape of POST /v1/projects/change-orders.
type changeOrderRequest struct {
	ProjectID           string                   `json:"projectId"`
	Type                string                   `json:"type"`
	Title               string                   `json:"title"`
	Description         string                   `json:"description,omitempty"`
	CustomerApprovalRef *string                  `json:"customerApprovalRef,omitempty"`
	VendorPartyID       *string                  `json:"vendorPartyId,omitempty"`
	Lines               []changeOrderLineRequest `json:"lines"`
}

// decisionRequest is the optional body of submit and approve: the customer
// approval reference (evidence for a customer claim, BR-41.13).
type decisionRequest struct {
	CustomerApprovalRef *string `json:"customerApprovalRef,omitempty"`
}

type changeOrderList struct {
	Items []domain.ChangeOrder `json:"items"`
}

// applyResponse is the result of POST .../apply.
type applyResponse struct {
	ChangeOrder domain.ChangeOrder `json:"changeOrder"`
	Effect      domain.ApplyResult `json:"effect"`
	Baseline    domain.PlanVersion `json:"baseline"`
}

// registerChangeOrders mounts the change order routes (FR-41.18). The static
// /change-orders prefix wins over /{id}; project ids are capitals and hyphens.
func (a *API) registerChangeOrders(r chi.Router) {
	r.With(authz.RequireDuty(DutyMaintain)).Post("/change-orders", a.createChangeOrder)
	r.With(authz.RequireDuty(DutyInquire)).Get("/change-orders", a.listChangeOrders)
	r.With(authz.RequireDuty(DutyInquire)).Get("/change-orders/{coId}", a.getChangeOrder)
	r.With(authz.RequireDuty(DutyMaintain)).Post("/change-orders/{coId}/submit", a.decideChangeOrder(domain.COStatusSubmitted))
	r.With(authz.RequireDuty(DutyChangeOrderApprove)).Post("/change-orders/{coId}/approve", a.decideChangeOrder(domain.COStatusApproved))
	r.With(authz.RequireDuty(DutyChangeOrderApprove)).Post("/change-orders/{coId}/reject", a.decideChangeOrder(domain.COStatusRejected))
	r.With(authz.RequireDuty(DutyChangeOrderApprove)).Post("/change-orders/{coId}/apply", a.applyChangeOrder)
}

// changeOrderID reads {coId}; a value that is not a UUID cannot name one (404).
func changeOrderID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "coId")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "change order not found"))
		return "", false
	}
	return id, true
}

// createChangeOrder records a DRAFT change order or claim with its impact
// lines. Idempotency-Key is required; a replay returns the stored change order
// with 200 and Idempotent-Replayed: true.
func (a *API) createChangeOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok || !requireUser(w, r, p) {
		return
	}
	var req changeOrderRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	if fields == nil {
		fields = map[string]string{}
	}
	in := domain.ChangeOrder{
		ProjectID: req.ProjectID, Type: domain.ChangeOrderType(req.Type), Title: strings.TrimSpace(req.Title),
		Description: req.Description, CustomerApprovalRef: req.CustomerApprovalRef, VendorPartyID: req.VendorPartyID,
	}
	for _, l := range req.Lines {
		in.Lines = append(in.Lines, domain.ChangeOrderLine{
			WBSNodeID: l.WBSNodeID, CostImpact: l.CostImpact, RevenueImpact: l.RevenueImpact,
			EffortHoursDelta: l.EffortHoursDelta, ScheduleDaysDelta: l.ScheduleDaysDelta,
		})
	}
	domain.NormalizeChangeOrder(&in)
	for k, v := range domain.ValidateChangeOrder(in) {
		fields[k] = v
	}
	if !domain.ValidProjectID(in.ProjectID) {
		fields["projectId"] = "must be a project id"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	out, created, err := a.store.CreateChangeOrder(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, createdStatus(created), out)
}

// listChangeOrders is the change order log of one project (projectId required;
// type and status optional).
func (a *API) listChangeOrders(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	f := store.ChangeOrderFilter{ProjectID: q.Get("projectId"), Type: domain.ChangeOrderType(q.Get("type")), Status: domain.ChangeOrderStatus(q.Get("status"))}
	fields := map[string]string{}
	if !domain.ValidProjectID(f.ProjectID) {
		fields["projectId"] = "is required and must be a project id"
	}
	if f.Type != "" && !domain.ValidChangeOrderType(f.Type) {
		fields["type"] = "must be SCOPE_CHANGE, CLAIM_CUSTOMER, CLAIM_VENDOR or INTERNAL"
	}
	switch f.Status {
	case "", domain.COStatusDraft, domain.COStatusSubmitted, domain.COStatusApproved, domain.COStatusRejected, domain.COStatusApplied:
	default:
		fields["status"] = "must be DRAFT, SUBMITTED, APPROVED, REJECTED or APPLIED"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	items, err := a.store.ListChangeOrders(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if items == nil {
		items = []domain.ChangeOrder{}
	}
	httpx.JSON(w, http.StatusOK, changeOrderList{Items: items})
}

func (a *API) getChangeOrder(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := changeOrderID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetChangeOrder(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

// decideChangeOrder returns the handler of one lifecycle move: submit
// (projects.maintain), approve or reject (projects.change-order.approve). If-Match carries
// the change order version (428 when absent, 412 when stale). The optional
// body attaches the customer approval reference on submit or approve.
func (a *API) decideChangeOrder(to domain.ChangeOrderStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal(w, r)
		if !ok || !requireUser(w, r, p) {
			return
		}
		id, ok := changeOrderID(w, r)
		if !ok {
			return
		}
		version, ok := ifMatchVersion(w, r)
		if !ok {
			return
		}
		var req decisionRequest
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &req); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		if req.CustomerApprovalRef != nil {
			ref := strings.TrimSpace(*req.CustomerApprovalRef)
			if n := len([]rune(ref)); n < 1 || n > 200 {
				httpx.Fail(w, r, validation(map[string]string{"customerApprovalRef": "must be 1 to 200 characters when given"}))
				return
			}
			req.CustomerApprovalRef = &ref
		}
		out, err := a.store.DecideChangeOrder(r.Context(), id, version, to, req.CustomerApprovalRef)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		setETag(w, out.Version)
		httpx.JSON(w, http.StatusOK, out)
	}
}

// applyChangeOrder applies an APPROVED change order (BR-41.13): contract value,
// a new BASELINE and the changeorder.applied event, atomically. If-Match
// carries the change order version; a second apply is PROJ-CO-001 whatever the
// version, so a retry after a lost response is safe without a key.
func (a *API) applyChangeOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok || !requireUser(w, r, p) {
		return
	}
	id, ok := changeOrderID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	out, err := a.store.ApplyChangeOrder(r.Context(), id, version)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.ChangeOrder.Version)
	httpx.JSON(w, http.StatusOK, applyResponse{ChangeOrder: out.ChangeOrder, Effect: out.Effect, Baseline: out.Baseline})
}
