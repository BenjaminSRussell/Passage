package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// bulkRequest is the body of POST /v1/production/orders:bulk-status (FR-21.12).
type bulkRequest struct {
	OrderIDs []string `json:"orderIds"`
	Command  string   `json:"command"`
}

// bulkResponse reports every order's outcome. The call succeeds when the batch
// is valid; Code and Detail carry PROD-0022 when some orders failed.
type bulkResponse struct {
	Command   domain.Command   `json:"command"`
	Succeeded int              `json:"succeeded"`
	Failed    int              `json:"failed"`
	Code      string           `json:"code,omitempty"`
	Detail    string           `json:"detail,omitempty"`
	Results   []store.BulkItem `json:"results"`
}

// splitRequest is the body of POST /v1/production/orders/{id}:split (FR-21.15).
// minimumQty and multiple are the item's settings, passed by the caller until
// released-product settings (wave 8) are read by the service.
type splitRequest struct {
	SplitQty   *decimal.Decimal `json:"splitQty"`
	MinimumQty *decimal.Decimal `json:"minimumQty"`
	Multiple   *decimal.Decimal `json:"multiple"`
	// Allocation is proportional (default) or keep-on-original; it matters for a Started order (BR-21.11).
	Allocation string `json:"allocation"`
}

// mergeRequest is the body of POST /v1/production/orders:merge. The first order survives.
type mergeRequest struct {
	Orders []mergeRef `json:"orders"`
}

type mergeRef struct {
	OrderID string `json:"orderId"`
	Version int    `json:"version"`
}

// bulkStatus applies one status command to up to 500 orders, each in its own
// transaction, and reports per-order results (FR-21.12). The route needs the
// release duty; the command's own duty is checked as well.
func (a *API) bulkStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req bulkRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	if err := domain.ValidateBulk(req.OrderIDs); err != nil {
		fields["orderIds"] = err.(*domain.Error).Detail
	}
	for i, id := range req.OrderIDs {
		if !ids.Valid(id) {
			fields["orderIds["+strconv.Itoa(i)+"]"] = "must be a UUID"
		}
	}
	cmd, known := commands[req.Command]
	if !known || !domain.BulkCommand(cmd) {
		fields["command"] = "must be estimate, schedule, release, start, end or cancel"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if duty := commandDuty[cmd]; !p.HasDuty(duty) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+duty))
		return
	}
	results := a.store.BulkTransition(r.Context(), store.Bulk{OrderIDs: req.OrderIDs, Command: cmd, ZeroOutputDuty: p.HasDuty(DutyZeroOutput),
		Actor: p.UserID, TraceID: middleware.GetReqID(r.Context()), IdempotencyKey: key, RequestHash: commandHash(cmd, "", decimal.Zero, decimal.Zero)})
	resp := bulkResponse{Command: cmd, Results: results}
	for _, item := range results {
		switch {
		case !item.OK:
			resp.Failed++
			a.refused.Add(1)
		case item.Replayed:
			resp.Succeeded++
			a.replayed.Add(1)
		default:
			resp.Succeeded++
			a.moved.Add(1)
		}
	}
	if resp.Failed > 0 {
		resp.Code = domain.CodeBulkPartial
		resp.Detail = fmt.Sprintf("Bulk status update: %d succeeded, %d failed; see the results", resp.Succeeded, resp.Failed)
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// split handles POST /v1/production/orders/{id}:split; transition routes it here
// behind the release duty.
func (a *API) split(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	var req splitRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	in := store.Split{OrderID: id, IfMatch: ifMatch, Actor: p.UserID, TraceID: middleware.GetReqID(r.Context()), IdempotencyKey: key}
	if req.SplitQty == nil {
		fields["splitQty"] = "is required: the quantity the new order receives"
	} else {
		checkQuantity("splitQty", *req.SplitQty, true, fields)
		in.SplitQty = *req.SplitQty
	}
	if req.MinimumQty != nil {
		checkQuantity("minimumQty", *req.MinimumQty, false, fields)
		in.MinQty = *req.MinimumQty
	}
	if req.Multiple != nil {
		checkQuantity("multiple", *req.Multiple, false, fields)
		in.Multiple = *req.Multiple
	}
	in.Allocation = domain.Allocation(req.Allocation)
	if in.Allocation == "" {
		in.Allocation = domain.AllocProportional
	}
	if !in.Allocation.Valid() {
		fields["allocation"] = "must be proportional or keep-on-original"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	in.RequestHash = hashOf(struct {
		Command                    domain.Command
		SplitQty, MinQty, Multiple string
		Allocation                 domain.Allocation
	}{domain.CmdSplit, in.SplitQty.String(), in.MinQty.String(), in.Multiple.String(), in.Allocation})
	res, err := a.store.SplitOrder(r.Context(), in)
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(res.Original.Version))
	w.Header().Set("Location", "/v1/production/orders/"+res.New.ID)
	if res.Replayed {
		a.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, res)
		return
	}
	a.moved.Add(1)
	a.created.Add(1)
	httpx.JSON(w, http.StatusCreated, res)
}

// merge handles POST /v1/production/orders:merge (FR-21.15).
func (a *API) merge(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req mergeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	if len(req.Orders) < 2 || len(req.Orders) > domain.MaxBulkOrders {
		fields["orders"] = fmt.Sprintf("a merge takes 2 to %d orders", domain.MaxBulkOrders)
	}
	seen := map[string]bool{}
	refs := make([]store.MergeRef, 0, len(req.Orders))
	for i, ref := range req.Orders {
		at := "orders[" + strconv.Itoa(i) + "]"
		switch {
		case !ids.Valid(ref.OrderID):
			fields[at+".orderId"] = "must be a UUID"
		case seen[ref.OrderID]:
			fields[at+".orderId"] = "is listed twice"
		}
		seen[ref.OrderID] = true
		if ref.Version < 1 {
			fields[at+".version"] = "must be the order version (ETag) read from the API"
		}
		refs = append(refs, store.MergeRef{OrderID: ref.OrderID, Version: ref.Version})
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, err := a.store.MergeOrders(r.Context(), store.Merge{Orders: refs, Actor: p.UserID, TraceID: middleware.GetReqID(r.Context()),
		IdempotencyKey: key, RequestHash: hashOf(struct {
			Command domain.Command
			Orders  []mergeRef
		}{domain.CmdMerge, req.Orders})})
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(res.Survivor.Version))
	if res.Replayed {
		a.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.moved.Add(1)
	}
	httpx.JSON(w, http.StatusOK, res)
}

// commandHash fingerprints a status command for its Idempotency-Key.
func commandHash(cmd domain.Command, resetTo domain.Status, good, errorQty decimal.Decimal) string {
	return hashOf(struct {
		Command domain.Command
		ResetTo domain.Status
		Good    string
		Error   string
	}{cmd, resetTo, good.String(), errorQty.String()})
}
