package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

type operationRowView struct {
	OrderID       string            `json:"orderId"`
	OrderNumber   string            `json:"orderNumber"`
	OrderStatus   domain.Status     `json:"orderStatus"`
	LegalEntityID string            `json:"legalEntityId"`
	SiteID        string            `json:"siteId"`
	ProductID     string            `json:"productId"`
	DueDate       *string           `json:"dueDate"`
	OperNumber    int               `json:"operNumber"`
	ResourceID    string            `json:"resourceId"`
	Subcontract   bool              `json:"subcontract"`
	ProcessQty    string            `json:"processQty"`
	PlannedHours  string            `json:"plannedHours"`
	HoursReported string            `json:"hoursReported"`
	QtyGood       string            `json:"qtyGood"`
	QtyError      string            `json:"qtyError"`
	OperStatus    domain.OperStatus `json:"operStatus"`
}

type operationTotalsView struct {
	Operations    int    `json:"operations"`
	Open          int    `json:"open"`
	Started       int    `json:"started"`
	Finished      int    `json:"finished"`
	PlannedHours  string `json:"plannedHours"`
	HoursReported string `json:"hoursReported"`
	QtyGood       string `json:"qtyGood"`
	QtyError      string `json:"qtyError"`
}

type operationsView struct {
	Items      []operationRowView  `json:"items"`
	Totals     operationTotalsView `json:"totals"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

// operationsOverview serves the operations overview (PRD 4.21.14): every
// operation of the caller's orders with planned and reported hours, good and
// error quantities and status, ordered by order and operation.
func (a *API) operationsOverview(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.OperationFilter{LegalEntityID: q.Get("legalEntityId"), SiteID: q.Get("siteId"), ResourceID: q.Get("resourceId"), OrderID: q.Get("orderId"),
		OperStatus: domain.OperStatus(q.Get("status")), OrderStatus: domain.Status(q.Get("orderStatus")), DueFrom: q.Get("dueFrom"), DueTo: q.Get("dueTo")}
	f.Limit = reportLimit(q.Get("limit"), fields)
	for name, v := range map[string]string{"legalEntityId": f.LegalEntityID, "siteId": f.SiteID, "resourceId": f.ResourceID, "orderId": f.OrderID} {
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
	rep, err := a.store.OperationsOverview(r.Context(), f)
	if err != nil {
		a.fail(w, r, "read the operations overview", err)
		return
	}
	v := operationsView{Items: make([]operationRowView, 0, len(rep.Items)), NextCursor: rep.NextCursor,
		Totals: operationTotalsView{Operations: rep.Totals.Operations, Open: rep.Totals.Open, Started: rep.Totals.Started, Finished: rep.Totals.Finished,
			PlannedHours: rep.Totals.PlannedHours.StringFixed(2), HoursReported: rep.Totals.HoursReported.StringFixed(2),
			QtyGood: rep.Totals.QtyGood.String(), QtyError: rep.Totals.QtyError.String()}}
	for _, x := range rep.Items {
		v.Items = append(v.Items, operationRowView{OrderID: x.OrderID, OrderNumber: x.OrderNumber, OrderStatus: x.OrderStatus,
			LegalEntityID: x.LegalEntityID, SiteID: x.SiteID, ProductID: x.ProductID, DueDate: x.DueDate, OperNumber: x.OperNumber,
			ResourceID: x.ResourceID, Subcontract: x.Subcontract, ProcessQty: x.ProcessQty.String(), PlannedHours: x.PlannedHours.StringFixed(2),
			HoursReported: x.HoursReported.StringFixed(2), QtyGood: x.QtyGood.String(), QtyError: x.QtyError.String(), OperStatus: x.OperStatus})
	}
	httpx.JSON(w, http.StatusOK, v)
}

type journalRowView struct {
	ID             string             `json:"id"`
	OrderID        string             `json:"orderId"`
	OrderNumber    string             `json:"orderNumber"`
	LegalEntityID  string             `json:"legalEntityId"`
	Type           domain.JournalType `json:"type"`
	JournalNumber  string             `json:"journalNumber"`
	PostingDate    string             `json:"postingDate"`
	Posted         bool               `json:"posted"`
	PostedAt       *time.Time         `json:"postedAt"`
	QtyGood        string             `json:"qtyGood"`
	QtyError       string             `json:"qtyError"`
	ReversalOf     *string            `json:"reversalOf"`
	JournalEntryID *string            `json:"journalEntryId"`
	Currency       string             `json:"currency"`
	Lines          int                `json:"lines"`
	Consumed       string             `json:"consumed"`
	Received       string             `json:"received"`
}

type journalInquiryView struct {
	Items      []journalRowView `json:"items"`
	Total      int              `json:"total"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func journalTypeValid(t domain.JournalType) bool {
	return t.Valid() || t == domain.JournalSplitTransfer
}

// journalInquiry serves the production journals across orders (PRD 4.21.14):
// number, type, order, posting date, posted state, quantities, line count and
// the values consumed and received, filtered by type, posted state, order (id
// or number) and posting date.
func (a *API) journalInquiry(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.JournalFilter{LegalEntityID: q.Get("legalEntityId"), OrderID: q.Get("orderId"), OrderNumber: strings.TrimSpace(q.Get("orderNumber")),
		Type:   domain.JournalType(q.Get("type")),
		Posted: q.Get("posted"), From: q.Get("from"), To: q.Get("to"), Cursor: q.Get("cursor")}
	f.Limit = reportLimit(q.Get("limit"), fields)
	for name, v := range map[string]string{"legalEntityId": f.LegalEntityID, "orderId": f.OrderID, "cursor": f.Cursor} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if f.Type != "" && !journalTypeValid(f.Type) {
		fields["type"] = "must be picking-list, route-card, job-card, report-as-finished or split-transfer"
	}
	if len(f.OrderNumber) > 64 {
		fields["orderNumber"] = "must be at most 64 characters"
	}
	if f.Posted != "" && f.Posted != "true" && f.Posted != "false" {
		fields["posted"] = "must be true or false"
	}
	for name, v := range map[string]string{"from": f.From, "to": f.To} {
		if v != "" && !isDate(v) {
			fields[name] = "must be a date (YYYY-MM-DD)"
		}
	}
	if isDate(f.From) && isDate(f.To) && f.From > f.To {
		fields["to"] = "must not be before from"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rep, err := a.store.JournalInquiry(r.Context(), f)
	if err != nil {
		a.fail(w, r, "read the production journal inquiry", err)
		return
	}
	v := journalInquiryView{Items: make([]journalRowView, 0, len(rep.Items)), Total: rep.Total, NextCursor: rep.NextCursor}
	for _, x := range rep.Items {
		v.Items = append(v.Items, journalRowView{ID: x.ID, OrderID: x.OrderID, OrderNumber: x.OrderNumber, LegalEntityID: x.LegalEntityID,
			Type: x.Type, JournalNumber: x.JournalNumber, PostingDate: x.PostingDate, Posted: x.Posted, PostedAt: x.PostedAt,
			QtyGood: x.QtyGood.String(), QtyError: x.QtyError.String(), ReversalOf: x.ReversalOf, JournalEntryID: x.JournalEntryID,
			Currency: x.Currency, Lines: x.Lines, Consumed: fixed(x.Consumed, x.Currency), Received: fixed(x.Received, x.Currency)})
	}
	httpx.JSON(w, http.StatusOK, v)
}
