package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

var operStatuses = map[domain.OperStatus]bool{domain.OperOpen: true, domain.OperStarted: true, domain.OperFinished: true}

// subcontractRowView is one subcontract operation; amount is in the currency's minor unit.
type subcontractRowView struct {
	OrderID       string            `json:"orderId"`
	OrderNumber   string            `json:"orderNumber"`
	OrderStatus   domain.Status     `json:"orderStatus"`
	LegalEntityID string            `json:"legalEntityId"`
	SiteID        string            `json:"siteId"`
	Currency      string            `json:"currency"`
	DueDate       *string           `json:"dueDate"`
	OperNumber    int               `json:"operNumber"`
	ResourceID    string            `json:"resourceId"`
	Qty           string            `json:"qty"`
	QtyGood       string            `json:"qtyGood"`
	UnitPrice     string            `json:"unitPrice"`
	Amount        string            `json:"amount"`
	OperStatus    domain.OperStatus `json:"operStatus"`
	PORequestedAt *time.Time        `json:"poRequestedAt"`
}

type subcontractTotalView struct {
	Currency   string `json:"currency"`
	Operations int    `json:"operations"`
	Amount     string `json:"amount"`
}

type subcontractReportView struct {
	Items      []subcontractRowView   `json:"items"`
	Totals     []subcontractTotalView `json:"totals"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

// parseSubcontractCursor reads "<order id>:<operation number>".
func parseSubcontractCursor(raw string) (string, int, bool) {
	orderID, oper, found := strings.Cut(raw, ":")
	n, err := strconv.Atoi(oper)
	if !found || !ids.Valid(orderID) || err != nil || n < 1 {
		return "", 0, false
	}
	return orderID, n, true
}

// subcontractReport serves the subcontract operations inquiry (PRD 4.21.14,
// FR-21.10): every subcontract operation with its vendor resource, quantity,
// price, status and when release requested its vendor PO, with totals per currency.
func (a *API) subcontractReport(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.SubcontractFilter{LegalEntityID: q.Get("legalEntityId"), SiteID: q.Get("siteId"), ResourceID: q.Get("resourceId"),
		OperStatus: domain.OperStatus(q.Get("status")), OrderStatus: domain.Status(q.Get("orderStatus")), DueFrom: q.Get("dueFrom"), DueTo: q.Get("dueTo")}
	f.Limit = reportLimit(q.Get("limit"), fields)
	for name, v := range map[string]string{"legalEntityId": f.LegalEntityID, "siteId": f.SiteID, "resourceId": f.ResourceID} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if f.OperStatus != "" && !operStatuses[f.OperStatus] {
		fields["status"] = "must be open, started or finished"
	}
	if f.OrderStatus != "" && !f.OrderStatus.Valid() {
		fields["orderStatus"] = "must be a production order status"
	}
	for name, v := range map[string]string{"dueFrom": f.DueFrom, "dueTo": f.DueTo} {
		if v != "" && !isDate(v) {
			fields[name] = "must be a date (YYYY-MM-DD)"
		}
	}
	if isDate(f.DueFrom) && isDate(f.DueTo) && f.DueFrom > f.DueTo {
		fields["dueTo"] = "must not be before dueFrom"
	}
	if raw := q.Get("cursor"); raw != "" {
		var ok bool
		if f.CursorOrder, f.CursorOper, ok = parseSubcontractCursor(raw); !ok {
			fields["cursor"] = "must be the nextCursor of a previous page"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rep, err := a.store.SubcontractOperations(r.Context(), f)
	if err != nil {
		a.fail(w, r, "read the subcontract operations", err)
		return
	}
	v := subcontractReportView{Items: make([]subcontractRowView, 0, len(rep.Items)), Totals: make([]subcontractTotalView, 0, len(rep.Totals)),
		NextCursor: rep.NextCursor}
	for _, x := range rep.Items {
		v.Items = append(v.Items, subcontractRowView{OrderID: x.OrderID, OrderNumber: x.OrderNumber, OrderStatus: x.OrderStatus,
			LegalEntityID: x.LegalEntityID, SiteID: x.SiteID, Currency: x.Currency, DueDate: x.DueDate, OperNumber: x.OperNumber,
			ResourceID: x.ResourceID, Qty: x.Qty.String(), QtyGood: x.QtyGood.String(), UnitPrice: x.UnitPrice.String(),
			Amount: fixed(x.Amount, x.Currency), OperStatus: x.OperStatus, PORequestedAt: x.PORequestedAt})
	}
	for _, t := range rep.Totals {
		v.Totals = append(v.Totals, subcontractTotalView{Currency: t.Currency, Operations: t.Operations, Amount: fixed(t.Amount, t.Currency)})
	}
	httpx.JSON(w, http.StatusOK, v)
}
