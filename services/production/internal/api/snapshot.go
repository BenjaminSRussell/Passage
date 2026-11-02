package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// snapshotRequest is the body of POST /v1/production/cost-snapshots:run.
type snapshotRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	// Date is the snapshot day (YYYY-MM-DD, UTC); empty is today. A day already snapshotted is
	// replaced, so a run is idempotent.
	Date string `json:"date"`
}

// runCostSnapshot handles POST /v1/production/cost-snapshots:run: the on-demand form of the
// daily `order-cost-snapshot` job (PRD 4.21.16). The scheduler (wave 4) calls it; the order
// KPI trend of wave 46 reads the rows. It takes the cost read duty: it posts nothing and stores
// only what the caller may already read, as a dated copy.
func (a *API) runCostSnapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req snapshotRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	} else if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		fields["legalEntityId"] = "must be the legal entity of the caller"
	}
	if req.Date != "" && !isDate(req.Date) {
		fields["date"] = "must be a date (YYYY-MM-DD)"
	} else if req.Date > time.Now().UTC().Format(time.DateOnly) {
		fields["date"] = "must not be in the future"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, err := a.store.RunCostSnapshot(r.Context(), store.CostSnapshotRun{LegalEntityID: req.LegalEntityID, Date: req.Date, Actor: p.UserID})
	if err != nil {
		a.fail(w, r, "run the order cost snapshot", err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

type snapshotView struct {
	OrderID       string `json:"orderId"`
	OrderNumber   string `json:"orderNumber"`
	LegalEntityID string `json:"legalEntityId"`
	SiteID        string `json:"siteId"`
	SnapshotDate  string `json:"snapshotDate"`
	Status        string `json:"status"`
	Currency      string `json:"currency"`
	QtyOrdered    string `json:"qtyOrdered"`
	QtyReported   string `json:"qtyReported"`
	PlannedCost   string `json:"plannedCost"`
	ActualCost    string `json:"actualCost"`
	WIPBalance    string `json:"wipBalance"`
	// CostVariance is actual minus planned cost to date, to the currency's minor unit.
	CostVariance string `json:"costVariance"`
}

type snapshotPageView struct {
	Items      []snapshotView `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// listCostSnapshots handles GET /v1/production/cost-snapshots: the stored snapshots, newest
// day first, filtered by order and date range.
func (a *API) listCostSnapshots(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.CostSnapshotFilter{LegalEntityID: q.Get("legalEntityId"), OrderID: q.Get("orderId"), From: q.Get("from"), To: q.Get("to"), Cursor: q.Get("cursor")}
	f.Limit = reportLimit(q.Get("limit"), fields)
	for name, v := range map[string]string{"legalEntityId": f.LegalEntityID, "orderId": f.OrderID} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	for name, v := range map[string]string{"from": f.From, "to": f.To} {
		if v != "" && !isDate(v) {
			fields[name] = "must be a date (YYYY-MM-DD)"
		}
	}
	if f.Cursor != "" {
		d, id, found := strings.Cut(f.Cursor, "|")
		if !found || !isDate(d) || !ids.Valid(id) {
			fields["cursor"] = "must be the nextCursor of a previous page"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	page, err := a.store.CostSnapshots(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list the order cost snapshots", err)
		return
	}
	v := snapshotPageView{Items: make([]snapshotView, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, x := range page.Items {
		v.Items = append(v.Items, snapshotView{OrderID: x.OrderID, OrderNumber: x.OrderNumber, LegalEntityID: x.LegalEntityID, SiteID: x.SiteID,
			SnapshotDate: x.SnapshotDate, Status: x.Status, Currency: x.Currency, QtyOrdered: x.QtyOrdered.String(), QtyReported: x.QtyReported.String(),
			PlannedCost: fixed(x.PlannedCost, x.Currency), ActualCost: fixed(x.ActualCost, x.Currency), WIPBalance: fixed(x.WIPBalance, x.Currency),
			CostVariance: fixed(x.ActualCost.Sub(x.PlannedCost), x.Currency)})
	}
	httpx.JSON(w, http.StatusOK, v)
}
