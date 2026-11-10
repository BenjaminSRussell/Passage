package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/go-chi/chi/v5"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
	maxAgeDays      = 3650
)

var (
	receiptStates = []domain.ReceiptState{domain.Arrived, domain.Registered, domain.Posted, domain.Cancelled}
	grirStatuses  = []string{"OPEN", "PARTIAL", "CLEARED", "WRITTEN_OFF"}

	invoiceStatuses = []string{domain.InvoiceStatusPosted, domain.InvoiceStatusAuditBlocked, domain.InvoiceStatusAuditRejected}
)

type query struct {
	values url.Values
	fields map[string]string
}

func (q query) uuid(name string) string {
	v := q.values.Get(name)
	if v != "" {
		checkUUID(q.fields, name, v)
	}
	return v
}

func (q query) date(name string) time.Time {
	v := q.values.Get(name)
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(dateLayout, v)
	if err != nil {
		q.fields[name] = "must be a YYYY-MM-DD date"
	}
	return t
}

func (q query) limit() int {
	v := q.values.Get("limit")
	if v == "" {
		return defaultPageSize
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxPageSize {
		q.fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
		return defaultPageSize
	}
	return n
}

func (q query) cursor() (time.Time, string) {
	v := q.values.Get("cursor")
	if v == "" {
		return time.Time{}, ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(v)
	stamp, id, found := strings.Cut(string(raw), "|")
	at, parseErr := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || !found || parseErr != nil || at.IsZero() {
		q.fields["cursor"] = "is not a cursor issued by this API"
		return time.Time{}, ""
	}
	checkUUID(q.fields, "cursor", id)
	return at, id
}

func encodeCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id))
}

type receiptPage struct {
	Items      []receiptBody `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

func (a *API) listReceipts(w http.ResponseWriter, r *http.Request) {
	q := query{values: r.URL.Query(), fields: map[string]string{}}
	f := store.ReceiptFilter{VendorID: q.uuid("vendorId"), PurchaseOrderID: q.uuid("purchaseOrderId"), State: domain.ReceiptState(q.values.Get("state")),
		From: q.date("from"), To: q.date("to"), Limit: q.limit()}
	f.CursorCreatedAt, f.CursorID = q.cursor()
	if f.State != "" && !slices.Contains(receiptStates, f.State) {
		q.fields["state"] = "must be ARRIVED, REGISTERED, POSTED or CANCELLED"
	}
	if len(q.fields) > 0 {
		httpx.Fail(w, r, validationProblem(q.fields))
		return
	}
	rows, err := a.store.ListReceipts(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page := receiptPage{Items: make([]receiptBody, 0, len(rows))}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		page.NextCursor = encodeCursor(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
	}
	for _, rec := range rows {
		page.Items = append(page.Items, toReceiptBody(rec))
	}
	httpx.JSON(w, http.StatusOK, page)
}

type invoicePage struct {
	Items      []invoiceBody `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

func (a *API) listInvoices(w http.ResponseWriter, r *http.Request) {
	q := query{values: r.URL.Query(), fields: map[string]string{}}
	f := store.InvoiceFilter{VendorID: q.uuid("vendorId"), ReceiptID: q.uuid("receiptId"), PurchaseOrderID: q.uuid("purchaseOrderId"), Status: q.values.Get("status"), Limit: q.limit()}
	f.CursorCreatedAt, f.CursorID = q.cursor()
	if f.Status != "" && !slices.Contains(invoiceStatuses, f.Status) {
		q.fields["status"] = "must be POSTED, AUDIT_BLOCKED or AUDIT_REJECTED"
	}
	if len(q.fields) > 0 {
		httpx.Fail(w, r, validationProblem(q.fields))
		return
	}
	rows, err := a.store.ListInvoices(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page := invoicePage{Items: make([]invoiceBody, 0, len(rows))}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		page.NextCursor = encodeCursor(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
	}
	for _, inv := range rows {
		page.Items = append(page.Items, toInvoiceBody(inv))
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return
	}
	inv, err := a.store.GetInvoice(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := toInvoiceBody(inv)
	if body.Match, err = a.invoiceMatch(r.Context(), inv); err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}

type grirItemBody struct {
	ID             string `json:"id"`
	ReceiptID      string `json:"receiptId"`
	ReceiptNumber  string `json:"receiptNumber"`
	LineNo         int    `json:"lineNo"`
	VendorID       string `json:"vendorId"`
	POLineID       string `json:"poLineId"`
	UnitPrice      string `json:"unitPrice"`
	ReceivedQty    string `json:"receivedQty"`
	CWQty          *int   `json:"cwQty,omitempty"`
	InvoicedQty    string `json:"invoicedQty"`
	ReceivedAmount string `json:"receivedAmount"`
	ClearedAmount  string `json:"clearedAmount"`
	OpenAmount     string `json:"openAmount"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	AgeDays        int    `json:"ageDays"`
	CreatedAt      string `json:"createdAt"`
}

type grirTotalsBody struct {
	Count      int         `json:"count"`
	OpenAmount []moneyBody `json:"openAmount"`
}

type grirPage struct {
	Items      []grirItemBody `json:"items"`
	Totals     grirTotalsBody `json:"totals"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

func (a *API) listGrir(w http.ResponseWriter, r *http.Request) {
	q := query{values: r.URL.Query(), fields: map[string]string{}}
	f := store.GrirFilter{VendorID: q.uuid("vendorId"), PurchaseOrderID: q.uuid("purchaseOrderId"), Status: q.values.Get("status"), Limit: q.limit()}
	f.CursorCreatedAt, f.CursorID = q.cursor()
	if f.Status != "" && !slices.Contains(grirStatuses, f.Status) {
		q.fields["status"] = "must be OPEN, PARTIAL, CLEARED or WRITTEN_OFF"
	}
	if v := q.values.Get("ageGt"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxAgeDays {
			q.fields["ageGt"] = fmt.Sprintf("must be a whole number of days from 0 to %d", maxAgeDays)
		}
		f.AgeGt = &n
	}
	if len(q.fields) > 0 {
		httpx.Fail(w, r, validationProblem(q.fields))
		return
	}
	got, err := a.store.ListGrir(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := got.Items
	out := grirPage{Items: make([]grirItemBody, 0, len(items)), Totals: grirTotalsBody{Count: got.Totals.Count, OpenAmount: make([]moneyBody, 0, len(got.Totals.OpenAmount))}}
	if len(items) > f.Limit {
		items = items[:f.Limit]
		out.NextCursor = encodeCursor(items[len(items)-1].CreatedAt, items[len(items)-1].ID)
	}
	for _, g := range items {
		out.Items = append(out.Items, toGrirBody(g))
	}
	for _, c := range got.Totals.OpenAmount {
		out.Totals.OpenAmount = append(out.Totals.OpenAmount, toMoney(money.Amount{Value: c.Amount, Currency: c.Currency}))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func toGrirBody(g store.GrirItem) grirItemBody {
	scale := money.MinorUnits(g.Currency)
	var cwQty *int
	if g.CWQty > 0 {
		cwQty = &g.CWQty
	}
	return grirItemBody{
		CWQty: cwQty,
		ID:    g.ID, ReceiptID: g.ReceiptID, ReceiptNumber: g.ReceiptNumber, LineNo: g.LineNo, VendorID: g.VendorID, POLineID: g.POLineID,
		UnitPrice: g.UnitPrice.String(), ReceivedQty: g.ReceivedQty.String(), InvoicedQty: g.InvoicedQty.String(), ReceivedAmount: g.ReceivedAmount.StringFixed(scale),
		ClearedAmount: g.ClearedAmount.StringFixed(scale), OpenAmount: g.OpenAmount.StringFixed(scale), Currency: g.Currency, Status: g.Status,
		AgeDays: g.AgeDays, CreatedAt: g.CreatedAt.UTC().Format(time.RFC3339),
	}
}
