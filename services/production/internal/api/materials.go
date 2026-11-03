package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

type materialLineView struct {
	OrderID        string        `json:"orderId"`
	OrderNumber    string        `json:"orderNumber"`
	OrderStatus    domain.Status `json:"orderStatus"`
	LegalEntityID  string        `json:"legalEntityId"`
	SiteID         string        `json:"siteId"`
	ProductID      string        `json:"productId"`
	DueDate        *string       `json:"dueDate"`
	LineNumber     string        `json:"lineNumber"`
	ItemID         string        `json:"itemId"`
	QtyRequired    string        `json:"qtyRequired"`
	QtyIssued      string        `json:"qtyIssued"`
	QtyOutstanding string        `json:"qtyOutstanding"`
}

type materialTotalsView struct {
	Lines  int `json:"lines"`
	Items  int `json:"items"`
	Orders int `json:"orders"`
}

type materialRequirementsView struct {
	Items      []materialLineView `json:"items"`
	Totals     materialTotalsView `json:"totals"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

type materialItemView struct {
	ItemID         string  `json:"itemId"`
	Orders         int     `json:"orders"`
	Lines          int     `json:"lines"`
	QtyRequired    string  `json:"qtyRequired"`
	QtyIssued      string  `json:"qtyIssued"`
	QtyOutstanding string  `json:"qtyOutstanding"`
	EarliestDue    *string `json:"earliestDue"`
}

type materialItemsView struct {
	Items      []materialItemView `json:"items"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

// materialFilter reads and checks the query shared by both material inquiries. byItem selects the
// cursor form: an item id, or an order id and BOM line sequence.
func materialFilter(r *http.Request, byItem bool) (store.MaterialFilter, map[string]string) {
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.MaterialFilter{LegalEntityID: q.Get("legalEntityId"), SiteID: q.Get("siteId"), ItemID: q.Get("itemId"), OrderID: q.Get("orderId"),
		OrderStatus: domain.Status(q.Get("orderStatus")), DueFrom: q.Get("dueFrom"), DueTo: q.Get("dueTo")}
	f.Limit = reportLimit(q.Get("limit"), fields)
	for name, v := range map[string]string{"legalEntityId": f.LegalEntityID, "siteId": f.SiteID, "itemId": f.ItemID, "orderId": f.OrderID} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
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
	switch q.Get("all") {
	case "", "false":
	case "true":
		f.All = true
	default:
		fields["all"] = "must be true or false"
	}
	if raw := q.Get("cursor"); raw != "" {
		if byItem {
			if !ids.Valid(raw) {
				fields["cursor"] = "must be the nextCursor of a previous page"
			}
			f.CursorItem = raw
		} else {
			var ok bool
			if f.CursorOrder, f.CursorSeq, ok = parseSubcontractCursor(raw); !ok {
				fields["cursor"] = "must be the nextCursor of a previous page"
			}
		}
	}
	return f, fields
}

// materialRequirements serves the BOM lines of open orders with what is still to issue (PRD 4.21.14
// material shortage inquiry, without on-hand until wave 16): required, issued (the line's booked
// consumption) and outstanding, by order and line.
func (a *API) materialRequirements(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	f, fields := materialFilter(r, false)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rep, err := a.store.MaterialRequirements(r.Context(), f)
	if err != nil {
		a.fail(w, r, "read the material requirements", err)
		return
	}
	v := materialRequirementsView{Items: make([]materialLineView, 0, len(rep.Items)), NextCursor: rep.NextCursor,
		Totals: materialTotalsView{Lines: rep.Totals.Lines, Items: rep.Totals.Items, Orders: rep.Totals.Orders}}
	for _, x := range rep.Items {
		v.Items = append(v.Items, materialLineView{OrderID: x.OrderID, OrderNumber: x.OrderNumber, OrderStatus: x.OrderStatus, LegalEntityID: x.LegalEntityID,
			SiteID: x.SiteID, ProductID: x.ProductID, DueDate: x.DueDate, LineNumber: x.LineNumber, ItemID: x.ItemID,
			QtyRequired: x.QtyRequired.String(), QtyIssued: x.QtyIssued.String(), QtyOutstanding: x.QtyOutstanding.String()})
	}
	httpx.JSON(w, http.StatusOK, v)
}

// materialItems serves the same demand rolled up per component item.
func (a *API) materialItems(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	f, fields := materialFilter(r, true)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rep, err := a.store.MaterialItems(r.Context(), f)
	if err != nil {
		a.fail(w, r, "read the material requirements by item", err)
		return
	}
	v := materialItemsView{Items: make([]materialItemView, 0, len(rep.Items)), NextCursor: rep.NextCursor}
	for _, x := range rep.Items {
		v.Items = append(v.Items, materialItemView{ItemID: x.ItemID, Orders: x.Orders, Lines: x.Lines, QtyRequired: x.QtyRequired.String(),
			QtyIssued: x.QtyIssued.String(), QtyOutstanding: x.QtyOutstanding.String(), EarliestDue: x.EarliestDue})
	}
	httpx.JSON(w, http.StatusOK, v)
}
