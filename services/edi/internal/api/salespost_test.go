package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

const (
	spProductA  = "00000000-0000-0000-0000-0000000000a1"
	spProductB  = "00000000-0000-0000-0000-0000000000a2"
	spSite      = "00000000-0000-0000-0000-0000000000f1"
	spWarehouse = "00000000-0000-0000-0000-0000000000f2"
	spInvoice   = "00000000-0000-0000-0000-0000000000c2"
	spOtherLE   = "00000000-0000-0000-0000-0000000000e2"
)

type placedOrder struct {
	key   string
	le    string
	order domain.SalesOrderRequest
}

// fakeIntake stands in for the sales service: it dedups by externalRef the way
// BR-27.10 does and can be told to refuse or fail.
type fakeIntake struct {
	calls  []placedOrder
	orders map[string]string
	err    error
}

func (f *fakeIntake) PlaceOrder(_ context.Context, le, key string, order domain.SalesOrderRequest) (domain.SalesOrderRef, error) {
	f.calls = append(f.calls, placedOrder{key: key, le: le, order: order})
	if f.err != nil {
		return domain.SalesOrderRef{}, f.err
	}
	if f.orders == nil {
		f.orders = map[string]string{}
	}
	if _, seen := f.orders[order.ExternalRef]; !seen {
		f.orders[order.ExternalRef] = fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.orders)+1)
	}
	return domain.SalesOrderRef{ID: f.orders[order.ExternalRef]}, nil
}

type postFixture struct {
	h       http.Handler
	pool    *pgxpool.Pool
	app     *pgxpool.Pool
	intake  *fakeIntake
	partner string
}

func newPostFixture(t *testing.T) *postFixture {
	t.Helper()
	pool, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	f := &postFixture{pool: pool, app: app, intake: &fakeIntake{}}
	a := New(store.New(app, 30))
	a.Intake = f.intake
	f.h = a.Routes()
	rec := do(t, f.h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "p"})
	var p store.Partner
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("partner: %d %s", rec.Code, rec.Body.String())
	}
	f.partner = p.ID
	putProfile(t, f.h, p.ID, profileBody)
	return f
}

func (f *postFixture) admin(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	rec := do(t, f.h, method, "/edi/v1/partners/"+f.partner+path, body, "edi.admin", srcScope)
	return rec.Code, rec.Body.String()
}

func (f *postFixture) defaults(t *testing.T) {
	t.Helper()
	if code, body := f.admin(t, http.MethodPut, "/sales-defaults", `{"invoiceAccountId":"`+spInvoice+`","currency":"EUR"}`); code != http.StatusOK {
		t.Fatalf("defaults: %d %s", code, body)
	}
}

func (f *postFixture) xref(t *testing.T, qualifier, item, product string) string {
	t.Helper()
	code, body := f.admin(t, http.MethodPost, "/item-xrefs", `{"qualifier":"`+qualifier+`","partnerItem":"`+item+`","productId":"`+product+
		`","siteId":"`+spSite+`","warehouseId":"`+spWarehouse+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("xref: %d %s", code, body)
	}
	var x domain.ItemXref
	_ = json.Unmarshal([]byte(body), &x)
	return x.ID
}

func (f *postFixture) receive(t *testing.T, control, purpose string) string {
	t.Helper()
	ic := domain.Interchange{
		Delims:   domain.Delims{Element: '*', Component: '>', Segment: '~'},
		Sender:   domain.Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"},
		Receiver: domain.Party{Qualifier: "ZZ", ID: "MANUFACTURER"},
		Date:     "261021", Time: "1200", Version: "00401", Control: control, Usage: "T",
		Groups: []domain.Group{{
			FunctionalID: "PO", Sender: "DISTRIBUTOR", Receiver: "MANUFACTURER", Date: "20261021", Time: "1200", Control: "1", Version: "004010",
			Transactions: []domain.Transaction{{SetID: "850", Control: "0001", Body: []domain.Segment{
				{ID: "BEG", Elements: []string{purpose, "SA", "PO1234", "", "20261021"}},
				{ID: "CUR", Elements: []string{"BY", "USD"}},
				{ID: "DTM", Elements: []string{"010", "20261105"}},
				{ID: "PO1", Elements: []string{"1", "10", "EA", "2.50", "", "BP", "ABC-1"}},
				{ID: "PO1", Elements: []string{"2", "4", "CA", "11", "", "UP", "012345678905"}},
				{ID: "CTT", Elements: []string{"2"}},
			}}},
		}},
	}
	raw, err := domain.Serialize(ic)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, f.h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", raw, "edi.operate", nil)
	var got receiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusCreated || len(got.Messages) != 1 || got.Messages[0].Status != "MAPPED" {
		t.Fatalf("receive: %d %s", rec.Code, rec.Body.String())
	}
	return got.Messages[0].MessageID
}

func (f *postFixture) post(t *testing.T, id string) (int, string) {
	t.Helper()
	rec := do(t, f.h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope)
	return rec.Code, rec.Body.String()
}

func (f *postFixture) message(t *testing.T, id string) (status, errText string) {
	t.Helper()
	rec := do(t, f.h, http.MethodGet, "/edi/v1/messages/"+id, "", "edi.operate", nil)
	var m struct{ Status, Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("message: %d %s", rec.Code, rec.Body.String())
	}
	return m.Status, m.Error
}

func (f *postFixture) events(t *testing.T, typ, id string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1 and aggregate_id = $2`, typ, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostMessage_850BecomesASalesOrderAndUnmappedItemsAreParkedVisibly(t *testing.T) {
	f := newPostFixture(t)
	f.defaults(t)
	msg := f.receive(t, "000000001", "00")

	code, body := f.post(t, msg)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, domain.CodeNoItemRef) || len(f.intake.calls) != 0 {
		t.Fatalf("no cross-reference: %d %s", code, body)
	}
	status, errText := f.message(t, msg)
	if status != "FAILED" || !strings.Contains(errText, "BP ABC-1") || !strings.Contains(errText, "UP 012345678905") {
		t.Fatalf("parked in the monitor with the unmapped items: %s %q", status, errText)
	}
	listed := do(t, f.h, http.MethodGet, "/edi/v1/messages?status=FAILED", "", "edi.operate", nil)
	if !strings.Contains(listed.Body.String(), msg) {
		t.Fatalf("the monitor list shows the parked message: %s", listed.Body.String())
	}
	if f.events(t, ediev.MessageFailed, msg) != 1 {
		t.Fatal("parking publishes message.failed")
	}
	if code, _ := f.post(t, msg); code != http.StatusUnprocessableEntity || f.events(t, ediev.MessageFailed, msg) != 1 {
		t.Fatalf("parking again for the same reason publishes nothing new: %d", code)
	}

	f.xref(t, "BP", "ABC-1", spProductA)
	f.xref(t, "UP", "012345678905", spProductB)
	code, body = f.post(t, msg)
	var posted postResult
	if err := json.Unmarshal([]byte(body), &posted); err != nil || code != http.StatusOK || posted.Status != "POSTED" || posted.SalesOrderID == "" || posted.MessageID != msg {
		t.Fatalf("post: %d %s", code, body)
	}
	if len(f.intake.calls) != 1 {
		t.Fatalf("one call to sales: %d", len(f.intake.calls))
	}
	call := f.intake.calls[0]
	if call.key != msg || call.le != srcLE || call.order.Channel != "EDI" || call.order.CustomerID != srcCustomer || call.order.InvoiceAccountID != spInvoice ||
		call.order.Currency != "USD" || call.order.ExternalRef != f.partner+":PO1234" || call.order.RequestedShipDate != "2026-11-05" ||
		len(call.order.Lines) != 2 || call.order.Lines[0].ProductID != spProductA || call.order.Lines[1].ProductID != spProductB ||
		call.order.Lines[0].SiteID != spSite || call.order.Lines[1].Quantity != "4" || call.order.Lines[1].Unit != "CA" {
		t.Fatalf("sales request: %+v", call)
	}
	if status, errText := f.message(t, msg); status != "POSTED" || errText != "" {
		t.Fatalf("message after posting: %s %q", status, errText)
	}
	if f.events(t, ediev.MessagePosted, msg) != 1 {
		t.Fatal("posting publishes message.posted")
	}

	again, againBody := f.post(t, msg)
	if again != http.StatusOK || !strings.Contains(againBody, posted.SalesOrderID) || len(f.intake.calls) != 1 || f.events(t, ediev.MessagePosted, msg) != 1 {
		t.Fatalf("posting twice is one order, one call, one event: %d %s", again, againBody)
	}

	second := f.receive(t, "000000002", "00")
	code, body = f.post(t, second)
	var dup postResult
	if err := json.Unmarshal([]byte(body), &dup); err != nil || code != http.StatusOK || dup.SalesOrderID != posted.SalesOrderID || len(f.intake.orders) != 1 {
		t.Fatalf("the same PO in another interchange is the same sales order: %d %s", code, body)
	}
	if f.intake.calls[1].key != second || f.intake.calls[1].order.ExternalRef != call.order.ExternalRef {
		t.Fatalf("keyed by the message, deduped by the partner's PO: %+v", f.intake.calls[1])
	}
}

func TestPostMessage_ParksChangesAndSalesRefusalsAndRetriesOutages(t *testing.T) {
	f := newPostFixture(t)
	f.defaults(t)
	f.xref(t, "BP", "ABC-1", spProductA)
	f.xref(t, "UP", "012345678905", spProductB)

	change := f.receive(t, "000000001", "04")
	if code, body := f.post(t, change); code != http.StatusUnprocessableEntity || !strings.Contains(body, domain.CodeOrderUnpostable) || len(f.intake.calls) != 0 {
		t.Fatalf("a change order is parked, not posted: %d %s", code, body)
	}
	if status, errText := f.message(t, change); status != "FAILED" || !strings.Contains(errText, "purpose 04") {
		t.Fatalf("change order in the monitor: %s %q", status, errText)
	}

	msg := f.receive(t, "000000002", "00")
	f.intake.err = &sources.RefusedError{Status: http.StatusUnprocessableEntity, Code: "SLS-0007", Detail: "payload differs"}
	if code, body := f.post(t, msg); code != http.StatusUnprocessableEntity || !strings.Contains(body, domain.CodeSalesRefused) {
		t.Fatalf("sales refusal: %d %s", code, body)
	}
	if status, errText := f.message(t, msg); status != "FAILED" || !strings.Contains(errText, "SLS-0007") || !strings.Contains(errText, "payload differs") {
		t.Fatalf("refusal parked with sales' reason: %s %q", status, errText)
	}

	f.intake.err = errors.New("connection refused")
	if code, body := f.post(t, msg); code != http.StatusBadGateway || !strings.Contains(body, CodeSourceDown) {
		t.Fatalf("outage: %d %s", code, body)
	}
	if status, errText := f.message(t, msg); status != "FAILED" || !strings.Contains(errText, "SLS-0007") {
		t.Fatalf("an outage leaves the parked reason as it was: %s %q", status, errText)
	}

	f.intake.err = sources.ErrForbidden
	if code, body := f.post(t, msg); code != http.StatusForbidden || !strings.Contains(body, CodeSourceForbidden) {
		t.Fatalf("forbidden: %d %s", code, body)
	}

	f.intake.err = nil
	if code, body := f.post(t, msg); code != http.StatusOK || !strings.Contains(body, "POSTED") {
		t.Fatalf("a parked order posts once the cause is gone: %d %s", code, body)
	}
	if status, _ := f.message(t, msg); status != "POSTED" {
		t.Fatalf("status %s", status)
	}
	if code, body := f.post(t, change); code != http.StatusUnprocessableEntity {
		t.Fatalf("the change order stays parked: %d %s", code, body)
	}
}

func TestPostMessage_RefusalsBeforeAnyCallToSales(t *testing.T) {
	f := newPostFixture(t)
	msg := f.receive(t, "000000001", "00")
	if code, body := f.post(t, msg); code != http.StatusConflict || !strings.Contains(body, domain.CodeNoSalesDefaults) {
		t.Fatalf("no sales defaults: %d %s", code, body)
	}
	if status, _ := f.message(t, msg); status != "MAPPED" {
		t.Fatalf("a missing configuration does not park the order: %s", status)
	}
	if code, _ := f.post(t, "not-a-uuid"); code != http.StatusNotFound {
		t.Fatalf("bad id: %d", code)
	}
	if code, _ := f.post(t, "00000000-0000-0000-0000-0000000000ff"); code != http.StatusNotFound {
		t.Fatalf("unknown message: %d", code)
	}
	if rec := do(t, f.h, http.MethodPost, "/edi/v1/messages/"+msg+":post", "", "edi.admin", srcScope); rec.Code != http.StatusForbidden {
		t.Fatalf("edi.admin alone may not post orders: %d", rec.Code)
	}
	outOfScope := map[string]string{tenant.HeaderLegalEntity: spOtherLE}
	if rec := do(t, f.h, http.MethodPost, "/edi/v1/messages/"+msg+":post", "", "edi.operate", outOfScope); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeNoTradeProfile) {
		t.Fatalf("a caller outside the profile's legal entity sees no profile and parks nothing: %d %s", rec.Code, rec.Body.String())
	}
	if status, _ := f.message(t, msg); status != "MAPPED" {
		t.Fatalf("status %s", status)
	}

	bad := do(t, f.h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", strings.ReplaceAll(sample850(t, "2"), "000000001", "000000009"), "edi.operate", nil)
	var got receiveResponse
	_ = json.Unmarshal(bad.Body.Bytes(), &got)
	if len(got.Messages) != 1 || got.Messages[0].Status != "FAILED" {
		t.Fatalf("fixture: %s", bad.Body.String())
	}
	if code, body := f.post(t, got.Messages[0].MessageID); code != http.StatusConflict || !strings.Contains(body, CodeNotPostable) {
		t.Fatalf("a message that failed mapping is reprocessed, not posted: %d %s", code, body)
	}
	if len(f.intake.calls) != 0 {
		t.Fatalf("sales was never called: %d", len(f.intake.calls))
	}

	a := New(store.New(f.app, 30))
	rec := do(t, a.Routes(), http.MethodPost, "/edi/v1/messages/"+msg+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no intake configured: %d", rec.Code)
	}
}

func TestItemXrefAPI_CRUDDuplicatesAndLegalEntityScope(t *testing.T) {
	f := newPostFixture(t)
	id := f.xref(t, "BP", "ABC-1", spProductA)
	if code, body := f.admin(t, http.MethodPost, "/item-xrefs", `{"qualifier":"BP","partnerItem":"ABC-1","productId":"`+spProductB+`","siteId":"`+spSite+`","warehouseId":"`+spWarehouse+`"}`); code != http.StatusConflict || !strings.Contains(body, CodeXrefExists) {
		t.Fatalf("duplicate: %d %s", code, body)
	}
	if code, body := f.admin(t, http.MethodPost, "/item-xrefs", `{"qualifier":"ZZ","partnerItem":"","productId":"x","siteId":"x","warehouseId":"x"}`); code != http.StatusBadRequest || !strings.Contains(body, "qualifier") {
		t.Fatalf("validation: %d %s", code, body)
	}
	other := f.xref(t, "VP", "V-9", spProductB)
	if code, body := f.admin(t, http.MethodPut, "/item-xrefs/"+id, `{"qualifier":"VP","partnerItem":"V-9","productId":"`+spProductA+`","siteId":"`+spSite+`","warehouseId":"`+spWarehouse+`"}`); code != http.StatusConflict {
		t.Fatalf("update into another mapping: %d %s", code, body)
	}
	code, body := f.admin(t, http.MethodPut, "/item-xrefs/"+id, `{"qualifier":"BP","partnerItem":"ABC-1","productId":"`+spProductB+`","siteId":"`+spSite+`","warehouseId":"`+spWarehouse+`"}`)
	var updated domain.ItemXref
	if err := json.Unmarshal([]byte(body), &updated); err != nil || code != http.StatusOK || updated.ProductID != spProductB || updated.Version != 2 {
		t.Fatalf("update: %d %s", code, body)
	}
	if code, _ := f.admin(t, http.MethodPut, "/item-xrefs/00000000-0000-0000-0000-0000000000ff", `{"qualifier":"BP","partnerItem":"Q","productId":"`+spProductB+`","siteId":"`+spSite+`","warehouseId":"`+spWarehouse+`"}`); code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", code)
	}

	outOfScope := map[string]string{tenant.HeaderLegalEntity: spOtherLE}
	list := do(t, f.h, http.MethodGet, "/edi/v1/partners/"+f.partner+"/item-xrefs", "", "edi.admin", outOfScope)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "ABC-1") {
		t.Fatalf("another legal entity sees none of them: %d %s", list.Code, list.Body.String())
	}
	if rec := do(t, f.h, http.MethodDelete, "/edi/v1/partners/"+f.partner+"/item-xrefs/"+id, "", "edi.admin", outOfScope); rec.Code != http.StatusNotFound {
		t.Fatalf("another legal entity cannot delete: %d", rec.Code)
	}
	if rec := do(t, f.h, http.MethodPost, "/edi/v1/partners/"+f.partner+"/item-xrefs", `{"qualifier":"EN","partnerItem":"E","productId":"`+spProductA+`","siteId":"`+spSite+`","warehouseId":"`+spWarehouse+`"}`, "edi.admin", outOfScope); rec.Code != http.StatusConflict {
		t.Fatalf("another legal entity cannot add: %d", rec.Code)
	}
	if rec := do(t, f.h, http.MethodGet, "/edi/v1/partners/"+f.partner+"/item-xrefs", "", "edi.operate", srcScope); rec.Code != http.StatusForbidden {
		t.Fatalf("partner management is edi.admin: %d", rec.Code)
	}

	if code, _ := f.admin(t, http.MethodDelete, "/item-xrefs/"+id, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := f.admin(t, http.MethodDelete, "/item-xrefs/"+id, ""); code != http.StatusNotFound {
		t.Fatalf("delete again: %d", code)
	}
	var items struct{ Items []domain.ItemXref }
	_, body = f.admin(t, http.MethodGet, "/item-xrefs", "")
	if err := json.Unmarshal([]byte(body), &items); err != nil || len(items.Items) != 1 || items.Items[0].ID != other {
		t.Fatalf("list after delete: %s", body)
	}
}

func TestSalesDefaultsAPI_RoundTripValidationAndProfile(t *testing.T) {
	f := newPostFixture(t)
	if code, _ := f.admin(t, http.MethodGet, "/sales-defaults", ""); code != http.StatusNotFound {
		t.Fatalf("none yet: %d", code)
	}
	if code, body := f.admin(t, http.MethodPut, "/sales-defaults", `{"invoiceAccountId":"x","currency":"usd"}`); code != http.StatusBadRequest || !strings.Contains(body, "currency") {
		t.Fatalf("validation: %d %s", code, body)
	}
	f.defaults(t)
	f.defaults(t)
	code, body := f.admin(t, http.MethodGet, "/sales-defaults", "")
	if code != http.StatusOK || !strings.Contains(body, `"EUR"`) {
		t.Fatalf("read: %d %s", code, body)
	}
	if rec := do(t, f.h, http.MethodPut, "/edi/v1/partners/"+f.partner+"/sales-defaults", `{"invoiceAccountId":"`+spInvoice+`","currency":"EUR"}`, "edi.admin", map[string]string{tenant.HeaderLegalEntity: spOtherLE}); rec.Code != http.StatusConflict {
		t.Fatalf("another legal entity cannot set them: %d", rec.Code)
	}
}

func TestPostMessage_EdifactORDERSBecomesASalesOrderToo(t *testing.T) {
	f := newPostFixture(t)
	eu := createPartner(t, f.h, "eu", edifactPartnerBody("EU distributor", "EUDIST", "4012345000009", ""))
	f.partner = eu.ID
	putProfile(t, f.h, eu.ID, profileBody)
	f.defaults(t)
	res, code, _ := postEdifact(t, f.h, ordersEdifact(t))
	if code != http.StatusCreated || len(res.Messages) != 1 || res.Messages[0].TransactionSet != "ORDERS" {
		t.Fatalf("receive: %d %+v", code, res)
	}
	msg := res.Messages[0].MessageID
	rec := do(t, f.h, http.MethodGet, "/edi/v1/messages/"+msg, "", "edi.operate,edi.pii.view", nil)
	var m struct{ Canonical domain.Order }
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || len(m.Canonical.Lines) != 2 {
		t.Fatalf("canonical order: %s", rec.Body.String())
	}
	for _, l := range m.Canonical.Lines {
		if l.ItemRef == nil {
			t.Fatalf("the ORDERS fixture carries an item reference per line: %+v", l)
		}
		f.xref(t, l.ItemRef.Qualifier, l.ItemRef.Value, spProductA)
	}
	code2, body := f.post(t, msg)
	if code2 != http.StatusOK || !strings.Contains(body, "POSTED") || len(f.intake.calls) != 1 {
		t.Fatalf("post: %d %s", code2, body)
	}
	order := f.intake.calls[0].order
	if order.ExternalRef != eu.ID+":PO-EU-881" || order.Currency != "EUR" || order.RequestedShipDate != "2026-11-12" || len(order.Lines) != 2 || order.Lines[0].Unit != "PCE" {
		t.Fatalf("sales request: %+v", order)
	}
}
