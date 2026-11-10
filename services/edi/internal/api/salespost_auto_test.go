package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
	"github.com/BenjaminSRussell/passage/services/edi/internal/poster"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

func (f *postFixture) poster() *poster.Poster {
	return &poster.Poster{Store: store.New(f.app, 30), Intake: f.intake, PlaceAs: poster.AsServicePrincipal}
}

func (f *postFixture) runPoster(t *testing.T) int {
	t.Helper()
	n, err := f.poster().RunOnce(context.Background())
	if err != nil {
		t.Fatalf("poster run: %v", err)
	}
	return n
}

func (f *postFixture) due(t *testing.T, msg string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `update message set post_retry_at = now() - interval '1 second' where id = $1`, msg); err != nil {
		t.Fatal(err)
	}
}

func (f *postFixture) setup(t *testing.T) {
	t.Helper()
	f.defaults(t)
	f.xref(t, "BP", "ABC-1", spProductA)
	f.xref(t, "UP", "012345678905", spProductB)
}

func TestAutoPost_AReceivedOrderBecomesASalesOrderWithNoAPICall(t *testing.T) {
	f := newPostFixture(t)
	f.setup(t)
	msg := f.receive(t, "000000001", "00")

	if n := f.runPoster(t); n != 1 {
		t.Fatalf("the poster took %d orders", n)
	}
	if status, _ := f.message(t, msg); status != "POSTED" || f.events(t, ediev.MessagePosted, msg) != 1 {
		t.Fatalf("posted by the poster: %s", status)
	}
	if len(f.intake.calls) != 1 {
		t.Fatalf("one call to sales: %d", len(f.intake.calls))
	}
	call := f.intake.calls[0]
	if call.actor != "service:edi-sales" || call.duties != 1 || call.key != msg || call.le != srcLE || call.order.ExternalRef != f.partner+":PO1234" || call.order.Lines[1].Unit != "CS" {
		t.Fatalf("signed as the edi-sales principal with its one duty, keyed by the message: %+v", call)
	}
	if n := f.runPoster(t); n != 0 || len(f.intake.calls) != 1 {
		t.Fatalf("a posted order is not taken again: %d %d", n, len(f.intake.calls))
	}
}

func TestAutoPost_OutageBacksOffAndRetriesTheSameBody(t *testing.T) {
	f := newPostFixture(t)
	f.setup(t)
	msg := f.receive(t, "000000001", "00")

	f.intake.err = errors.New("connection refused")
	if n := f.runPoster(t); n != 1 {
		t.Fatalf("first attempt: %d", n)
	}
	if status, _ := f.message(t, msg); status != "MAPPED" {
		t.Fatalf("an outage leaves the order mapped: %s", status)
	}
	var attempts int
	var waitSeconds float64
	if err := f.pool.QueryRow(context.Background(), `select post_attempts, extract(epoch from post_retry_at - now()) from message where id = $1`, msg).Scan(&attempts, &waitSeconds); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || waitSeconds < 30 || waitSeconds > 90 {
		t.Fatalf("first back-off is about a minute: attempts %d wait %.0fs", attempts, waitSeconds)
	}
	if n := f.runPoster(t); n != 0 || len(f.intake.calls) != 1 {
		t.Fatalf("no new attempt before the back-off ends: %d calls %d", n, len(f.intake.calls))
	}

	f.due(t, msg)
	if n := f.runPoster(t); n != 1 {
		t.Fatalf("second attempt: %d", n)
	}
	if err := f.pool.QueryRow(context.Background(), `select post_attempts, extract(epoch from post_retry_at - now()) from message where id = $1`, msg).Scan(&attempts, &waitSeconds); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || waitSeconds < 90 || waitSeconds > 150 {
		t.Fatalf("the back-off doubles: attempts %d wait %.0fs", attempts, waitSeconds)
	}

	code, body := f.admin(t, http.MethodGet, "/item-xrefs", "")
	var list struct{ Items []domain.ItemXref }
	_ = json.Unmarshal([]byte(body), &list)
	var abc1 string
	for _, x := range list.Items {
		if x.PartnerItem == "ABC-1" {
			abc1 = x.ID
		}
	}
	if code != http.StatusOK || abc1 == "" {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body := f.admin(t, http.MethodPut, "/item-xrefs/"+abc1, `{"qualifier":"BP","partnerItem":"ABC-1","productId":"`+spProductB+`","siteId":"`+spSite+`","warehouseId":"`+spWarehouse+`"}`); code != http.StatusOK {
		t.Fatalf("edit the cross-reference after the first attempt: %d %s", code, body)
	}

	f.intake.err = nil
	f.due(t, msg)
	if n := f.runPoster(t); n != 1 {
		t.Fatalf("third attempt: %d", n)
	}
	if status, _ := f.message(t, msg); status != "POSTED" {
		t.Fatalf("posted after the outage: %s", status)
	}
	first, last := f.intake.calls[0].order, f.intake.calls[len(f.intake.calls)-1].order
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(last)
	if string(a) != string(b) || last.Lines[0].ProductID != spProductA {
		t.Fatalf("a retry under the same key sends the first attempt's body, not the edited cross-reference:\n%s\n%s", a, b)
	}
}

func TestAutoPost_ARefusalParksAndIsNotTakenAgain(t *testing.T) {
	f := newPostFixture(t)
	f.setup(t)
	msg := f.receive(t, "000000001", "00")

	f.intake.err = &sources.RefusedError{Status: http.StatusUnprocessableEntity, Code: "SLS-0007", Detail: "payload differs"}
	if n := f.runPoster(t); n != 1 {
		t.Fatalf("attempt: %d", n)
	}
	if status, errText := f.message(t, msg); status != "FAILED" || !strings.Contains(errText, domain.CodeSalesRefused) || !strings.Contains(errText, "SLS-0007") {
		t.Fatalf("parked with sales' reason: %s %q", status, errText)
	}
	if f.events(t, ediev.MessageFailed, msg) != 1 {
		t.Fatal("parking publishes message.failed")
	}
	if n := f.runPoster(t); n != 0 || len(f.intake.calls) != 1 {
		t.Fatalf("a parked order waits for a manual repost: %d %d", n, len(f.intake.calls))
	}

	f.intake.err = nil
	if code, body := f.post(t, msg); code != http.StatusOK || !strings.Contains(body, "POSTED") {
		t.Fatalf("manual repost: %d %s", code, body)
	}
}

func TestAutoPost_AMappingFailureParksAndAnOptedOutPartnerIsLeftAlone(t *testing.T) {
	f := newPostFixture(t)
	f.defaults(t)
	msg := f.receive(t, "000000001", "00")
	if n := f.runPoster(t); n != 1 {
		t.Fatalf("attempt: %d", n)
	}
	if status, errText := f.message(t, msg); status != "FAILED" || !strings.Contains(errText, domain.CodeNoItemRef) || len(f.intake.calls) != 0 {
		t.Fatalf("no cross-reference parks without a call to sales: %s %q", status, errText)
	}

	f.xref(t, "BP", "ABC-1", spProductA)
	f.xref(t, "UP", "012345678905", spProductB)
	if code, body := f.admin(t, http.MethodPut, "/sales-defaults", `{"invoiceAccountId":"`+spInvoice+`","currency":"EUR","autoPost":false,"unitMap":{"EA":"EA","CA":"CS"}}`); code != http.StatusOK || !strings.Contains(body, `"autoPost":false`) {
		t.Fatalf("opt out: %d %s", code, body)
	}
	second := f.receive(t, "000000002", "00")
	if n := f.runPoster(t); n != 0 || len(f.intake.calls) != 0 {
		t.Fatalf("an opted-out partner is not auto-posted: %d %d", n, len(f.intake.calls))
	}
	if status, _ := f.message(t, second); status != "MAPPED" {
		t.Fatalf("still mapped: %s", status)
	}
	if code, body := f.post(t, second); code != http.StatusOK || !strings.Contains(body, "POSTED") {
		t.Fatalf("the route still posts it: %d %s", code, body)
	}
}

func TestPostMessage_AUnitWithNoMappingParksTheOrder(t *testing.T) {
	f := newPostFixture(t)
	f.xref(t, "BP", "ABC-1", spProductA)
	f.xref(t, "UP", "012345678905", spProductB)
	if code, body := f.admin(t, http.MethodPut, "/sales-defaults", `{"invoiceAccountId":"`+spInvoice+`","currency":"EUR","unitMap":{"EA":"EA"}}`); code != http.StatusOK {
		t.Fatalf("defaults: %d %s", code, body)
	}
	msg := f.receive(t, "000000001", "00")
	code, body := f.post(t, msg)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, domain.CodeUnitUnmapped) || len(f.intake.calls) != 0 {
		t.Fatalf("CA has no mapping: %d %s", code, body)
	}
	if status, errText := f.message(t, msg); status != "FAILED" || !strings.Contains(errText, `"CA"`) {
		t.Fatalf("parked with the unit: %s %q", status, errText)
	}
	f.defaults(t)
	if code, body := f.post(t, msg); code != http.StatusOK {
		t.Fatalf("posts once the unit is mapped: %d %s", code, body)
	}
}

func TestReprocess_LeavesParkedOrdersAlone(t *testing.T) {
	f := newPostFixture(t)
	f.defaults(t)
	msg := f.receive(t, "000000001", "00")
	if code, _ := f.post(t, msg); code != http.StatusUnprocessableEntity {
		t.Fatalf("parked: %d", code)
	}
	var interchange string
	if err := f.pool.QueryRow(context.Background(), `select interchange_id::text from message where id = $1`, msg).Scan(&interchange); err != nil {
		t.Fatal(err)
	}
	_, parkedError := f.message(t, msg)
	received := f.events(t, ediev.MessageReceived, msg)

	key := map[string]string{"Idempotency-Key": "re-1"}
	rec := do(t, f.h, http.MethodPost, "/edi/v1/interchanges/"+interchange+":reprocess", "", "edi.operate", key)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"changed":0`) {
		t.Fatalf("single reprocess: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, f.h, http.MethodPost, "/edi/v1/interchanges:reprocess", `{"interchangeIds":["`+interchange+`"]}`, "edi.operate", map[string]string{"Idempotency-Key": "re-2"})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), msg) {
		t.Fatalf("bulk reprocess: %d %s", rec.Code, rec.Body.String())
	}
	status, errText := f.message(t, msg)
	if status != "FAILED" || errText != parkedError || f.events(t, ediev.MessageReceived, msg) != received {
		t.Fatalf("a parked order keeps its status and reason and nothing is republished: %s %q", status, errText)
	}
}

func TestMarkPosted_ErrorsWhenNoRowMatches(t *testing.T) {
	f := newPostFixture(t)
	f.setup(t)
	msg := f.receive(t, "000000001", "00")
	st := store.New(f.app, 30)
	ctx := store.SystemContext(context.Background(), testkit.TenantID(7))
	const order = "00000000-0000-0000-0000-000000000001"

	if err := st.MarkPosted(ctx, "00000000-0000-0000-0000-0000000000ff", order); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown message: %v", err)
	}
	if err := st.MarkPosted(ctx, msg, order); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPosted(ctx, msg, order); err != nil {
		t.Fatalf("the same order again is a no-op: %v", err)
	}
	if err := st.MarkPosted(ctx, msg, "00000000-0000-0000-0000-000000000002"); !errors.Is(err, store.ErrNotPostable) {
		t.Fatalf("a message posted to another order: %v", err)
	}
}
