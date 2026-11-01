package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
	"github.com/go-chi/chi/v5/middleware"
)

// housekeepingRequest is the body of the two housekeeping jobs (PRD 4.21.16).
type housekeepingRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	// DryRun lists what the job would do and changes nothing.
	DryRun bool `json:"dryRun"`
	// OrderIDs, when set, limits the job to these orders (the candidates a dry run showed): a
	// run then acts on no order the caller did not review. Orders that no longer qualify are left out.
	OrderIDs []string `json:"orderIds"`
}

// housekeepingResponse reports what a job selected and, unless it was a dry run, what
// happened to each order. Truncated says more orders qualify than one run takes.
type housekeepingResponse struct {
	Job           string `json:"job"`
	LegalEntityID string `json:"legalEntityId"`
	DryRun        bool   `json:"dryRun"`
	// Replayed says the Idempotency-Key was used before: the response replays that run's orders.
	Replayed   bool                      `json:"replayed"`
	Total      int                       `json:"total"`
	Truncated  bool                      `json:"truncated"`
	Candidates []store.HousekeepingOrder `json:"candidates"`
	Succeeded  int                       `json:"succeeded"`
	Failed     int                       `json:"failed"`
	Code       string                    `json:"code,omitempty"`
	Detail     string                    `json:"detail,omitempty"`
	Results    []store.BulkItem          `json:"results"`
}

const (
	jobCancelStale = "cancel-stale-created-orders"
	jobAutoEnd     = "auto-end-fully-reported"
)

// cancelStaleOrders handles POST /v1/production/orders:cancel-stale: it cancels the orders
// still Created beyond their site's stale age (PRD 4.21.16). Each cancel is the ordinary
// `:cancel` command with its status log row and events.
func (a *API) cancelStaleOrders(w http.ResponseWriter, r *http.Request) {
	a.housekeeping(w, r, jobCancelStale, domain.CmdCancel)
}

// autoEndOrders handles POST /v1/production/orders:auto-end: it ends the fully reported orders
// of the sites that opted in (PRD 4.21.16). Each end is the ordinary `:end` command, so its
// checks (unposted journals, as-built) and settlement apply and a refusal is listed.
func (a *API) autoEndOrders(w http.ResponseWriter, r *http.Request) {
	a.housekeeping(w, r, jobAutoEnd, domain.CmdEnd)
}

func (a *API) housekeeping(w http.ResponseWriter, r *http.Request, job string, cmd domain.Command) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req housekeepingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	key, fields := idempotencyKey(r)
	if req.DryRun {
		fields = map[string]string{}
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	} else if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		fields["legalEntityId"] = "must be the legal entity of the caller"
	}
	if len(req.OrderIDs) > 0 {
		if err := domain.ValidateBulk(req.OrderIDs); err != nil {
			fields["orderIds"] = err.(*domain.Error).Detail
		}
		for i, id := range req.OrderIDs {
			if !ids.Valid(id) {
				fields["orderIds["+strconv.Itoa(i)+"]"] = "must be a UUID"
			}
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	scan := store.HousekeepingScan{LegalEntityID: req.LegalEntityID, OrderIDs: req.OrderIDs, Limit: domain.MaxBulkOrders}
	// The job's own hash, so a key first used by bulk-status, a single command or the other job is refused.
	hash := "housekeeping:" + job
	var (
		found    []store.HousekeepingOrder
		total    int
		replayed bool
		err      error
	)
	if !req.DryRun {
		// A retry under the same key replays the orders that run moved rather than scanning again.
		found, err = a.store.HousekeepingReplay(r.Context(), req.LegalEntityID, key, hash)
		if err != nil {
			a.refuse(w, r, err)
			return
		}
		replayed, total = len(found) > 0, len(found)
	}
	switch {
	case replayed:
	case job == jobCancelStale:
		found, total, err = a.store.StaleCreatedOrders(r.Context(), scan, domain.DefaultStaleOrderDays)
	default:
		found, total, err = a.store.FullyReportedOrders(r.Context(), scan)
	}
	if err != nil {
		a.fail(w, r, "scan production orders for "+job, err)
		return
	}
	if found == nil {
		found = []store.HousekeepingOrder{}
	}
	resp := housekeepingResponse{Job: job, LegalEntityID: req.LegalEntityID, DryRun: req.DryRun, Replayed: replayed, Total: total,
		Truncated: total > len(found), Candidates: found, Results: []store.BulkItem{}}
	if !req.DryRun && len(found) > 0 {
		orderIDs := make([]string, len(found))
		versions := make(map[string]int, len(found))
		for i, o := range found {
			orderIDs[i], versions[o.OrderID] = o.OrderID, o.Version
		}
		// Each order runs at the version the scan saw: one changed since (estimated, journal added) is refused, not moved.
		resp.Results = a.store.BulkTransition(r.Context(), store.Bulk{OrderIDs: orderIDs, Versions: versions, Command: cmd, ZeroOutputDuty: false,
			Actor: p.UserID, TraceID: middleware.GetReqID(r.Context()), IdempotencyKey: key, RequestHash: hash})
		for _, item := range resp.Results {
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
			resp.Detail = fmt.Sprintf("%s: %d succeeded, %d failed; see the results", job, resp.Succeeded, resp.Failed)
		}
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, http.StatusOK, resp)
}
